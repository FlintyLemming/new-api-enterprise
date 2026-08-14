# Plan 5b — Recorder 生命周期与 Controller 接入

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §5、§6（除 usage/cost）、§8.1–8.3、§13 的生命周期部分与 §16.5 后半——`service/langfuse/recorder.go`（`Begin` 三阶段 / `BeginAttempt` / `EndAttempt` / `Finish` / 异步 worker materialization）、`ContextKeyLangfuseRecorder` + `FromContext`、controller 统一 finalizer 改造、共享 `doRequest` 窄 hook。usage/cost 属性留给 plan-6。

**Architecture:** Recorder 是请求级值对象持有者：runtime lease、捕获预算、capture writer、冻结 input（共享 `*string`）、attempt 值对象列表。请求路径**永不创建 OTel span**；span 只在 `Finish` 的同步 metadata-only 路径或 worker 中以显式历史时间创建，先 End root 再 End generation。worker 只接收不可变值，不引用 `gin.Context`/`RelayInfo`。

**Tech Stack:** Go, Gin, otel sdk（plan-3 的 runtime/IDGenerator），gopool（`common.RelayCtxGo` 或专用 pool）。

## Global Constraints

- 见 `00-master.md`；phase 顺序（§5.1）是热路径与隐私成本契约：Phase 0 不读 Header/body；Phase 1 最多一次 64KiB 完整身份读取（即使未采样）；Phase 2 命中后才取 lease/预算/包装 writer。
- 采样算法固定：`digest=sha256(key)`、`bucket=BigEndian.Uint64(digest[0:8])`、`threshold=floor(rate*2^64)`（`math/big` 精确整数，禁止把 bucket 转 float、禁止其他字节序/取模）；trace ID 只从 request ID 派生。
- `Begin/BeginAttempt/EndAttempt/Finish` 各自最外层 `defer recover()` → 降级/省略 + 限频告警（只含 request ID/阶段/panic 摘要），绝不传播到 relay。
- generation 固定 denylist（§13，逐项）：`langfuse.trace.name/input/output/metadata`、`user.id`、`session.id`、`langfuse.trace.public/tags`、`langfuse.user.id`、`langfuse.session.id`、`langfuse.observation.metadata.langfuse_user_id/langfuse_session_id/langfuse_tags`、`langfuse.trace.metadata.langfuse_session_id/langfuse_user_id/langfuse_tags`、`ai.telemetry.metadata.sessionId/userId/tags`、`tag.tags`、以及任何 `langfuse.trace.metadata` 前缀 key。
- 已知行为不得"顺手修复"：AWS SDK（非 API Key）与 Xunfei 旁路 = root-only/`no_active_attempt`（plan-6 落结算侧标记）；非 Gemini 入站格式被 Gemini 渠道映射为 embedding/Imagen 时仍按原格式导出；Gemini handler/validator/adaptor 分支零改动。

---

### Task 1: `ContextKeyLangfuseRecorder` 与 `FromContext`

**Files:**
- Modify: `constant/context_key.go`
- Modify: `service/langfuse/recorder.go`（本任务先建包级 getter，Recorder 类型在 Task 2）
- Test: `service/langfuse/recorder_context_test.go`

- [ ] **Step 1: 失败测试**：`FromContext(nil)==nil`；空 context==nil；`SetContextKey(c, key, (*Recorder)(nil))`（typed nil）→nil；存入非 nil Recorder→取出同一指针；`SetContextKey(c, key, "wrong")`→nil；不 panic。
- [ ] **Step 2: 实现**

`constant/context_key.go` 追加：

```go
	ContextKeyLangfuseRecorder ContextKey = "langfuse_recorder"
```

`recorder.go`：

```go
// FromContext 返回当前请求的 Langfuse Recorder;任何缺失/类型不匹配/typed nil 都返回 nil。
// 所有结算函数与 attempt hook 统一使用本 getter,禁止各调用点自行 c.Get+断言。
func FromContext(c *gin.Context) *Recorder {
	if c == nil {
		return nil
	}
	v, ok := common.GetContextKey(c, constant.ContextKeyLangfuseRecorder)
	if !ok || v == nil {
		return nil
	}
	r, ok := v.(*Recorder)
	if !ok || r == nil {
		return nil
	}
	return r
}
```

