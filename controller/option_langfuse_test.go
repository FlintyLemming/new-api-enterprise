package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/langfuse"
	"github.com/QuantumNous/new-api/service/langfuseconfig"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// langfuseEnableBody is a complete, valid enable payload: the UI always submits
// the whole group, including the presence-aware sample_rate/send_content.
const langfuseEnableBody = `{
	"enabled": true,
	"host": "http://langfuse:3000",
	"public_key": "pk-lf-1",
	"secret_key": "sk-lf-1",
	"environment": "default",
	"sample_rate": 0.2,
	"send_content": true,
	"max_content_bytes": 65536,
	"max_response_bytes": 524288,
	"max_in_flight_capture_bytes": 536870912,
	"max_session_body_bytes": 65536,
	"session_header_names": ["X-Conversation-Id"],
	"session_body_paths": ["metadata.session_id"],
	"queue_size": 64,
	"batch_size": 16,
	"flush_interval_seconds": 5
}`

type langfuseAPIResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func useLangfuseOptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousOptionMap := common.OptionMap
	previousDatabaseType := common.MainDatabaseType()
	previousRedis := common.RedisEnabled
	previousBinding := langfuse.LoadBinding()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.User{}))

	model.DB = db
	model.LOG_DB = db
	common.OptionMap = map[string]string{}
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.OptionMap = previousOptionMap
		common.SetMainDatabaseType(previousDatabaseType)
		common.RedisEnabled = previousRedis
		langfuse.PublishBinding(*previousBinding)
	})
	return db
}

func putLangfuseSetting(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/langfuse", strings.NewReader(body))
	UpdateLangfuseSetting(context)
	return response
}

func getLangfuseSetting(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/langfuse", nil)
	GetLangfuseSetting(context)
	return response
}

func decodeLangfuseResponse(t *testing.T, response *httptest.ResponseRecorder) langfuseAPIResponse {
	t.Helper()
	var payload langfuseAPIResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	return payload
}

func storedLangfuseOptions(t *testing.T) map[string]string {
	t.Helper()
	stored, err := model.AllOptionsByPrefix("langfuse_setting.")
	require.NoError(t, err)
	return stored
}

func TestUpdateOptionRejectsLangfuseKeys(t *testing.T) {
	useLangfuseOptionDB(t)

	for _, key := range []string{"langfuse_setting.host", "langfuse_setting.secret_key", "langfuse_setting.enabled"} {
		t.Run(key, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(
				http.MethodPut,
				"/api/option/",
				strings.NewReader(`{"key":"`+key+`","value":"whatever"}`),
			)

			UpdateOption(context)

			assert.Equal(t, http.StatusBadRequest, response.Code)
			payload := decodeLangfuseResponse(t, response)
			assert.False(t, payload.Success)
			assert.Contains(t, payload.Message, "专用设置接口")
			assert.Empty(t, storedLangfuseOptions(t))
		})
	}
}

func TestGetOptionsExcludesLangfuseKeys(t *testing.T) {
	useLangfuseOptionDB(t)
	common.OptionMap = map[string]string{
		"SystemName":                  "new-api",
		"langfuse_setting.host":       "http://langfuse:3000",
		"langfuse_setting.public_key": "pk-lf-1",
		"langfuse_setting.enabled":    "true",
	}

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)

	GetOptions(context)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "SystemName")
	assert.NotContains(t, response.Body.String(), "langfuse_setting.")
}

