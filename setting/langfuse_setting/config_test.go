package langfuse_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingFromOptionMapEmptyReturnsDefaults(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{})

	assert.False(t, got.Enabled)
	assert.Equal(t, "", got.Host)
	assert.Equal(t, "", got.PublicKey)
	assert.Equal(t, "", got.SecretKey)
	assert.Equal(t, "default", got.Environment)
	assert.Equal(t, 0.1, got.SampleRate)
	assert.False(t, got.SendContent)
	assert.Equal(t, 65536, got.MaxContentBytes)
	assert.Equal(t, 524288, got.MaxResponseBytes)
	assert.Equal(t, 536870912, got.MaxInFlightCaptureBytes)
	assert.Equal(t, 65536, got.MaxSessionBodyBytes)
	assert.Empty(t, got.SessionHeaderNames)
	assert.Empty(t, got.SessionBodyPaths)
	assert.Equal(t, 64, got.QueueSize)
	assert.Equal(t, 16, got.BatchSize)
	assert.Equal(t, 5, got.FlushIntervalSeconds)
}

func TestSettingFromOptionMapOverlaysPrefixedKeys(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{
		"langfuse_setting.enabled":              "true",
		"langfuse_setting.host":                 "http://langfuse:3000",
		"langfuse_setting.public_key":           "pk-lf-1",
		"langfuse_setting.secret_key":           "sk-lf-1",
		"langfuse_setting.sample_rate":          "0.25",
		"langfuse_setting.send_content":         "true",
		"langfuse_setting.max_content_bytes":    "8192",
		"langfuse_setting.session_header_names": `["X-Conversation-Id"]`,
		"langfuse_setting.session_body_paths":   `["metadata.session_id"]`,
		"langfuse_setting.queue_size":           "128",
		// 非 langfuse 前缀的 key 必须被忽略。
		"other_setting.queue_size": "999",
	})

	assert.True(t, got.Enabled)
	assert.Equal(t, "http://langfuse:3000", got.Host)
	assert.Equal(t, "pk-lf-1", got.PublicKey)
	assert.Equal(t, "sk-lf-1", got.SecretKey)
	assert.Equal(t, 0.25, got.SampleRate)
	assert.True(t, got.SendContent)
	assert.Equal(t, 8192, got.MaxContentBytes)
	assert.Equal(t, []string{"X-Conversation-Id"}, got.SessionHeaderNames)
	assert.Equal(t, []string{"metadata.session_id"}, got.SessionBodyPaths)
	assert.Equal(t, 128, got.QueueSize)
	// 未出现的 key 保持默认值。
	assert.Equal(t, "default", got.Environment)
	assert.Equal(t, 524288, got.MaxResponseBytes)
	assert.Equal(t, 16, got.BatchSize)
}

func TestSettingFromOptionMapKeepsDefaultOnUnparsableValue(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{
		"langfuse_setting.sample_rate":          "not-a-float",
		"langfuse_setting.queue_size":           "not-an-int",
		"langfuse_setting.enabled":              "not-a-bool",
		"langfuse_setting.session_header_names": "not-json",
		"langfuse_setting.unknown_field":        "ignored",
	})

	assert.Equal(t, 0.1, got.SampleRate)
	assert.Equal(t, 64, got.QueueSize)
	assert.False(t, got.Enabled)
	assert.Empty(t, got.SessionHeaderNames)
}

func TestSettingFromOptionMapDoesNotMutateDefaults(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{
		"langfuse_setting.queue_size":           "256",
		"langfuse_setting.session_header_names": `["X-A"]`,
	})
	require.Equal(t, 256, got.QueueSize)

	assert.Equal(t, 64, DefaultLangfuseSetting.QueueSize)
	assert.Empty(t, DefaultLangfuseSetting.SessionHeaderNames)
}
