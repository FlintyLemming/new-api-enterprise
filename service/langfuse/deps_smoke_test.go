package langfuse

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestOtelDependenciesSmoke(t *testing.T) {
	// 只验证依赖链可编译、SDK 基本可用;不建立任何全局状态。
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	tr := tp.Tracer("smoke")
	var span trace.Span
	func() {
		_, s := tr.Start(t.Context(), "smoke-span")
		defer s.End()
		span = s
	}()
	if !span.SpanContext().IsValid() {
		t.Fatal("expected valid span context")
	}
	_ = otel.GetTracerProvider()

	// 仅构造 OTLP/HTTP client(不发起任何网络连接),锁定 exporter 为 direct 依赖。
	if otlptracehttp.NewClient(otlptracehttp.WithEndpointURL("http://127.0.0.1:1/api/public/otel/v1/traces")) == nil {
		t.Fatal("expected non-nil otlptracehttp client")
	}
}
