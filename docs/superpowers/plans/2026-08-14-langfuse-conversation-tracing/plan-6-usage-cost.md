# Plan 6 — 最终 Usage/Cost 结算接入与互斥桶归一化

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) 或 superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §8.4/§8.5 与 §16.6——`UsageRecord` 归一化为 Langfuse 互斥 usage 桶、`SettleBilling` 调用点快照 quota/`common.QuotaPerUnit`、权威 cost 换算、`langfuse.observation.model.name` 硬契约、root `quota`/`billing_source` 成对摘要、`no_active_attempt` 降级。前置：plan-0 已合入（`summary.InputExcludesCache` 可用）。

**Architecture:** 纯归一化函数放 `service/langfuse/usage.go`（白名单内，只依赖 `relaykit/dto` 值）；结算调用点改动在 `service/text_quota.go` 与 `service/quota.go`（root `service` 导入 `service/langfuse` 合法——禁令是 `service/langfuse` 闭包不得含根 `service`，方向相反）。属性策略补在 `attributes.go`。

**Tech Stack:** Go + testify 表驱动。

## Global Constraints

- 见 `00-master.md`。本计划额外硬约束：
  - `total == sum(所有非 total 桶)`，checked addition；负源值/溢出/未知语义 → 整组省略 + 闭合 reason，不部分导出。
  - canonical keys：`input_cache_creation`、`output_audio_tokens`（内建口径）；`input_image_tokens`、`input_audio_tokens`（New API 永久自定义 usage type）。禁止 `cache_creation`/`input_cache_write` 等变体。
  - cost JSON number 由 `common.Marshal` 生成；不导出负成本；`Quota=1, QuotaPerUnit=500000` → 精确 `{"total":0.000002}`；`Quota=0` → `{"total":0}`；合法科学计数法不得拒绝、不得写成 string。
  - **不得**把 `text_quota.go` 的 cache creation 计价档位判断（`!IsClaudeUsageSemantic && !legacyClaudeDerived`）替换为 `InputExcludesCache`；不得读取 `UsageSemantic`/channel 声明/`UpstreamPromptTokensIncludeCache` 重新推导 Recorder 口径。
  - tiered 四参行为由 plan-0 的独立回归保护，本计划不重算 quota。

## 闭合 reason 枚举（usage.go 常量）

```go
const (
	UsageOmittedUnavailable     = "usage_unavailable"
	UsageOmittedNoActiveAttempt = "no_active_attempt"
	UsageOmittedInvalidSource   = "invalid_source"
	UsageOmittedOverflow        = "arithmetic_overflow"
	UsageOmittedSemanticUnknown = "semantic_unknown"

	CostOmittedAttemptFailed          = "attempt_failed"
	CostOmittedAttemptSuperseded      = "attempt_superseded"
	CostOmittedSettlementUnavailable  = "settlement_unavailable"
	CostOmittedSettlementFailed       = "settlement_failed"
	CostOmittedNoBillableUsage        = "no_billable_usage"
	CostOmittedInvalidQuota           = "invalid_quota"
	CostOmittedInvalidQuotaPerUnit    = "invalid_quota_per_unit"
)
```

---

### Task 1: `normalizeUsageBuckets` 纯函数

**Files:**
- Create: `service/langfuse/usage.go`
- Test: `service/langfuse/usage_test.go`

**Interfaces:**
- Produces:

```go
// 返回导出的互斥桶(键见常量)与省略原因/标记;rec.Available==false 时两者皆零值。
// flags 含 "usage_input_clamped"/"usage_output_clamped"。
func normalizeUsageBuckets(rec UsageRecord) (buckets map[string]int, omitReason string, flags []string)
// cost JSON: Settled && Quota>=0 && QuotaPerUnit 有限正 → {"total": Quota/QuotaPerUnit};否则 ("", false)。
func costDetailsJSON(rec UsageRecord) (string, bool)
// 按闭合集合与优先级计算 cost_omitted_reason(§8.2 顺序)。
func costOmittedReason(a *attemptValue) string
```

