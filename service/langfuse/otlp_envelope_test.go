package langfuse

import (
	"compress/gzip"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// The measurements below answer one deployment question: how large can a single
// OTLP request from this feature get, so an operator can size an ingress body
// limit. They are wire measurements of the official exporter's output only.
// They are deliberately NOT compared against Langfuse's 9,500,000 byte single
// span warning, which is computed on the TypeScript side from
// JSON.stringify(span) and has no fixed relation to protobuf bytes; the real
// ingestion behaviour is a §14.6 E2E observation, not a Go assertion.

// envelopeConfig is one whole-group-valid configuration. The first two are the
// per-field boundaries of design §10 (content at its maximum against response
// at its minimum, and the reverse). The third is the shipping default, which is
// what an operator actually has to size ingress for.
type envelopeConfig struct {
	name             string
	maxContentBytes  int
	maxResponseBytes int
	queueSize        int
	batchSize        int
	// inFlightBytes has to be stated because the response maximum now exceeds
	// the shipped capture budget default on its own.
	inFlightBytes int
}

var envelopeConfigs = []envelopeConfig{
	{name: "content_max_4MiB", maxContentBytes: 4 * 1024 * 1024, maxResponseBytes: 64 * 1024, queueSize: 16, batchSize: 1, inFlightBytes: 536870912},
	{name: "response_max_64MiB", maxContentBytes: 4 * 1024, maxResponseBytes: 64 * 1024 * 1024, queueSize: 16, batchSize: 1, inFlightBytes: 536870912},
	{name: "shipping_default", maxContentBytes: 65536, maxResponseBytes: 524288, queueSize: 64, batchSize: 16, inFlightBytes: 536870912},
}

// queuedSpanBytes is what one queued span body can reach: a span carries one
// input plus one output, both already reduced to max_content_bytes. The
// response buffer holds framed SSE that the worker aggregates and drops before
// a span exists, so it is memory the request occupies and never contributes
// here — which is why response_max_64MiB produces a tiny span.
func (c envelopeConfig) queuedSpanBytes() int { return 2 * c.maxContentBytes }

// escapingMaterial is a worst case body for the JSON string escaping the
// attribute budget is measured in: incompressible bytes, characters that all
// need an escape, and multi byte runes that must survive UTF-8 safe cuts.
type escapingMaterial struct {
	name  string
	build func(units int) string
}

var escapingMaterials = []escapingMaterial{
	{name: "incompressible_ascii", build: incompressibleASCII},
	{name: "escape_heavy", build: func(units int) string { return strings.Repeat("\"\\\n\t\x01\x1f", units) }},
	{name: "multibyte_utf8", build: func(units int) string { return strings.Repeat("他们好，😀", units) }},
}

// incompressibleASCII produces deterministic high entropy text, so the gzip
// figure recorded below is the worst case an operator has to plan for rather
// than the compression of a repeated pattern.
func incompressibleASCII(units int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	source := rand.New(rand.NewSource(0x5eed))
	out := make([]byte, units)
	for i := range out {
		out[i] = alphabet[source.Intn(len(alphabet))]
	}
	return string(out)
}

// envelopeCollector records the size of every OTLP request as it arrived, then
// decodes it. Both figures matter: the compressed one is what an ingress sees,
// the decompressed one is what Langfuse's route reads after gunzip.
type envelopeCollector struct {
	t *testing.T

	mu       sync.Mutex
	requests []envelopeRequest
}

type envelopeRequest struct {
	wireBytes  int
	protoBytes int
	spans      []*tracepb.Span
}

func (c *envelopeCollector) handler(w http.ResponseWriter, r *http.Request) {
	wire, err := io.ReadAll(r.Body)
	require.NoError(c.t, err)

	raw := wire
	if r.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(strings.NewReader(string(wire)))
		require.NoError(c.t, err)
		raw, err = io.ReadAll(unzipped)
		require.NoError(c.t, err)
		require.NoError(c.t, unzipped.Close())
	}

	var request coltracepb.ExportTraceServiceRequest
	require.NoError(c.t, proto.Unmarshal(raw, &request))

	var spans []*tracepb.Span
	for _, resourceSpan := range request.GetResourceSpans() {
		for _, scopeSpan := range resourceSpan.GetScopeSpans() {
			spans = append(spans, scopeSpan.GetSpans()...)
		}
	}

	c.mu.Lock()
	c.requests = append(c.requests, envelopeRequest{wireBytes: len(wire), protoBytes: len(raw), spans: spans})
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *envelopeCollector) snapshot() []envelopeRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]envelopeRequest{}, c.requests...)
}

// maximalDocument binary searches the largest document of this material whose
// contribution as an OTel string attribute still fits limit. Exporting that
// document is what makes the measurement a worst case rather than a sample.
func maximalDocument(t *testing.T, build func(units int) []byte, limit int) []byte {
	t.Helper()

	low, high := 0, 1
	for jsonEscapedLen(string(build(high))) <= limit {
		low = high
		high *= 2
		require.Less(t, high, 1<<24, "the document must eventually exceed the limit")
	}
	for low+1 < high {
		mid := (low + high) / 2
		if jsonEscapedLen(string(build(mid))) <= limit {
			low = mid
		} else {
			high = mid
		}
	}
	require.Positive(t, low, "the limit must fit at least one unit of this material")
	return build(low)
}