func TestGetLangfuseSettingReturnsDefaultsWithoutSecret(t *testing.T) {
	useLangfuseOptionDB(t)

	response := getLangfuseSetting(t)

	assert.Equal(t, http.StatusOK, response.Code)
	payload := decodeLangfuseResponse(t, response)
	require.True(t, payload.Success)

	var view langfuseconfig.SettingView
	require.NoError(t, common.Unmarshal(payload.Data, &view))
	assert.False(t, view.Enabled)
	assert.Equal(t, "", view.Host)
	assert.Equal(t, "", view.PublicKey)
	assert.False(t, view.SecretKeyConfigured)
	assert.Equal(t, "default", view.Environment)
	assert.Equal(t, 0.1, view.SampleRate)
	assert.False(t, view.SendContent)
	assert.Equal(t, 65536, view.MaxContentBytes)
	assert.Equal(t, 524288, view.MaxResponseBytes)
	assert.Equal(t, 536870912, view.MaxInFlightCaptureBytes)
	assert.Equal(t, 65536, view.MaxSessionBodyBytes)
	assert.Equal(t, []string{}, view.SessionHeaderNames)
	assert.Equal(t, []string{}, view.SessionBodyPaths)
	assert.Equal(t, 64, view.QueueSize)
	assert.Equal(t, 16, view.BatchSize)
	assert.Equal(t, 5, view.FlushIntervalSeconds)
	assert.NotContains(t, response.Body.String(), "secret_key\"")
}

func TestGetLangfuseSettingReportsSecretPresenceWithoutValue(t *testing.T) {
	useLangfuseOptionDB(t)
	require.Equal(t, http.StatusOK, putLangfuseSetting(t, langfuseEnableBody).Code)

	response := getLangfuseSetting(t)

	assert.Equal(t, http.StatusOK, response.Code)
	var view langfuseconfig.SettingView
	require.NoError(t, common.Unmarshal(decodeLangfuseResponse(t, response).Data, &view))
	assert.True(t, view.SecretKeyConfigured)
	assert.Equal(t, "pk-lf-1", view.PublicKey)
	assert.NotContains(t, response.Body.String(), "sk-lf-1")
}

func TestUpdateLangfuseSettingPersistsWholeGroupAndPublishesBinding(t *testing.T) {
	useLangfuseOptionDB(t)

	response := putLangfuseSetting(t, langfuseEnableBody)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, decodeLangfuseResponse(t, response).Success)
	assert.NotContains(t, response.Body.String(), "sk-lf-1")

	stored := storedLangfuseOptions(t)
	assert.Len(t, stored, 16)
	assert.Equal(t, "true", stored["langfuse_setting.enabled"])
	assert.Equal(t, "http://langfuse:3000", stored["langfuse_setting.host"])
	assert.Equal(t, "sk-lf-1", stored["langfuse_setting.secret_key"])
	assert.Equal(t, "0.2", stored["langfuse_setting.sample_rate"])
	assert.Equal(t, `["X-Conversation-Id"]`, stored["langfuse_setting.session_header_names"])
	assert.Equal(t, `["metadata.session_id"]`, stored["langfuse_setting.session_body_paths"])

	snapshot := langfuse.LoadBinding().Snapshot
	assert.True(t, snapshot.Enabled)
	assert.Equal(t, "http://langfuse:3000", snapshot.Host)
	assert.Equal(t, "http://langfuse:3000/api/public/otel/v1/traces", snapshot.TracesURL)
}

func TestUpdateLangfuseSettingRequiresEnableConfirmation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing sample rate", strings.Replace(langfuseEnableBody, `"sample_rate": 0.2,`, "", 1)},
		{"missing send content", strings.Replace(langfuseEnableBody, `"send_content": true,`, "", 1)},
		{"missing both", strings.NewReplacer(`"sample_rate": 0.2,`, "", `"send_content": true,`, "").Replace(langfuseEnableBody)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useLangfuseOptionDB(t)
			versionBefore := langfuse.CurrentVersion()

			response := putLangfuseSetting(t, tc.body)

			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Contains(t, decodeLangfuseResponse(t, response).Message, "sample_rate")
			assert.Empty(t, storedLangfuseOptions(t))
			assert.Equal(t, versionBefore, langfuse.CurrentVersion())
			assert.False(t, langfuse.LoadBinding().Snapshot.Enabled)
		})
	}
}

