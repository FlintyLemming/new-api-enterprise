package exchange_key

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveEnvOverridesOption(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: false, Secret: "option-secret-16"})
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvSecret, testSecret)

	got := Effective()
	assert.True(t, got.Enabled)
	assert.Equal(t, testSecret, got.Secret)
	assert.True(t, got.EnabledFromEnv)
	assert.True(t, got.SecretFromEnv)
	assert.True(t, got.SecretConfigured)
	assert.True(t, IsEffective())
}

func TestEffectiveShortEnvSecretDisablesFeature(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: true, Secret: "option-secret-16"})
	t.Setenv(EnvSecret, "too-short")
	t.Setenv(EnvEnabled, "true")

	got := Effective()
	assert.True(t, got.Enabled)
	assert.Equal(t, "", got.Secret)
	assert.True(t, got.SecretFromEnv)
	assert.False(t, got.SecretConfigured)
	assert.False(t, IsEffective())
}

func TestBuildViewNeverIncludesSecret(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: true, Secret: testSecret})
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvSecret, "")

	view := BuildView()
	assert.True(t, view.Enabled)
	assert.True(t, view.SecretConfigured)
	assert.False(t, view.SecretFromEnv)
	assert.False(t, view.EnabledFromEnv)
}

func TestApplyUpdateEmptySecretKeepsStored(t *testing.T) {
	stored := Setting{Enabled: true, Secret: testSecret}
	enabled := true
	got, err := ApplyUpdate(stored, UpdateRequest{Enabled: &enabled, SecretKey: ""})
	require.NoError(t, err)
	assert.Equal(t, testSecret, got.Secret)
	assert.True(t, got.Enabled)
}

func TestApplyUpdateRejectsShortSecretAndEnvLocks(t *testing.T) {
	t.Setenv(EnvSecret, testSecret)
	t.Setenv(EnvEnabled, "true")
	enabled := false
	_, err := ApplyUpdate(Setting{Enabled: true, Secret: ""}, UpdateRequest{Enabled: &enabled})
	require.ErrorIs(t, err, ErrEnabledLockedByEnv)

	_, err = ApplyUpdate(Setting{Enabled: true, Secret: ""}, UpdateRequest{SecretKey: "another-secret-16"})
	require.ErrorIs(t, err, ErrSecretLockedByEnv)
}

func TestApplyUpdateRejectsSecretShorterThanMinLength(t *testing.T) {
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvSecret, "")
	_, err := ApplyUpdate(Setting{Enabled: false, Secret: ""}, UpdateRequest{SecretKey: "123456789012345"})
	require.ErrorIs(t, err, ErrSecretTooShort)
}

func TestEffectiveShortStoredSecretTreatedAsEmpty(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: true, Secret: "123456789012345"})
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvSecret, "")

	got := Effective()
	assert.Equal(t, "", got.Secret)
	assert.False(t, got.SecretConfigured)
	assert.False(t, IsEffective())
}

func TestApplyUpdateClearRequiresDisabled(t *testing.T) {
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvSecret, "")
	enabled := true
	_, err := ApplyUpdate(Setting{Enabled: true, Secret: testSecret}, UpdateRequest{
		Enabled: &enabled, SecretKeyClear: true,
	})
	require.ErrorIs(t, err, ErrSecretClearWhileEnabled)

	enabled = false
	got, err := ApplyUpdate(Setting{Enabled: true, Secret: testSecret}, UpdateRequest{
		Enabled: &enabled, SecretKeyClear: true,
	})
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	assert.Equal(t, "", got.Secret)
}
