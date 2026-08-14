# Plan 4 — Identity、Capture Writer、脱敏截断与 Output 聚合

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §16.4、§5.1 Phase 1（身份提取）、§7、§9——`identity.go`（显式 session 提取/校验/作用域化/闭合 reason 枚举）、`writer.go`（有界 capture writer + 全局捕获预算 CAS）、`content.go`（base64 替换 + 结构层合法截断 + truncation envelope）、`aggregate.go`（四协议 output 聚合）。全部为纯数据面组件，无 OTel、无 gin.Context 依赖的部分尽量纯函数。

**Architecture:** 四个文件都在 `service/langfuse`；`identity`/`content`/`aggregate` 是纯函数（输入 bytes/snapshot，输出值），`writer` 是 gin.ResponseWriter wrapper + 全局 `inFlightCaptureBytes` 预算。plan-5b 的 Recorder 编排它们。

**Tech Stack:** Go, gin, tidwall/gjson, testify。

## Global Constraints

- 见 `00-master.md`。本计划额外约束：
  - 闭合枚举字符串逐字使用 §7/§8.1 的常量（见下方常量表），禁止拼接或自造变体。
  - sanitizer/aggregator 失败日志不得包含被拒绝正文（只允许长度/阶段/错误类别）。
  - `Write` 热路径除"转发 + 有界追加 + offset/截断标记"外不做任何解析/脱敏/gin.Context 访问；capture 侧 bookkeeping panic 必须在 `Write` 内 recover 且不吞底层错误。
  - 所有截断保证 UTF-8 完整。

## 常量（identity/content 共用，定义在各自文件）

```go
// identity.go
const (
	SessionOmittedEmptyAfterTrim       = "empty_after_trim"
	SessionOmittedInvalidUTF8          = "invalid_utf8"
	SessionOmittedInvalidControlChar   = "invalid_control_character"
	SessionOmittedTooLong              = "too_long"
	SessionOmittedUserScopeUnavailable = "user_scope_unavailable"

	SessionBodyOmittedNonJSONContentType  = "non_json_content_type"
	SessionBodyOmittedStorageUnavailable   = "body_storage_unavailable"
	SessionBodyOmittedStorageTypeMismatch  = "body_storage_type_mismatch"
	SessionBodyOmittedSizeUnknown          = "body_size_unknown"
	SessionBodyOmittedTooLarge             = "body_too_large"
	SessionBodyOmittedReadFailed           = "body_read_failed"
	SessionBodyOmittedReadIncomplete       = "body_read_incomplete"
	SessionBodyOmittedInvalidJSON          = "invalid_json"
	SessionBodyOmittedNoSupportedScalar    = "no_supported_scalar"
)
const LangfuseSessionHeader = "X-Langfuse-Session-Id"
```

---

### Task 1: `identity.go` — Header/body 提取、校验、作用域化

**Files:**
- Create: `service/langfuse/identity.go`
- Test: `service/langfuse/identity_test.go`

**Interfaces:**
- Produces（plan-5b 消费）:

```go
type SessionIdentity struct {
	ScopedID     string // "{userId}:{raw}";无合法 session/无正数用户 ID 时 ""
	RawSessionID string // 合法 trim 后原值,仅供内存诊断,绝不进 metadata
	Source       string // "x-langfuse-session-id"/自定义 header 名/gjson path;空=无 session
	OmittedReason        string // SessionOmitted* 或 ""
	BodyOmittedReason    string // SessionBodyOmitted* 或 ""
}
func extractHeaderSession(header func(name string) string, snap langfuse_setting.Snapshot) SessionIdentity
func extractBodySession(c *gin.Context, snap langfuse_setting.Snapshot, reuse []byte) (SessionIdentity, []byte)
func scopeSession(userID int, raw string) (scoped string, ok bool)
```

- [ ] **Step 1: 失败测试**（§14.1 identity 行全表，表驱动；`extractHeaderSession` 注入 header getter，body 用 `gin.CreateTestContext` + `c.Set(common.KeyBodyStorage, storage)`）：

