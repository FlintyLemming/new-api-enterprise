# use_time 毫秒精度改造 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change `logs.use_time` from integer seconds to integer milliseconds, aligning precision with the existing `frt` (first response time) field, per GitHub Issue #972.

**Architecture:** The change touches 3 layers: (1) Go data model in `model/log.go` — widen `int` to `int64`, (2) 7 call sites that compute `time.Now().Unix() - relayInfo.StartTime.Unix()` — switch to `.UnixMilli()` diff, (3) frontend display — update thresholds and add backward-compatibility heuristic for pre-migration records still stored in seconds. GORM auto-migration handles the DB column type change (`int` → `bigint`) on startup.

**Tech Stack:** Go 1.22+, GORM v2, React 18, Semi Design UI

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `model/log.go:31` | `Log.UseTime`: `int` → `int64` |
| Modify | `model/log.go:198` | `RecordConsumeLogParams.UseTimeSeconds`: `int` → `int64` |
| Modify | `model/log.go:145` | `RecordErrorLog` param `useTimeSeconds`: `int` → `int64` |
| Modify | `model/log.go:232` | `Log.UseTime = params.UseTimeSeconds` — assignment, no type cast needed after field type change |
| Modify | `model/log.go:171` | `Log.UseTime = useTimeSeconds` — assignment, no type cast needed after param type change |
| Modify | `service/text_quota.go:33` | `textQuotaSummary.UseTimeSeconds`: `int64` (already int64, stays) |
| Modify | `service/text_quota.go:84` | Millis calculation replacing Unix() diff |
| Modify | `service/text_quota.go:425` | Remove `int()` cast |
| Modify | `service/quota.go:160,231` | PostWssConsumeQuota: millis calc, remove `int()` cast |
| Modify | `service/quota.go:261,336` | PostAudioConsumeQuota: millis calc, remove `int()` cast |
| Modify | `service/violation_fee.go:134,157` | ChargeViolationFeeIfNeeded: millis calc, remove `int()` cast |
| Modify | `controller/relay.go:391` | processChannelError: `.Seconds()` → `.Milliseconds()`, remove `int()` cast |
| Modify | `controller/channel-test.go:483-495` | Channel test: already computes millis, remove `int()` cast |
| Modify | `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx:206-230` | `renderUseTime`: convert ms display + backward-compat heuristic |
| Create | `service/log_test.go` | Unit test for millisecond calculation accuracy |

---

### Task 1: Data Model — widen UseTime to int64

**Files:**
- Modify: `model/log.go:31`
- Modify: `model/log.go:145`
- Modify: `model/log.go:198`

- [ ] **Step 1: Write failing test for sub-second precision**

Create `service/log_test.go`:

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

	// Simulate a request that took 1.5 seconds
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

	// 1.5 seconds should be recorded as ~1500 milliseconds (±50ms for test timing)
	require.GreaterOrEqual(t, summary.UseTimeSeconds, int64(1450),
		"use_time should be in milliseconds, expected ~1500ms")
	require.LessOrEqual(t, summary.UseTimeSeconds, int64(1550),
		"use_time should be in milliseconds, expected ~1500ms")

	// Verify it's NOT in seconds (would be 1)
	require.Greater(t, summary.UseTimeSeconds, int64(1000),
		"use_time should not be in seconds (would be 1)")
}

func TestUseTimeSubSecondPrecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	// Simulate a very fast request: 200ms
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

	// 200ms should be recorded as ~200 milliseconds (±50ms)
	require.GreaterOrEqual(t, summary.UseTimeSeconds, int64(150),
		"sub-second request should record ~200ms, not truncated to 0")
	require.LessOrEqual(t, summary.UseTimeSeconds, int64(250),
		"sub-second request should record ~200ms, not truncated to 0")
}