- [ ] **Step 3: 通过 + Commit**

```bash
git add constant/context_key.go service/langfuse/
git commit -m "feat(langfuse): typed recorder context key and nil-safe getter"
```

---

### Task 2: Recorder 值对象与状态模型

**Files:**
- Create: `service/langfuse/recorder.go`（主体）、`service/langfuse/attributes.go`（属性 builder）
- Test: `service/langfuse/recorder_test.go`

**Interfaces:**
- Produces（plan-6 消费）:

```go
type UsageRecord struct { // §8.4 字段逐字
	Kind                     UsageKind // UsageKindText / UsageKindAudio
	Available                bool
	ModelName                string
	InputExcludesCache       bool
	UsageSemanticUnknown     bool
	InputTokens              int
	OutputTokens             int
	InputCachedTokens        int
	InputCacheWriteTokens    int
	InputImageTokens         int
	InputAudioTokens         int
	OutputAudioTokens        int
	OutputReasoningTokens    int
	Quota                    int
	QuotaPerUnit             float64
	BillingSource            string
	Settled                  bool
}
type attemptValue struct { // §5.2 快照清单
	Index, ChannelID, ChannelType int
	ChannelName, SelectedGroup, OriginModel, UpstreamModel, UpstreamRelayFormat, UpstreamRequestID string
	StartTime, EndTime, FirstResponseTime time.Time
	StartCaptured, StartLogical, EndCaptured, EndLogical int64
	ErrCode, ErrMessage string
	HTTPStatus int
	UpstreamCallStarted, WroteToClient, PartialOutput, OutputTruncated bool
	EndReason string // attempt_end_reason 闭合枚举
	GeminiFinal relayconstant.GeminiAction
	Superseded bool
	Usage *UsageRecord // plan-6 填
}
type Recorder struct { /* 见下 */ }
func (r *Recorder) RecordUsage(rec UsageRecord) // Task 5 实现,plan-6 调用
```

Recorder 字段（实现基线，允许增删私有字段但公有 API 不变）：`mu sync.Mutex`（与 capture writer 共享的请求级 state mutex——writer 持有 `*sync.Mutex` 指向同一把）、`snap`、`runtime *TelemetryRuntime`、`budget *BudgetReservation`、`writer *CaptureWriter`、`origWriter gin.ResponseWriter`、`input *string`（不可变共享）、`inputOmittedReason/inputScanTruncated`、`attempts []*attemptValue`、`active *attemptValue`、`frozen/finished bool`、`userId/tokenId`、`username/userGroup/tokenName`、`selectedGroup/requestId/originModel/relayFormat string`、`isStream/isPlayground bool`、`rootStart time.Time`、`rootEnd time.Time`、`session SessionIdentity`、`captureState string`、`settledQuota *struct{Quota int; BillingSource string}`（plan-6 用）、`clock func() time.Time`（默认 `time.Now`，测试注入）、`requestPath string`、`modelParams map[string]any`。

- [ ] **Step 1: 先写 Task 5 将覆盖的状态不变式测试骨架**（本任务只验证构造与锁语义：两个 goroutine 并发 lock/mutex 操作 attempts 列表无 race）。
- [ ] **Step 2/3: 实现结构体 + Commit**

```bash
git add service/langfuse/
git commit -m "feat(langfuse): recorder value objects and shared state model"
```

---

### Task 3: `Begin` 三阶段 + 采样器

**Files:**
- Modify: `service/langfuse/recorder.go`（追加）、`service/langfuse/sampler.go`（新建）
- Test: `service/langfuse/sampler_test.go`、`recorder_begin_test.go`

**Interfaces:**
- Produces:

```go
func Begin(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) *Recorder
func sampleHit(samplingKey string, rate float64, digest func([]byte) [32]byte) bool
func samplingKeyFor(session SessionIdentity, requestId string) string // scoped session 优先,否则 request ID
```

