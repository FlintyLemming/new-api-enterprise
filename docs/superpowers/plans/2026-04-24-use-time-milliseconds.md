# Use Time Milliseconds Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store and display `logs.use_time` with millisecond precision while preserving existing log data and cross-database compatibility.

**Architecture:** Convert all request-duration producers from integer seconds to integer milliseconds, keep `logs.use_time` as the persisted field, and migrate legacy second-based rows once. The API can remain backward-compatible by keeping existing JSON field names, but internal names should explicitly say milliseconds to avoid repeating the current TTFT/use_time precision split.

**Tech Stack:** Go 1.22+, Gin, GORM v2, SQLite/MySQL/PostgreSQL, React 18, Vite, Bun, Semi UI.

---

## Current State Summary

- TTFT (`other.frt`) already uses `UnixMilli()` in `service/log_info_generate.go` and stores milliseconds in the `other` JSON field.
- `use_time` historically used `time.Now().Unix() - relayInfo.StartTime.Unix()`, which truncates to whole seconds.
- The model historically used `int`, but current working tree already shows `model.Log.UseTime int64` with `gorm:"default:0;type:bigint"`.
- Current working tree already shows some milliseconds edits in `service/text_quota.go`, `service/quota.go`, and `service/violation_fee.go`; this plan should be used to verify and complete them cleanly.
- GitHub issue #972 requests exactly this: change `logs.use_time` from seconds to milliseconds and handle DB migration/backward compatibility.

## File Structure

- Modify `service/text_quota.go`: calculate text relay duration in milliseconds and rename internal fields from `UseTimeSeconds` to `UseTimeMilliseconds`.
- Modify `service/quota.go`: calculate WebSocket/audio relay duration in milliseconds and pass millisecond values into log recording.
- Modify `service/violation_fee.go`: calculate violation billing duration in milliseconds.
- Modify `model/log.go`: store `UseTime` as `int64`/`bigint`, rename parameter fields to millisecond semantics, and keep JSON field names stable where needed.
- Modify `model/main.go`: add a one-time cross-DB migration that converts legacy small `use_time` second values to milliseconds.
- Modify `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx`: render `use_time` as milliseconds divided by 1000, matching TTFT display behavior.
- Add/modify `service/log_test.go`: prove text quota uses millisecond precision, including sub-second durations and compile-time `int64` storage.
- Add/modify `model/log_migration_test.go` if the project has adjacent DB migration test patterns; otherwise keep migration tested manually across SQLite/MySQL/PostgreSQL.

## Data Compatibility Decision

Keep the database column name `use_time` and API JSON field `use_time` unchanged, but change the unit to milliseconds. This avoids schema/API breakage while matching issue #972. Document the unit at code boundaries with names like `UseTimeMilliseconds`; avoid creating a second column such as `use_time_ms` because it would force frontend/API consumers to handle two fields.

Legacy migration heuristic:

```sql
UPDATE logs SET use_time = use_time * 1000 WHERE use_time < 100000 AND use_time > 0
```

Rationale:

- Old second-based rows are normally far below `100000` seconds.
- New millisecond rows for ordinary API calls may be below `100000` too, so the migration must run only once and record completion in `options` using `log_use_time_migrated`.
- `0` remains `0`.
- The SQL uses only portable arithmetic and comparisons, compatible with SQLite, MySQL, and PostgreSQL.

---

### Task 1: Add Text Duration Tests

**Files:**
- Modify: `service/log_test.go`

- [ ] **Step 1: Write failing tests for millisecond precision**

Add or keep these tests in `service/log_test.go`. If the file already exists with equivalent tests, verify names and assertions match this intent.