Header 组：
1. `X-Langfuse-Session-Id: " abc "` → raw="abc"（trim），`X-Session-Id` 默认**不读**（配置了才读）；优先级：标准头有 raw 非空值时，即使配置了额外 header 也不用后者。
2. 标准头 raw 非空但非法（如 201 字节）→ OmittedReason=too_long，**不降级**到额外 header/body。
3. 额外 header 按配置顺序取首个 raw 非空；含 `Authorization`/`Cookie` 的配置在 plan-2 已被校验拒绝（此处只测合法名）。
4. trim 后空 → empty_after_trim；含 `\r`/`\n`/`\x01` → invalid_control_character；非法 UTF-8（`"\xff\xfe"`）→ invalid_utf8；>200 字节 → too_long；超长不截断。
5. 全部 header 无 raw 非空候选且未配置 body paths → Source=""、无 OmittedReason（完全未提供不写字段）。

Body 组（仅当"配置了 body paths 且无 raw 非空 header 候选"时进入）：
6. storage 不存在 → body_storage_unavailable；`c.Set(KeyBodyStorage, "not-a-storage")` → body_storage_type_mismatch；`Size()==-1` → body_size_unknown；`Size()` > max_session_body_bytes → body_too_large。
7. Content-Type 非 JSON（`text/plain`）→ non_json_content_type（该判定最先于 storage 检查，按 §7.2 顺序表：`non_json_content_type` 在 `body_storage_unavailable` 之前——若 storage 也缺失，取 non_json_content_type）。
8. 读全量失败（storage 关闭后 `NewReader` 失败）→ body_read_failed；读到字节数 != Size() → body_read_incomplete。
9. `gjson.Valid` false → invalid_json；JSON 合法但 paths 全部 miss / 命中 bool/array/object/null → no_supported_scalar；首个命中的 string/number 锁定来源（后续 path 不再查）。
10. 命中 scalar 但校验失败（超长等）→ 只写对应 `OmittedReason`，不写 BodyOmittedReason。
11. 超限 body 不读（断言 `NewReader` 未被调用——用 mock storage 记录调用）。
12. `reuse` 非空（Phase 2 退休重试用旧缓冲）→ 不再打开 reader，直接对新 paths 重查 gjson。
13. `scopeSession(42,"default")==("42:default",true)`；`scopeSession(0,"x")==("",false)`；用户 1 与 2 相同 raw 得不同 scoped；采样键由 plan-5b 生成。

- [ ] **Step 2: 确认失败** Run: `go test ./service/langfuse -run TestSession -v`
- [ ] **Step 3: 实现**（要点）：
  - Header 候选锁定语义：`raw != ""`（trim 前）即锁定；校验作用于 trim 后值。
  - body 读取：`io.ReadAll(io.LimitReader(r, snap.MaxSessionBodyBytes+1))`，若读满 limit+1 字节即超限（防 Size 谎报）；只调用 `c.Get(common.KeyBodyStorage)`，**绝不**调 `GetBodyStorage`/`GetRequestBody`。
  - scalar 判定：`res.Type == gjson.String || res.Type == gjson.Number`，且 `res.Raw != ""`/数值非零或字符串非空；string 候选需 trim 后非空才算"非空 scalar"。
  - 数字转字符串用 `strconv.FormatFloat(res.Float(), 'f', -1, 64)`（不产生科学计数法歧义即可，写进测试）。
- [ ] **Step 4: 通过 + Commit**

```bash
git add service/langfuse/identity.go service/langfuse/identity_test.go
git commit -m "feat(langfuse): explicit session identity extraction with closed reason enums"
```

---

### Task 2: `writer.go` — capture writer 与全局捕获预算

**Files:**
- Create: `service/langfuse/writer.go`
- Test: `service/langfuse/writer_test.go`

**Interfaces:**
- Produces（plan-5b 消费）:

```go
type CaptureWriter struct {
	gin.ResponseWriter          // 完整委托 Flush/Hijack/CloseNotify/status/size
	mu            sync.Mutex
	buf           []byte
	totalWritten  int64
	truncated     bool
	frozen        bool
	maxCapture    int
	origWriter    gin.ResponseWriter // Begin 时的原始 writer,Finish 恢复用
}
func NewCaptureWriter(orig gin.ResponseWriter, maxCapture int) *CaptureWriter
func (w *CaptureWriter) Write(b []byte) (int, error)
func (w *CaptureWriter) WriteString(s string) (int, error)
func (w *CaptureWriter) WriteHeader(code int)
func (w *CaptureWriter) Flush()
func (w *CaptureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *CaptureWriter) Offsets() (captured, logical int64)   // 锁内快照
func (w *CaptureWriter) Freeze() FrozenCapture                 // 锁内置 frozen,拷贝 buf
type FrozenCapture struct { Buf []byte; TotalWritten int64; Truncated bool }

// 全局捕获预算
func ReserveCaptureBudget(n int64) bool  // CAS add,不超上限
type BudgetReservation struct{ /* 内含 sync.Once */ }
func (b *BudgetReservation) Release()    // 幂等
func ReserveCapture(n int64) *BudgetReservation // nil 表示失败
```

