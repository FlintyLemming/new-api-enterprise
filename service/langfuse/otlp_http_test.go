package langfuse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// probedRequest records what the collector actually received. The Basic
// credential is reduced to a boolean on purpose: a failing assertion must never
// print the header value.
type probedRequest struct {
	method          string
	path            string
	contentEncoding string
	contentType     string
	basicAuth       bool
	overTLS         bool
}

type otlpProbe struct {
	mu       sync.Mutex
	requests []probedRequest
}

func (p *otlpProbe) handler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests = append(p.requests, probedRequest{
		method:          r.Method,
		path:            r.URL.Path,
		contentEncoding: r.Header.Get("Content-Encoding"),
		contentType:     r.Header.Get("Content-Type"),
		basicAuth:       strings.HasPrefix(r.Header.Get("Authorization"), "Basic "),
		overTLS:         r.TLS != nil,
	})
	p.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (p *otlpProbe) snapshot() []probedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]probedRequest{}, p.requests...)
}

// exportProbeSpan sends exactly one span through the real OTLP exporter. The
// syncer makes the export happen inside span.End, so the collector state is
// settled when this returns.
func exportProbeSpan(t *testing.T, options []otlptracehttp.Option) {
	t.Helper()
	exporter, err := otlptracehttp.New(t.Context(), options...)
	require.NoError(t, err)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = provider.Shutdown(shutdownCtx)
	})

	_, span := provider.Tracer("langfuse-test").Start(t.Context(), "probe")
	span.End()
	require.NoError(t, provider.ForceFlush(t.Context()))
}

func TestOtlpExportOverHTTPHitsTracesPathWithGzipAndBasicAuth(t *testing.T) {
	probe := &otlpProbe{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/public/otel/v1/traces", probe.handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	options, err := buildExporterOptions(snapshotForHost(t, server.URL))
	require.NoError(t, err)
	exportProbeSpan(t, options)

	requests := probe.snapshot()
	require.Len(t, requests, 1)
	assert.Equal(t, http.MethodPost, requests[0].method)
	assert.Equal(t, "/api/public/otel/v1/traces", requests[0].path)
	assert.Equal(t, "gzip", requests[0].contentEncoding)
	assert.Equal(t, "application/x-protobuf", requests[0].contentType)
	assert.True(t, requests[0].basicAuth, "authorization header must use the Basic scheme")
	assert.False(t, requests[0].overTLS, "an http host must not be upgraded to TLS")
}

func TestOtlpExportOverHTTPSPerformsTLS(t *testing.T) {
	probe := &otlpProbe{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/public/otel/v1/traces", probe.handler)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	options, err := buildExporterOptions(snapshotForHost(t, server.URL))
	require.NoError(t, err)
	// The exporter's own client cannot validate the ad hoc test certificate, so
	// the trust store is swapped while everything derived from the snapshot
	// (scheme, path, headers, compression) stays in place.
	trusting := server.Client()
	trusting.Timeout = 10 * time.Second
	options = append(options, otlptracehttp.WithHTTPClient(trusting))
	exportProbeSpan(t, options)

	requests := probe.snapshot()
	require.Len(t, requests, 1)
	assert.True(t, requests[0].overTLS, "an https host must perform TLS")
	assert.Equal(t, "/api/public/otel/v1/traces", requests[0].path)
}

func TestOtlpExportKeepsConfiguredBasePath(t *testing.T) {
	probe := &otlpProbe{}
	mux := http.NewServeMux()
	mux.HandleFunc("/langfuse/api/public/otel/v1/traces", probe.handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	options, err := buildExporterOptions(snapshotForHost(t, server.URL+"/langfuse/"))
	require.NoError(t, err)
	exportProbeSpan(t, options)

	requests := probe.snapshot()
	require.Len(t, requests, 1)
	assert.Equal(t, "/langfuse/api/public/otel/v1/traces", requests[0].path)
}

func TestOtlpExportIgnoresOtlpEndpointEnvironment(t *testing.T) {
	probe := &otlpProbe{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/public/otel/v1/traces", probe.handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://evil.example")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://evil.example/api/public/otel/v1/traces")

	options, err := buildExporterOptions(snapshotForHost(t, server.URL))
	require.NoError(t, err)
	exportProbeSpan(t, options)

	requests := probe.snapshot()
	require.Len(t, requests, 1, "the administrator configured host must win over the OTLP environment")
	assert.Equal(t, "/api/public/otel/v1/traces", requests[0].path)
}