```go
package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUseTimeMillisecondsPrecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	startTime := time.Now().Add(-1500 * time.Millisecond)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		OriginModelName:         "gpt-4",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenAI,
		},
		PriceData: types.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: startTime,
	}

	usage := &dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 5,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.GreaterOrEqual(t, summary.UseTimeMilliseconds, int64(1450))
	require.LessOrEqual(t, summary.UseTimeMilliseconds, int64(1550))
	require.Greater(t, summary.UseTimeMilliseconds, int64(1000))
}

func TestUseTimeSubSecondPrecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	startTime := time.Now().Add(-200 * time.Millisecond)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		OriginModelName:         "gpt-4",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenAI,
		},
		PriceData: types.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: startTime,
	}

	usage := &dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 5,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.GreaterOrEqual(t, summary.UseTimeMilliseconds, int64(150))
	require.LessOrEqual(t, summary.UseTimeMilliseconds, int64(250))
}

func TestUseTimeFitsInInt64(t *testing.T) {
	var _ int64 = model.Log{}.UseTime
}
```

- [ ] **Step 2: Run tests to verify they fail before implementation**

Run:

```bash
go test ./service -run 'TestUseTime(MillisecondsPrecision|SubSecondPrecision|FitsInInt64)' -count=1
```

Expected before implementation:

- If the old code is still present, the first two tests fail because `UseTimeSeconds` is measured in seconds or the renamed `UseTimeMilliseconds` field does not exist.
- If current working tree already has partial implementation, the tests may already pass; still continue with naming cleanup and full path audit.

---

### Task 2: Rename Text Summary Duration Semantics

**Files:**
- Modify: `service/text_quota.go`

- [ ] **Step 1: Rename the summary field**

Change `textQuotaSummary` from second semantics:

```go
type textQuotaSummary struct {
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	CacheTokens              int
	CacheCreationTokens      int
	CacheCreationTokens5m    int
	CacheCreationTokens1h    int
	ImageTokens              int
	AudioTokens              int
	ModelName                string
	TokenName                string
	UseTimeSeconds           int64
	CompletionRatio          float64
	CacheRatio               float64
	ImageRatio               float64
	ModelRatio               float64
	GroupRatio               float64
	ModelPrice               float64
	CacheCreationRatio       float64
	CacheCreationRatio5m     float64
	CacheCreationRatio1h     float64
	Quota                    int
	IsClaudeUsageSemantic    bool
	UsageSemantic            string
	WebSearchPrice           float64
	WebSearchCallCount       int
	ClaudeWebSearchPrice     float64
	ClaudeWebSearchCallCount int
	FileSearchPrice          float64
	FileSearchCallCount      int
	AudioInputPrice          float64
	ImageGenerationCallPrice float64
}
```

To millisecond semantics:

```go
type textQuotaSummary struct {
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	CacheTokens              int
	CacheCreationTokens      int
	CacheCreationTokens5m    int
	CacheCreationTokens1h    int
	ImageTokens              int
	AudioTokens              int
	ModelName                string
	TokenName                string
	UseTimeMilliseconds      int64
	CompletionRatio          float64
	CacheRatio               float64
	ImageRatio               float64
	ModelRatio               float64
	GroupRatio               float64
	ModelPrice               float64
	CacheCreationRatio       float64
	CacheCreationRatio5m     float64
	CacheCreationRatio1h     float64
	Quota                    int
	IsClaudeUsageSemantic    bool
	UsageSemantic            string
	WebSearchPrice           float64
	WebSearchCallCount       int
	ClaudeWebSearchPrice     float64
	ClaudeWebSearchCallCount int
	FileSearchPrice          float64
	FileSearchCallCount      int
	AudioInputPrice          float64
	ImageGenerationCallPrice float64
}
```

- [ ] **Step 2: Calculate milliseconds with `UnixMilli()`**

In `calculateTextQuotaSummary`, set the duration as milliseconds:

```go
summary := textQuotaSummary{
	ModelName:            relayInfo.OriginModelName,
	TokenName:            ctx.GetString("token_name"),
	UseTimeMilliseconds:  time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli(),
	CompletionRatio:      relayInfo.PriceData.CompletionRatio,
	CacheRatio:           relayInfo.PriceData.CacheRatio,
	ImageRatio:           relayInfo.PriceData.ImageRatio,
	ModelRatio:           relayInfo.PriceData.ModelRatio,
	GroupRatio:           relayInfo.PriceData.GroupRatioInfo.GroupRatio,
	ModelPrice:           relayInfo.PriceData.ModelPrice,
	CacheCreationRatio:   relayInfo.PriceData.CacheCreationRatio,
	CacheCreationRatio5m: relayInfo.PriceData.CacheCreation5mRatio,
	CacheCreationRatio1h: relayInfo.PriceData.CacheCreation1hRatio,
	UsageSemantic:        usageSemanticFromUsage(relayInfo, usage),
}
```

