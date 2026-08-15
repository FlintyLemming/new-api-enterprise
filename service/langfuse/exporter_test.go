package langfuse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

	options, err := buildExporterOptions(disabled, newForbiddenTransport())
	assert.Error(t, err, "an exporter without a target URL would fall back to the OTLP environment")
	assert.Nil(t, options)
}

func TestExporterHTTPClientUsesDedicatedTransportAndRequestTimeout(t *testing.T) {
	transport := newForbiddenTransport()
	client := newExporterHTTPClient(transport)

	// The 10s single-request timeout is the value the 15s shutdown budget is
	// sized against (design §10.2/§11).
	assert.Equal(t, 10*time.Second, client.Timeout)
	assert.Same(t, transport, client.Transport)
	assert.NotSame(t, http.DefaultTransport, transport.base,
		"Langfuse must not share the process default transport")
}

// recordedSpans produces real ReadOnlySpans so the exporter decorator can be
// driven directly, without a processor deciding when an export happens.
func recordedSpans(t *testing.T, count int) []sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	for i := 0; i < count; i++ {
		_, span := provider.Tracer("langfuse-test").Start(t.Context(), fmt.Sprintf("span-%d", i))
		span.End()
	}
	ended := recorder.Ended()
	require.Len(t, ended, count)
	return ended
}

// captureWarnings redirects the rate limited Langfuse warnings for the duration
// of a test.
func captureWarnings(t *testing.T) *[]string {
	t.Helper()
	previous := emitWarning
	captured := &[]string{}
	emitWarning = func(message string) { *captured = append(*captured, message) }
	t.Cleanup(func() { emitWarning = previous })
	return captured
}

func newTestExporter(t *testing.T, snapshot Snapshot) *countingExporter {
	t.Helper()
	transport := newForbiddenTransport()
	options, err := buildExporterOptions(snapshot, transport)
	require.NoError(t, err)
	inner, err := otlptracehttp.New(t.Context(), options...)
	require.NoError(t, err)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = inner.Shutdown(shutdownCtx)
	})
	return newCountingExporter(inner, transport, snapshot.Version)
}

func TestForbiddenIngestionFailsBatchWithoutRetryOrUpstreamBody(t *testing.T) {
	const suspendedBody = "sensitive-upstream-body-that-must-not-leak"
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(suspendedBody))
	}))
	t.Cleanup(server.Close)

	exporter := newTestExporter(t, snapshotForHost(t, server.URL))
	err := exporter.ExportSpans(t.Context(), recordedSpans(t, 3))

	require.Error(t, err)
	assert.EqualValues(t, 1, requests.Load(), "an ingestion suspended 403 must not be retried")
	assert.EqualValues(t, 3, exporter.received.Load())
	assert.EqualValues(t, 3, exporter.failed.Load())
	assert.EqualValues(t, 0, exporter.exported.Load())
	assert.NotContains(t, err.Error(), suspendedBody, "the upstream response body must never reach the error")
	assert.Contains(t, err.Error(), "ingestion_forbidden")
	assert.Contains(t, err.Error(), "3 span")
}

func TestThrottledIngestionIsRetriedAndThenExported(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	// In production this runs on the BatchSpanProcessor worker, so the backoff
	// never blocks a relay request.
	exporter := newTestExporter(t, snapshotForHost(t, server.URL))
	require.NoError(t, exporter.ExportSpans(t.Context(), recordedSpans(t, 2)))

	assert.GreaterOrEqual(t, requests.Load(), int64(2), "a 429 must be retried")
	assert.EqualValues(t, 2, exporter.received.Load())
	assert.EqualValues(t, 2, exporter.exported.Load())
	assert.EqualValues(t, 0, exporter.failed.Load())
}

type stubSpanExporter struct{ err error }

func (s *stubSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return s.err }

func (s *stubSpanExporter) Shutdown(context.Context) error { return nil }

func TestCountingExporterCountsSpansPerOutcome(t *testing.T) {
	stub := &stubSpanExporter{}
	exporter := newCountingExporter(stub, newForbiddenTransport(), 4)

	require.NoError(t, exporter.ExportSpans(t.Context(), recordedSpans(t, 2)))
	assert.EqualValues(t, 2, exporter.received.Load())
	assert.EqualValues(t, 2, exporter.exported.Load())
	assert.EqualValues(t, 0, exporter.failed.Load())

	stub.err = errors.New("upstream said: leaked body")
	err := exporter.ExportSpans(t.Context(), recordedSpans(t, 3))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "leaked body")
	assert.EqualValues(t, 5, exporter.received.Load(), "received counts every span handed to the exporter")
	assert.EqualValues(t, 2, exporter.exported.Load())
	assert.EqualValues(t, 3, exporter.failed.Load())
}

func TestErrorHandlerRateLimitsLangfuseErrorsAndForwardsTheRest(t *testing.T) {
	warnings := captureWarnings(t)
	var forwarded []error
	handler := newLangfuseErrorHandler(otel.ErrorHandlerFunc(func(err error) { forwarded = append(forwarded, err) }))

	exportErr := langfuseExportError{key: "ingestion_forbidden:9", summary: "langfuse export failed"}
	handler.Handle(exportErr)
	handler.Handle(exportErr)
	assert.Len(t, *warnings, 1, "the same failure must warn at most once per window")
	assert.Empty(t, forwarded)

	handler.mu.Lock()
	handler.lastWarned[exportErr.key] = time.Now().Add(-alertInterval - time.Second)
	handler.mu.Unlock()
	handler.Handle(exportErr)
	assert.Len(t, *warnings, 2, "a new window must warn again")

	unrelated := errors.New("unrelated otel failure")
	handler.Handle(unrelated)
	require.Len(t, forwarded, 1, "non Langfuse errors must keep reaching the previous handler")
	assert.Equal(t, unrelated, forwarded[0])
	assert.Len(t, *warnings, 2)
}

func TestForbiddenTransportReplacesSuspendedResponseBody(t *testing.T) {
	const suspendedBody = "sensitive-upstream-body-that-must-not-leak"
	var status atomic.Int64
	status.Store(http.StatusForbidden)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(suspendedBody))
	}))
	t.Cleanup(server.Close)

	transport := newForbiddenTransport()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, forbiddenBodyStandIn, string(body), "the exporter must never read the upstream body")
	assert.EqualValues(t, len(forbiddenBodyStandIn), response.ContentLength)
	assert.Equal(t, strconv.Itoa(len(forbiddenBodyStandIn)), response.Header.Get("Content-Length"))
	assert.True(t, transport.forbidden.Load())

	// A lifted usage limit must make the runtime report normally again.
	status.Store(http.StatusOK)
	request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	recovered, err := transport.RoundTrip(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Body.Close() })
	assert.False(t, transport.forbidden.Load())
}