规则实现（§8.4 逐条）：
- 文本（`Kind=UsageKindText`）：
  - 负源值检查（rec 各 token 字段任一 <0）→ `invalid_source`；`UsageSemanticUnknown` → `semantic_unknown`。
  - `InputExcludesCache=false`：`input = InputTokens - InputCachedTokens - InputCacheWriteTokens - InputImageTokens`，`InputAudioTokens>0` 再减；`output = OutputTokens - OutputReasoningTokens`。
  - `InputExcludesCache=true`：不减 cache 桶，仍无条件减 `InputImageTokens`（audio>0 再减）；`output` 同上。
  - `InputCacheWriteTokens` 口径由**调用点**填好（inclusive→summary.CacheCreationTokens；exclusive→checked `max(CacheCreation, 5m+1h)`，本函数不重算）。
  - clamp：input/output 减为负 → 0 + 对应 flag；`cached+cache_write>prompt` 是正常形态。
  - `Kind=text` 忽略 `OutputAudioTokens`（不导出 `output_audio_tokens`，该 token 留在 output）。
  - 桶集（已知 0 也保留）：`input`、`output`、`total`、`input_cached_tokens`、`input_cache_creation`、`input_image_tokens`、`output_reasoning_tokens`；`input_audio_tokens` 仅 `InputAudioTokens>0` 时。
- 音频（`Kind=UsageKindAudio`）：`input=InputTokens(text)`、`output=OutputTokens(text)`、`input_audio_tokens`、`output_audio_tokens`（0 也保留）；不臆造 cache/image/reasoning 桶。
- `total` = checked sum(全部非 total 桶)；溢出 → `arithmetic_overflow` 整组省略。
- `costDetailsJSON`：`total := float64(rec.Quota) / rec.QuotaPerUnit`，`common.Marshal(costDetails{Total: total})`；`QuotaPerUnit<=0/NaN/Inf` → false。

- [ ] **Step 1: 失败测试**——§14.1 usage 表 **14 行逐行落成表驱动**（期望桶写成 `map[string]int` 字面量；行内 `cached`=`input_cached_tokens`、`cache creation`=`input_cache_creation`），另加：全零真实 usage → 保留三个 0；负值/溢出（构造 `InputTokens=math.MaxInt`…使 sum 溢出）/semantic unknown → 整组省略 + reason；`Kind=text + OutputAudioTokens>0` → 无 `output_audio_tokens` 且 output 不减 25；cost 边界 `1/500000 → {"total":0.000002}`（字符串逐字）、`0 → {"total":0}`、科学计数法汇率（`QuotaPerUnit=1e-3` 类）产物仍是 JSON number 可解析、`Quota=-1`/`QuotaPerUnit=0` → false。
- [ ] **Step 2: 实现并跑绿** Run: `go test ./service/langfuse -run 'TestUsage|TestCost' -v`
- [ ] **Step 3: Commit**

```bash
git add service/langfuse/usage.go service/langfuse/usage_test.go
git commit -m "feat(langfuse): mutually exclusive usage buckets and authoritative cost encoding"
```

---

### Task 2: `RecordUsage` 与 root 结算摘要

**Files:**
- Modify: `service/langfuse/recorder.go`（实现 plan-5b 预留的 `RecordUsage`）、`attributes.go`（root metadata 增 `quota`/`billing_source`、generation 增 usage/cost/model 属性）
- Test: `service/langfuse/record_usage_test.go`、`attributes_test.go`（追加）

**Interfaces:**
- Produces: `func (r *Recorder) RecordUsage(rec UsageRecord)`（结算函数在 SettleBilling 调用点相邻处调用）。

语义：
- Recorder nil（调用方已判）/ 已 frozen → 忽略。
- 无 active attempt → 丢弃，置 root 级 `usage_unattributed=true`、`usage_omitted_reason=no_active_attempt`（AWS SDK/Xunfei 旁路自然命中）；该 record 不构成 settled 候选。
- 有 active attempt → 锁内存入 `a.Usage = &rec`。
- root 摘要（materialize 时计算）：**当且仅当**全部 attempts 中恰好一个 `Usage.Settled==true && Usage.Quota>=0` → metadata 复制该 record 的 `quota` 与 `BillingSource` 成对；零个/多个/非法 → 两者同时省略；root 永不写 `usage_details`/`cost_details`。

属性策略补全（§6.2 硬契约，materialize/worker 内）：
- 成功 attempt：有 cost → `usage_details` + `cost_details` + `langfuse.observation.model.name`（`ModelName`）；无 cost → 按 `costOmittedReason` 写 reason、`cost_source:"unavailable"`、**省略全部模型识别属性**（含 `gen_ai.response.model` 等 fallback——一律不写）。
- 失败/superseded attempt：无 usage 时可写 model + Error；**有 usage 无 cost 一律省略 model**（负向用例必须存在）。
- metadata：`cost_source:"new_api_settlement"`（有 cost 时）、`settlement_error=true`+`settlement_failed`（SettleBilling 失败）、`no_billable_usage`（文本无 billable usage 的正常省略）、`invalid_quota`/`invalid_quota_per_unit`、`usage_input_clamped/usage_output_clamped`、`usage_unattributed`/`usage_omitted_reason`。

