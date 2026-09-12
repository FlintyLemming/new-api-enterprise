# 用量统计缓存口径全局归一化开关 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 增加全局设置，让管理员选择 log 表 `prompt_tokens` 的落库口径（上游原始 / 统一不含缓存 / 统一含缓存），只影响内部统计与后台显示。

**Architecture:** 在日志写入点（`PostTextConsumeQuota` → `RecordConsumeLog` 之前）用纯函数把 `summary.PromptTokens` 从当前口径（`summary.InputExcludesCache` 标识）换算到目标口径；计费、客户端响应、Langfuse 完全不碰。换算结果与跳过原因写入 `other.admin_info.stats_normalization` 供管理员审计。

**Tech Stack:** Go 1.25（Gin/GORM v2）、React 19 + TypeScript（Rsbuild、React Hook Form + Zod、Base UI、i18next）、Vitest + React Testing Library、testify。

**Spec:** `docs/superpowers/specs/2026-09-09-usage-stats-cache-normalization-design.md`

**Spec deviation（已确认）:** 设计 §4 说设置 UI 放在 `quota-settings-section.tsx`「与 `quota_display_type` 同区」。实际代码中 `quota_display_type` 位于 `web/src/features/system-settings/general/pricing-section.tsx`（「Currency & Display」区）。按设计的意图（与 `quota_display_type` 同区、同属「统计/展示口径」类设置），本计划把控件放进 `pricing-section.tsx`。

## Global Constraints

- 分支：`internal-custom`（内部定制，非上游功能）。
- 不触碰：计费（quota 计算、预扣/结算）、Langfuse（`service/langfuse/`）、客户端响应、relaykit、任务插件（`plugins/tasks/`、`pkg/jsplugin/`）、数据库 schema。
- 无 DB schema 变更、无新 SQL、无迁移 → 不触发 SQLite/MySQL/PostgreSQL 三库验证矩阵；此理由必须写进最终交接/PR 说明。
- 口径三档取值常量：`"upstream"`（默认）/ `"exclude_cache"` / `"include_cache"`；非法存储值读取时按 `upstream` 处理。
- 开关为 `upstream` 时不写 `stats_normalization` 标记，行为与现状逐字节一致。
- 归一化输出永不为负；任一步不满足即放弃换算并保持原值（`applied=false + skip_reason`），归一化失败永不报错、不影响请求。
- 后端 JSON 操作一律用 `common.*` 包装函数，禁止直接 import `encoding/json`；改动的 Go 文件跑 `gofmt`。
- 后端测试全部加在现有 `service/text_quota_test.go`（不新开 Go 测试文件），用 `testify/require`（fatal）+ `testify/assert`（值检查）。
- 前端测试放在被测模块专属 `__tests__/` 目录；包管理/脚本用 `bun`；UI 文案走 i18n，英文源串作 key。
- commit message 用 conventional commits（参照近期提交：`feat(service): ...`、`feat(web): ...`）。

---

### Task 1: 后端设置字段与口径读取归一化

**Files:**
- Modify: `setting/operation_setting/general_setting.go`
- Test: `service/text_quota_test.go`

**Interfaces:**
- Consumes: 现有 `config.GlobalConfig.Register("general_setting", &generalSetting)` 机制（option API 读写，无迁移）。
- Produces:
  - 常量 `operation_setting.StatsCacheCaliberUpstream` / `StatsCacheCaliberExcludeCache` / `StatsCacheCaliberIncludeCache`（string）。
  - `GeneralSetting.UsageStatsCacheCaliber string`（json tag `usage_stats_cache_caliber`）。
  - `func operation_setting.GetUsageStatsCacheCaliber() string` — 读取时归一化，非法值返回 `StatsCacheCaliberUpstream`。

- [ ] **Step 1: 写失败测试**

在 `service/text_quota_test.go` 末尾追加：

```go
func TestGetUsageStatsCacheCaliberFallsBackToUpstream(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	original := setting.UsageStatsCacheCaliber
	t.Cleanup(func() { setting.UsageStatsCacheCaliber = original })

	setting.UsageStatsCacheCaliber = operation_setting.StatsCacheCaliberExcludeCache
	assert.Equal(t, operation_setting.StatsCacheCaliberExcludeCache, operation_setting.GetUsageStatsCacheCaliber())

	setting.UsageStatsCacheCaliber = operation_setting.StatsCacheCaliberIncludeCache
	assert.Equal(t, operation_setting.StatsCacheCaliberIncludeCache, operation_setting.GetUsageStatsCacheCaliber())

	// 非法存储值不信任，读取时按 upstream 处理
	setting.UsageStatsCacheCaliber = "bogus"
	assert.Equal(t, operation_setting.StatsCacheCaliberUpstream, operation_setting.GetUsageStatsCacheCaliber())

	setting.UsageStatsCacheCaliber = ""
	assert.Equal(t, operation_setting.StatsCacheCaliberUpstream, operation_setting.GetUsageStatsCacheCaliber())
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/ -run TestGetUsageStatsCacheCaliberFallsBackToUpstream -v`
Expected: 编译失败（`GetUsageStatsCacheCaliber`、`StatsCacheCaliber*` 未定义）。

