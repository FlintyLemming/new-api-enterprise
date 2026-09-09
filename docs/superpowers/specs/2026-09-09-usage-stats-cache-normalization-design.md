# 用量统计缓存口径全局归一化开关 · 设计

日期：2026-09-09
分支：internal-custom（内部定制，非上游功能）
状态：待用户审阅

## 背景与问题

OpenAI 的 `prompt_tokens` **包含**缓存 token（cache read/write），Anthropic 的 `input_tokens` **不包含**。网关目前把上游原始口径直接写进 log 表的 `prompt_tokens` 列（除非渠道显式声明了 `cache_prompt_token_semantic: prompt_includes_cache`，见 `service/text_quota.go:362-386` 的折叠逻辑）。因此 `sum(prompt_tokens)` 类聚合（TPM、仪表盘用量统计）跨厂商口径不一致。

计费路径已经正确处理两种口径（缓存单独按倍率计价），本设计**不触碰计费**。

## 目标

- 增加一个全局设置，让管理员选择 log 表 `prompt_tokens` 的落库口径：保持上游原始口径 / 统一不含缓存 / 统一含缓存。
- 开关只影响**内部统计与后台显示**（写入 log 表的值），不影响返回给客户端的响应、不影响计费、不影响 Langfuse 导出。
- 对「OpenAI 格式上游返回 Anthropic 形式缓存」等歧义场景有明确兜底。

## 非目标

- 不回溯历史日志。口径切换点前后的聚合数据不可直接对比。
- 不改 Langfuse 记录（它已经拿到 `InputExcludesCache` 标志和独立缓存桶，是自足的分桶格式）。
- 不改 `other` 里现有的 `cache_tokens` / `cache_write_tokens` / `input_tokens_total` 字段——原始口径永远可从日志还原。
- 不改任务类（视频/音乐等异步任务）日志路径；`plugins/tasks/` 与 `pkg/jsplugin/` 不参与。
- 不改 relaykit 模块。

## 总体方案（已确认：方案 A）

在日志写入点做归一化，计费摘要完全不碰：

1. `calculateTextQuotaSummary`（`service/text_quota.go:322`）维持现状，quota 计算一行不动。
2. 在 `PostTextConsumeQuota` 里、`model.RecordConsumeLog` 调用之前（`service/text_quota.go:675`），用归一化后的值替换传入的 `PromptTokens`。
3. 归一化是纯函数，输入为 `textQuotaSummary` 和全局目标口径，输出归一化后的 prompt tokens + 是否应用 + 跳过原因。

`service/text_quota.go:378` 的现有折叠逻辑（渠道声明触发，服务计费）保留不动。`summary.InputExcludesCache` 是两个机制之间的衔接点：它已经吸收了渠道声明、usage 语义自动判定和 OpenRouter 折叠的最终结果，标识 `summary.PromptTokens` 的**当前口径**。

## 组件

### 1. 设置项（`setting/operation_setting/general_setting.go`）

`GeneralSetting` 新增字段：

```go
// 用量统计 prompt_tokens 落库口径：upstream / exclude_cache / include_cache
UsageStatsCacheCaliber string `json:"usage_stats_cache_caliber"`
```

常量与默认值：

```go
const (
    StatsCacheCaliberUpstream    = "upstream"     // 默认：按上游原始口径落库，保持现状
    StatsCacheCaliberExcludeCache = "exclude_cache" // 统一为不含缓存（Anthropic 口径）
    StatsCacheCaliberIncludeCache = "include_cache" // 统一为含缓存（OpenAI 口径）
)
```

沿用现有 `config.GlobalConfig.Register("general_setting", &generalSetting)` 机制，通过管理后台 option API 读写，**无数据库迁移**。非法取值按 `upstream` 处理（读取时归一化，不信任存储值）。

### 2. 归一化函数（`service/text_quota.go` 所在包）

```go
// normalizeLogPromptTokens 把要落库的 prompt tokens 从当前口径换算到目标口径。
// 当前口径由 summary.InputExcludesCache 标识；返回换算后的值、是否应用了换算、
// 以及跳过原因（未应用时）。输出永不为负。
func normalizeLogPromptTokens(summary textQuotaSummary, target string) (promptTokens int, applied bool, skipReason string)
```

### 3. 接线点（`service/text_quota.go` `PostTextConsumeQuota`）

在组装完 `other` 之后、`RecordConsumeLog` 之前：

```go
logPromptTokens, normApplied, normSkip := normalizeLogPromptTokens(summary, operation_setting.GetGeneralSetting().UsageStatsCacheCaliber)
if 全局开关 != upstream {
    other.SetAdmin("stats_normalization", map[string]any{
        "target":                  目标口径,
        "upstream_caliber":        当前口径("exclude_cache"/"include_cache"),
        "original_prompt_tokens":  summary.PromptTokens,
        "applied":                 normApplied,
        "skip_reason":             normSkip, // 仅未应用时
    })
}
// RecordConsumeLogParams{ PromptTokens: logPromptTokens, ... }
```

`admin_info` 域普通用户不可见（非 admin 日志视图会剥离），审计信息天然只给管理员。开关为 `upstream` 时不写标记，行为与现状逐字节一致。

### 4. 前端设置 UI