`dto.Request` 来自 `relaykit/dto`（白名单内）。`Begin` 内部流程（逐条落实 §5.1）：

**Phase 0**（`relayInfo.RequestId==""` → nil 防御；其余按序）：
```go
	binding := LoadBinding()
	snap := binding.Snapshot
	if !snap.Enabled || binding.Runtime == nil { return nil }   // 已验证状态随快照
	if snap.SampleRate <= 0 { return nil }
	if !formatSupported(relayFormat, relayMode) { return nil }  // openai+ChatCompletions / claude / openai_responses+Responses
	if relayFormat == types.RelayFormatGemini {
		if relayconstant.ClassifyGeminiAction(path, info.OriginModelName) != relayconstant.GeminiActionGenerate {
			return nil // 首轮排除 embedding/predict/unknown/imagen*
		}
	}
```
`formatSupported`：`RelayFormatOpenAI` 要求 `RelayMode == relayconstant.RelayModeChatCompletions`；`RelayFormatClaude` 直接过；`RelayFormatOpenAIResponses` 要求 `RelayMode == relayconstant.RelayModeResponses`；其余 false。（Playground 走 `/pg/chat/completions` → Path2RelayMode 已映射 ChatCompletions，天然在范围。）

**Phase 1**：
```go
	sess := extractHeaderSession(headerGetter(c), snap)
	var bodyBuf []byte
	if sess.RawSessionID == "" && sess.Source == "" && len(snap.SessionBodyPaths) > 0 {
		sess2, buf := extractBodySession(c, snap, nil)
		bodyBuf = buf // 最多一次 64KiB 完整读取,含未采样请求
		sess = mergeHeaderBodySession(sess, sess2) // Header 已锁定时保留 Header 结论
	}
	key := samplingKeyFor(sess, info.RequestId)
	if !sampleHit(key, snap.SampleRate, sha256.Sum256) { return nil }
```

**Phase 2**（有界重试一次）：
```go
	runtime := binding.Runtime
	if !runtime.TryAcquire() {
		// 唯一允许的重载:旧 runtime 恰好退休。用新 snapshot 重跑全部守卫与身份判定。
		binding = LoadBinding(); snap = binding.Snapshot
		…重跑 Phase 0 全部检查…; if binding.Runtime == nil { return nil }
		// Header 按新配置重查;body 缓冲复用(仅当新上限允许且旧缓冲存在才免读;第一次失败不重读)
		sess = …(bodyBuf 复用传给 extractBodySession)…
		if !sampleHit(samplingKeyFor(sess, info.RequestId), snap.SampleRate, sha256.Sum256) { return nil }
		runtime = binding.Runtime
		if !runtime.TryAcquire() { return nil }
	}
```
随后固定快照（root 开始时间直接用 `info.StartTime`）、构造 Recorder、`SetContextKey(c, constant.ContextKeyLangfuseRecorder, r)`；`SendContent=false` → `captureState="disabled"` 返回；否则 `ReserveCapture(2*content+response)`（checked；失败 → `captureState="budget"`）；从**已缓存** `common.KeyBodyStorage`（只 `c.Get`）经 `NewReader` 读 `maxContentBytes+1` 冻结不可变 `*string`（读满 +1 → `inputScanTruncated=true`；storage 缺失 → `input_omitted_reason=body_storage_unavailable`）；`NewCaptureWriter(orig, snap.MaxResponseBytes)` 替换 `c.Writer`。模型参数提取（§8.3）从 `request` 类型断言（`*dto.GeneralOpenAIRequest`、Claude/Gemini/Responses 请求结构）取 temperature/top_p/max_tokens/max_completion_tokens/stream/tool_count/thinking 设置，保留显式零值。

采样器实现（精确整数阈值）：

