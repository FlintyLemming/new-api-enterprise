# Plan 0 — Tiered Billing / Channel Cache Semantic 前置改动

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地设计文档 §16.0/§8.4 的独立计费前置改动：新增渠道声明 `cache_prompt_token_semantic`，为 `textQuotaSummary` 增加 `UpstreamPromptTokensIncludeCache`（raw 口径）与 `InputExcludesCache`（fold 后口径）两个字段，并把 `BuildTieredTokenParams` 改为 4 参签名。**这是独立 PR**（从 `origin/main` 切 `feat/channel-cache-semantic` 分支），Langfuse 计划（plan-6）只消费其契约。

**Architecture:** 渠道设置字段放在 `relaykit/dto/channel_settings.go`（照 `anthropic_messages_exclude_cache` patch 的模式，relaykit 保持独立构建）；声明解析与 fold 在 `service/text_quota.go` 的 `calculateTextQuotaSummary`；`BuildTieredTokenParams` 在 `service/tiered_settle.go` 加第 4 参。**不得**把唯一的 cache creation 计价档位判断（`!summary.IsClaudeUsageSemantic && !summary.legacyClaudeDerived` 控制的扁平 vs 5m/1h 拆分）替换为 `InputExcludesCache`。

**Tech Stack:** Go + testify；前端 Bun/React（渠道抽屉一个 Select）。

## Global Constraints

- 见 `00-master.md`。本计划额外约束：
  - `relaykit/` 只允许加 `ChannelSettings` 字段及其单测，改完必须 `cd relaykit && GOWORK=off go build ./... && GOWORK=off go test ./dto`。
  - 本 PR **不改** Langfuse 任何代码，不引入 telemetry。
  - 计费行为变化仅限：声明了 `prompt_excludes_cache` 的非 Claude 渠道不再从 base tokens 扣减 cache；声明了 `prompt_includes_cache` 的渠道在 summary 层 fold。未声明的所有路径行为必须逐字节不变（用现有表驱动测试证明）。

## 语义定义（实现的唯一权威，测试照此写）

记 `A = summary.IsClaudeUsageSemantic`，`L = legacyClaudeDerived`（`isLegacyClaudeDerivedOpenAIUsage` 现有实现，保持不变）。

- **raw 口径** `promptTokensIncludeCache`：
  - 渠道声明非空时：声明为 `prompt_includes_cache` → `true`；`prompt_excludes_cache` → `false`（声明优先，`declaredCacheSemantic=true`）。
  - 未声明（`declaredCacheSemantic=false`）：自动判定 `!(A || L)`（OpenAI inclusive → true；Anthropic/legacy/OpenRouter-Claude → false）。
- **summary fold**（只改 `summary.PromptTokens`，不动 billing 小数变量）：
  - `isOpenRouterClaudeBilling` 分支：保持现有代码不动，分支末尾把局部 `promptTokensIncludeCache` 置 `false`。
  - 否则若 `promptTokensIncludeCache && declaredCacheSemantic`（声明为 includes）：`summary.PromptTokens -= summary.CacheTokens; summary.PromptTokens -= summary.CacheCreationTokens`，再 `if summary.PromptTokens < 0 { summary.PromptTokens = 0 }`，然后置 `promptTokensIncludeCache = false`。
  - 其他情况不 fold。
- **导出字段**：进入 `calculateTextQuotaSummary` 时（fold 前）`summary.UpstreamPromptTokensIncludeCache = promptTokensIncludeCache`；函数内 fold 全部完成后 `summary.InputExcludesCache = !promptTokensIncludeCache`。
- **billing 扣减条件**：`calculateTextQuotaSummary` 的 `!relayInfo.PriceData.UsePrice` 块中，cache read / cache creation 从 `baseTokens` 扣减的条件由 `!A && !L` 改为 fold 后的 `promptTokensIncludeCache` 局部变量；cache creation 的"扁平倍率 vs Claude 5m/1h 拆分"选择**保持** `A || L`（即原来的 else 分支条件）不变，新增"既不扣减也不是 Claude 档"的第三种情况走扁平倍率（声明 excludes 的非 Claude 渠道）。
- **`BuildTieredTokenParams` 第 4 参** `promptTokensIncludeCache bool`（raw 口径）：非 Claude 语义下从 `P` 扣减各子桶的条件由原来的非 Claude 语义改为 `!isClaudeSemantic && promptTokensIncludeCache`；Claude 语义分支不变。

各路径期望值（§8.4 契约，写成表驱动）：