- [ ] **Step 1: 失败测试**：
  1. `no_active_attempt`：无 attempt 时 RecordUsage → root 两个标记、无 attempt 拿到 usage。
  2. 摘要配对五情形（§14.2：恰好一个 settled / 零个 / 多个 / no_active_attempt / 非法 quota）→ 只有第一种成对出现。
  3. OTLP 四类 generation（`Error+model+无usage无cost` 允许 model；`Error+usage+无cost` 省略 model；成功 `usage+无cost` 省略全部 model 属性；成功 `usage+model+cost` 全保留）+ superseded 属第一类带 `attempt_superseded`。
  4. root 永无 usage/cost details（解码断言）。
- [ ] **Step 2/3: 实现跑绿 + Commit**

Run: `go test ./service/langfuse -race -v`

```bash
git add service/langfuse/
git commit -m "feat(langfuse): usage attribution, settlement summary and model-name hard contract"
```

---

### Task 3: `PostTextConsumeQuota` 接入

**Files:**
- Modify: `service/text_quota.go`（`PostTextConsumeQuota`）
- Test: `service/text_quota_langfuse_test.go`

**改造点（在现有语句之间插入，不改既有计费语句）：**

1. `summary := calculateTextQuotaSummary(...)` **之前**：源值校验，仅供 telemetry：

```go
	// telemetry 源值校验:必须直接检查 billingUsage 的原始字段(在 CacheCreationTokensTotal 之前),
	// summary.CacheCreationTokens 已经过负值归零,无法发现负源值。
	usageSourcesValid := checkUsageSourcesValid(billingUsage)
```

`checkUsageSourcesValid`（新增私有函数，检查 `PromptTokens/CompletionTokens/PromptTokensDetails.{CachedTokens,CachedCreationTokens,CacheWriteTokens,ImageTokens,AudioTokens}/ClaudeCacheCreation{5m,1h}Tokens/CompletionTokenDetails.*` 全部 ≥0，nil→false）。

2. `SettleBilling(ctx, relayInfo, summary.Quota)` 调用改为保留返回值（现有代码已 `if err := ...`，改为 `settleErr := ...`），并在其**紧后**（tiered 覆盖完成、`summary.Quota` 已是最终值的位置）：

```go
	if r := langfuse.FromContext(ctx); r != nil {
		cacheWrite := summary.CacheCreationTokens // inclusive: 计费从 baseTokens 扣的正是该口径
		if summary.InputExcludesCache {           // exclusive: 导出 Claude 5m/1h 完整物理总数
			cacheWrite = checkedMaxCacheWrite(summary) // checked max(CacheCreationTokens, 5m+1h),溢出按 CacheCreation
		}
		quotaPerUnit := common.QuotaPerUnit // 与最终 quota 相邻快照,worker 不得重读全局
		r.RecordUsage(langfuse.UsageRecord{
			Kind:                  langfuse.UsageKindText,
			Available:             billingUsage != nil && usageSourcesValid,
			ModelName:             relayInfo.GetUpstreamModelName(),
			InputExcludesCache:    summary.InputExcludesCache, // 只读 fold 后口径,禁止重推导
			InputTokens:           summary.PromptTokens,
			OutputTokens:          summary.CompletionTokens,
			InputCachedTokens:     summary.CacheTokens,
			InputCacheWriteTokens: cacheWrite,
			InputImageTokens:      summary.ImageTokens,
			InputAudioTokens:      summary.AudioTokens,
			OutputReasoningTokens: billingUsageReasoningTokens(billingUsage),
			Quota:                 summary.Quota,
			QuotaPerUnit:          quotaPerUnit,
			BillingSource:         normalizeBillingSource(relayInfo, priceData.FreeModel),
			Settled:               settleErr == nil && summary.hasBillableUsage(),
		})
	}
```

`normalizeBillingSource`：`FreeModel → "free"`；`relayInfo.BillingSource != "" → 原值`；否则 `"unknown"`（空串不冒充钱包，§8.4 原文）。注意 `PostTextConsumeQuota` 当前没有 `priceData` 参数——用 `relayInfo.PriceData.FreeModel`（实现时确认字段可达；`priceData` 在 controller 层，`relayInfo.PriceData` 是同源引用）。

