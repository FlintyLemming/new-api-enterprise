package langfuse

import (
	"context"
	"crypto/rand"
	"crypto/sha256"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// traceIDHash is the digest DeriveTraceID is built on. It is a variable so the
// all zero defence can be tested without searching for a real SHA-256 preimage.
var traceIDHash = sha256.Sum256

// DeriveTraceID maps a New API request ID to a stable Langfuse trace ID, so the
// same request always lands on the same trace even when the span is created
// later by the async worker (design §5.1).
func DeriveTraceID(requestID string) trace.TraceID {
	digest := traceIDHash([]byte(requestID))
	var traceID trace.TraceID
	copy(traceID[:], digest[:len(traceID)])
	if !traceID.IsValid() {
		// An all zero digest prefix would be an invalid OTel trace ID and the
		// span would be dropped; one fixed bit keeps it usable.
		traceID[len(traceID)-1] = 0x01
	}
	return traceID
}

type traceIDCtxKey struct{}

// contextWithTraceID hands the derived trace ID to the ID generator, which is
// the only supported way to pin a trace ID in the OTel SDK.
func contextWithTraceID(ctx context.Context, traceID trace.TraceID) context.Context {
	return context.WithValue(ctx, traceIDCtxKey{}, traceID)
}

// langfuseIDGenerator keeps the derived trace ID and randomises span IDs.
type langfuseIDGenerator struct{}

var _ sdktrace.IDGenerator = langfuseIDGenerator{}

func (g langfuseIDGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	traceID, ok := ctx.Value(traceIDCtxKey{}).(trace.TraceID)
	if !ok || !traceID.IsValid() {
		// Every Langfuse span is created with a derived ID; a random one only
		// keeps an unexpected caller from producing an invalid span context.
		_, _ = rand.Read(traceID[:])
		if !traceID.IsValid() {
			traceID[len(traceID)-1] = 0x01
		}
	}
	return traceID, g.NewSpanID(ctx, traceID)
}

func (g langfuseIDGenerator) NewSpanID(context.Context, trace.TraceID) trace.SpanID {
	var spanID trace.SpanID
	_, _ = rand.Read(spanID[:])
	if !spanID.IsValid() {
		spanID[len(spanID)-1] = 0x01
	}
	return spanID
}