- [ ] **Step 3: 实现**

`setting/operation_setting/general_setting.go`：在 `QuotaDisplayType` 常量块后加：

```go
// 用量统计 prompt_tokens 落库口径
const (
	StatsCacheCaliberUpstream     = "upstream"      // 默认：按上游原始口径落库，保持现状
	StatsCacheCaliberExcludeCache = "exclude_cache" // 统一为不含缓存（Anthropic 口径）
	StatsCacheCaliberIncludeCache = "include_cache" // 统一为含缓存（OpenAI 口径）
)
```

`GeneralSetting` 结构体追加字段（放在 `CustomCurrencyExchangeRate` 之后）：

```go
	// 用量统计 prompt_tokens 落库口径：upstream / exclude_cache / include_cache
	UsageStatsCacheCaliber string `json:"usage_stats_cache_caliber"`
```

`generalSetting` 默认值追加：

```go
	UsageStatsCacheCaliber:     StatsCacheCaliberUpstream,
```

文件末尾追加读取函数：

```go
// GetUsageStatsCacheCaliber 返回用量统计 prompt_tokens 落库口径。
// 不信任存储值：非法取值按 upstream（保持现状口径）处理。
func GetUsageStatsCacheCaliber() string {
	switch generalSetting.UsageStatsCacheCaliber {
	case StatsCacheCaliberExcludeCache, StatsCacheCaliberIncludeCache:
		return generalSetting.UsageStatsCacheCaliber
	default:
		return StatsCacheCaliberUpstream
	}
}
```

- [ ] **Step 4: 跑测试确认通过 + gofmt**

Run: `gofmt -w setting/operation_setting/general_setting.go service/text_quota_test.go && go test ./service/ -run TestGetUsageStatsCacheCaliberFallsBackToUpstream -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add setting/operation_setting/general_setting.go service/text_quota_test.go
git commit -m "feat(setting): add usage stats cache caliber option with read-time normalization"
```

---

### Task 2: 归一化纯函数 `normalizeLogPromptTokens`

**Files:**
- Modify: `service/text_quota.go`（在 `checkedCacheWriteTokensTotal`（`:91`）之后插入新函数）
- Test: `service/text_quota_test.go`

**Interfaces:**
- Consumes: `textQuotaSummary`（`service/text_quota.go:42`）、`checkedCacheWriteTokensTotal`（`:91`，5m/1h 拆分优先、饱和求和）、Task 1 的口径常量。
- Produces: `func normalizeLogPromptTokens(summary textQuotaSummary, target string) (promptTokens int, applied bool, skipReason string)` — Task 3 的接线点调用它。`skipReason` 取值：`"already_matches_target"` / `"no_cache_tokens"` / `"prompt_less_than_cache"` / `"unsupported_target"`。

- [ ] **Step 1: 写失败测试**

在 `service/text_quota_test.go` 末尾追加表驱动测试：