| 路径 | Upstream 包括? | summary.PromptTokens | InputExcludesCache | billing 是否再扣 |
|---|---|---|---|---|
| 未声明 OpenAI（inclusive） | true | 保持 raw（含 cache） | false | 是（现状不变） |
| 声明 includes（非 Claude） | true | fold 后（扣 cache） | true | 否 |
| 声明 excludes（非 Claude） | false | raw（本就 exclusive） | true | 否（**行为变化点**） |
| Anthropic / legacy Claude-derived | false | raw exclusive | true | 否（现状） |
| OpenRouter Claude | true→fold | 现有 fold | true | 否（现状） |

---

### Task 1: relaykit 增加 `CachePromptTokenSemantic` 渠道设置字段

**Files:**
- Modify: `relaykit/dto/channel_settings.go`（在 `anthropic_messages_exclude_cache` 字段旁）
- Test: `relaykit/dto/channel_settings_test.go`

**Interfaces:**
- Produces: `dto.ChannelSettings.CachePromptTokenSemantic string`，json tag `cache_prompt_token_semantic,omitempty`；合法值 `""`、`"prompt_includes_cache"`、`"prompt_excludes_cache"`（校验在根模块 service 层做，relaykit 只承载字段）。

- [ ] **Step 1: 写失败测试**（照 `channel_settings_test.go` 中现有字段的编码往返测试风格新增一条：设置该字段后 marshal 结果包含 `"cache_prompt_token_semantic":"prompt_includes_cache"`，零值时 `assert.NotContains`）

- [ ] **Step 2: 运行确认失败**

Run: `cd relaykit && GOWORK=off go test ./dto -run TestChannelSettings -v`
Expected: FAIL（字段不存在，编译错误）

- [ ] **Step 3: 加字段**

在 `relaykit/dto/channel_settings.go` 的 `ChannelSettings` 中，紧邻 `AnthropicMessagesExcludeCache` 增加：

```go
// CachePromptTokenSemantic declares whether the upstream provider counts
// cached prompt tokens inside usage.prompt_tokens. Empty means auto-detect
// from usage semantic; "prompt_includes_cache"/"prompt_excludes_cache"
// are explicit channel declarations that take priority.
CachePromptTokenSemantic string `json:"cache_prompt_token_semantic,omitempty"`
```

- [ ] **Step 4: 测试通过 + relaykit 独立构建**

Run: `cd relaykit && GOWORK=off go test ./dto && GOWORK=off go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add relaykit/dto/channel_settings.go relaykit/dto/channel_settings_test.go
git commit -m "feat(routing): add channel cache_prompt_token_semantic setting field"
```

---

### Task 2: `resolvePromptCacheInclusion` 与 summary 字段/fold（TDD）

**Files:**
- Modify: `service/text_quota.go`（`textQuotaSummary` 结构体 ~L41-69、`calculateTextQuotaSummary` ~L231-385）
- Test: `service/text_quota_test.go`（新增表驱动测试；若该文件不存在则新建）

**Interfaces:**
- Consumes: `relayInfo.HasChannelMeta()`、`relayInfo.ChannelSetting.CachePromptTokenSemantic`（Task 1）。
- Produces（plan-6 Langfuse 依赖的契约，签名不得偏离）:
  - `func resolvePromptCacheInclusion(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) (promptTokensIncludeCache bool, declaredCacheSemantic bool)`
  - `textQuotaSummary.UpstreamPromptTokensIncludeCache bool`
  - `textQuotaSummary.InputExcludesCache bool`

- [ ] **Step 1: 写失败测试**

表驱动覆盖上面的"各路径期望值"表 5 行 + `relayInfo == nil` / `ChannelMeta == nil` 时按未声明自动判定。测试直接构造 `*relaycommon.RelayInfo`（`ChannelMeta` 为 nil 或带 `ChannelSetting`）、`*dto.Usage`（含 `UsageSemantic`、`ClaudeCacheCreation*Tokens` 等字段），调用 `calculateTextQuotaSummary`（价格数据按现有测试的 fixture 构造），断言三个值：

