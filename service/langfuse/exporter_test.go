package langfuse

import (
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotForHost builds a validated snapshot pointing at host, which is the
// only input the exporter options are allowed to read.
func snapshotForHost(t *testing.T, host string) Snapshot {
	t.Helper()
	setting := enabledSetting()
	setting.Host = host
	snapshot, err := langfuse_setting.BuildSnapshot(setting, 7)
	require.NoError(t, err)
	return snapshot
}

func TestExporterBasicAuthHeaderEncodesKeyPair(t *testing.T) {
	assert.Equal(t, "Basic cGs6c2s=", basicAuthHeader("pk", "sk"))
}

func TestExporterOptionsRejectSnapshotWithoutTracesURL(t *testing.T) {
	disabled, err := langfuse_setting.BuildSnapshot(langfuse_setting.DefaultLangfuseSetting, 0)
	require.NoError(t, err)
	require.Empty(t, disabled.TracesURL)

	options, err := buildExporterOptions(disabled)
	assert.Error(t, err, "an exporter without a target URL would fall back to the OTLP environment")
	assert.Nil(t, options)
}

func TestExporterHTTPClientUsesDedicatedTransportAndRequestTimeout(t *testing.T) {
	client := newExporterHTTPClient()

	// The 10s single-request timeout is the value the 15s shutdown budget is
	// sized against (design §10.2/§11).
	assert.Equal(t, 10*time.Second, client.Timeout)
	require.NotNil(t, client.Transport)
	assert.NotSame(t, http.DefaultTransport, client.Transport,
		"Langfuse must not share the process default transport")
}