```go
func TestNormalizeLogPromptTokens(t *testing.T) {
	// cacheRead=100；cacheWrite 取 checkedCacheWriteTokensTotal：5m+1h=60 > 50 → 60。
	// 缓存合计 = 160。
	inclusive := textQuotaSummary{
		PromptTokens:          1000,
		CacheTokens:           100,
		CacheCreationTokens:   50,
		CacheCreationTokens5m: 20,
		CacheCreationTokens1h: 40,
		InputExcludesCache:    false,
	}
	exclusive := inclusive
	exclusive.InputExcludesCache = true

	zeroCache := textQuotaSummary{PromptTokens: 1000, InputExcludesCache: false}

	underflow := inclusive
	underflow.PromptTokens = 100 // 100 < 160，减法越界

	// 拆分合计小于总量时回退扁平 cache_creation_tokens：cacheWrite=80，缓存合计=180
	flatLarger := inclusive
	flatLarger.CacheCreationTokens = 80

	tests := []struct {
		name             string
		summary          textQuotaSummary
		target           string
		wantPromptTokens int
		wantApplied      bool
		wantSkipReason   string
	}{
		{"exclude from inclusive subtracts cache", inclusive, operation_setting.StatsCacheCaliberExcludeCache, 840, true, ""},
		{"exclude from exclusive is no-op", exclusive, operation_setting.StatsCacheCaliberExcludeCache, 1000, false, "already_matches_target"},
		{"include from exclusive adds cache", exclusive, operation_setting.StatsCacheCaliberIncludeCache, 1160, true, ""},
		{"include from inclusive is no-op", inclusive, operation_setting.StatsCacheCaliberIncludeCache, 1000, false, "already_matches_target"},
		{"upstream target is no-op", inclusive, operation_setting.StatsCacheCaliberUpstream, 1000, false, "unsupported_target"},
		{"unknown target is no-op", inclusive, "bogus", 1000, false, "unsupported_target"},
		{"zero cache buckets are no-op both directions", zeroCache, operation_setting.StatsCacheCaliberExcludeCache, 1000, false, "no_cache_tokens"},
		{"subtraction underflow keeps original", underflow, operation_setting.StatsCacheCaliberExcludeCache, 100, false, "prompt_less_than_cache"},
		{"flat creation total larger than split sum", flatLarger, operation_setting.StatsCacheCaliberExcludeCache, 820, true, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTokens, gotApplied, gotSkip := normalizeLogPromptTokens(tc.summary, tc.target)
			assert.Equal(t, tc.wantPromptTokens, gotTokens)
			assert.Equal(t, tc.wantApplied, gotApplied)
			assert.Equal(t, tc.wantSkipReason, gotSkip)
			assert.GreaterOrEqual(t, gotTokens, 0, "normalized output must never be negative")
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/ -run TestNormalizeLogPromptTokens -v`
Expected: 编译失败（`normalizeLogPromptTokens` 未定义）。

- [ ] **Step 3: 实现**

在 `service/text_quota.go` 的 `checkedCacheWriteTokensTotal` 函数（`:91-102`）之后插入：

```go
// normalizeLogPromptTokens 把要落库的 prompt tokens 从当前口径换算到目标口径。
// 当前口径由 summary.InputExcludesCache 标识；cache_write 总量取与日志
// other.cache_write_tokens 一致的口径（5m/1h 拆分优先、饱和求和）。
// 返回换算后的值、是否应用了换算、以及跳过原因（未应用时）。
// 输出永不为负：任一步不满足即放弃换算并保持原值（最坏情况落回现状口径）。
func normalizeLogPromptTokens(summary textQuotaSummary, target string) (promptTokens int, applied bool, skipReason string) {
	cacheTotal := summary.CacheTokens + checkedCacheWriteTokensTotal(summary)
	switch target {
	case operation_setting.StatsCacheCaliberExcludeCache:
		if summary.InputExcludesCache {
			return summary.PromptTokens, false, "already_matches_target"
		}
		if cacheTotal <= 0 {
			return summary.PromptTokens, false, "no_cache_tokens"
		}
		if summary.PromptTokens < cacheTotal {
			return summary.PromptTokens, false, "prompt_less_than_cache"
		}
		return summary.PromptTokens - cacheTotal, true, ""
	case operation_setting.StatsCacheCaliberIncludeCache:
		if !summary.InputExcludesCache {
			return summary.PromptTokens, false, "already_matches_target"
		}
		if cacheTotal <= 0 {
			return summary.PromptTokens, false, "no_cache_tokens"
		}
		return summary.PromptTokens + cacheTotal, true, ""
	default:
		return summary.PromptTokens, false, "unsupported_target"
	}
}
```

- [ ] **Step 4: 跑测试确认通过 + gofmt**

Run: `gofmt -w service/text_quota.go service/text_quota_test.go && go test ./service/ -run TestNormalizeLogPromptTokens -v`
Expected: PASS（9 个子用例）

- [ ] **Step 5: Commit**

```bash
git add service/text_quota.go service/text_quota_test.go
git commit -m "feat(service): add normalizeLogPromptTokens caliber conversion"
```

---

### Task 3: `PostTextConsumeQuota` 接线与 admin 审计标记

**Files:**
- Modify: `service/text_quota.go`（`PostTextConsumeQuota`，`:673-688` 区间）
- Test: `service/text_quota_test.go`

**Interfaces:**
- Consumes: Task 1 的 `operation_setting.GetUsageStatsCacheCaliber()`、Task 2 的 `normalizeLogPromptTokens`、现有 `model.LogOther.SetAdmin`（`model/log_other.go:77`）、测试夹具 `truncate`/`seedUser`/`seedToken`/`seedChannel`/`getLastLog`（`service/task_billing_test.go`，同包共享；包级 `TestMain` 已建内存 SQLite 并迁移 `model.Log`）。
- Produces: `other.admin_info.stats_normalization`，JSON 结构 `{target, upstream_caliber, original_prompt_tokens, applied, skip_reason?}`（`skip_reason` 仅在 `applied=false` 时存在）。Task 5 前端按此结构渲染。