```go
func sampleHit(samplingKey string, rate float64, digest func([]byte) [32]byte) bool {
	if rate >= 1 { return true } // rate<=0 由 Phase 0 排除
	d := digest([]byte(samplingKey))
	bucket := new(big.Int).SetUint64(binary.BigEndian.Uint64(d[0:8]))
	f := new(big.Float).SetPrec(128).SetFloat64(rate)      // float64 精确值
	f.Mul(f, new(big.Float).SetPrec(128).SetInt(new(big.Int).Lsh(big.NewInt(1), 64)))
	threshold, _ := f.Int(nil)                              // 截断=floor(正数)
	return bucket.Cmp(threshold) < 0
}
```

- [ ] **Step 1: 失败测试**：
  - 采样向量：注入固定 digest，构造 rate 与 key 使 bucket 恰好等于/小于/大于 `floor(rate*2^64)`（用 `new(big.Int).SetUint64(bucket)` 对比 `rate*math.Ldexp(1,64)` 的精确 floor）；rate=1 全命中；rate=0 走 Phase 0 不进 sampler；同一 scoped session 两次判定一致；不同用户相同 raw → 不同 key → 允许不同结果。
  - Phase 契约（§14.1 sampling 行）：disabled / rate≤0 / 不支持格式 / Gemini 首轮非 generate → **不**调用 header/body 访问（注入会 panic 的 getter 断言零调用）；rate=0 且配置了 body paths → 不触碰 BodyStorage/gjson；配置 body paths 且未采样 → 恰好一次 ≤64KiB 读取（mock storage 计数 NewReader）；未采样不 `TryAcquire`、不 Reserve、不替换 `c.Writer`、attempts 为空。
  - 退休重试：第一个 runtime `Retire()` 后 `TryAcquire` 失败 → 重载 binding 用新 snap 重跑（新 rate=0 → nil）；旧 body 缓冲被复用（mock storage 第二轮 NewReader==0）；混用检查：新 runtime + 旧 rate 采样判定（构造 rate 差异使旧命中新不命中 → 最终 nil）。
  - `send_content=false`：不 Reserve、`c.Writer` 不变、`capture_state=disabled`；body 身份读取仍发生（配置 paths 时）。
  - 预算失败：`SetMaxInFlightCaptureBytes` 压到 1 → `capture_state=budget`、不替换 writer。
  - input 冻结：65537 字节 body → input 65536 字节 + `input_scan_truncated=true`；storage 缺失 → reason 且 output 仍可捕获；input 是同一 `*string`（后续断言引用一致）。
  - panic 注入（Begin 内部某步 panic，通过注入 hook）：返回 nil 或 metadata-only，不向调用方传播，writer 若已安装则按身份规则恢复。
- [ ] **Step 2: 实现并跑绿** Run: `go test ./service/langfuse -race -run 'TestSampler|TestBegin' -v`
- [ ] **Step 3: Commit**

```bash
git add service/langfuse/
git commit -m "feat(langfuse): three-phase begin with deterministic sampling and bounded retry"
```

---

### Task 4: `BeginAttempt`/`EndAttempt` 与 `doRequest` 窄 hook

**Files:**
- Modify: `service/langfuse/recorder.go`（追加）
- Modify: `relay/channel/api_request.go`（`doRequest` 内、`relayClient.Do(req)` 紧前一行）
- Test: `service/langfuse/recorder_attempt_test.go`

**Interfaces:**
- Produces:

```go
// BeginAttempt 只在共享 doRequest 的 relayClient.Do 紧前调用一次。
func BeginAttempt(c *gin.Context, info *relaycommon.RelayInfo)
// EndAttempt 在 handler 返回后调用;apiErr 可为 nil。
func EndAttempt(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError)
```

`doRequest` 的 hook（唯一位置；`DoApiRequest`/`DoFormRequest`/导出 `DoRequest`/`DoTaskApiRequest`/`DoWssRequest` 都**不加**）：

```go
	langfuse.BeginAttempt(c, info) // no-op when recorder absent (task/realtime/test paths)
	resp, err := relayClient.Do(req)
```