- [ ] **Step 1: 失败测试**（§14.1 writer 行 + §14.1 writer 并发契约 + `Unwrap`）：
  1. **透传与有界缓冲**：写 3×100B 到 maxCapture=250 的 writer → 底层收到全部 300B（自定义底层 recorder 记账），buf==250B 前缀，totalWritten==300，truncated==true。
  2. **ResponseController**：底层 fixture 实现 `SetWriteDeadline(time.Time) error` 并记录值；经 capture writer 后 `http.NewResponseController(cw).SetWriteDeadline(d)` 返回 nil 且底层收到同一 d；不支持 deadline 的底层 → 返回 `http.ErrNotSupported` 且透传不变。
  3. **offset 单调/归属**：BeginAttempt 时刻记录 offsets（模拟）→ 写 chunk A → 记录 → 写 chunk B → Freeze；两次 Freeze 调用结果一致（幂等）；frozen 后再 `Write` → 底层仍收到字节、FrozenCapture 不变、出现一条限频 `late_write_after_freeze` 告警（注入告警 hook 断言，30s 内不重复）。
  4. **并发写**（`-race`）：请求 goroutine + 2 个模拟 ping/data goroutine 各写可识别 chunk（如 `"REQ-"`、`"PING-"`、`"DATA-"` 循环），底层 recorder 记录收到的连接串；join 全部写者后 Freeze：底层串与 FrozenCapture.Buf（未截断场景）逐字节一致、每个 chunk 前缀只出现一次、offset 单调；再用 barrier 让最后一个写者持锁时另一 goroutine 调 `Freeze`，断言 Freeze 等待写完成（FrozenCapture 含最后一次写入）。判序只用 channel/barrier。
  5. **panic 隔离**：注入使 buf 追加 panic 的场景（maxCapture=-1 触发切片越界或注入 appendHook）→ `Write` 返回底层结果不 panic、告警 hook 收到一条含阶段的记录、后续 Write 继续透传。
  6. **预算**：上限 1000，`ReserveCapture(600)`×1 成功、第二个 600 失败、300 成功；Release 幂等（两次调只还一次）；并发 200 goroutine 各 Reserve/Release，任意时刻总和 ≤1000（用 hook 或事后断言无超限日志）。
- [ ] **Step 2: 确认失败**。Run: `go test ./service/langfuse -race -run 'TestCapture|TestBudget' -v`
- [ ] **Step 3: 实现**（要点）：
  - `Write`：`w.mu.Lock(); defer Unlock()`；临界区内先 `n, err := w.ResponseWriter.Write(b)`；仅当 err==nil 才追加 `min(len(b), 余量)`、`totalWritten += int64(n)`、余量不足置 truncated；整个临界区 body 包在 `func(){ defer recover(){...告警+放弃本次捕获状态更新...} }()` 内，**recover 后仍返回 (n, err)**。
  - `WriteString` 复用 `Write`（与 audit.go 一致）。
  - `WriteHeader`/`Flush` 加同一把锁后委托。
  - 告警 hook：包级 `var captureWarn = func(stage string, keys ...string) {}`（生产实现按 request/阶段限频，且不含正文），测试注入。限频器实现为固定时间窗 + 哈希桶的无锁有界结构（`sync.Map` + 每 key 上次时间戳即可满足"低争用"）。
  - 预算：`var inFlightCaptureBytes, maxInFlightCaptureBytes atomic.Int64`；上限值由 runtime 发布时随 binding 更新（plan-5b 接线，本任务暴露 `SetMaxInFlightCaptureBytes`）。
- [ ] **Step 4: 通过 + Commit**

```bash
git add service/langfuse/writer.go service/langfuse/writer_test.go
git commit -m "feat(langfuse): bounded capture writer with budget and race-safe offsets"
```

---

### Task 3: `content.go` — 脱敏与结构层合法截断

**Files:**
- Create: `service/langfuse/content.go`
- Test: `service/langfuse/content_test.go`