- [ ] **Step 1: 写失败测试**

在 `service/text_quota_test.go` 末尾追加：

```go
func TestPostTextConsumeQuotaNormalizesLoggedPromptTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	truncate(t)
	seedUser(t, 1, 1000000)
	seedToken(t, 1, 1, "sk-test", 1000000)
	seedChannel(t, 1)

	setting := operation_setting.GetGeneralSetting()
	originalCaliber := setting.UsageStatsCacheCaliber
	t.Cleanup(func() { setting.UsageStatsCacheCaliber = originalCaliber })

	newRelayInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			RelayFormat:             types.RelayFormatOpenAI,
			FinalRequestRelayFormat: types.RelayFormatOpenAI,
			OriginModelName:         "gpt-test",
			UserId:                  1,
			ChannelId:               1,
			TokenId:                 1,
			TokenKey:                "sk-test",
			UserQuota:               100000000, // 远高于通知阈值，避免触发额度提醒
			UsingGroup:              "default",
			PriceData: hosttypes.PriceData{
				ModelRatio:      1,
				CompletionRatio: 1,
				GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
			},
			StartTime: time.Now(),
		}
	}
	// openai 语义 → inclusive 口径（prompt 含缓存）；cacheRead=100, cacheWrite=50。
	newUsage := func() *dto.Usage {
		return &dto.Usage{
			PromptTokens:     1000,
			CompletionTokens: 200,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         100,
				CachedCreationTokens: 50,
			},
		}
	}

	readMarker := func(t *testing.T, log *model.Log) map[string]any {
		t.Helper()
		var other map[string]any
		require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
		adminInfo, ok := other["admin_info"].(map[string]any)
		require.True(t, ok, "admin_info must exist")
		marker, ok := adminInfo["stats_normalization"].(map[string]any)
		require.True(t, ok, "stats_normalization marker must exist")
		return marker
	}

	// 对照组：upstream 口径，行为与现状逐字节一致（无标记）。
	setting.UsageStatsCacheCaliber = operation_setting.StatsCacheCaliberUpstream
	ctx1, _ := gin.CreateTestContext(httptest.NewRecorder())
	PostTextConsumeQuota(ctx1, newRelayInfo(), newUsage(), nil)
	controlLog := getLastLog(t)
	require.NotNil(t, controlLog)
	require.Equal(t, 1000, controlLog.PromptTokens)
	require.Equal(t, 1050, controlLog.Quota) // (1000-100-50)*1 + 200*1，归一化不影响计费
	var controlOther map[string]any
	require.NoError(t, common.UnmarshalJsonStr(controlLog.Other, &controlOther))
	if adminInfo, ok := controlOther["admin_info"].(map[string]any); ok {
		assert.NotContains(t, adminInfo, "stats_normalization")
	}

	// exclude_cache：落库 prompt 被替换为 1000-(100+50)=850，quota 不变，标记内容正确。
	setting.UsageStatsCacheCaliber = operation_setting.StatsCacheCaliberExcludeCache
	ctx2, _ := gin.CreateTestContext(httptest.NewRecorder())
	PostTextConsumeQuota(ctx2, newRelayInfo(), newUsage(), nil)
	normalizedLog := getLastLog(t)
	require.NotNil(t, normalizedLog)
	assert.Equal(t, 850, normalizedLog.PromptTokens)
	assert.Equal(t, controlLog.Quota, normalizedLog.Quota, "normalization must not change billing")
	marker := readMarker(t, normalizedLog)
	assert.Equal(t, "exclude_cache", marker["target"])
	assert.Equal(t, "include_cache", marker["upstream_caliber"])
	assert.Equal(t, float64(1000), marker["original_prompt_tokens"])
	assert.Equal(t, true, marker["applied"])
	assert.NotContains(t, marker, "skip_reason")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/ -run TestPostTextConsumeQuotaNormalizesLoggedPromptTokens -v`
Expected: FAIL — 对照组即报错或 normalized 组 `PromptTokens` 仍是 1000 / 无 marker（接线尚未存在）。

- [ ] **Step 3: 实现接线**

`service/text_quota.go` `PostTextConsumeQuota` 中，在 `attachQuotaSaturation(ctx, relayInfo, other)`（`:673`）之后、`model.RecordConsumeLog`（`:675`）之前插入：

```go
	logPromptTokens := summary.PromptTokens
	if caliber := operation_setting.GetUsageStatsCacheCaliber(); caliber != operation_setting.StatsCacheCaliberUpstream {
		normalized, applied, skipReason := normalizeLogPromptTokens(summary, caliber)
		upstreamCaliber := "include_cache"
		if summary.InputExcludesCache {
			upstreamCaliber = "exclude_cache"
		}
		marker := map[string]any{
			"target":                 caliber,
			"upstream_caliber":       upstreamCaliber,
			"original_prompt_tokens": summary.PromptTokens,
			"applied":                applied,
		}
		if !applied {
			marker["skip_reason"] = skipReason
		}
		other.SetAdmin("stats_normalization", marker)
		logPromptTokens = normalized
	}
```