func TestUseTimeFitsInInt64(t *testing.T) {
	// Verify the model field type is int64 (compile-time check)
	var _ int64 = model.Log{}.UseTime
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./service -run 'TestUseTime' -v`

Expected:
- `TestUseTimeMillisecondsPrecision` FAILS — current code returns `1` (seconds), not `~1500` (milliseconds)
- `TestUseTimeSubSecondPrecision` FAILS — current code returns `0` (truncated from 0.2s), not `~200`
- `TestUseTimeFitsInInt64` FAILS — `Log.UseTime` is `int`, compilation error or type mismatch

- [ ] **Step 3: Widen Log.UseTime to int64**

In `model/log.go:31`, change:
```go
	UseTime          int    `json:"use_time" gorm:"default:0"`
```
to:
```go
	UseTime          int64  `json:"use_time" gorm:"default:0;type:bigint"`
```

- [ ] **Step 4: Widen RecordConsumeLogParams.UseTimeSeconds to int64**

In `model/log.go:198`, change:
```go
	UseTimeSeconds   int                    `json:"use_time_seconds"`
```
to:
```go
	UseTimeSeconds   int64                  `json:"use_time_seconds"`
```

- [ ] **Step 5: Widen RecordErrorLog param to int64**

In `model/log.go:145`, change the function signature from:
```go
func RecordErrorLog(c *gin.Context, userId int, channelId int, modelName string, tokenName string, content string, tokenId int, useTimeSeconds int,
```
to:
```go
func RecordErrorLog(c *gin.Context, userId int, channelId int, modelName string, tokenName string, content string, tokenId int, useTimeSeconds int64,
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./service -run 'TestUseTime' -v`

Expected: All 3 tests PASS

- [ ] **Step 7: Verify the project compiles**

Run: `go build ./...`

Expected: BUILD FAILS — several call sites still pass `int` where `int64` is now required, and `int()` casts exist. This is expected; subsequent tasks will fix these.

---

### Task 2: Switch time calculations from seconds to milliseconds

**Files:**
- Modify: `service/text_quota.go:84` — `calculateTextQuotaSummary`
- Modify: `service/quota.go:160` — `PostWssConsumeQuota`
- Modify: `service/quota.go:261` — `PostAudioConsumeQuota`
- Modify: `service/violation_fee.go:134` — `ChargeViolationFeeIfNeeded`
- Modify: `controller/relay.go:391` — `processChannelError`

- [ ] **Step 1: Fix text_quota.go calculation**

In `service/text_quota.go:84`, change:
```go
		UseTimeSeconds:       time.Now().Unix() - relayInfo.StartTime.Unix(),
```
to:
```go
		UseTimeSeconds:       time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli(),
```

In `service/text_quota.go:425`, change:
```go
		UseTimeSeconds:   int(summary.UseTimeSeconds),
```
to:
```go
		UseTimeSeconds:   summary.UseTimeSeconds,
```

- [ ] **Step 2: Fix quota.go — PostWssConsumeQuota**

In `service/quota.go:160`, change:
```go
	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
```
to:
```go
	useTimeSeconds := time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli()
```

In `service/quota.go:231`, change:
```go
		UseTimeSeconds:   int(useTimeSeconds),
```
to:
```go
		UseTimeSeconds:   useTimeSeconds,
```

- [ ] **Step 3: Fix quota.go — PostAudioConsumeQuota**

In `service/quota.go:261`, change:
```go
	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
```
to:
```go
	useTimeSeconds := time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli()
```

In `service/quota.go:336`, change:
```go
		UseTimeSeconds:   int(useTimeSeconds),
```
to:
```go
		UseTimeSeconds:   useTimeSeconds,
```

- [ ] **Step 4: Fix violation_fee.go**

In `service/violation_fee.go:134`, change:
```go
	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
```
to:
```go
	useTimeSeconds := time.Now().UnixMilli() - relayInfo.StartTime.UnixMilli()
```

In `service/violation_fee.go:157`, change:
```go
		UseTimeSeconds: int(useTimeSeconds),
```
to:
```go
		UseTimeSeconds: useTimeSeconds,
```

- [ ] **Step 5: Fix controller/relay.go — error path**

In `controller/relay.go:391`, change:
```go
		useTimeSeconds := int(time.Since(startTime).Seconds())
```
to:
```go
		useTimeSeconds := time.Since(startTime).Milliseconds()
```

- [ ] **Step 6: Fix controller/channel-test.go**

In `controller/channel-test.go:482-495`, the code already computes milliseconds but casts to `int`. Update it to use `int64`:

Change lines 482-484:
```go
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	consumedTime := float64(milliseconds) / 1000.0
```
to:
```go
	consumedTimeMillis := time.Since(tik).Milliseconds()
```

And line 495:
```go
		UseTimeSeconds:   int(consumedTime),
```
to:
```go
		UseTimeSeconds:   consumedTimeMillis,
```

- [ ] **Step 7: Run the full build**

Run: `go build ./...`

Expected: BUILD SUCCESS — no type mismatches.

- [ ] **Step 8: Run all existing tests**

Run: `go test ./service/... ./model/... ./controller/... -v`

Expected: All tests PASS, including the new `TestUseTime*` tests from Task 1.

- [ ] **Step 9: Commit**

```bash
git add model/log.go service/text_quota.go service/quota.go service/violation_fee.go service/log_test.go controller/relay.go controller/channel-test.go
git commit -m "feat: change use_time from seconds to milliseconds precision

- Widen Log.UseTime and related fields from int to int64
- Use UnixMilli() diff instead of Unix() diff for millisecond precision
- Remove unnecessary int() casts throughout the call chain

Fixes #972"
```

---

### Task 3: Frontend display — millisecond rendering with backward compatibility

**Files:**
- Modify: `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx:206-230`

**Backward-compatibility strategy:** Since old records (pre-migration) store seconds as small integers, while new records store milliseconds (larger integers), use a magnitude heuristic: values `< 100000` are assumed to be seconds (old records), values `>= 100000` are milliseconds (new records). The threshold of 100000ms = 100s is safe because:
- No API request takes 100+ seconds in normal operation
- Old second-based values for even a 27-hour request (97200) would be `< 100000`

- [ ] **Step 1: Update renderUseTime function**

In `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx`, replace the `renderUseTime` function (lines 206-230):

Old:
```jsx
function renderUseTime(type, t) {
  const time = parseInt(type);
  if (time < 101) {
    return (
      <Tag color='green' shape='circle'>
        {' '}
        {time} s{' '}
      </Tag>
    );
  } else if (time < 300) {
    return (
      <Tag color='orange' shape='circle'>
        {' '}
        {time} s{' '}
      </Tag>
    );
  } else {
    return (
      <Tag color='red' shape='circle'>
        {' '}
        {time} s{' '}
      </Tag>
    );
  }
}
```

New:
```jsx
function renderUseTime(type, t) {
  const rawValue = parseInt(type);
  // Backward compatibility: old records store seconds (< 100000),
  // new records store milliseconds (>= 100000).
  const isMs = rawValue >= 100000;
  const time = isMs ? (rawValue / 1000.0).toFixed(1) : rawValue;
  const unit = isMs ? 's' : 's';
  const color = isMs ? (rawValue < 101000 ? 'green' : rawValue < 300000 ? 'orange' : 'red') : (rawValue < 101 ? 'green' : rawValue < 300 ? 'orange' : 'red');
  return (
    <Tag color={color} shape='circle'>
      {' '}
      {time} {unit}{' '}
    </Tag>
  );
}
```

- [ ] **Step 2: Verify frontend builds**

Run: `cd web && bun run build`

Expected: BUILD SUCCESS with no type errors.

- [ ] **Step 3: Manual verification (dev server)**

Run: `bun run dev`

Verify in browser:
- Navigate to Usage Logs page
- New log entries should display `1.5 s` format (1 decimal, from milliseconds)
- Old log entries (from before migration) should still display as `5 s` format (integer seconds)
- Color coding: green (< 101s = < 101000ms), orange (101-299s), red (300s+)

- [ ] **Step 4: Commit**

```bash
git add web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx
git commit -m "feat(ui): render use_time as milliseconds with backward compatibility

- Convert new millisecond values to seconds for display (1 decimal)
- Use magnitude heuristic (< 100000) to detect old second-based records
- Update color thresholds to match millisecond precision

Part of #972"
```

---

### Task 4: Verify GORM auto-migration handles the column type change

**Files:**
- Verify: `model/main.go` (auto-migration code)

- [ ] **Step 1: Find the auto-migration call for Log**

Grep for where `Log` is auto-migrated:

```bash
grep -rn 'AutoMigrate.*Log\|&model.Log{}' model/
```

Verify that `model.Log` is included in the `AutoMigrate` call. GORM will automatically alter the column from `int` to `bigint` when the application restarts.

- [ ] **Step 2: Verify migration works on all three databases**

Run: `go build -o /tmp/new-api-test ./...`

Then start the app locally with SQLite (default dev config):
```bash
./new-api
```

Check the logs for the AutoMigrate output — it should show the `logs` table being migrated. Look for messages indicating the `use_time` column type change.

If the app doesn't log migration details, verify by checking the schema:
```bash
# SQLite
sqlite3 new-api.db ".schema logs" | grep use_time
# Should show use_time as integer (SQLite doesn't distinguish int vs bigint)

# MySQL (if available)
# mysql -e "DESCRIBE logs;" | grep use_time
# Should show use_time as bigint(20)

# PostgreSQL (if available)
# psql -c "\d logs" | grep use_time
# Should show use_time as bigint
```

- [ ] **Step 3: End-to-end test**

With the app running, send a test API request:
```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <your-api-key>" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "hello"}],
    "stream": false
  }'
```

Then check the logs table:
```bash
# SQLite
sqlite3 new-api.db "SELECT id, use_time, created_at FROM logs ORDER BY id DESC LIMIT 5;"
```

Expected: `use_time` values should be in the thousands (e.g., `1523` for a 1.5s request), not single digits.

- [ ] **Step 4: Commit any migration-related changes (if needed)**

If GORM auto-migration requires explicit migration code (unlikely), add it. Otherwise, no commit needed — the model type change in Task 1 is sufficient.

---

## Risk & Rollback

**Risk:** GORM AutoMigrate may fail to alter a column type on some database configurations (e.g., SQLite with existing data). If this happens, add an explicit migration step that creates a new column, copies data with conversion, and drops the old column.

**Rollback:** Revert the model type change and frontend changes. Old code will read millisecond values as seconds (over-reporting duration by 1000x), but the system won't break. To avoid this, the frontend backward-compat heuristic handles both old and new formats.

## Post-Implementation Verification Checklist

- [ ] `go build ./...` succeeds
- [ ] `go test ./...` passes
- [ ] GORM auto-migration runs without errors on startup
- [ ] New log entries have `use_time` in milliseconds (thousands range for typical requests)
- [ ] Frontend displays both old (seconds) and new (milliseconds) records correctly
- [ ] No regression: `frt` (first response time) still works (already in milliseconds, unchanged)