- [ ] **Step 3: Update log recording call sites in `service/text_quota.go`**

Replace `summary.UseTimeSeconds` with `summary.UseTimeMilliseconds` where `RecordConsumeLogParams` is built:

```go
recordConsumeLogParams := model.RecordConsumeLogParams{
	UserId:                  userId,
	Username:                username,
	ChannelId:               relayInfo.ChannelId,
	PromptTokens:            summary.PromptTokens,
	CompletionTokens:        summary.CompletionTokens,
	ModelName:               summary.ModelName,
	TokenName:               summary.TokenName,
	Quota:                   summary.Quota,
	Content:                 content,
	UseTimeMilliseconds:     summary.UseTimeMilliseconds,
	IsStream:                relayInfo.IsStream,
	Group:                   relayInfo.Group,
	RequestId:               relayInfo.RequestId,
	Other:                   other,
	ChannelType:             relayInfo.ChannelType,
	CacheTokens:             summary.CacheTokens,
	CacheCreationTokens:     summary.CacheCreationTokens,
	CacheCreationTokens5m:   summary.CacheCreationTokens5m,
	CacheCreationTokens1h:   summary.CacheCreationTokens1h,
	UsageSemantic:           summary.UsageSemantic,
	WebSearchPrice:          summary.WebSearchPrice,
	WebSearchCallCount:      summary.WebSearchCallCount,
	ClaudeWebSearchPrice:    summary.ClaudeWebSearchPrice,
	ClaudeWebSearchCallCount: summary.ClaudeWebSearchCallCount,
	FileSearchPrice:         summary.FileSearchPrice,
	FileSearchCallCount:     summary.FileSearchCallCount,
	AudioInputPrice:         summary.AudioInputPrice,
	ImageGenerationCallPrice: summary.ImageGenerationCallPrice,
}
```

Keep only fields that already exist in the actual struct; the important required change is `UseTimeMilliseconds: summary.UseTimeMilliseconds`.

- [ ] **Step 4: Search for stale field names**

Run:

```bash
rg -n "UseTimeSeconds" service model
```

Expected after Task 2 and Task 4:

- No matches remain, or only intentionally backward-compatible JSON/API names in comments/tests if unavoidable.

---

### Task 3: Update Model Log Types

**Files:**
- Modify: `model/log.go`

- [ ] **Step 1: Ensure `Log.UseTime` is `int64` and stored as `bigint`**

Use this field definition in `model.Log`:

```go
UseTime int64 `json:"use_time" gorm:"default:0;type:bigint"`
```

This supports millisecond values and remains compatible with SQLite/MySQL/PostgreSQL through GORM.

- [ ] **Step 2: Rename log parameter field**

Change `RecordConsumeLogParams` from:

```go
type RecordConsumeLogParams struct {
	UserId          int
	Username        string
	ChannelId       int
	PromptTokens    int
	CompletionTokens int
	ModelName       string
	TokenName       string
	Quota           int
	Content         string
	UseTimeSeconds  int64
	IsStream        bool
	Group           string
	RequestId       string
	Other           map[string]interface{}
}
```

To:

```go
type RecordConsumeLogParams struct {
	UserId              int
	Username            string
	ChannelId           int
	PromptTokens        int
	CompletionTokens    int
	ModelName           string
	TokenName           string
	Quota               int
	Content             string
	UseTimeMilliseconds int64
	IsStream            bool
	Group               string
	RequestId           string
	Other               map[string]interface{}
}
```

Preserve all additional fields that exist in the real struct; only rename the duration field and keep its type `int64`.

- [ ] **Step 3: Store millisecond values without narrowing conversion**

