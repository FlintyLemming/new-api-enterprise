package langfuse

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestDeriveTraceIDUsesSha256Prefix(t *testing.T) {
	digest := sha256.Sum256([]byte("abc"))
	var expected trace.TraceID
	copy(expected[:], digest[:16])

	derived := DeriveTraceID("abc")
	assert.Equal(t, expected, derived, "the digest prefix must be copied in order")
	assert.True(t, derived.IsValid())
	assert.Equal(t, derived, DeriveTraceID("abc"), "the same request id must always map to the same trace")
	assert.NotEqual(t, derived, DeriveTraceID("abd"))
}

func TestDeriveTraceIDReplacesAllZeroDigest(t *testing.T) {
	previous := traceIDHash
	traceIDHash = func([]byte) [32]byte { return [32]byte{} }
	t.Cleanup(func() { traceIDHash = previous })

	derived := DeriveTraceID("any-request-id")
	require.True(t, derived.IsValid(), "an all zero trace id would be rejected by OTel")
	assert.Equal(t, byte(0x01), derived[15])
	for _, b := range derived[:15] {
		assert.Zero(t, b, "only the last byte may be adjusted")
	}
}

func TestIDGeneratorPrefersContextTraceIDAndVariesSpanIDs(t *testing.T) {
	generator := langfuseIDGenerator{}
	expected := DeriveTraceID("request-42")
	ctx := contextWithTraceID(t.Context(), expected)

	traceID, spanID := generator.NewIDs(ctx)
	assert.Equal(t, expected, traceID)
	require.True(t, spanID.IsValid())

	_, second := generator.NewIDs(ctx)
	assert.NotEqual(t, spanID, second, "span ids must stay random per span")

	child := generator.NewSpanID(ctx, expected)
	require.True(t, child.IsValid())
	assert.NotEqual(t, spanID, child)
}

func TestIDGeneratorFallsBackWhenContextCarriesNoTraceID(t *testing.T) {
	generator := langfuseIDGenerator{}

	traceID, spanID := generator.NewIDs(t.Context())
	assert.True(t, traceID.IsValid(), "a missing context trace id must not produce an invalid span context")
	assert.True(t, spanID.IsValid())

	other, _ := generator.NewIDs(contextWithTraceID(t.Context(), trace.TraceID{}))
	assert.True(t, other.IsValid(), "an invalid context trace id must be replaced, not propagated")
}