- 位置：`web/src/features/system-settings/general/quota-settings-section.tsx`（与 `quota_display_type` 同区，同属「统计/展示口径」类设置）。
- 控件：三档 Select + 说明文案。说明文案须包含：仅对新日志生效、历史数据不回溯、切换前后聚合数据不可直接对比、歧义渠道的逃生门（见下文兜底规则 2）。
- i18n：en/zh 双语 key，遵循 `useTranslation()` + 英文源串作 key 的约定。

### 5. 日志显示

- 用量日志的 Tokens 列和 Cache↓/↑ 明细不用改（读的就是落库值）。
- admin 的日志详情对话框：`other.admin_info.stats_normalization` 存在时显示一行归一化信息（原始值 → 归一化值、目标口径或跳过原因）。
- 非 admin 视图不动。

## 换算规则

`cache_write` 总量取与日志 `other.cache_write_tokens` 一致的口径：5m/1h 拆分优先、饱和求和（复用 `checkedCacheWriteTokensTotal`，`service/text_quota.go:91` 的逻辑）。

| 目标口径 | 当前口径（`InputExcludesCache`） | 动作 |
|---|---|---|
| `upstream` | 任意 | no-op，不写标记 |
| `exclude_cache` | 不含缓存（true） | no-op |
| `exclude_cache` | 含缓存（false） | `prompt − (cache_read + cache_write)` |
| `include_cache` | 含缓存（false） | no-op |
| `include_cache` | 不含缓存（true） | `prompt + cache_read + cache_write` |

## 兜底规则

1. **减法越界**：`prompt < cache_read + cache_write` 时放弃换算、按原值落库，`skip_reason: "prompt_less_than_cache"`。覆盖「OpenAI 格式上游实际返回不含缓存的 prompt」被自动判定误伤的一部分情形。
2. **数值上兜不住的误判**（prompt 实际不含缓存但仍大于缓存合计）：数据本身无解。逃生门是现有渠道级声明——管理员给该渠道设置 `cache_prompt_token_semantic: prompt_excludes_cache` 后，当前口径即为 exclude，减法不会发生。此说明写进设置项 UI 文案。
3. **缓存桶为零**（无缓存请求、`usage == nil` 的预估路径）：两个方向均 no-op。
4. **Claude→OpenAI 转换链**：`effectiveBillingUsage`（`service/billing_usage.go:19`）从 BillingUsage 快照还原出 anthropic 语义、exclusive 口径，`InputExcludesCache=true`，无需特殊处理。
5. **legacy Claude-derived OpenAI usage**（5m/1h 拆分字段存在但未标语义）：现有 `isLegacyClaudeDerivedOpenAIUsage` 已将其判为 exclusive，自动衔接。
6. **不变量**：换算输出永不为负；任一步不满足即放弃换算并保持原值。token 计数为非负 int（`checkUsageSourcesValid` 已验证），64 位平台加法无溢出风险，不引入额外钳制。

## 错误处理

归一化失败不报错、不影响请求——这是最坏情况落回现状口径的设计（统计口径偏差可接受，请求不可用不可接受）。所有放弃换算的情形都通过 `stats_normalization.applied=false + skip_reason` 审计。

## 测试

- **后端**（加在现有 `service/text_quota_test.go`，不新开测试文件）：
  - `normalizeLogPromptTokens` 表驱动测试：三档目标 × 两种当前口径 × 零缓存 × 减法越界 × 5m/1h 拆分求和。
  - 一个 `PostTextConsumeQuota` 接线用例：开启 `exclude_cache` 后落库 `prompt_tokens` 被替换、`summary.Quota` 与现状一致、`other.admin_info.stats_normalization` 内容正确；开关为 `upstream` 时无标记。
  - 测试内显式初始化 `generalSetting` 状态（遵循项目测试夹具约定）。
- **前端**：按 `quota-settings-section` 现有测试模式补设置项渲染/提交用例；`bun run i18n:sync`。
- 测试断言用 `testify/require` + `assert`。

## 验证

- `go build ./...` + `gofmt` 处理改动文件；前端 `bun run build`。
- relaykit 不触碰，无需 `GOWORK=off` 独立构建验证。
- **数据库兼容性声明**：无 schema 变更、无新 SQL、无迁移——`RecordConsumeLog` 写入路径不变，仅传入的 `prompt_tokens` 数值不同；设置走已存在的 option 存储。因此本变更不触发 SQLite/MySQL/PostgreSQL 三库验证矩阵，此理由须写进 PR/交接说明。
- 手动验证：起一个实例，同一 Claude 渠道和 OpenAI 渠道各发一笔带缓存的请求，分别在 `upstream` / `exclude_cache` / `include_cache` 三档下检查 log 表 `prompt_tokens` 与后台展示。

## 影响面清单

- 改：`setting/operation_setting/general_setting.go`、`service/text_quota.go`（含接线与标记）、`service/text_quota_test.go`
- 改（前端）：`web/src/features/system-settings/general/quota-settings-section.tsx`、i18n locales、日志详情对话框组件（admin 归一化信息行）
- 不改：计费（quota 计算、预扣/结算）、Langfuse（`service/langfuse/`）、客户端响应、relaykit、任务插件、数据库 schema
