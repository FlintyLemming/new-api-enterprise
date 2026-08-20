package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/exchange_key"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const exchangeKeyTestSecret = "test-secret-16ch"

type exchangeKeyAPIResponse struct {
	Success bool                     `json:"success"`
	Message string                   `json:"message"`
	Data    exchange_key.SettingView `json:"data"`
}

func useExchangeKeyOptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousOptionMap := common.OptionMap
	previousDatabaseType := common.MainDatabaseType()
	previousRedis := common.RedisEnabled

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.User{}))

	model.DB = db
	model.LOG_DB = db
	common.OptionMap = map[string]string{}
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	exchange_key.ReplaceStoredForTest(t, exchange_key.Setting{})
	t.Setenv(exchange_key.EnvEnabled, "")
	t.Setenv(exchange_key.EnvSecret, "")
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.OptionMap = previousOptionMap
		common.SetMainDatabaseType(previousDatabaseType)
		common.RedisEnabled = previousRedis
	})
	return db
}

func putExchangeKeySetting(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/exchange-key", strings.NewReader(body))
	UpdateExchangeKeySetting(context)
	return response
}

func getExchangeKeySetting(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/exchange-key", nil)
	GetExchangeKeySetting(context)
	return response
}

func decodeExchangeKeyResponse(t *testing.T, response *httptest.ResponseRecorder) exchangeKeyAPIResponse {
	t.Helper()
	var payload exchangeKeyAPIResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	return payload
}

func storedExchangeKeyOptions(t *testing.T) map[string]string {
	t.Helper()
	stored, err := model.AllOptionsByPrefix(exchange_key.OptionKeyPrefix)
	require.NoError(t, err)
	return stored
}

func TestGetExchangeKeySettingReturnsDefaultsWithoutSecret(t *testing.T) {
	useExchangeKeyOptionDB(t)

	response := getExchangeKeySetting(t)

	assert.Equal(t, http.StatusOK, response.Code)
	payload := decodeExchangeKeyResponse(t, response)
	assert.True(t, payload.Success)
	assert.False(t, payload.Data.Enabled)
	assert.False(t, payload.Data.SecretConfigured)
	assert.NotContains(t, response.Body.String(), "test-secret")
	assert.NotContains(t, response.Body.String(), `"secret":`)
}

func TestUpdateExchangeKeySettingPersistsWithoutExposingSecret(t *testing.T) {
	useExchangeKeyOptionDB(t)

	response := putExchangeKeySetting(t, `{"enabled":true,"secret_key":"`+exchangeKeyTestSecret+`"}`)
	require.Equal(t, http.StatusOK, response.Code)

	response = getExchangeKeySetting(t)
	assert.Equal(t, http.StatusOK, response.Code)
	payload := decodeExchangeKeyResponse(t, response)
	assert.True(t, payload.Data.Enabled)
	assert.True(t, payload.Data.SecretConfigured)
	assert.NotContains(t, response.Body.String(), exchangeKeyTestSecret)
}

func TestUpdateExchangeKeySettingKeepsSecretWhenEmpty(t *testing.T) {
	useExchangeKeyOptionDB(t)
	require.Equal(t, http.StatusOK, putExchangeKeySetting(t, `{"enabled":true,"secret_key":"`+exchangeKeyTestSecret+`"}`).Code)

	response := putExchangeKeySetting(t, `{"secret_key":""}`)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, exchangeKeyTestSecret, storedExchangeKeyOptions(t)[exchange_key.OptionKeyPrefix+"secret"])
}

func TestUpdateExchangeKeySettingRejectsSecretManagedByEnvironment(t *testing.T) {
	useExchangeKeyOptionDB(t)
	t.Setenv(exchange_key.EnvSecret, exchangeKeyTestSecret)

	response := putExchangeKeySetting(t, `{"secret_key":"replacement-secret-16"}`)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, exchange_key.ErrSecretLockedByEnv.Error(), decodeExchangeKeyResponse(t, response).Message)
}

func TestGetOptionsExcludesExchangeKey(t *testing.T) {
	useExchangeKeyOptionDB(t)
	common.OptionMap = map[string]string{
		"SystemName":           "new-api",
		"exchange_key.enabled": "true",
		"exchange_key.secret":  exchangeKeyTestSecret,
	}
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)

	GetOptions(context)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "SystemName")
	assert.NotContains(t, response.Body.String(), exchange_key.OptionKeyPrefix)
}

func TestUpdateExchangeKeySettingRejectsGenericOptionEndpoint(t *testing.T) {
	useExchangeKeyOptionDB(t)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"exchange_key.enabled","value":"true"}`),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(
		t,
		"Exchange Key 配置请使用专用设置接口 /api/option/exchange-key",
		decodeExchangeKeyResponse(t, response).Message,
	)
	assert.Empty(t, storedExchangeKeyOptions(t))
}