并把 `RecordConsumeLogParams` 里的 `PromptTokens: summary.PromptTokens` 改为 `PromptTokens: logPromptTokens`。

注意：`other` 在此处必非 nil（上方 `GenerateClaudeOtherInfo`/`GenerateTextOtherInfo` 分支已赋值），无需判空。

- [ ] **Step 4: 跑测试确认通过 + gofmt + 全量 service 测试**

Run:
```bash
gofmt -w service/text_quota.go service/text_quota_test.go
go test ./service/ -run TestPostTextConsumeQuotaNormalizesLoggedPromptTokens -v
go test ./service/ ./setting/...
```
Expected: 全部 PASS（全量是为确认接线没有破坏既有计费/日志测试）。

- [ ] **Step 5: Commit**

```bash
git add service/text_quota.go service/text_quota_test.go
git commit -m "feat(service): normalize logged prompt_tokens by global stats caliber"
```

---

### Task 4: 前端设置项（Pricing & Display 区）

**Files:**
- Modify: `web/src/features/system-settings/general/pricing-section.tsx`
- Modify: `web/src/features/system-settings/billing/section-registry.tsx`（`:86-104` 的 PricingSection defaultValues）
- Modify: `web/src/features/system-settings/billing/index.tsx`（`defaultBillingSettings`，`:37` 附近）
- Modify: `web/src/features/system-settings/types.ts`（`BillingSettings`，`:266` 附近）
- Modify: `web/src/i18n/locales/en.json`、`web/src/i18n/locales/zh.json`
- Test: `web/src/features/system-settings/general/__tests__/usage-stats-cache-caliber.test.tsx`（新建）

**Interfaces:**
- Consumes: Task 1 的后端字段（option key `general_setting.usage_stats_cache_caliber`，经 `handleConfigUpdate`（`model/option.go:665`）分层配置机制读写）；现有 `Select`（`@/components/ui/select`）、`useUpdateOption`、`SettingsPageProvider`。
- Produces: `USAGE_STATS_CACHE_CALIBERS` 常量与 `parseUsageStatsCacheCaliber(value)`（从 `pricing-section.tsx` 导出，registry 用于把服务端字符串安全收敛为枚举）；zod schema 字段 `general_setting.usage_stats_cache_caliber`。

- [ ] **Step 1: 写失败测试**

新建 `web/src/features/system-settings/general/__tests__/usage-stats-cache-caliber.test.tsx`：

```tsx
/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { PricingSection } from '../pricing-section'

// FormNavigationGuard 依赖 TanStack Router 上下文，与本次测试的行为无关。
vi.mock('../../components/form-navigation-guard', () => ({
  FormNavigationGuard: () => null,
}))

const defaultValues = {
  QuotaPerUnit: 500000,
  USDExchangeRate: 7,
  DisplayInCurrencyEnabled: true,
  DisplayTokenStatEnabled: true,
  general_setting: {
    quota_display_type: 'USD' as const,
    custom_currency_symbol: '¤',
    custom_currency_exchange_rate: 1,
    usage_stats_cache_caliber: 'upstream' as const,
  },
}

function renderSection() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  queryClient.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  const actionsContainer = document.createElement('div')
  document.body.appendChild(actionsContainer)
  render(
    <QueryClientProvider client={queryClient}>
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <PricingSection defaultValues={defaultValues} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

afterEach(cleanup)

describe('usage stats cache caliber setting', () => {
  test('renders the caliber select with the upstream default', () => {
    renderSection()
    expect(
      screen.getByRole('combobox', { name: 'Upstream original' })
    ).toBeInTheDocument()
  })

  test('submits the selected caliber as a system option', async () => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, message: '' } })
    renderSection()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('combobox', { name: 'Upstream original' })
    )
    await user.click(
      await screen.findByRole('option', { name: 'Exclude cache tokens' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/option/', {
        key: 'general_setting.usage_stats_cache_caliber',
        value: 'exclude_cache',
      })
    )
  })
})
```