```go
func TestCalculateTextQuotaSummary_CacheSemantic(t *testing.T) {
	cases := []struct {
		name           string
		semantic       string // usage.UsageSemantic
		declared       string // ChannelSetting.CachePromptTokenSemantic
		legacyClaude   bool   // 触发 isLegacyClaudeDerivedOpenAIUsage 的 5m/1h 字段
		openRouter     bool
		rawPrompt      int
		cacheRead      int
		cacheCreation  int
		wantSummaryPT  int // fold 后 summary.PromptTokens
		wantUpstreamIn bool
		wantExcludes   bool
	}{
		{name: "undeclared openai inclusive", semantic: "openai", rawPrompt: 1000, cacheRead: 800,
			wantSummaryPT: 1000, wantUpstreamIn: true, wantExcludes: false},
		{name: "declared includes non-claude", semantic: "openai", declared: "prompt_includes_cache",
			rawPrompt: 1000, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 100, wantUpstreamIn: true, wantExcludes: true},
		{name: "declared excludes non-claude", semantic: "openai", declared: "prompt_excludes_cache",
			rawPrompt: 200, cacheRead: 800,
			wantSummaryPT: 200, wantUpstreamIn: false, wantExcludes: true},
		{name: "anthropic semantic", semantic: "anthropic", rawPrompt: 200, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 200, wantUpstreamIn: false, wantExcludes: true},
		{name: "legacy claude derived", semantic: "openai", legacyClaude: true, rawPrompt: 1000, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 1000, wantUpstreamIn: false, wantExcludes: true},
		// OpenRouter Claude 行用现有 OpenRouter 表驱动用例所在文件的构造方式补一条,
		// 期望与现状一致:summary fold 后 prompt 为 raw-cacheRead-cacheCreation,InputExcludesCache=true。
	}
	// ... 每行:构造 relayInfo/usage,调用 calculateTextQuotaSummary,
	// require.NoError/assert.Equal 断言 summary.PromptTokens / UpstreamPromptTokensIncludeCache / InputExcludesCache。
	// declared includes 且 raw-cache < 0 时断言 clamp 为 0(补一行 negative-clamp 用例)。
}
```

fixture 构造方式参考 `service/tiered_settle_test.go` 里现有构造 `RelayInfo`+`PriceData` 的写法（同包内已有 helper 可复用则复用）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./service -run TestCalculateTextQuotaSummary_CacheSemantic -v`
Expected: FAIL（字段/函数不存在）

- [ ] **Step 3: 实现**

`textQuotaSummary` 增加两字段（放在 `IsClaudeUsageSemantic` 旁）：

```go
	IsClaudeUsageSemantic          bool
	UpstreamPromptTokensIncludeCache bool // fold 前 raw upstream prompt 口径(声明优先),仅供 tiered billing
	InputExcludesCache             bool // fold 后 summary.PromptTokens 的实际口径,仅供 telemetry 消费
```

`text_quota.go` 新增（放在 `isLegacyClaudeDerivedOpenAIUsage` 旁，`usage` 为 nil 时按未声明处理）：

```go
const (
	cacheSemanticIncludes = "prompt_includes_cache"
	cacheSemanticExcludes = "prompt_excludes_cache"
)

// resolvePromptCacheInclusion reports whether the raw upstream prompt tokens
// include cache read/write counts. A channel's cache_prompt_token_semantic
// declaration takes priority over usage-semantic auto-detection.
func resolvePromptCacheInclusion(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) (bool, bool) {
	if relayInfo.HasChannelMeta() {
		switch relayInfo.ChannelSetting.CachePromptTokenSemantic {
		case cacheSemanticIncludes:
			return true, true
		case cacheSemanticExcludes:
			return false, true
		}
	}
	// 自动判定:Anthropic 语义与 legacy Claude-derived OpenAI 均为 exclusive 口径。
	if usage != nil && usage.UsageSemantic == "anthropic" {
		return false, false
	}
	if isLegacyClaudeDerivedOpenAIUsage(relayInfo, usage) {
		return false, false
	}
	if usage != nil && usage.UsageSemantic == "openai" {
		return true, false
	}
	// semantic 未标注时沿用现有计费口径:非 Claude relay format 视为 inclusive,
	// Claude relay format 视为 exclusive(与 usageSemanticFromUsage 的推导一致)。
	if usage != nil && relayInfo.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		return false, false
	}
	return true, false
}
```

（注意：`usageSemanticFromUsage` 现有实现顺序是先看 `usage.UsageSemantic`、再看 relay format；上面的自动判定必须与其对齐——若实现时发现两者推导可能不一致，以 `usageSemanticFromUsage(relayInfo, usage) == "anthropic"` 作为唯一判定表达式重写自动分支，保持单一事实来源。）

`calculateTextQuotaSummary` 中，在 `legacyClaudeDerived := ...` 与 `isOpenRouterClaudeBilling := ...` 之后插入：

```go
	promptTokensIncludeCache, declaredCacheSemantic := resolvePromptCacheInclusion(relayInfo, usage)
	summary.UpstreamPromptTokensIncludeCache = promptTokensIncludeCache