In `RecordConsumeLog`, set:

```go
UseTime: params.UseTimeMilliseconds,
```

Do not cast to `int`; that reintroduces a platform-sized limit and contradicts the `bigint` storage.

- [ ] **Step 4: Keep API JSON stable**

If `RecordConsumeLogParams` or response DTOs expose JSON tags, keep existing `json:"use_time"` for `model.Log.UseTime`. Do not rename the API field to `use_time_ms` unless a separate API versioning task is explicitly created.

---

### Task 4: Update Non-Text Relay Producers

**Files:**
- Modify: `service/quota.go`
- Modify: `service/violation_fee.go`

- [ ] **Step 1: Update WebSocket duration in `service/quota.go`**

Replace:

```go
useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
```

With:

```go
useTimeMilliseconds := time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli()
```

When building `model.RecordConsumeLogParams`, pass:

```go
UseTimeMilliseconds: useTimeMilliseconds,
```

- [ ] **Step 2: Update audio duration in `service/quota.go`**

Replace:

```go
useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
```

With:

```go
useTimeMilliseconds := time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli()
```

When building `model.RecordConsumeLogParams`, pass:

```go
UseTimeMilliseconds: useTimeMilliseconds,
```

- [ ] **Step 3: Update violation duration in `service/violation_fee.go`**

Replace:

```go
useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
```

With:

```go
useTimeMilliseconds := time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli()
```

When building `model.RecordConsumeLogParams`, pass:

```go
UseTimeMilliseconds: useTimeMilliseconds,
```

- [ ] **Step 4: Audit all duration producers**

Run:

```bash
rg -n "Unix\(\) - relayInfo\.StartTime\.Unix\(\)|UseTimeSeconds|useTimeSeconds" service model relay
```

Expected:

- No matches remain.
- Unrelated `time.Now().Unix()` usages for timestamps remain untouched.

---

### Task 5: Add One-Time DB Migration

**Files:**
- Modify: `model/main.go`

- [ ] **Step 1: Call migration after `LOG_DB.AutoMigrate(&Log{})`**

Ensure `migrateLOGDB()` includes:

```go
func migrateLOGDB() error {
	var err error
	if err = LOG_DB.AutoMigrate(&Log{}); err != nil {
		return err
	}

	if err := migrateLogUseTime(); err != nil {
		common.SysLog(fmt.Sprintf("Warning: failed to migrate log use_time: %v", err))
	}

	return nil
}
```

`model/main.go` already imports `fmt` in many current trees; if it does not, add it to the import list.

- [ ] **Step 2: Implement migration guard using `Option`**

Add this function near other migration helpers in `model/main.go`:

```go
func migrateLogUseTime() error {
	var count int64
	if err := DB.Model(&Option{}).Where(&Option{Key: "log_use_time_migrated"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	common.SysLog("migrating log use_time to milliseconds precision...")

	if err := LOG_DB.Exec("UPDATE logs SET use_time = use_time * 1000 WHERE use_time < 100000 AND use_time > 0").Error; err != nil {
		return err
	}

	if err := DB.Create(&Option{Key: "log_use_time_migrated", Value: "true"}).Error; err != nil {
		common.SysLog(fmt.Sprintf("Warning: failed to record log_use_time_migrated: %v", err))
	}

	common.SysLog("successfully migrated log use_time to milliseconds")
	return nil
}
```

- [ ] **Step 3: Confirm cross-database compatibility**

Checklist:

- The migration SQL does not use MySQL-only or PostgreSQL-only functions.
- The migration does not use `ALTER COLUMN`, so SQLite is safe.
- `LOG_DB.Exec` targets the logs database, while `DB.Create(&Option{})` records the guard in the main options table.
- If a deployment has separate main/log databases, confirm `options` always lives in `DB`, not `LOG_DB`, matching existing migration patterns.

- [ ] **Step 4: Consider failure behavior**

The plan intentionally logs and continues if migration fails from `migrateLOGDB()`. This matches a best-effort compatibility migration and avoids blocking startup. If maintainers prefer fail-fast, change the caller to `return err`; do not silently ignore failures.

