package langfuse

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initialBinding is captured during package variable initialization, so the
// startup contract can be asserted no matter which test publishes first.
var initialBinding = LoadBinding()

func enabledSetting() langfuse_setting.LangfuseSetting {
	s := langfuse_setting.DefaultLangfuseSetting
	s.Enabled = true
	s.Host = "https://x.example/langfuse/"
	s.PublicKey = "pk-lf-1"
	s.SecretKey = "sk-lf-1"
	return s
}

// keepBinding restores whatever binding was active before the test, so package
// level state does not leak between tests.
func keepBinding(t *testing.T) {
	t.Helper()
	previous := LoadBinding()
	t.Cleanup(func() { PublishBinding(*previous) })
}

func TestInitialBindingIsDisabledWithoutRuntime(t *testing.T) {
	require.NotNil(t, initialBinding)
	assert.False(t, initialBinding.Snapshot.Enabled)
	assert.Nil(t, initialBinding.Runtime)
	assert.Equal(t, uint64(0), initialBinding.Snapshot.Version)
	assert.Equal(t, "", initialBinding.Snapshot.TracesURL)
}

func TestPublishSnapshotPublishesValidatedSnapshotWithMonotonicVersion(t *testing.T) {
	keepBinding(t)
	before := CurrentVersion()

	require.NoError(t, PublishSnapshot(enabledSetting()))

	binding := LoadBinding()
	require.NotNil(t, binding)
	assert.True(t, binding.Snapshot.Enabled)
	assert.Equal(t, "https://x.example/langfuse/api/public/otel/v1/traces", binding.Snapshot.TracesURL)
	assert.Equal(t, before+1, binding.Snapshot.Version)
	assert.Equal(t, binding.Snapshot.Version, CurrentVersion())

	require.NoError(t, PublishSnapshot(enabledSetting()))
	assert.Equal(t, before+2, CurrentVersion())
}

func TestPublishSnapshotRejectsInvalidSettingAndKeepsBinding(t *testing.T) {
	keepBinding(t)
	require.NoError(t, PublishSnapshot(enabledSetting()))
	published := LoadBinding()

	invalid := enabledSetting()
	invalid.SecretKey = ""
	assert.Error(t, PublishSnapshot(invalid))

	assert.Same(t, published, LoadBinding())
	// A rejected candidate must not consume a version either.
	assert.Equal(t, published.Snapshot.Version, CurrentVersion())
}

func TestBindingLoadAndPublishAreConcurrencySafe(t *testing.T) {
	keepBinding(t)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s := enabledSetting()
			s.SampleRate = float64(i%11) / 10
			assert.NoError(t, PublishSnapshot(s))
		}(i)
		go func() {
			defer wg.Done()
			binding := LoadBinding()
			require.NotNil(t, binding)
			// Read through the binding to catch torn snapshot/runtime pairs.
			_ = binding.Snapshot.TracesURL
			_ = binding.Snapshot.SessionHeaderNames
			_ = binding.Runtime
		}()
	}
	wg.Wait()

	final := LoadBinding()
	assert.True(t, final.Snapshot.Enabled)
	assert.Equal(t, final.Snapshot.Version, CurrentVersion())
}