```

OpenRouter 分支（现有代码，~L271-281）末尾追加一行 `promptTokensIncludeCache = false`，并紧跟其后增加声明 includes 的 summary fold：

```go
	if promptTokensIncludeCache && declaredCacheSemantic {
		summary.PromptTokens -= summary.CacheTokens
		summary.PromptTokens -= summary.CacheCreationTokens
		if summary.PromptTokens < 0 {
			summary.PromptTokens = 0
		}
		promptTokensIncludeCache = false
	}
	summary.InputExcludesCache = !promptTokensIncludeCache
```

`!relayInfo.PriceData.UsePrice` 块内的两处扣减条件改造（cache read，~L307-312）：

```go
	if !dCacheTokens.IsZero() {
		if promptTokensIncludeCache {
			baseTokens = baseTokens.Sub(dCacheTokens)
		}
		cachedTokensWithRatio = dCacheTokens.Mul(dCacheRatio)
	}
```

cache creation（~L314-330）改为三路：

```go
	if !dCachedCreationTokens.IsZero() || hasSplitCacheCreationTokens {
		switch {
		case promptTokensIncludeCache:
			// inclusive 原始口径:从 base tokens 扣减并按扁平 cache creation 倍率计价(现状)。
			baseTokens = baseTokens.Sub(dCachedCreationTokens)
			cachedCreationTokensWithRatio = dCachedCreationTokens.Mul(dCacheCreationRatio)
		case summary.IsClaudeUsageSemantic || legacyClaudeDerived:
			// Claude 档位:prompt 已 exclusive,不扣 base;按通用/5m/1h 拆分倍率计价(现状,逐字保留原 else 分支)。
			// ……原 else 分支的 remaining/5m/1h 计算原样搬入……
		default:
			// 声明 prompt_excludes_cache 的非 Claude 渠道:不扣 base,按扁平倍率计价。
			cachedCreationTokensWithRatio = dCachedCreationTokens.Mul(dCacheCreationRatio)
		}
	}
```

**注意**：原 else 分支的语句必须逐字搬入第二个 case，不得改动 5m/1h 语义；`legacyClaudeDerived` 变量继续保留（本 PR 之后它仍只服务计价档位选择）。

- [ ] **Step 4: 运行新旧测试**

Run: `go test ./service -run 'TestCalculateTextQuotaSummary|TestPostTextConsumeQuota' -v && go test ./service`
Expected: 新表 PASS；现有全部计费测试 PASS（未声明路径必须零变化——如有失败，只允许修本 PR 引入的声明分支，禁止调整既有期望值）。

- [ ] **Step 5: Commit**

```bash
git add service/text_quota.go service/text_quota_test.go
git commit -m "feat(billing): channel cache_prompt_token_semantic declaration and fold-aware summary flags"
```

---

### Task 3: `BuildTieredTokenParams` 4 参签名

**Files:**
- Modify: `service/tiered_settle.go`（~L25-95）、`service/quota.go:289`、`service/text_quota.go:417`（PostTextConsumeQuota 内调用）、`controller/channel-test.go:535`
- Test: `service/tiered_settle_test.go`（全部 3 参调用点改 4 参 + 新增声明用例）

**Interfaces:**
- Produces: `func BuildTieredTokenParams(usage *dto.Usage, isClaudeSemantic bool, promptTokensIncludeCache bool, usedVars map[string]bool) billingexpr.TokenParams`
- Consumes: `summary.UpstreamPromptTokensIncludeCache`（Task 2）。

- [ ] **Step 1: 改签名并修编译**

`tiered_settle.go` 中函数签名加第 3 参 `promptTokensIncludeCache bool`；非 Claude 语义下每个"从 `P` 扣减子桶"的守卫由 `!isClaudeSemantic`（或等价现有写法）改为 `!isClaudeSemantic && promptTokensIncludeCache`。Claude 分支与 `C` 的扣减逻辑不动。

调用点：
- `service/text_quota.go`（PostTextConsumeQuota）：`BuildTieredTokenParams(billingUsage, summary.IsClaudeUsageSemantic, summary.UpstreamPromptTokensIncludeCache, tieredUsedVars)`
- `service/quota.go:289`（PostAudioConsumeQuota）：`BuildTieredTokenParams(usage, false, false, tieredUsedVars)`，并加一行注释 `// audio usage 只含 text/audio 细分桶,无 cache 口径可言`。
- `controller/channel-test.go:535`：`BuildTieredTokenParams(usage, isClaudeUsageSemantic, !isClaudeUsageSemantic, usedVars)`（channel test 无渠道声明，按自动口径）。
- `service/tiered_settle_test.go`：所有 3 参调用补第 3 参——现有用例语义为 OpenAI 的传 `true`、Claude 的传 `false`，期望值**不得**变化。