（`relay/channel` 已依赖 `service/*`，新增 import `service/langfuse` 满足白名单方向 `relay/channel -> service/langfuse -> relay/common`。）

`BeginAttempt` 语义（§5.2）：
- `FromContext` nil → no-op；外层 defer recover。
- 锁内：若 `active != nil && active.UpstreamCallStarted`（未收口）→ 先以**当前时间/当前 offsets** 收口：`EndReason="replaced_by_next_upstream_call"`、`Superseded=true`、EndTime=clock()、End offsets 快照，再创建新 attempt。
- 新 attempt：Index=len(attempts)、StartTime=clock()（入口立即）、ChannelID/Type 经 `info.GetChannelID()/GetChannelType()`、ChannelName 从 `common.GetContextKeyString(c, constant.ContextKeyChannelName)` trim 快照（c 为 nil 视为缺失；**不得**从 ChannelMeta 臆造）、SelectedGroup、OriginModel、StartCaptured/StartLogical（writer 存在则锁内 offsets，否则 0/0）、`UpstreamCallStarted=true`。

`EndAttempt` 语义：
- 只对 active 且 `UpstreamCallStarted` 生效；`EndTime=clock()` **先取再做任何整理**；最终模型经 `info.GetUpstreamModelName()`；Gemini 入站格式时用入站 path+该模型重分类存入 `GeminiFinal`；UpstreamRelayFormat=`info.GetFinalRequestRelayFormat()` 字符串；UpstreamRequestID 从 `common.UpstreamRequestIdKey`（doRequest 已写入 c）；End offsets 锁内快照；FirstResponseTime 归属仅当本 attempt 有写出（logical delta>0 之前提）且 `info.FirstResponseTime` ∈ [StartTime, EndTime]；错误码/消息（`apiErr` 的 code 与 `MaskSensitiveErrorWithStatusCode()` 限长结果）/HTTP status；`EndReason="handler_returned"`；WroteToClient = logical delta>0；置 active=nil。

- [ ] **Step 1: 失败测试**（§14.1 recorder 行相关项）：
  1. 一次真实 `doRequest`（httptest 上游）只产生一个 attempt；三个 wrapper 直接调用不触发第二个（构造 `DoApiRequest` 全链 + 计数）。
  2. `DoTaskApiRequest`：无 Recorder 时 no-op（断言 FromContext(nil)==nil 路径 + 无 panic）。
  3. supersede：同一 handler 内第二次 `BeginAttempt` → 前一个 attempt `EndReason=replaced_by_next_upstream_call`、`Superseded`、Error 状态素材（materialize 断言放 Task 5）；只有最新 active 接收 usage（Task 5/6 断言）。
  4. `chatCompletionsViaResponses` 二次 relay：模拟同一 handler 两次进入共享边界 → 两个 attempt 值对象。
  5. channel name 三层 fallback：正 ID → `channel-{id}`；ID≤0 type>0 → `channel-type-{type}`；都无 → `channel-unknown`；真实 name 非空才写 metadata `channel_name`。
  6. offsets：attempt1 写 100B、attempt2 写 200B，maxCapture=250 → attempt1 EndCaptured=100、attempt2 EndCaptured=250、attempt2 `OutputTruncated=true`（logical delta 200 > captured delta 150）、两者 output 切片互不重叠。
  7. nil-safe accessor：`RelayInfo` 无 ChannelMeta 时 GetChannelID/Type=0、模型 ""，不 panic。
  8. EndAttempt 对未到达边界的 attempt（无 BeginAttempt 直接调）→ no-op。
- [ ] **Step 2: 实现并跑绿** Run: `go test ./service/langfuse -race -run TestAttempt -v && go build ./relay/...`
- [ ] **Step 3: Commit**

```bash
git add service/langfuse/ relay/channel/api_request.go
git commit -m "feat(langfuse): attempt lifecycle with single shared-boundary hook"
```

---

### Task 5: `Finish`、属性 builder、worker materialization

**Files:**
- Modify: `service/langfuse/recorder.go`、`service/langfuse/attributes.go`
- Test: `service/langfuse/finish_test.go`、`attributes_test.go`