---

### Task 6: Update Frontend Display

**Files:**
- Modify: `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx`

- [ ] **Step 1: Render `use_time` from milliseconds**

Use this implementation:

```jsx
function renderUseTime(type, t, record) {
  const rawValue = parseInt(type) || 0;
  const timeNum = rawValue / 1000.0;
  const time = timeNum.toFixed(1);
  const color = timeNum < 101 ? 'green' : timeNum < 300 ? 'orange' : 'red';
  return (
    <Tag color={color} shape='circle'>
      {' '}
      {time} s{' '}
    </Tag>
  );
}
```

- [ ] **Step 2: Keep TTFT behavior unchanged**

Do not change `renderFirstUseTime` unless a bug is found. It already divides `other.frt` by `1000.0`:

```jsx
function renderFirstUseTime(type, t) {
  let time = parseFloat(type) / 1000.0;
  time = time.toFixed(1);
  // existing color/tag rendering continues here
}
```

- [ ] **Step 3: Check i18n impact**

No new visible strings are required if the UI continues to show `{time} s`. Do not add translation keys.

---

### Task 7: Verify and Format

**Files:**
- All modified Go and frontend files

- [ ] **Step 1: Format Go files**

Run:

```bash
gofmt -w service/text_quota.go service/quota.go service/violation_fee.go service/log_test.go model/log.go model/main.go
```

Expected:

- Command exits `0`.

- [ ] **Step 2: Run targeted Go tests**

Run:

```bash
go test ./service -run 'TestUseTime(MillisecondsPrecision|SubSecondPrecision|FitsInInt64)' -count=1
```

Expected:

- Tests pass.

- [ ] **Step 3: Run broader affected package tests**

Run:

```bash
go test ./service ./model -count=1
```

Expected:

- Tests pass.
- If unrelated pre-existing tests fail, capture the failing package/test names and do not fix unrelated issues in this task.

- [ ] **Step 4: Run frontend check if configured**

Inspect `web/package.json` scripts:

```bash
cat web/package.json | sed -n '/"scripts"/,/}/p'
```

If `lint` exists, run:

```bash
cd web && bun run lint
```

If only `build` exists or lint is unavailable, run:

```bash
cd web && bun run build
```

Expected:

- The frontend command exits `0`.
- Use Bun per project convention.

- [ ] **Step 5: Final audit for stale second semantics**

Run:

```bash
rg -n "UseTimeSeconds|useTimeSeconds|use_time.*秒|秒.*use_time|Unix\(\) - relayInfo\.StartTime\.Unix\(\)" service model web/src docs -S
```

Expected:

- No stale code references remain.
- Documentation may mention legacy seconds only when explaining the migration.

---

## Manual QA Checklist

- Create or simulate a text request lasting around `1500ms`; logs API should return `use_time` around `1500`, not `1`.
- Create or simulate a sub-second request around `200ms`; logs API should return around `200`, not `0`.
- Confirm usage logs table displays `1.5 s` for `use_time = 1500`.
- Confirm TTFT still displays correctly from `other.frt`.
- Start once with old rows like `use_time = 2`; after migration the value should become `2000`.
- Restart again; the same row must remain `2000`, not become `2000000`.

## Rollback Notes

If this change must be reverted after migration has run, do not blindly divide all rows by `1000`: new rows and migrated rows are indistinguishable except by deployment timing. Prefer restoring from backup or adding a deliberate reverse migration bounded by `created_at` and deployment timestamp.

## Self-Review

- Spec coverage: issue #972 is covered by producer calculations, `int64` storage, DB migration, frontend display, and tests.
- Placeholder scan: no `TBD`, `TODO`, or “similar to” implementation gaps remain.
- Type consistency: plan uses `UseTimeMilliseconds int64` internally and `use_time` externally; tests assert the renamed field.
- Cross-DB check: migration uses portable SQL and avoids unsupported SQLite `ALTER COLUMN`.
- Project policy check: protected project identifiers are not modified or removed.