func TestUpdateLangfuseSettingAllowsPlainUpdateWhileEnabled(t *testing.T) {
	useLangfuseOptionDB(t)
	require.Equal(t, http.StatusOK, putLangfuseSetting(t, langfuseEnableBody).Code)

	// Already enabled: the confirmation was given when it was switched on.
	body := strings.NewReplacer(
		`"sample_rate": 0.2,`, "",
		`"send_content": true,`, "",
		`"host": "http://langfuse:3000"`, `"host": "https://langfuse.example/base"`,
	).Replace(langfuseEnableBody)

	response := putLangfuseSetting(t, body)

	assert.Equal(t, http.StatusOK, response.Code)
	stored := storedLangfuseOptions(t)
	assert.Equal(t, "https://langfuse.example/base", stored["langfuse_setting.host"])
	// The persisted values survive an update that omits them.
	assert.Equal(t, "0.2", stored["langfuse_setting.sample_rate"])
	assert.Equal(t, "true", stored["langfuse_setting.send_content"])
	assert.Equal(t,
		"https://langfuse.example/base/api/public/otel/v1/traces",
		langfuse.LoadBinding().Snapshot.TracesURL)
}

func TestUpdateLangfuseSettingKeepsSecretWhenOmitted(t *testing.T) {
	useLangfuseOptionDB(t)
	require.Equal(t, http.StatusOK, putLangfuseSetting(t, langfuseEnableBody).Code)

	response := putLangfuseSetting(t, strings.Replace(langfuseEnableBody, `"secret_key": "sk-lf-1",`, `"secret_key": "",`, 1))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "sk-lf-1", storedLangfuseOptions(t)["langfuse_setting.secret_key"])
}

func TestUpdateLangfuseSettingClearsSecretOnlyWhileDisabled(t *testing.T) {
	useLangfuseOptionDB(t)
	require.Equal(t, http.StatusOK, putLangfuseSetting(t, langfuseEnableBody).Code)

	stillEnabled := strings.Replace(langfuseEnableBody, `"secret_key": "sk-lf-1",`, `"secret_key": "", "secret_key_clear": true,`, 1)
	response := putLangfuseSetting(t, stillEnabled)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "sk-lf-1", storedLangfuseOptions(t)["langfuse_setting.secret_key"])
	assert.True(t, langfuse.LoadBinding().Snapshot.Enabled)

	disabled := strings.NewReplacer(
		`"enabled": true,`, `"enabled": false,`,
		`"secret_key": "sk-lf-1",`, `"secret_key": "", "secret_key_clear": true,`,
	).Replace(langfuseEnableBody)
	response = putLangfuseSetting(t, disabled)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "", storedLangfuseOptions(t)["langfuse_setting.secret_key"])
	assert.False(t, langfuse.LoadBinding().Snapshot.Enabled)
}

func TestUpdateLangfuseSettingRejectsInvalidConfigurationWithoutSideEffects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"invalid host", strings.Replace(langfuseEnableBody, `"host": "http://langfuse:3000"`, `"host": "langfuse:3000"`, 1)},
		{"host with full traces path", strings.Replace(langfuseEnableBody, `"host": "http://langfuse:3000"`, `"host": "http://langfuse:3000/api/public/otel/v1/traces"`, 1)},
		{"queue below range", strings.Replace(langfuseEnableBody, `"queue_size": 64`, `"queue_size": 15`, 1)},
		{"credential session header", strings.Replace(langfuseEnableBody, `["X-Conversation-Id"]`, `["Authorization"]`, 1)},
		{"enabled without public key", strings.Replace(langfuseEnableBody, `"public_key": "pk-lf-1"`, `"public_key": ""`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useLangfuseOptionDB(t)
			versionBefore := langfuse.CurrentVersion()

			response := putLangfuseSetting(t, tc.body)

			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.False(t, decodeLangfuseResponse(t, response).Success)
			assert.Empty(t, storedLangfuseOptions(t))
			assert.Equal(t, versionBefore, langfuse.CurrentVersion())
		})
	}
}