// maximalContent returns the largest sanitized input and output a request of
// this material could ever export, and asserts the §9.1 contract on both: the
// production sanitizer keeps them parsable JSON within the escaped budget.
func maximalContent(t *testing.T, material escapingMaterial, limit int) (input, output []byte) {
	t.Helper()

	inputDocument := func(units int) []byte {
		encoded, err := common.Marshal(map[string]any{
			"model":    "test-model",
			"stream":   false,
			"messages": []any{map[string]any{"role": "user", "content": material.build(units)}},
		})
		require.NoError(t, err)
		return encoded
	}
	outputDocument := func(units int) []byte {
		encoded, err := common.Marshal(map[string]any{
			"text":          material.build(units),
			"finish_reason": "stop",
		})
		require.NoError(t, err)
		return encoded
	}

	input = sanitizedWithinBudget(t, maximalDocument(t, inputDocument, limit), limit)
	output = sanitizedWithinBudget(t, maximalDocument(t, outputDocument, limit), limit)

	// The same material oversized by an order of magnitude must still come back
	// as a legal, budgeted document: the export below is the maximum, this is
	// the guarantee that overflowing it degrades instead of leaking bytes.
	sanitizedWithinBudget(t, inputDocument(10*len(input)), limit)
	return input, output
}

func sanitizedWithinBudget(t *testing.T, raw []byte, limit int) []byte {
	t.Helper()

	sanitized, ok := SanitizeJSON(raw, limit)
	require.True(t, ok, "the sanitizer must accept a JSON document")
	require.LessOrEqual(t, jsonEscapedLen(string(sanitized)), limit,
		"the escaped attribute contribution is the budget, not the raw length")

	var decoded any
	require.NoError(t, common.Unmarshal(sanitized, &decoded), "a sanitized attribute must stay parsable JSON")
	return sanitized
}

// envelopeRuntime publishes a runtime for one configuration and proves the
// configuration is one a validator would accept as a whole group.
func envelopeRuntime(t *testing.T, config envelopeConfig, host string) (*TelemetryRuntime, Snapshot) {
	t.Helper()

	setting := enabledSetting()
	setting.Host = host
	setting.MaxContentBytes = config.maxContentBytes
	setting.MaxResponseBytes = config.maxResponseBytes
	setting.MaxInFlightCaptureBytes = config.inFlightBytes
	setting.QueueSize = config.queueSize
	setting.BatchSize = config.batchSize
	// A one hour batch timeout keeps every span in the queue until the explicit
	// flush, so a batch is measured whole instead of split by a timer.
	setting.FlushIntervalSeconds = 300

	snapshot, err := langfuse_setting.BuildSnapshot(setting, 1)
	require.NoError(t, err, "the boundary configuration must pass whole group validation")
	return newRuntimeForTest(t, snapshot), snapshot
}

// envelopeMaterial builds a production shaped request: a root plus one settled
// generation, both carrying the maximal input and output.
func envelopeMaterial(runtime *TelemetryRuntime, snapshot Snapshot, requestID string) recorderMaterial {
	start := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	return recorderMaterial{
		Snapshot:      snapshot,
		Runtime:       runtime,
		RequestId:     requestID,
		Username:      "envelope-user",
		UserGroup:     "default",
		TokenName:     "envelope-token",
		SelectedGroup: "default",
		OriginModel:   "test-model",
		RelayFormat:   "openai",
		RequestPath:   "/v1/chat/completions",
		UserId:        7,
		TokenId:       11,
		RootStart:     start,
		RootEnd:       start.Add(1200 * time.Millisecond),
		Session:       SessionIdentity{ScopedID: "7:envelope-session", Source: "x-langfuse-session-id"},
		CaptureState:  CaptureStateFull,
		Capture:       FrozenCapture{Truncated: true},
		Attempts: []attemptValue{{
			Index:               0,
			ChannelID:           3,
			ChannelType:         1,
			ChannelName:         "envelope-channel",
			OriginModel:         "test-model",
			UpstreamModel:       "test-model-upstream",
			UpstreamRelayFormat: "openai",
			StartTime:           start.Add(10 * time.Millisecond),
			EndTime:             start.Add(1100 * time.Millisecond),
			FirstResponseTime:   start.Add(300 * time.Millisecond),
			EndReason:           AttemptEndHandlerReturned,
			Usage: &UsageRecord{
				Kind:          UsageKindText,
				Available:     true,
				ModelName:     "test-model-upstream",
				InputTokens:   1000,
				OutputTokens:  100,
				Quota:         615,
				QuotaPerUnit:  500000,
				BillingSource: "wallet",
				Settled:       true,
			},
		}},
	}
}