**Interfaces:**
- Produces:

```go
func Finish(c *gin.Context, info *relaycommon.RelayInfo, finalErr *types.NewAPIError)
// 属性 builder(测试直接调用):
func buildRootAttributes(r *recorderMaterial) []attribute.KeyValue
func buildGenerationAttributes(a *attemptValue, r *recorderMaterial) []attribute.KeyValue
func buildRootMetadata(r *recorderMaterial) map[string]any      // §8.1 JSON
func buildGenerationMetadata(a *attemptValue) map[string]any    // §8.2 JSON
func buildModelParams(r *recorderMaterial) string               // JSON string,无参数时 ""
```

`Finish` 顺序（§5.3/§5.2 冻结语义）：
1. `rootEnd = clock()` **最先**；幂等（`finished` 置位）。
2. Gemini 终判：入站格式为 Gemini 且**任一**到达 outbound 边界的 attempt `GeminiFinal != Generate` → 丢弃整个值对象：释放 budget/lease（幂等）、不创建任何 span、return。
3. writer 身份恢复：仅当 `c.Writer` 底层实例仍是本 Recorder 安装的 capture writer（接口底层实例比较，非动态类型）→ `c.Writer = origWriter`；否则保留并限频 `writer_replaced_before_finish`。
4. 锁内 `frozen=true`、复制 FrozenCapture 与 attempts/值快照为 `recorderMaterial`（不可变 worker 输入，不再引用 gin.Context/RelayInfo——username/userGroup/firstResponse 等已快照在值里）。
5. 无正文（`disabled/budget/panic`）→ 同步路径：构造有界 metadata → `materialize(material, nil, nil)`；有正文 → admission（有界 semaphore，容量随 publish 配置为 `MaxInFlightCaptureBytes/reservation`，封顶 4096）：失败 → `captureState="admission"` 走同步 metadata-only；成功 → `common.RelayCtxGo(ctx, func(){ defer release budget+lease; worker(material) })`，`Finish` 返回时**不**释放二者。

`materialize(m, inputOut, attemptsOut map[int][]byte)`（同步与 worker 共用）：
- ctx = `context.WithValue(bg, traceIDCtxKey{}, DeriveTraceID(m.RequestId))`；root, ctx2 := `runtime.tracer.Start(ctx, name, trace.WithTimestamp(m.RootStart))`；`langfuse.internal.as_root="true"`、root 属性（§6.1 表：`langfuse.observation.type=span`、`user.id`、`session.id`（scoped 非空时）、input/output（有正文时；JSON string via common.Marshal）、environment、release=`common.Version`、metadata；**不写** `langfuse.trace.*`）；OTel status Error 仅整体失败（finalErr 非 nil 或所有 attempt 失败）。
- generations 以 ctx2 为 parent、各自 StartTime 创建；属性（§6.2 减 usage/cost：type=generation、input（脱敏客户端请求）、output（本 attempt 切片聚合）、model.parameters、completion_start_time（归属成立时 RFC3339Nano UTC）、Error status + 脱敏 message）；superseded/失败 attempt Error；**denylist 属性一律不写**。
- 属性设置完成后：`root.End(trace.WithTimestamp(m.RootEnd))` **先**，再按 attempt 顺序 `generation.End(WithTimestamp(a.EndTime))`；每 span `End` 后 `runtime.recordMaterialized(1)`。
- 全部非正文属性合计 64KiB 上限：builder 内超限截 metadata（保诊断字段、置 `metadata_truncated=true`、不截坏 JSON）。

`worker(material)`：sanitizer 处理 input（先清原始 input 引用再聚合 output——顺序：`sanitizedInput := …; material.Input = nil`），每 attempt 与 root 的 output 切片 → `AggregateOutput(kind, isStream, seg)` → SanitizeJSON/Envelope 限长 → `materialize`；最外层 defer：正常完成释放 budget+lease；panic → 用值对象时间戳收口已创建 spans、`captureState="panic"` 的降级 materialize 或丢弃、幂等释放，worker 墙钟不作业务时间。

