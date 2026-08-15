package langfuse

import (
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
)

// Export transport constants from design §11. The single request timeout is the
// value the 15s process shutdown budget is sized against, so the two must move
// together if either changes.
const (
	exporterRequestTimeout = 10 * time.Second

	retryInitialInterval = 1 * time.Second
	retryMaxInterval     = 5 * time.Second
	retryMaxElapsedTime  = 30 * time.Second
)

// basicAuthHeader builds the Langfuse ingestion credential. The result is a
// secret: it may only be handed to the exporter options and must never reach a
// log line, an error message or a test failure output.
func basicAuthHeader(publicKey, secretKey string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(publicKey+":"+secretKey))
}

// newExporterHTTPClient builds the dedicated client the OTLP exporter posts
// spans with. Langfuse never shares a transport with relay traffic or with the
// process default, so a stuck ingestion endpoint cannot consume connections
// that upstream requests need.
func newExporterHTTPClient() *http.Client {
	return &http.Client{
		Timeout: exporterRequestTimeout,
		Transport: &http.Transport{
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
		},
	}
}

// buildExporterOptions turns a validated snapshot into the complete OTLP/HTTP
// exporter configuration. Every endpoint aspect is set explicitly from the
// snapshot so that OTEL_EXPORTER_OTLP*_ENDPOINT in the process environment
// cannot redirect an administrator's Langfuse traffic (design §11).
func buildExporterOptions(snapshot Snapshot) ([]otlptracehttp.Option, error) {
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
		otlptracehttp.WithHTTPClient(newExporterHTTPClient()),
	}, nil
}
