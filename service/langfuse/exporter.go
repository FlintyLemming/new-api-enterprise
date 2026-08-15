package langfuse

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Export transport constants from design §11. The single request timeout is the
// value the 15s process shutdown budget is sized against, so the two must move
// together if either changes.
const (
	exporterRequestTimeout = 10 * time.Second

	retryInitialInterval = 1 * time.Second
	retryMaxInterval     = 5 * time.Second
	retryMaxElapsedTime  = 30 * time.Second

	// alertInterval is the rate limit every Langfuse export warning shares: at
	// most one line per key per window, so a suspended or unreachable ingestion
	// endpoint cannot flood the process log.
	alertInterval = 30 * time.Second

	// forbiddenBodyStandIn replaces the upstream body of an ingestion suspended
	// response. The official exporter embeds whatever it reads into its error
	// message, so nothing from Langfuse may survive past the transport.
	forbiddenBodyStandIn = "ingestion forbidden"
)

// warningSink receives every Langfuse diagnostic. Retirement drains report from
// background goroutines, so the indirection is atomic rather than a plain
// variable a test could swap underneath a running drain.
var warningSink atomic.Pointer[func(string)]

func init() {
	sink := func(message string) { common.SysError(message) }
	warningSink.Store(&sink)
}

func emitWarning(message string) { (*warningSink.Load())(message) }

// basicAuthHeader builds the Langfuse ingestion credential. The result is a
// secret: it may only be handed to the exporter options and must never reach a
// log line, an error message or a test failure output.
func basicAuthHeader(publicKey, secretKey string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(publicKey+":"+secretKey))
}

// forbiddenTransport classifies the ingestion suspended response Langfuse
// returns when an account exceeds its usage threshold. The official exporter
// already treats 403 as non-retryable; this transport exists so the upstream
// response body is closed and replaced before the exporter can read it into an
// error message (design §11).
type forbiddenTransport struct {
	base http.RoundTripper
	// forbidden is the persistent ingestion_forbidden state the counting
	// exporter reads when it classifies a failed batch. A later non-403
	// response clears it so a lifted limit is visible again.
	forbidden atomic.Bool
}

// newForbiddenTransport wraps a transport dedicated to Langfuse. Sharing the
// process default transport would let a stuck ingestion endpoint consume
// connections that relay traffic needs.
func newForbiddenTransport() *forbiddenTransport {
	return &forbiddenTransport{base: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}}
}

func (t *forbiddenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}
	if response.StatusCode != http.StatusForbidden {
		t.forbidden.Store(false)
		return response, nil
	}

	if response.Body != nil {
		_ = response.Body.Close()
	}
	response.Body = io.NopCloser(strings.NewReader(forbiddenBodyStandIn))
	response.ContentLength = int64(len(forbiddenBodyStandIn))
	response.Header.Set("Content-Length", strconv.Itoa(len(forbiddenBodyStandIn)))
	response.Header.Del("Content-Encoding")
	t.forbidden.Store(true)
	return response, nil
}

// newExporterHTTPClient builds the dedicated client the OTLP exporter posts
// spans with.
func newExporterHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Timeout: exporterRequestTimeout, Transport: transport}
}

// langfuseExportError is the only export failure the global OTel error handler
// ever sees from Langfuse. It carries a fixed summary instead of the exporter's
// own error, which embeds the upstream response body, the target URL and would
// otherwise reach the process log verbatim.
type langfuseExportError struct {
	key     string
	summary string
}

func (e langfuseExportError) Error() string { return e.summary }

// countingExporter turns export outcomes into the per-runtime span counters of
// design §11. It changes nothing about encoding, batching, retries or
// transport; it only labels errors and counts spans.
type countingExporter struct {
	sdktrace.SpanExporter
	transport *forbiddenTransport
	version   uint64

	received, exported, failed atomic.Int64
}

func newCountingExporter(inner sdktrace.SpanExporter, transport *forbiddenTransport, version uint64) *countingExporter {
	return &countingExporter{SpanExporter: inner, transport: transport, version: version}
}

func (e *countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	count := int64(len(spans))
	e.received.Add(count)
	if err := e.SpanExporter.ExportSpans(ctx, spans); err != nil {
		e.failed.Add(count)
		category := "export_failed"
		if e.transport != nil && e.transport.forbidden.Load() {
			category = "ingestion_forbidden"
		}
		return langfuseExportError{
			key: fmt.Sprintf("%s:%d", category, e.version),
			summary: fmt.Sprintf("langfuse export failed: %s (runtime version %d, %d span(s))",
				category, e.version, count),
		}
	}
	e.exported.Add(count)
	return nil
}

// langfuseErrorHandler is the process wide OTel error handler proxy. Langfuse
// export errors are rate limited and logged by their stable summary; everything
// else keeps reaching the handler that was in place before.
type langfuseErrorHandler struct {
	previous otel.ErrorHandler

	mu         sync.Mutex
	lastWarned map[string]time.Time
}

func newLangfuseErrorHandler(previous otel.ErrorHandler) *langfuseErrorHandler {
	return &langfuseErrorHandler{previous: previous, lastWarned: map[string]time.Time{}}
}

func (h *langfuseErrorHandler) Handle(err error) {
	var exportErr langfuseExportError
	if !errors.As(err, &exportErr) {
		h.previous.Handle(err)
		return
	}

	h.mu.Lock()
	last, seen := h.lastWarned[exportErr.key]
	allowed := !seen || time.Since(last) >= alertInterval
	if allowed {
		h.lastWarned[exportErr.key] = time.Now()
	}
	h.mu.Unlock()
	if allowed {
		emitWarning(exportErr.Error())
	}
}

var errorHandlerOnce sync.Once

// installErrorHandlerOnce installs the proxy handler exactly once per process.
//
// The handler returned by otel.GetErrorHandler before the first SetErrorHandler
// call is the SDK delegator, which SetErrorHandler immediately re-points at the
// handler being installed; forwarding to it would recurse forever. Unrelated
// OTel errors therefore go straight to the process log, which is what the SDK
// default does anyway.
func installErrorHandlerOnce() {
	errorHandlerOnce.Do(func() {
		otel.SetErrorHandler(newLangfuseErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			emitWarning("otel error: " + err.Error())
		})))
	})
}

// buildExporterOptions turns a validated snapshot into the complete OTLP/HTTP
// exporter configuration. Every endpoint aspect is set explicitly from the
// snapshot so that OTEL_EXPORTER_OTLP*_ENDPOINT in the process environment
// cannot redirect an administrator's Langfuse traffic (design §11).
func buildExporterOptions(snapshot Snapshot, transport http.RoundTripper) ([]otlptracehttp.Option, error) {
	if snapshot.TracesURL == "" {
		return nil, errors.New("langfuse traces url is empty")
	}
	return []otlptracehttp.Option{
		// One option covers scheme/insecure, authority and path at once, so an
		// https host can never inherit an insecure environment default.
		otlptracehttp.WithEndpointURL(snapshot.TracesURL),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": basicAuthHeader(snapshot.PublicKey, snapshot.SecretKey)}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: retryInitialInterval,
			MaxInterval:     retryMaxInterval,
			MaxElapsedTime:  retryMaxElapsedTime,
		}),
		otlptracehttp.WithHTTPClient(newExporterHTTPClient(transport)),
	}, nil
}