- [ ] **Step 1: 失败测试**（核心项；OTLP 解码复用 plan-3 的 httptest+proto 手法）：
  1. root 先入队：测试 SpanProcessor 收到顺序 root, g1, g2；注入 clock 固定 attempt 时长 → 解码后 duration 精确等于注入值、g1.End < g2.Start；root duration == RootStart→RootEnd。
  2. 幂等：Finish 两次只导出一份 observation。
  3. writer 恢复两分支 + `writer_replaced_before_finish` 只告警一次（§14.1 writer 行）。
  4. Gemini 终判表（§14.3 七行 Langfuse 部分）：入站 generate + 各最终模型 → 只有全部 generate 才 materialize；任一非 generate → 零 span、budget/lease 已释放（断言 runtime inFlight 归零、预算计数回落）；无 attempt 的 generate 请求 → metadata-only root 存在。
  5. denylist 契约：解码 OTLP JSON，generation 无任何 denylist 属性、显式断言无 `user.id`/`session.id`；root 无 `langfuse.trace.*`、有 `langfuse.internal.as_root="true"`、有 observation input/output/metadata、trace name==span name（无 trace.name 属性）。
  6. 属性编码：metadata/params 是可解析 JSON string；64 属性上限（builder 输出 len ≤64）。
  7. admission：把 semaphore 容量压到 0 → `capture_state=admission`、同步 materialize、无正文属性、budget 释放。
  8. panic 注入（sanitizer/aggregator/Finish 内部）→ `capture_state=panic`、客户端字节流与未启用时逐字节一致（在 controller 层重复覆盖）、lease/budget 幂等释放。
  9. worker 隔离：material 传给 worker 后修改原 Recorder 状态不影响已提交作业（值拷贝断言）；worker 不持 gin.Context（编译期由白名单+material 类型保证，测试断言 material 为纯值类型可用 reflect 递归无指针到 gin 的字段——简化为 code review + 类型评审，写入计划即可，不做反射测试）。
  10. root metadata 字段集（§8.1 示例）逐 key 断言；`attempt_end_reason` 每个已创建 attempt 恰好一个；五值 `capture_state` 各有用例；免费/失败/超时等结算摘要字段本计划不出现（plan-6 加）。
- [ ] **Step 2: 实现并跑绿** Run: `go test ./service/langfuse -race -v`
- [ ] **Step 3: Commit**

```bash
git add service/langfuse/
git commit -m "feat(langfuse): finish/freeze/worker materialization with root-first ordering"
```

---

### Task 6: Controller 统一 finalizer 与接入

**Files:**
- Modify: `controller/relay.go`
- Test: `controller/relay_langfuse_test.go`

**改造（§5.3 逐条；先读当前 L71-255 全文再动手）：**

1. 顶部 var 块扩为：

```go
	var (
		newAPIError        *types.NewAPIError
		ws                 *websocket.Conn
		relayInfo          *relaycommon.RelayInfo
		recorder           *langfuse.Recorder
		billingPhaseReached bool
	)
```

2. `GenRelayInfo` 调用改赋值：`relayInfo, err = relaycommon.GenRelayInfo(c, relayFormat, request, ws)`；错误分支不变。
3. `GenRelayInfo` 成功后：`recorder = langfuse.Begin(c, relayInfo, request)`。
4. **删除**现有错误响应 defer（L92-110）与退款 defer（L173-182），在原错误响应 defer 的位置（Realtime `defer ws.Close()` 之后）注册统一 finalizer：

```go
	defer func() {
		panicValue := recover() // 仅检测,不吞
		if newAPIError != nil && billingPhaseReached && relayInfo != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
		if panicValue == nil && newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{"type": "error", "error": newAPIError.ToClaudeError()})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{"error": newAPIError.ToOpenAIError()})
			}
		}
		if recorder != nil {
			if panicValue != nil {
				langfuse.EndAttempt(c, relayInfo, nil) // 防御性收口 active attempt;EndReason 由 Finish 阶段覆盖为 lifecycle_panic
			}
			langfuse.Finish(c, relayInfo, newAPIError)
		}
		if panicValue != nil {
			panic(panicValue) // 交给外层 Gin Recovery;不得吞掉
		}
	}()
```