- [ ] **Step 2: 新增声明用例**

在 `tiered_settle_test.go` 加表驱动：`isClaudeSemantic=false, promptTokensIncludeCache=false`（声明 excludes）且 usage 带 cached tokens、`usedVars` 含 `CR`/`CC` 时，`params.P` **不**扣减（等于 raw `usage.PromptTokens`）；同输入 `promptTokensIncludeCache=true` 时保持现有扣减期望。另加 `isClaudeSemantic=true` 时第 3 参无论真假都不影响 P 的用例。

- [ ] **Step 3: 运行**

Run: `go test ./service -run 'TestBuildTieredTokenParams|TestTryTieredSettle' -v && go test ./service ./controller`
Expected: PASS（含 channel-test 相关编译）

- [ ] **Step 4: Commit**

```bash
git add service/tiered_settle.go service/tiered_settle_test.go service/quota.go service/text_quota.go controller/channel-test.go
git commit -m "feat(billing): BuildTieredTokenParams takes raw prompt cache inclusion flag"
```

---

### Task 4: 渠道编辑抽屉 UI（Select）+ 七语言 i18n

**Files:**
- Modify: `web/src/features/channels/` 下渠道编辑抽屉（搜索 `anthropic_messages_exclude_cache` 在前端的开关位置，同区块新增）
- Modify（临时脚本方式）: `web/src/i18n/locales/*.json`

**Interfaces:** 无代码接口；UI 暴露三态 Select：`Auto (default)` / `prompt_includes_cache` / `prompt_excludes_cache`，保存进 channel 的 `setting.cache_prompt_token_semantic`。

- [ ] **Step 1: 加 Select 控件**（照同抽屉里现有 Select/开关的写法；label/帮助文案全部 `t('...')`，英文 key 自拟但需语义完整，如 `'Prompt cache token semantic'`、`'Whether the upstream counts cached prompt tokens inside prompt_tokens. Auto-detect follows usage semantics.'`）
- [ ] **Step 2: i18n**：先读 `.agents/skills/i18n-translate/SKILL.md`；`cd web && bun run i18n:sync`，用临时 `web/scripts/add-missing-keys.mjs` 一次性写入 7 语言（`en, zh, zh-TW, fr, ja, ru, vi`；zh 值：`'Prompt 缓存 token 口径'` / `'上游是否把缓存命中的 prompt token 计入 prompt_tokens。默认按 usage 语义自动判定。'`，其余语言按 skill 规则翻译），跑 missing-key 检查与 `bun run i18n:sync`，删除临时脚本。
- [ ] **Step 3: 验证**

Run: `cd web && bun run typecheck && bun run build`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add web/src/features/channels web/src/i18n/locales
git commit -m "feat(channels): cache_prompt_token_semantic selector with i18n"
```

---

### Task 5: 全量验证与台账

- [ ] **Step 1: 构建/测试**

```bash
go build ./... && go test ./service ./controller ./relay/... && cd relaykit && GOWORK=off go build ./... && GOWORK=off go test ./... && cd .. && cd web && bun run typecheck && cd ..
```

Expected: 全绿（`go build` 的 `web/dist` embed 报错在未构建前端时存在，属已知，可用 `go vet ./...` 或先 `bun run build` 消除）。

- [ ] **Step 2: 按台账流程**：从 `docs/internal/patches/_template.md` 复制 `patches/feat-channel-cache-semantic.md` 填写，`docs/internal/README.md` 表加一行，`docs(internal)` 单独提交。

---

## Self-Review 已核对

- §16.0 要求的全部对齐点（签名/两处结算/channel-test/tiered 单测）均有任务；§8.4 的 5 条口径路径有表驱动；`InputExcludesCache`/`UpstreamPromptTokensIncludeCache` 命名与设计文档一致；未触碰档位选择判断。
- 已知风险：`calculateTextQuotaSummary` 的 fixture 构造在 `tiered_settle_test.go` 中的既有 helper 形状实现时需现场确认；若自动判定与 `usageSemanticFromUsage` 存在不对齐，按 Task 2 Step 3 括号内的指示收敛为单一表达式。
