package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/exchange_key"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const exchangeKeyAuthTestSecret = "test-secret-16ch"

type exchangeKeyAuthBody struct {
	ID                int    `json:"id"`
	TokenID           int    `json:"token_id"`
	TokenName         string `json:"token_name"`
	TokenKey          string `json:"token_key"`
	Exchange          bool   `json:"exchange"`
	Group             string `json:"group"`
	SpecificChannelID string `json:"specific_channel_id"`
}

func setupExchangeKeyAuthTest(t *testing.T) {
	t.Helper()
	t.Setenv(exchange_key.EnvEnabled, "")
	t.Setenv(exchange_key.EnvSecret, "")
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	model.DB = db
	model.LOG_DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, model.InitLogDB())
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousType)
		common.SetLogDatabaseType(previousLogType)
		common.RedisEnabled = previousRedis
	})
}

func enableExchangeKeyAuth(t *testing.T) {
	t.Helper()
	exchange_key.ReplaceStoredForTest(t, exchange_key.Setting{Enabled: true, Secret: exchangeKeyAuthTestSecret})
	t.Setenv(exchange_key.EnvEnabled, "true")
	t.Setenv(exchange_key.EnvSecret, exchangeKeyAuthTestSecret)
}

func exchangeKey(username string) string {
	mac := hmac.New(sha256.New, []byte(exchangeKeyAuthTestSecret))
	_, _ = mac.Write([]byte(username))
	return "sk-" + username + "-" + hex.EncodeToString(mac.Sum(nil))
}

func createExchangeKeyUser(t *testing.T, username, group string, status, role int) *model.User {
	t.Helper()
	user := &model.User{
		Username:    username,
		Password:    "password-placeholder",
		Role:        role,
		Status:      status,
		Group:       group,
		AuthVersion: 1,
		AffCode:     "ek-" + username,
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func createOrdinaryToken(t *testing.T, userID int, key string) *model.Token {
	t.Helper()
	token := &model.Token{
		UserId:         userID,
		Key:            key,
		Status:         common.TokenStatusEnabled,
		Name:           "ordinary",
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}
	require.NoError(t, model.DB.Create(token).Error)
	return token
}

func writeExchangeKeyAuthContext(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"id":                  c.GetInt("id"),
		"token_id":            c.GetInt("token_id"),
		"token_name":          c.GetString("token_name"),
		"token_key":           c.GetString("token_key"),
		"exchange":            common.GetContextKeyBool(c, constant.ContextKeyExchangeKey),
		"group":               common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
		"specific_channel_id": c.GetString("specific_channel_id"),
	})
}

func serveExchangeKeyAuth(t *testing.T, middleware gin.HandlerFunc, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/chat/completions", middleware, writeExchangeKeyAuthContext)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", authorization)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeExchangeKeyAuthBody(t *testing.T, response *httptest.ResponseRecorder) exchangeKeyAuthBody {
	t.Helper()
	var body exchangeKeyAuthBody
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	return body
}

func TestExchangeKeyAuthValidSetsVirtualContext(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)
	user := createExchangeKeyUser(t, "alice", "vip", common.UserStatusEnabled, common.RoleCommonUser)
	key := exchangeKey("alice")

	response := serveExchangeKeyAuth(t, TokenAuth(), "Bearer "+key)

	require.Equal(t, http.StatusOK, response.Code)
	body := decodeExchangeKeyAuthBody(t, response)
	assert.Equal(t, user.Id, body.ID)
	assert.Equal(t, 0, body.TokenID)
	assert.Equal(t, exchange_key.TokenName, body.TokenName)
	assert.Equal(t, "", body.TokenKey)
	assert.True(t, body.Exchange)
	assert.Equal(t, "vip", body.Group)
	assert.NotContains(t, response.Body.String(), strings.TrimPrefix(key, "sk-alice-"))
}

func TestExchangeKeyAuthDisabledReturns401(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	exchange_key.ReplaceStoredForTest(t, exchange_key.Setting{Enabled: false, Secret: exchangeKeyAuthTestSecret})
	createExchangeKeyUser(t, "alice", "default", common.UserStatusEnabled, common.RoleCommonUser)

	response := serveExchangeKeyAuth(t, TokenAuth(), "Bearer "+exchangeKey("alice"))

	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestExchangeKeyAuthWrongMACReturns401(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)
	createExchangeKeyUser(t, "alice", "default", common.UserStatusEnabled, common.RoleCommonUser)

	response := serveExchangeKeyAuth(t, TokenAuth(), "Bearer sk-alice-"+strings.Repeat("0", 64))

	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestExchangeKeyAuthUnknownUserMatchesWrongMAC(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)

	wrongMAC := serveExchangeKeyAuth(t, TokenAuth(), "Bearer sk-alice-"+strings.Repeat("0", 64))
	unknown := serveExchangeKeyAuth(t, TokenAuth(), "Bearer "+exchangeKey("nobody"))

	require.Equal(t, http.StatusUnauthorized, wrongMAC.Code)
	require.Equal(t, http.StatusUnauthorized, unknown.Code)
	assert.Equal(t, wrongMAC.Body.String(), unknown.Body.String())
}

func TestExchangeKeyAuthBannedUserReturns403(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)
	createExchangeKeyUser(t, "alice", "default", common.UserStatusDisabled, common.RoleCommonUser)

	response := serveExchangeKeyAuth(t, TokenAuth(), "Bearer "+exchangeKey("alice"))

	require.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), i18n.MsgAuthUserBanned)
}

func TestExchangeKeyAuthOrdinary48CharToken(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)
	user := createExchangeKeyUser(t, "alice", "default", common.UserStatusEnabled, common.RoleCommonUser)
	key := strings.Repeat("a", 48)
	token := createOrdinaryToken(t, user.Id, key)

	response := serveExchangeKeyAuth(t, TokenAuth(), "Bearer sk-"+key)

	require.Equal(t, http.StatusOK, response.Code)
	body := decodeExchangeKeyAuthBody(t, response)
	assert.Equal(t, user.Id, body.ID)
	assert.Equal(t, token.Id, body.TokenID)
	assert.False(t, body.Exchange)
}

func TestExchangeKeyAuthAdminChannelSuffix(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)
	user := createExchangeKeyUser(t, "admin", "default", common.UserStatusEnabled, common.RoleAdminUser)
	key := strings.Repeat("a", 48)
	createOrdinaryToken(t, user.Id, key)

	response := serveExchangeKeyAuth(t, TokenAuth(), "Bearer sk-"+key+"-42")

	require.Equal(t, http.StatusOK, response.Code)
	body := decodeExchangeKeyAuthBody(t, response)
	assert.Equal(t, "42", body.SpecificChannelID)
	assert.False(t, body.Exchange)
}

func TestExchangeKeyAuthReadOnlyValid(t *testing.T) {
	setupExchangeKeyAuthTest(t)
	enableExchangeKeyAuth(t)
	createExchangeKeyUser(t, "alice", "default", common.UserStatusEnabled, common.RoleCommonUser)
	key := exchangeKey("alice")

	response := serveExchangeKeyAuth(t, TokenAuthReadOnly(), "Bearer "+key)

	require.Equal(t, http.StatusOK, response.Code)
	body := decodeExchangeKeyAuthBody(t, response)
	assert.Equal(t, "", body.TokenKey)
	assert.NotContains(t, response.Body.String(), strings.TrimPrefix(key, "sk-alice-"))
}