（实现时与现状逐字核对：保持既有日志与响应语句原样搬入；`lifecycle_panic` 标记：panic 路径 Finish 前在 Recorder 上置 panic 状态——通过 `Finish` 前调用包内 `MarkLifecyclePanic(c)` 小函数实现，闭合枚举 `attempt_end_reason=lifecycle_panic` 写入 active attempt。）

5. `billingPhaseReached = true` 只在 `if priceData.FreeModel { ... } else { PreConsumeBilling... }` 整个分支后的统一位置（两分支汇合处）。
6. retry loop：switch handler 调用后、`if newAPIError == nil` 判定**之前**插入：

```go
	langfuse.EndAttempt(c, relayInfo, newAPIError)
```

7. import `service/langfuse`。

- [ ] **Step 1: 失败测试**（§14.3 controller 回归，尽量用现有 controller 测试 harness；无法起完整 relay 时对 `Relay` 函数注入 stub handler）：
  1. 顺序断言：计费阶段失败时调用序严格 `NormalizeViolationFeeError → Refund → ChargeViolationFeeIfNeeded → SetMessage/错误写入 → Finish`（用 hook/monkey 不可行则以 Recorder 侧记录的最终错误正文 + rootEnd 时序替代：断言 root output 含归一化后的最终错误 JSON 与 request ID）。
  2. 阶段守卫：请求校验失败 / `GenRelayInfo` 失败 / 预扣失败 → 不进退款/违规费（relayInfo nil 或未置位路径），且无 trace（Begin 未调用——校验失败时）。
  3. 成功路径：finalizer 无错误写入、Finish 恰好一次。
  4. 业务 panic：handler 写出部分正文后 panic → 同一 panic value 重新抛出、`lifecycle_panic` 标记、root output 不含 Gin Recovery 500（冻结后写入）、客户端收到 500（Gin Recovery 写）。
  5. Realtime 顺序：`WssError` 在 `ws.Close` 之前（Realtime 无 Recorder——格式不支持，断言 Begin 返回 nil 且无 trace）。
  6. Playground：`/pg/chat/completions` 成功请求 → root metadata `is_playground=true`。
  7. channel test：`controller/channel-test.go` 路径不产生 root/generation（hook nil no-op）。
  8. 413/400 防御性单测：`GetBodyStorage` 失败路径 attempt 数为 0（用可控 mock；若现有 harness 不可行则降级为 BeginAttempt 未被调用的单元级断言并注明）。
- [ ] **Step 2: 实现并跑绿** Run: `go test ./controller -run 'TestRelay|TestLangfuse' -v && go test ./service/... ./relay/... && go build ./...`
- [ ] **Step 3: Commit**

```bash
git add controller/relay.go controller/relay_langfuse_test.go
git commit -m "feat(langfuse): unified relay finalizer with billing phase and recorder finish"
```

---

### Task 7: 收尾

- [ ] Run: `go test ./service/langfuse/... -race && go test ./... （环境允许） && cd relaykit && GOWORK=off go build ./...`
- [ ] 核对包边界测试仍绿（新增 import 未破坏白名单）。

## Self-Review 已核对

- §5.1 三阶段/有界重试/traceID 派生 → Task 3；§5.2 hook 唯一性/supersede/offset/渠道名 fallback → Task 4；§5.2 冻结 happens-before/panic 边界 → Task 5 + plan-4 writer join 语义；§5.3 finalizer/billingPhaseReached/panic 语义 → Task 6；§6.1/6.2（除 usage/cost）/§8.1–8.3/§13 denylist → Task 5；§14.1 recorder 行与 §14.3 → 各任务用例。AWS/Xunfei root-only 的结算侧 `no_active_attempt` 标记在 plan-6（RecordUsage 无 active attempt 时写）。
