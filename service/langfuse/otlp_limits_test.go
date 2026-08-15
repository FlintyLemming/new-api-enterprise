package langfuse

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpCollector decodes what the official exporter actually put on the wire, so
// the assertions cover the exported protobuf rather than the SDK's in-memory
// span.
type otlpCollector struct {
	t  *testing.T
	mu sync.Mutex

	resourceSpans []*tracepb.ResourceSpans
}

func (c *otlpCollector) handler(w http.ResponseWriter, r *http.Request) {
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(r.Body)
		require.NoError(c.t, err)
		defer func() { _ = unzipped.Close() }()
		body = unzipped
	}
	raw, err := io.ReadAll(body)
	require.NoError(c.t, err)

	var request coltracepb.ExportTraceServiceRequest
	require.NoError(c.t, proto.Unmarshal(raw, &request))

	c.mu.Lock()
	c.resourceSpans = append(c.resourceSpans, request.GetResourceSpans()...)
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *otlpCollector) spans() []*tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	var spans []*tracepb.Span
	for _, resourceSpan := range c.resourceSpans {
		for _, scopeSpan := range resourceSpan.GetScopeSpans() {
			spans = append(spans, scopeSpan.GetSpans()...)
		}
	}
	return spans
}

func (c *otlpCollector) resourceAttributes() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	attributes := map[string]string{}
	for _, resourceSpan := range c.resourceSpans {
		for _, attr := range resourceSpan.GetResource().GetAttributes() {
			attributes[attr.GetKey()] = attr.GetValue().GetStringValue()
		}
	}
	return attributes
}

func spanAttributes(span *tracepb.Span) map[string]string {
	attributes := map[string]string{}
	for _, attr := range span.GetAttributes() {
		attributes[attr.GetKey()] = attr.GetValue().GetStringValue()
	}
	return attributes
}

// TestSpanLimitsOverrideOtelEnvironment is the design §14.2 hard requirement:
// hostile OTEL_*ATTRIBUTE* settings must not truncate or drop the structured
// JSON attributes Langfuse ingestion depends on.
func TestSpanLimitsOverrideOtelEnvironment(t *testing.T) {
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "1")
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "1")
	t.Setenv("OTEL_ATTRIBUTE_COUNT_LIMIT", "1")
	t.Setenv("OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT", "1")

	collector := &otlpCollector{t: t}
	server := httptest.NewServer(http.HandlerFunc(collector.handler))
	t.Cleanup(server.Close)

	largeJSON, err := common.Marshal(map[string]string{"content": strings.Repeat("langfuse", 12800)})
	require.NoError(t, err)
	require.Greater(t, len(largeJSON), 100_000)

	attributes := []attribute.KeyValue{attribute.String("langfuse.observation.input", string(largeJSON))}
	for i := 0; i < 9; i++ {
		attributes = append(attributes, attribute.String(fmt.Sprintf("langfuse.test.attribute.%d", i), fmt.Sprintf("value-%d", i)))
	}

	runtime := newRuntimeForTest(t, snapshotForHost(t, server.URL))
	expectedTraceID := DeriveTraceID("limits-request")
	ctx := contextWithTraceID(t.Context(), expectedTraceID)

	rootCtx, root := runtime.tracer.Start(ctx, "root")
	root.SetAttributes(attributes...)
	generations := make([]trace.Span, 0, 2)
	for i := 0; i < 2; i++ {
		_, generation := runtime.tracer.Start(rootCtx, fmt.Sprintf("generation-%d", i))
		generation.SetAttributes(attributes...)
		generations = append(generations, generation)
	}
	root.End()
	for _, generation := range generations {
		generation.End()
	}
	require.NoError(t, runtime.provider.ForceFlush(t.Context()))

	spans := collector.spans()
	require.Len(t, spans, 3)

	var rootSpanID []byte
	for _, span := range spans {
		assert.Equal(t, expectedTraceID[:], span.GetTraceId(), "every span must carry the derived trace id")

		exported := spanAttributes(span)
		assert.Len(t, exported, len(attributes), "the locked count limit must keep every produced attribute")
		assert.LessOrEqual(t, len(exported), langfuseSpanAttributeCountLimit)
		assert.Zero(t, span.GetDroppedAttributesCount())
		assert.Equal(t, string(largeJSON), exported["langfuse.observation.input"],
			"the locked value length limit must keep the JSON attribute complete")
		for i := 0; i < 9; i++ {
			assert.Equal(t, fmt.Sprintf("value-%d", i), exported[fmt.Sprintf("langfuse.test.attribute.%d", i)])
		}

		if span.GetName() == "root" {
			assert.Empty(t, span.GetParentSpanId(), "a Langfuse root must have no parent")
			rootSpanID = span.GetSpanId()
		}
	}
	require.NotEmpty(t, rootSpanID)
	for _, span := range spans {
		if span.GetName() != "root" {
			assert.Equal(t, rootSpanID, span.GetParentSpanId(), "generations hang under the root span")
		}
	}

	resourceAttributes := collector.resourceAttributes()
	assert.Equal(t, "new-api", resourceAttributes["service.name"])
	assert.Equal(t, common.Version, resourceAttributes["service.version"])
	assert.Equal(t, "default", resourceAttributes["deployment.environment.name"])
}