**Interfaces:**
- Produces（plan-5b worker 与 aggregate 消费）:

```go
// SanitizeJSON 解析完整 JSON,替换 data:...;base64 与已知 inline data 字段,
// 再按 limit(最终 OTel string attribute 的 JSON 转义贡献字节数)做结构层缩减。
// 解析失败返回 (nil, false)。
func SanitizeJSON(raw []byte, limit int) ([]byte, bool)
// SanitizeRawText 对本来就不是 JSON/SSE 的 raw 文本做 UTF-8 边界截断 + 省略标记。
func SanitizeRawText(raw []byte, limit int) []byte
// TruncationEnvelope 生成合法最小 envelope,originalBytes 为原始捕获字节数。
func TruncationEnvelope(originalBytes int) []byte
// jsonEscapedLen 返回 s 作为 JSON string attribute 的转义贡献(len(带引号编码))。
func jsonEscapedLen(s string) int
// RedactSSEPayload 对单个 SSE data: payload(应为 JSON)做脱敏;失败返回原文与 false。
func RedactSSEPayload(payload []byte) ([]byte, bool)
```

- [ ] **Step 1: 失败测试**（§14.1 content 行）：
  1. **base64 替换**：嵌套 JSON 中 `"image":"data:image/png;base64,AAAA..."`（1KB）→ 替换为 `{"_langfuse_redacted":true,"media_type":"image/png","approx_bytes":<原长度>}`；`data:audio/wav;base64,...` 同理；`input_audio.data` 字段（非 data URL 的裸 base64）也替换且保留 `format`；占位符不保留内容。
  2. **结构层缩减**：构造含 3 个超长（>limit）字符串叶子的 JSON，limit=256 → 缩减后 `common.Unmarshal` 可解析、`jsonEscapedLen(string(out))` ≤ limit（注意：输出整体是 JSON 文档，计量的是**该文档作为 string attribute 的转义贡献**）、顶层含 `_langfuse_truncated:true` 与 `_langfuse_omitted_bytes`≥0。
  3. **最坏转义**：全文由 `"`、`\`、``-``、多字节 UTF-8 组成的 64KB 文档，limit=4096 → 输出仍合法 JSON 且转义贡献 ≤ limit（证明不能裸字节切片）。
  4. **Envelope**：无法在结构层救回（单字符串超限且回退仍超）→ `{"_langfuse_truncated":true,"_langfuse_original_bytes":N}`，可解析。
  5. **RawText**：含多字节字符的文本在 UTF-8 边界截断，尾缀含省略字节数标记；输入本就 ≤limit 原样返回。
  6. **SSE payload**：`data: {"choices":[{"delta":{"content":"data:image/jpeg;base64,..."}}]}` → payload JSON 内替换成功且结构不变。
- [ ] **Step 2: 确认失败**。
- [ ] **Step 3: 实现**（要点，全部纯函数）：
  - 解析用 `common.Unmarshal(raw, &v any)`；树上遍历（`map[string]any`/`[]any`/`string`）替换 `dataURLRe = regexp.MustCompile(`^data:([^;,]+);base64,`)` 命中的叶子与 key 为 `data` 且父 key 为 `input_audio`（及 `input_audio` map 内 `data` 成员）的字符串。
  - 缩减循环（§9.1 固定顺序）：`for jsonEscapedLen(string(cur)) > limit { 一轮缩减; re-marshal }`，每轮优先级：超长字符串叶子（UTF-8 边界替换为 `"_langfuse_omitted:<N> bytes_"`）→ 丢尾部 array element → 丢低优先级 object field（按字段名字典序从尾部丢，系统字段 `_langfuse_*` 与 `role`/`content`/`type` 优先保留）→ 仍超限则 Envelope。顶层 array 缩减后在尾部追加 `{"_langfuse_truncated":true,...}`。循环上限 32 轮防死循环，超限退 Envelope。
  - `jsonEscapedLen(s)`：`b, _ := common.Marshal(s); return len(b)`。
  - panic 边界：`SanitizeJSON` 最外层 `defer recover()` 返回 `(nil, false)` + 限频告警（不含正文）。
- [ ] **Step 4: 通过 + Commit**

```bash
git add service/langfuse/content.go service/langfuse/content_test.go
git commit -m "feat(langfuse): structured redaction and legal json truncation"
```

---

### Task 4: `aggregate.go` — 四协议 output 聚合

**Files:**
- Create: `service/langfuse/aggregate.go`、fixtures 目录 `service/langfuse/testdata/`
- Test: `service/langfuse/aggregate_test.go`

**Interfaces:**
- Produces（plan-5b worker 消费；输出是"值"（map/slice/string），worker 再经 SanitizeJSON/Envelope 限长）:

```go
// kind: "openai"|"claude"|"responses"|"gemini";isStream 为入站请求流式标志。
// body 为该 attempt/root 的完整捕获字节。ok=false 表示无法识别(调用方按 §9.3 处置)。
func AggregateOutput(kind string, isStream bool, body []byte) (any, bool)
```

- [ ] **Step 1: fixtures**——在 `testdata/` 放真实形态 fixture（每协议 stream 与非 stream 各 ≥1，含 tool call / thinking / functionCall；从各 handler 的响应构造代码或上游文档摘取真实片段，长度适中）：`openai_chat.json`、`openai_chat_sse.txt`、`claude.json`、`claude_sse.txt`、`responses.json`、`responses_sse.txt`、`gemini.json`、`gemini_sse.txt`、`unknown_blob.txt`。
- [ ] **Step 2: 失败测试**（§14.1 aggregate 行；对每个 fixture 断言精确输出结构——期望值写成 Go 字面量）：

| kind | 非流式提取 | 流式提取 |
|---|---|---|
| openai | `choices[0].message` 的 `{role?, content, reasoning_content?, tool_calls[]}`（tool_calls 保留 id/type/function.{name,arguments}）；usage 等**不**提取（聚合不得重算 usage） | 逐 `data:` 行 parse JSON（跳过 `[DONE]`），按 index 合并 `delta.content`/`delta.reasoning_content`/`delta.tool_calls`（增量 arguments 字符串拼接），输出与非流式同形 |
| claude | `content[]` 块按 type 归并：`text`→拼接文本、`thinking`→单独字段、`tool_use`→保留完整块；附 `stop_reason` | SSE 事件流 `content_block_delta`（text_delta/thinking_delta/input_json_delta 按 index 累积）重组为非流式同形，`message_delta` 取 stop_reason |
| responses | `output[]` 中 `type=="message"` 的 `content[].text` 拼接 + `type=="function_call"` 块（name/arguments/call_id） | 事件流：`response.output_text.delta` 的 delta 拼接、`response.output_item.added`(function_call) 收集、`response.completed` 的最终结构兜底 |
| gemini | `candidates[0].content.parts[]`：`text` 拼接、`functionCall` 块保留；附 `finishReason` | SSE `data:` 每项为同形响应，parts 增量拼接 |

  - 未知 blob（非 JSON/SSE 结构）→ `(原文, false)`；空 body → `(nil, false)`；SSE 行 JSON 非法 → 该行跳过，全部行非法 → false。
- [ ] **Step 3: 实现**：每协议两个函数（`aggregateX(body []byte)` / `aggregateXSSE(body []byte)`）+ `AggregateOutput` 分发。SSE 拆行用 `bufio.Scanner`（放大 buffer 上限 1MiB）；每行 `data: ` 前缀剥离后 `common.Unmarshal`。各函数最外层 `defer recover()` → `(nil, false)`。不读 usage、不读价格、不写全局。
- [ ] **Step 4: 通过 + Commit**

Run: `go test ./service/langfuse -v && go build ./...`

```bash
git add service/langfuse/aggregate.go service/langfuse/aggregate_test.go service/langfuse/testdata/
git commit -m "feat(langfuse): per-protocol output aggregation with fixtures"
```

---

## Self-Review 已核对

- §7 的优先级/锁定/拒绝/作用域/两条闭合枚举 → Task 1 用例 1–13；§9.1 有界读取/64KiB session 上限/raw fallback UTF-8 → Task 1/3；§9.2 → Task 3；§9.3 四协议 + raw 保留 → Task 4；§13 多层 writer/Unwrap/身份恢复（恢复语义在 plan-5b Finish，本计划只提供 origWriter 与测试）→ Task 2。writer 级 mutex、join、Write 硬约束、panic 边界、预算 CAS 均有 `-race` 用例。
- 依赖检查：`gjson`（tidwall）经 `relay/common` 闭包已存在于 go.sum——实现时若 `go list` 显示需要显式依赖，允许直接引入 `github.com/tidwall/gjson`（外部库不受白名单限制）。