注意：combobox 的可访问名来自 trigger 内当前选中项文本（Base UI Select 行为）。若 jsdom 下 option 交互不稳定，允许把断言降级为直接断言「trigger 存在 + Save 提交的值」，但提交断言 `key/value` 不可删。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && bun run test -- src/features/system-settings/general/__tests__/usage-stats-cache-caliber.test.tsx`
Expected: FAIL — typecheck/渲染失败（`usage_stats_cache_caliber` 不在 schema 与 defaultValues 类型中）。

- [ ] **Step 3: 实现**

**3a. `pricing-section.tsx`**：

在文件顶部常量区（`createPricingSchema` 之前）加：

```tsx
export const USAGE_STATS_CACHE_CALIBERS = [
  'upstream',
  'exclude_cache',
  'include_cache',
] as const
export type UsageStatsCacheCaliber = (typeof USAGE_STATS_CACHE_CALIBERS)[number]

export function parseUsageStatsCacheCaliber(
  value: string | undefined
): UsageStatsCacheCaliber {
  return USAGE_STATS_CACHE_CALIBERS.includes(value as UsageStatsCacheCaliber)
    ? (value as UsageStatsCacheCaliber)
    : 'upstream'
}
```

zod schema 的 `general_setting` 对象内（`quota_display_type` 之后）加：

```tsx
        usage_stats_cache_caliber: z.enum(USAGE_STATS_CACHE_CALIBERS),
```

JSX 中在「Display Mode」`FormField`（`:188-230`）之后插入新 `FormField`（结构完全复用 Display Mode 的 Select 写法）：

```tsx
            <FormField
              control={form.control}
              name='general_setting.usage_stats_cache_caliber'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Usage Stats Cache Caliber')}</FormLabel>
                  <Select
                    items={[
                      { value: 'upstream', label: t('Upstream original') },
                      {
                        value: 'exclude_cache',
                        label: t('Exclude cache tokens'),
                      },
                      {
                        value: 'include_cache',
                        label: t('Include cache tokens'),
                      },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue placeholder={t('Select cache caliber')} />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        <SelectItem value='upstream'>
                          {t('Upstream original')}
                        </SelectItem>
                        <SelectItem value='exclude_cache'>
                          {t('Exclude cache tokens')}
                        </SelectItem>
                        <SelectItem value='include_cache'>
                          {t('Include cache tokens')}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    {t(
                      'Controls the prompt_tokens caliber written to usage logs: keep the upstream original, or normalize across providers to exclude/include cached tokens. Applies only to new logs; historical data is not backfilled and aggregates across a switch are not directly comparable. Billing and client responses are unaffected. If a channel is misdetected, set its cache_prompt_token_semantic channel setting instead.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
```

**3b. `section-registry.tsx`**：import 加 `parseUsageStatsCacheCaliber`（来自 `../general/pricing-section`）；PricingSection 的 `general_setting` defaultValues（`:92-100`）加：

```tsx
            usage_stats_cache_caliber: parseUsageStatsCacheCaliber(
              settings['general_setting.usage_stats_cache_caliber']
            ),
```

**3c. `billing/index.tsx`** `defaultBillingSettings`（`'general_setting.quota_display_type': 'USD',` 之后）加：

```tsx
  'general_setting.usage_stats_cache_caliber': 'upstream',
```

**3d. `types.ts`** `BillingSettings`（`'general_setting.quota_display_type': string` 之后）加：

```tsx
  'general_setting.usage_stats_cache_caliber': string
```

**3e. i18n**：`web/src/i18n/locales/en.json` 加（值与 key 相同）；`zh.json` 加中文翻译：

| key | zh |
|---|---|
| `Usage Stats Cache Caliber` | `用量统计缓存口径` |
| `Upstream original` | `上游原始口径` |
| `Exclude cache tokens` | `统一不含缓存` |
| `Include cache tokens` | `统一含缓存` |
| `Select cache caliber` | `选择统计口径` |
| `Controls the prompt_tokens caliber written to usage logs: keep the upstream original, or normalize across providers to exclude/include cached tokens. Applies only to new logs; historical data is not backfilled and aggregates across a switch are not directly comparable. Billing and client responses are unaffected. If a channel is misdetected, set its cache_prompt_token_semantic channel setting instead.` | `控制写入用量日志的 prompt_tokens 口径：保留上游原始口径，或跨厂商统一为不含/含缓存。仅对新日志生效，历史数据不回溯，切换前后的聚合数据不可直接对比。不影响计费与返回给客户端的响应。若某渠道被误判，请改用该渠道的 cache_prompt_token_semantic 设置。` |

然后 `cd web && bun run i18n:sync` 同步其余语言。

- [ ] **Step 4: 跑测试 + typecheck + lint**

Run:
```bash
cd web
bun run test -- src/features/system-settings/general/__tests__/usage-stats-cache-caliber.test.tsx
bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/system-settings/general/pricing-section.tsx src/features/system-settings/general/__tests__/usage-stats-cache-caliber.test.tsx src/features/system-settings/billing/section-registry.tsx src/features/system-settings/billing/index.tsx src/features/system-settings/types.ts
```
Expected: 测试 PASS、typecheck 无错误、lint 无 error。

- [ ] **Step 5: Commit**

```bash
git add web/src/features/system-settings web/src/i18n/locales
git commit -m "feat(web): add usage stats cache caliber setting to pricing section"
```

---

### Task 5: 日志详情对话框的归一化信息（admin）

**Files:**
- Modify: `web/src/features/usage-logs/types.ts`（`LogOtherData.admin_info`，`:117-148`）
- Modify: `web/src/features/usage-logs/components/dialogs/details-dialog.tsx`（在 quota_saturation 区块 `:760-793` 之后插入）
- Modify: `web/src/i18n/locales/en.json`、`web/src/i18n/locales/zh.json`
- Test: `web/src/features/usage-logs/components/__tests__/stats-normalization.test.tsx`（新建，模式照搬 `reject-reason.test.tsx`）

**Interfaces:**
- Consumes: Task 3 写入的 `other.admin_info.stats_normalization`（`{target, upstream_caliber, original_prompt_tokens, applied, skip_reason?}`）；现有 `DetailSection`/`DetailRow` 与 `props.log.prompt_tokens`（归一化后的落库值）。
- Produces: `LogOtherData.admin_info.stats_normalization` 类型；admin 日志详情的「Stats normalization」区块。

- [ ] **Step 1: 写失败测试**

新建 `web/src/features/usage-logs/components/__tests__/stats-normalization.test.tsx`（fixture 与渲染方式照搬同目录 `reject-reason.test.tsx`）：

```tsx
/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

const queryClients: QueryClient[] = []

function makeLog(other: LogOtherData, promptTokens = 850): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-test',
    quota: 1050,
    prompt_tokens: promptTokens,
    completion_tokens: 200,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: 'channel',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(isAdmin: boolean, other: LogOtherData): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  queryClients.push(queryClient)
  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(other)}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const queryClient of queryClients) {
    queryClient.clear()
  }
  queryClients.length = 0
})