3. `Available=false` 情形（`billingUsage==nil` 或源值非法）：仍调用 RecordUsage（Recorder 借此区分"上游无 usage"），Recorder 对 `Available=false` 不导出 usage（Task 1 已处理）；结算失败仍传已确认 usage 的语义由 `Available` 与 `Settled` 两维独立表达。

- [ ] **Step 1: 失败测试**（service 层，构造 gin ctx + Recorder 安装 + 各 fixture summary）：
  1. 成功结算 → attempt 拿到 `Settled=true`、`Quota==summary.Quota`（tiered 覆盖后的值——构造 tiered 命中场景断言不是覆盖前中间值）、`QuotaPerUnit==快照`。
  2. `hasBillableUsage()==false` 且 Settle 成功 → `Settled=false`（billing session 收口≠实际计费）。
  3. SettleBilling 失败（注入失败 billing）→ `Settled=false`、`Available=true`（usage 仍可导出）、generation 标 `settlement_error`。
  4. `billingUsage==nil` → `Available=false`。
  5. 负源值（构造 `CachedTokens=-5`）→ `usageSourcesValid=false` → Recorder 省 usage，计费照常执行（断言 RecordConsumeLog 仍发生）。
  6. 全局汇率在 RecordUsage 之后被改 → worker 产物用旧快照（直接断言 UsageRecord.QuotaPerUnit 是调用时值）。
- [ ] **Step 2/3: 实现跑绿 + Commit**

Run: `go test ./service -run 'TestPostTextConsumeQuota|TestTextQuotaLangfuse' -v && go test ./service`

```bash
git add service/text_quota.go service/text_quota_langfuse_test.go
git commit -m "feat(langfuse): text settlement usage snapshot at settle point"
```

---

### Task 4: `PostAudioConsumeQuota` 接入 + 音频路由回归

**Files:**
- Modify: `service/quota.go`（`PostAudioConsumeQuota`，SettleBilling 后同模式：`Kind=UsageKindAudio`、`InputTokens=usage.PromptTokensDetails.TextTokens`、`OutputTokens=usage.CompletionTokenDetails.TextTokens`、`InputAudioTokens=PromptTokensDetails.AudioTokens`、`OutputAudioTokens=CompletionTokenDetails.AudioTokens`、`Available=usage!=nil&&源值非负`、`Settled=settleErr==nil`、quota 取 tiered 覆盖后的最终 `quota`；不读/不写 summary 概念）
- Test: `service/quota_langfuse_test.go`、`relay/compatible_handler_test.go`（追加路由回归）

- [ ] **Step 1: 失败测试**：
  1. audio 结算成功 → 四桶 + 权威 cost/model；`totalTokens==0` → quota=0 且 `Settled=true`（结算成功）→ cost `{"total":0}`。
  2. **路由两组独立回归（§14.3，不得共用参数化）**：
     - Chat Completions（含 `chatCompletionsViaResponses` 与普通路径）：token>0 且 input/output 任一 ratio 已配置 → `PostAudioConsumeQuota`；有 token 无 ratio、有 ratio 无 token → `PostTextConsumeQuota`；"有 completion audio token 但无 ratio"断言 `Kind=text` 归一化（不丢 token、无 `output_audio_tokens`）。
     - Responses（非 Compact）：`gpt-4o-audio*` 前缀 → audio；非前缀但带 audio token/ratio → text；Compact 固定 text 不受前缀影响。
- [ ] **Step 2/3: 实现跑绿 + Commit**

Run: `go test ./service ./relay -run 'TestAudio|TestQuotaLangfuse|TestResponses' -v && go build ./...`

```bash
git add service/quota.go service/quota_langfuse_test.go relay/
git commit -m "feat(langfuse): audio settlement usage and split routing regressions"
```

---

## Self-Review 已核对

- §8.4 归一化全部规则/§14.1 表 14 行 → Task 1；`UsageRecord` 字段与快照纪律（quota/QuotaPerUnit 同点、tiered 后取值、worker 不重读全局）→ Task 3/4；§8.5 cost 边界与推价抑制 → Task 1/2；§8.1 成对摘要与 §13 `no_active_attempt` → Task 2；§14.3 音频路由两组 → Task 4。`InputCacheWriteTokens` 的 inclusive=summary.CacheCreationTokens / exclusive=checked max 语义与 §8.4 一致；未触碰计价档位判断。