// TestOtlpEnvelopeMaxRootSpanWireSize exports the largest span each boundary
// configuration allows and records what the official exporter actually put on
// the wire (design §14.2, plan-8 Task 1).
func TestOtlpEnvelopeMaxRootSpanWireSize(t *testing.T) {
	for _, config := range envelopeConfigs {
		for _, material := range escapingMaterials {
			t.Run(config.name+"/"+material.name, func(t *testing.T) {
				assert.LessOrEqual(t, config.queuedSpanBytes(), 9_000_000,
					"2*max_content_bytes is the configured single span envelope")

				collector := &envelopeCollector{t: t}
				server := httptest.NewServer(http.HandlerFunc(collector.handler))
				t.Cleanup(server.Close)

				input, output := maximalContent(t, material, config.maxContentBytes)
				runtime, snapshot := envelopeRuntime(t, config, server.URL)

				content := &materializedContent{
					Input:          input,
					RootOutput:     output,
					AttemptOutputs: map[int][]byte{0: output},
				}
				materialize(envelopeMaterial(runtime, snapshot, "envelope-"+config.name+"-"+material.name), content)
				require.NoError(t, runtime.provider.ForceFlush(t.Context()))

				requests := collector.snapshot()
				require.NotEmpty(t, requests)

				var spans []*tracepb.Span
				largestWire, largestProto, largestSpanBytes := 0, 0, 0
				for _, request := range requests {
					spans = append(spans, request.spans...)
					largestWire = max(largestWire, request.wireBytes)
					largestProto = max(largestProto, request.protoBytes)
				}
				require.Len(t, spans, 2, "one root and one generation")

				for _, span := range spans {
					largestSpanBytes = max(largestSpanBytes, proto.Size(span))
					attributes := spanAttributes(span)

					for _, key := range []string{attrObservationInput, attrObservationOutput} {
						value, present := attributes[key]
						require.True(t, present, "%s must survive the export", key)

						var decoded any
						require.NoError(t, common.Unmarshal([]byte(value), &decoded),
							"%s must be parsable JSON after the round trip", key)
						assert.LessOrEqual(t, jsonEscapedLen(value), config.maxContentBytes,
							"%s must not exceed max_content_bytes as an escaped attribute", key)
					}
					assert.LessOrEqual(t, len(attributes[attrObservationMetadata]), maxMetadataBytes,
						"%s shares the per span non-content budget", attrObservationMetadata)
				}

				// Sizes and attribute names only: no payload and no credential
				// may reach a test log.
				t.Logf("config=%s material=%s envelope=%d span_protobuf_max=%d request_protobuf_max=%d request_gzip_max=%d requests=%d",
					config.name, material.name, config.queuedSpanBytes(),
					largestSpanBytes, largestProto, largestWire, len(requests))
			})
		}
	}
}

// TestOtlpEnvelopeFullBatchWireSize measures a full batch of the same kind of
// span, which is the largest single body this feature can send and therefore
// the figure an ingress body limit has to be derived from.
func TestOtlpEnvelopeFullBatchWireSize(t *testing.T) {
	// Incompressible ASCII is the material that survives gzip, so it is the one
	// an ingress limit has to be sized against; the per span worst case across
	// all three materials is measured above.
	material := escapingMaterials[0]
	require.Equal(t, "incompressible_ascii", material.name)

	for _, config := range envelopeConfigs {
		t.Run(config.name, func(t *testing.T) {
			collector := &envelopeCollector{t: t}
			server := httptest.NewServer(http.HandlerFunc(collector.handler))
			t.Cleanup(server.Close)

			input, output := maximalContent(t, material, config.maxContentBytes)
			runtime, snapshot := envelopeRuntime(t, config, server.URL)

			content := &materializedContent{
				Input:          input,
				RootOutput:     output,
				AttemptOutputs: map[int][]byte{0: output},
			}
			// Each materialized request contributes a root and a generation, so
			// half as many requests fill exactly one batch.
			for i := 0; i < max(1, config.batchSize/2); i++ {
				materialize(envelopeMaterial(runtime, snapshot, fmt.Sprintf("envelope-batch-%s-%d", config.name, i)), content)
			}
			require.NoError(t, runtime.provider.ForceFlush(t.Context()))

			requests := collector.snapshot()
			require.NotEmpty(t, requests)

			fullest, wire, protobufBytes := 0, 0, 0
			for _, request := range requests {
				if len(request.spans) > fullest {
					fullest, wire, protobufBytes = len(request.spans), request.wireBytes, request.protoBytes
				}
			}
			assert.LessOrEqual(t, fullest, config.batchSize, "the exporter must not exceed the configured batch size")
			assert.Equal(t, config.batchSize, fullest, "the flush must produce one full batch to measure")

			// Ingress sizing datum. The projection states what the same batch
			// would cost if every span were the configured worst case, which is
			// the number to configure a body limit from.
			t.Logf("config=%s batch_size=%d spans_in_largest_request=%d request_protobuf=%d request_gzip=%d worst_case_projection_protobuf=%d",
				config.name, config.batchSize, fullest, protobufBytes, wire,
				config.batchSize*config.queuedSpanBytes())
		})
	}
}