describe('usage log stats normalization', () => {
  test('shows original to normalized prompt tokens to admins', () => {
    renderDetails(true, {
      admin_info: {
        stats_normalization: {
          target: 'exclude_cache',
          upstream_caliber: 'include_cache',
          original_prompt_tokens: 1000,
          applied: true,
        },
      },
    })
    expect(screen.getByText('Stats normalization')).toBeInTheDocument()
    expect(screen.getByText('1000')).toBeInTheDocument()
    expect(screen.getByText('850')).toBeInTheDocument()
  })

  test('shows the skip reason when normalization was not applied', () => {
    renderDetails(true, {
      admin_info: {
        stats_normalization: {
          target: 'exclude_cache',
          upstream_caliber: 'include_cache',
          original_prompt_tokens: 100,
          applied: false,
          skip_reason: 'prompt_less_than_cache',
        },
      },
    })
    expect(screen.getByText('prompt_less_than_cache')).toBeInTheDocument()
  })

  test('hides the normalization info from non-admin users', () => {
    renderDetails(false, {
      admin_info: {
        stats_normalization: {
          target: 'exclude_cache',
          upstream_caliber: 'include_cache',
          original_prompt_tokens: 1000,
          applied: true,
        },
      },
    })
    expect(screen.queryByText('Stats normalization')).toBeNull()
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && bun run test -- src/features/usage-logs/components/__tests__/stats-normalization.test.tsx`
Expected: FAIL — 类型错误（`stats_normalization` 不在 `admin_info` 类型中）或区块不渲染。

- [ ] **Step 3: 实现**

**3a. `web/src/features/usage-logs/types.ts`**：`admin_info` 内（`quota_saturation` 块之后、`reject_reason` 之前）加：

```tsx
    // Stats caliber normalization marker: present when the global usage-stats
    // cache caliber switch rewrote prompt_tokens at log write time.
    // Admin-only (nested under admin_info).
    stats_normalization?: {
      target: string
      upstream_caliber: string
      original_prompt_tokens: number
      applied: boolean
      skip_reason?: string
    }
```

**3b. `details-dialog.tsx`**：在 quota_saturation 区块（`:760-793`）之后插入：

```tsx
        {/* Stats caliber normalization marker (admin only) */}
        {props.isAdmin && other?.admin_info?.stats_normalization && (
          <DetailSection label={t('Stats normalization')}>
            <DetailRow
              label={t('Target caliber')}
              value={other.admin_info.stats_normalization.target}
              mono
            />
            <DetailRow
              label={t('Upstream caliber')}
              value={other.admin_info.stats_normalization.upstream_caliber}
              mono
            />
            <DetailRow
              label={t('Original prompt tokens')}
              value={String(
                other.admin_info.stats_normalization.original_prompt_tokens
              )}
              mono
            />
            {other.admin_info.stats_normalization.applied ? (
              <DetailRow
                label={t('Normalized prompt tokens')}
                value={String(props.log.prompt_tokens)}
                mono
              />
            ) : (
              <DetailRow
                label={t('Skip reason')}
                value={other.admin_info.stats_normalization.skip_reason}
                mono
              />
            )}
          </DetailSection>
        )}
```

**3c. i18n**：`en.json` 加 key（值同 key），`zh.json` 加翻译，然后 `bun run i18n:sync`：

| key | zh |
|---|---|
| `Stats normalization` | `统计口径归一化` |
| `Target caliber` | `目标口径` |
| `Upstream caliber` | `上游口径` |
| `Original prompt tokens` | `原始 prompt tokens` |
| `Normalized prompt tokens` | `归一化 prompt tokens` |
| `Skip reason` | `跳过原因` |

- [ ] **Step 4: 跑测试 + typecheck + lint**

Run:
```bash
cd web
bun run test -- src/features/usage-logs/components/__tests__/stats-normalization.test.tsx
bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/usage-logs/types.ts src/features/usage-logs/components/dialogs/details-dialog.tsx src/features/usage-logs/components/__tests__/stats-normalization.test.tsx
```
Expected: 测试 PASS、typecheck 无错误、lint 无 error。

- [ ] **Step 5: Commit**

```bash
git add web/src/features/usage-logs web/src/i18n/locales
git commit -m "feat(web): show stats normalization info in admin log details"
```

---

### Task 6: 整体验证与交接说明

**Files:** 无新增改动（仅验证；交接说明写入 PR/提交描述，不新建 docs 文件——项目规则禁止未经请求新增 `docs/` 文件）。

- [ ] **Step 1: 后端全量构建与测试**

Run: `go build ./... && go test ./service/ ./setting/...`
Expected: 构建成功，测试全 PASS。

- [ ] **Step 2: 前端全量验证**

Run: `cd web && bun run typecheck && bun run test && bun run build`
Expected: typecheck 无错误、测试全 PASS、构建成功。

- [ ] **Step 3: 手动验证（需要可运行实例）**

起一个实例，同一 Claude 渠道和 OpenAI 渠道各发一笔带缓存的请求，分别在 `upstream` / `exclude_cache` / `include_cache` 三档下检查 log 表 `prompt_tokens` 与后台日志详情展示：
- `upstream`：两渠道落库值与现状一致，详情无「Stats normalization」区块。
- `exclude_cache`：OpenAI 渠道落库值减去缓存合计并出现标记；Claude 渠道值不变、标记 `applied=false, skip_reason=already_matches_target`。
- `include_cache`：Claude 渠道落库值加上缓存合计；OpenAI 渠道 `already_matches_target`。

- [ ] **Step 4: 交接说明必须包含的声明**

- **数据库兼容性**：无 schema 变更、无新 SQL、无迁移——`RecordConsumeLog` 写入路径不变，仅传入的 `prompt_tokens` 数值不同；设置走已存在的 option 存储（`handleConfigUpdate` 分层配置机制）。因此本变更不触发 SQLite/MySQL/PostgreSQL 三库验证矩阵。
- **不影响面**：计费（quota 计算、预扣/结算）、Langfuse、客户端响应、relaykit、任务插件均未改动（relaykit 无需 `GOWORK=off` 独立构建验证）。
- **口径切换提示**：不回溯历史日志，切换点前后聚合数据不可直接对比。

---

## Self-Review 记录

- **Spec coverage**：设置项(§组件1)→Task 1；归一化函数(§组件2、换算规则、兜底规则 1/3/6)→Task 2；接线与标记(§组件3、错误处理)→Task 3；前端设置 UI(§组件4)→Task 4；日志显示(§组件5)→Task 5；测试/验证(§测试、§验证)→各 Task 内嵌 + Task 6。兜底规则 2（渠道级逃生门）为文案说明，落在 Task 4 的 FormDescription；兜底规则 4/5 由现有 `InputExcludesCache` 机制衔接，归一化函数直接消费它，无需新增代码。
- **Placeholder scan**：所有代码步骤含完整代码；测试含完整断言。
- **Type consistency**：`normalizeLogPromptTokens(summary textQuotaSummary, target string) (int, bool, string)`（Task 2 定义 = Task 3 调用）；marker 键 `target/upstream_caliber/original_prompt_tokens/applied/skip_reason`（Task 3 写入 = Task 5 读取）；`parseUsageStatsCacheCaliber`（Task 4 定义于 pricing-section、registry 消费）；`GetUsageStatsCacheCaliber()`（Task 1 定义 = Task 3 调用）。
