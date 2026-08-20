package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	exchangeKeyBillingRemainQuota = 1_000_000
	exchangeKeyBillingUsedQuota   = 250_000
)

type billingErrorEnvelope struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func setupExchangeKeyBillingTest(t *testing.T) *model.User {
	t.Helper()
	gin.SetMode(gin.TestMode)

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	previousDisplayTokenStat := common.DisplayTokenStatEnabled
	gs := operation_setting.GetGeneralSetting()
	previousDisplayType := gs.QuotaDisplayType

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	model.LOG_DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.DisplayTokenStatEnabled = true
	gs.QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD

	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousType)
		common.SetLogDatabaseType(previousLogType)
		common.RedisEnabled = previousRedis
		common.DisplayTokenStatEnabled = previousDisplayTokenStat
		gs.QuotaDisplayType = previousDisplayType
	})

	user := &model.User{
		Username:    "exchange-billing",
		Password:    "password-placeholder",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Quota:       exchangeKeyBillingRemainQuota,
		UsedQuota:   exchangeKeyBillingUsedQuota,
		AuthVersion: 1,
		AffCode:     "ek-billing",
	}
	require.NoError(t, model.DB.Create(user).Error)
	require.NotZero(t, user.Id)
	return user
}

func exchangeKeyBillingContext(t *testing.T, userID int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/dashboard/billing/subscription", nil)
	c.Set("id", userID)
	c.Set("token_id", 0)
	common.SetContextKey(c, constant.ContextKeyExchangeKey, true)
	return c, recorder
}

func TestGetSubscriptionUsesUserQuotaForExchangeKey(t *testing.T) {
	user := setupExchangeKeyBillingTest(t)
	c, recorder := exchangeKeyBillingContext(t, user.Id)

	require.NotPanics(t, func() {
		GetSubscription(c)
	})

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope billingErrorEnvelope
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Nil(t, envelope.Error, "body must not be an error envelope: %s", recorder.Body.String())

	var subscription OpenAISubscriptionResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &subscription))
	assert.Equal(t, "billing_subscription", subscription.Object)
	expected := float64(exchangeKeyBillingRemainQuota+exchangeKeyBillingUsedQuota) / common.QuotaPerUnit
	assert.InDelta(t, expected, subscription.SoftLimitUSD, 1e-9)
	assert.InDelta(t, expected, subscription.HardLimitUSD, 1e-9)
	assert.InDelta(t, expected, subscription.SystemHardLimitUSD, 1e-9)
	assert.Equal(t, int64(0), subscription.AccessUntil)
}

func TestGetUsageUsesUserQuotaForExchangeKey(t *testing.T) {
	user := setupExchangeKeyBillingTest(t)
	c, recorder := exchangeKeyBillingContext(t, user.Id)

	require.NotPanics(t, func() {
		GetUsage(c)
	})

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope billingErrorEnvelope
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Nil(t, envelope.Error, "body must not be an error envelope: %s", recorder.Body.String())

	var usage OpenAIUsageResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &usage))
	assert.Equal(t, "list", usage.Object)
	expected := float64(exchangeKeyBillingUsedQuota) / common.QuotaPerUnit * 100
	assert.InDelta(t, expected, usage.TotalUsage, 1e-9)
}
