package model

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupDurationLogDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB, LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&Log{}))
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.LogConsumeEnabled = previousLogConsumeEnabled
		_ = sqlDB.Close()
	})
}

func newDurationTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// TestRecordConsumeLogPersistsDurationMs pins the millisecond-duration
// contract of consume logs: the use_time column keeps truncated seconds for
// backward compatibility while other.duration_ms carries the precise value
// consumed by the timing UI. Callers that pass no duration (task/MJ billing)
// must not get a duration_ms key so the frontend falls back to use_time.
func TestRecordConsumeLogPersistsDurationMs(t *testing.T) {
	setupDurationLogDB(t)

	cases := []struct {
		name           string
		useTimeMillis  int64
		other          map[string]interface{}
		wantUseTime    int
		wantDurationMs *float64 // nil = key must be absent
	}{
		{
			name:           "millisecond duration recorded with truncated seconds column",
			useTimeMillis:  1543,
			other:          map[string]interface{}{"model_ratio": 1.0},
			wantUseTime:    1,
			wantDurationMs: &[]float64{1543}[0],
		},
		{
			name:          "zero duration skips duration_ms for legacy callers",
			useTimeMillis: 0,
			other:         map[string]interface{}{"model_ratio": 1.0},
			wantUseTime:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			otherMetadata := NewLogOther()
			otherMetadata.MergePublic(tc.other)
			RecordConsumeLog(newDurationTestContext(), 1, RecordConsumeLogParams{
				ModelName:     "gpt-test",
				Content:       "test",
				UseTimeMillis: tc.useTimeMillis,
				Other:         otherMetadata,
			})

			var log Log
			require.NoError(t, DB.Last(&log).Error)
			assert.Equal(t, tc.wantUseTime, log.UseTime)
			other, err := common.StrToMap(log.Other)
			require.NoError(t, err)
			if tc.wantDurationMs == nil {
				assert.NotContains(t, other, "duration_ms")
			} else {
				assert.Equal(t, *tc.wantDurationMs, other["duration_ms"])
			}
		})
	}
}

// TestRecordErrorLogPersistsDurationMs pins the same duration contract for
// error logs, whose other map is built by the caller and therefore needs the
// injection inside RecordErrorLog.
func TestRecordErrorLogPersistsDurationMs(t *testing.T) {
	setupDurationLogDB(t)

	metadata := NewLogOther()
	metadata.SetPublic("error_code", "500")
	RecordErrorLog(newDurationTestContext(), 1, 2, "gpt-test", "tok", "boom", 3,
		1543, false, "default", metadata)

	var log Log
	require.NoError(t, DB.Last(&log).Error)
	assert.Equal(t, 1, log.UseTime)
	other, err := common.StrToMap(log.Other)
	require.NoError(t, err)
	require.Contains(t, other, "duration_ms")
	assert.Equal(t, float64(1543), other["duration_ms"])
}
