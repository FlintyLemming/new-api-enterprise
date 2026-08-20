package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupExchangeKeyQuotaTest(t *testing.T) *model.User {
	t.Helper()

	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousBatchUpdate := common.BatchUpdateEnabled

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))

	model.DB = db
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.BatchUpdateEnabled = previousBatchUpdate
		require.NoError(t, sqlDB.Close())
	})

	user := &model.User{
		Username: "exchange-key-quota-user",
		Password: "password-placeholder",
		Quota:    1000,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		AffCode:  "exchange-key-quota",
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func TestExchangeKeyPostConsumeDoesNotTouchTokens(t *testing.T) {
	user := setupExchangeKeyQuotaTest(t)
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: 0, TokenKey: "", TokenUnlimited: true, IsExchangeKey: true,
	}

	require.NoError(t, PostConsumeQuota(info, 10, 0, false))

	var tokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Equal(t, int64(0), tokenCount)

	fresh, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, 990, fresh)
}

func TestLimitedTokenStillDecreasesRemainQuota(t *testing.T) {
	user := setupExchangeKeyQuotaTest(t)
	token := &model.Token{
		UserId: user.Id, Key: "limited-token", Name: "limited", Status: common.TokenStatusEnabled, RemainQuota: 20,
	}
	require.NoError(t, model.DB.Create(token).Error)
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: false,
	}

	require.NoError(t, PreConsumeTokenQuota(info, 5))

	var got model.Token
	require.NoError(t, model.DB.First(&got, token.Id).Error)
	assert.Equal(t, token.RemainQuota-5, got.RemainQuota)
}

func TestPreWssConsumeQuotaExchangeKeySkipsMissingTokenRow(t *testing.T) {
	user := setupExchangeKeyQuotaTest(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: 0, TokenKey: "", TokenUnlimited: true,
		IsExchangeKey: true, OriginModelName: "gpt-4", UsingGroup: "default", UserGroup: "default",
	}

	err := PreWssConsumeQuota(c, info, &dto.RealtimeUsage{
		InputTokenDetails:  dto.InputTokenDetails{TextTokens: 1},
		OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 1},
	})

	require.NoError(t, err)
}

func TestExchangeKeyBillingSessionWalletOnlySkipsTokenRows(t *testing.T) {
	user := setupExchangeKeyQuotaTest(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: 0, TokenKey: "", TokenUnlimited: true,
		IsExchangeKey: true, ForcePreConsume: true,
		UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
	}

	session, apiErr := NewBillingSession(c, info, 8)

	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, 8, session.GetPreConsumedQuota())
	fresh, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, 992, fresh)
	var tokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Equal(t, int64(0), tokenCount)
}
