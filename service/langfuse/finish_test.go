package langfuse

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// finishFixture drives a whole traced request against an in-memory span
// processor, so the exported observations and their End order can be asserted
// without a collector.
type finishFixture struct {
	*beginFixture

	spans    *tracetest.SpanRecorder
	clock    *testClock
	recorder *Recorder
}

func newFinishFixture(t *testing.T, mutate func(*langfuse_setting.LangfuseSetting), body string) *finishFixture {
	t.Helper()
	setting := alwaysSampled()
	if mutate != nil {
		mutate(&setting)
	}

	// Every materialization job runs inline, so span assertions never race the
	// relay goroutine pool.
	previousSubmit := submitWorker
	submitWorker = func(job func()) { job() }
	t.Cleanup(func() { submitWorker = previousSubmit })

	base := newBeginFixture(t, setting, body)
	spans := tracetest.NewSpanRecorder()
	require.NotNil(t, base.runtime)
	replaceProcessor(t, base.runtime, spans)

	recorder := Begin(base.context, base.info, nil)
	require.NotNil(t, recorder)

	clock := &testClock{now: base.info.StartTime.Add(time.Second)}
	recorder.clock = func() time.Time { return clock.now }

	return &finishFixture{beginFixture: base, spans: spans, clock: clock, recorder: recorder}
}

// replaceProcessor swaps the batch pipeline for a synchronous recorder while
// keeping the deterministic ID generator and the raw span limits the Langfuse
// provider is built with.
func replaceProcessor(t *testing.T, runtime *TelemetryRuntime, spans *tracetest.SpanRecorder) {
	t.Helper()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithIDGenerator(langfuseIDGenerator{}),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit:   -1,
			AttributeCountLimit:         langfuseSpanAttributeCountLimit,
			EventCountLimit:             0,
			LinkCountLimit:              0,
			AttributePerEventCountLimit: 0,
			AttributePerLinkCountLimit:  0,
		}),
		sdktrace.WithSpanProcessor(spans),
	)
	runtime.tracer = provider.Tracer(tracerName)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
}

func (f *finishFixture) runAttempt(t *testing.T, channelName, upstreamModel, response string, apiErr *types.NewAPIError) {
	t.Helper()
	common.SetContextKey(f.context, constant.ContextKeyChannelName, channelName)
	f.info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 7, ChannelType: 1, UpstreamModelName: upstreamModel}

	BeginAttempt(f.context, f.info)
	f.clock.advance(100 * time.Millisecond)
	if response != "" {
		_, err := f.context.Writer.Write([]byte(response))
		require.NoError(t, err)
	}
	f.clock.advance(100 * time.Millisecond)
	EndAttempt(f.context, f.info, apiErr)
	f.clock.advance(10 * time.Millisecond)
}

func attributesOf(span sdktrace.ReadOnlySpan) map[attribute.Key]string {
	values := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		values[kv.Key] = kv.Value.Emit()
	}
	return values
}

func TestFinishEndsRootBeforeGenerationsWithHistoricalTimestamps(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o","messages":[]}`)

	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[{"message":{"role":"assistant","content":"one"}}]}`,
		types.NewErrorWithStatusCode(errors.New("upstream down"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway))
	fixture.runAttempt(t, "openai-us", "gpt-4o-2", `{"choices":[{"message":{"role":"assistant","content":"two"}}]}`, nil)

	rootEnd := fixture.clock.now
	Finish(fixture.context, fixture.info, nil)

	ended := fixture.spans.Ended()
	require.Len(t, ended, 3)
	assert.Equal(t, "openai gpt-4o", ended[0].Name(), "the root reaches the queue first")
	assert.Equal(t, "azure-eu gpt-4o-1", ended[1].Name())
	assert.Equal(t, "openai-us gpt-4o-2", ended[2].Name())

	root, first, second := ended[0], ended[1], ended[2]
	assert.Equal(t, fixture.info.StartTime.UnixNano(), root.StartTime().UnixNano())
	assert.Equal(t, rootEnd.UnixNano(), root.EndTime().UnixNano())
	assert.Equal(t, 200*time.Millisecond, first.EndTime().Sub(first.StartTime()))
	assert.Equal(t, 200*time.Millisecond, second.EndTime().Sub(second.StartTime()))
	assert.True(t, first.EndTime().Before(second.StartTime()),
		"a failed attempt must not outlive the attempt that replaced it")

	// Every observation belongs to the one derived trace, and the generations
	// are children of the root.
	assert.Equal(t, DeriveTraceID(fixture.info.RequestId), root.SpanContext().TraceID())
	assert.False(t, root.Parent().IsValid(), "the root must have no parent")
	assert.Equal(t, root.SpanContext().SpanID(), first.Parent().SpanID())
	assert.Equal(t, root.SpanContext().SpanID(), second.Parent().SpanID())
}

func TestFinishIsIdempotent(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	Finish(fixture.context, fixture.info, nil)

	assert.Len(t, fixture.spans.Ended(), 2, "a defensive second Finish must not duplicate observations")
}

func TestFinishRestoresTheWriterItInstalled(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	original := fixture.recorder.origWriter
	require.NotSame(t, original, fixture.context.Writer)

	Finish(fixture.context, fixture.info, nil)
	assert.Same(t, original, fixture.context.Writer)
}

func TestFinishKeepsALaterWrapperAndReportsTheViolation(t *testing.T) {
	resetCaptureWarnWindows(t)
	warnings := captureWarnings(t)
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)

	replacement := &recordingWriter{ResponseWriter: fixture.context.Writer}
	fixture.context.Writer = replacement

	Finish(fixture.context, fixture.info, nil)
	assert.Same(t, replacement, fixture.context.Writer, "a later wrapper must not be swallowed")
	assert.Contains(t, warnings.joined(), "writer_replaced_before_finish")
}

func TestFinishDiscardsGeminiRequestsThatLeftTheSupportedAction(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"contents":[]}`)
	fixture.recorder.relayFormat = types.RelayFormatGemini
	fixture.recorder.requestPath = "/v1beta/models/gemini-2.5-flash:generateContent"
	fixture.info.RelayFormat = types.RelayFormatGemini

	// The channel mapped the alias onto an embedding model, so the request was
	// never a conversation.
	fixture.runAttempt(t, "gemini", "text-embedding-004", `{"embedding":{}}`, nil)
	require.Equal(t, relayconstant.GeminiActionEmbedding, fixture.recorder.attempts[0].GeminiFinal)

	Finish(fixture.context, fixture.info, nil)

	assert.Empty(t, fixture.spans.Ended(), "an out of scope Gemini request produces no observation")
	assert.Zero(t, fixture.runtime.inFlightLeases(), "the lease is handed back")
	assert.Zero(t, inFlightCaptureBytes.Load(), "the capture budget is handed back")
}

func TestFinishKeepsGeminiGenerateRequests(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"contents":[]}`)
	fixture.recorder.relayFormat = types.RelayFormatGemini
	fixture.recorder.requestPath = "/v1beta/models/gemini-2.5-flash:generateContent"
	fixture.info.RelayFormat = types.RelayFormatGemini

	fixture.runAttempt(t, "gemini", "gemini-2.5-flash", `{"candidates":[]}`, nil)
	Finish(fixture.context, fixture.info, nil)

	assert.Len(t, fixture.spans.Ended(), 2)
}

func TestFinishWithoutAttemptsStillMaterializesTheRoot(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	Finish(fixture.context, fixture.info, nil)

	ended := fixture.spans.Ended()
	require.Len(t, ended, 1)
	assert.Equal(t, "openai gpt-4o", ended[0].Name())
	assert.Zero(t, fixture.runtime.inFlightLeases())
	assert.Zero(t, inFlightCaptureBytes.Load())
}

func TestFinishDegradesToMetadataOnlyWhenAdmissionIsFull(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[]}`, nil)

	// Saturate the semaphore so this request cannot hand its payload to a
	// worker.
	admissionInFlight.Store(maxAdmittedWorkers)
	t.Cleanup(func() { admissionInFlight.Store(0) })

	Finish(fixture.context, fixture.info, nil)

	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)
	root := attributesOf(ended[0])
	assert.NotContains(t, root, attribute.Key(attrObservationInput))
	assert.NotContains(t, root, attribute.Key(attrObservationOutput))
	assert.Contains(t, root[attrObservationMetadata], `"capture_state":"admission"`)
	assert.Zero(t, inFlightCaptureBytes.Load())
}

func TestRootWritesObservationDomainAttributesOnly(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	fixture.context.Request.Header.Set(LangfuseSessionHeader, "s-1")
	fixture.recorder.session = SessionIdentity{ScopedID: "42:s-1", RawSessionID: "s-1", Source: "x-langfuse-session-id"}
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1",
		`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	root := attributesOf(ended[0])
	assert.Equal(t, observationTypeSpan, root[attrObservationType])
	assert.Equal(t, "true", root[attrInternalAsRoot])
	assert.Equal(t, "42", root[attrUserID])
	assert.Equal(t, "42:s-1", root[attrSessionID])
	assert.Contains(t, root, attribute.Key(attrObservationInput))
	assert.Contains(t, root, attribute.Key(attrObservationOutput))
	assert.Contains(t, root, attribute.Key(attrObservationMetadata))

	for key := range root {
		assert.False(t, strings.HasPrefix(string(key), "langfuse.trace."),
			"the root must not duplicate the trace domain: %s", key)
	}
	// The span name is the only source of the Langfuse trace name.
	assert.NotContains(t, root, attribute.Key("langfuse.trace.name"))
	assert.LessOrEqual(t, len(ended[0].Attributes()), langfuseSpanAttributeCountLimit)
}

func TestGenerationNeverCarriesATraceUpdateAttribute(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.recorder.session = SessionIdentity{ScopedID: "42:s-1", RawSessionID: "s-1", Source: "x-langfuse-session-id"}
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	// The fixed denylist of design §13, item by item.
	denied := []string{
		"langfuse.trace.name", "langfuse.trace.input", "langfuse.trace.output",
		"langfuse.trace.metadata", "user.id", "session.id", "langfuse.trace.public",
		"langfuse.trace.tags", "langfuse.user.id", "langfuse.session.id",
		"langfuse.observation.metadata.langfuse_user_id",
		"langfuse.observation.metadata.langfuse_session_id",
		"langfuse.observation.metadata.langfuse_tags",
		"langfuse.trace.metadata.langfuse_session_id",
		"langfuse.trace.metadata.langfuse_user_id",
		"langfuse.trace.metadata.langfuse_tags",
		"ai.telemetry.metadata.sessionId", "ai.telemetry.metadata.userId",
		"ai.telemetry.metadata.tags", "tag.tags",
	}
	generation := attributesOf(ended[1])
	for _, key := range denied {
		assert.NotContains(t, generation, attribute.Key(key))
	}
	for key := range generation {
		assert.False(t, strings.HasPrefix(string(key), "langfuse.trace.metadata"),
			"no trace metadata prefix is allowed on a generation: %s", key)
	}
	assert.Equal(t, observationTypeGeneration, generation[attrObservationType])
}

func TestGenerationNamesTheModelOnlyWhenItFailed(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[]}`,
		types.NewErrorWithStatusCode(errors.New("boom"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway))
	fixture.runAttempt(t, "openai-us", "gpt-4o-2", `{"choices":[]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 3)

	failed := attributesOf(ended[1])
	assert.Equal(t, "gpt-4o-1", failed[attrModelName],
		"a failed attempt without usage may name its model")

	succeeded := attributesOf(ended[2])
	assert.NotContains(t, succeeded, attribute.Key(attrModelName),
		"a successful attempt without an authoritative cost must not name its model")
}

// TestFailedAttemptWithEmptyUpstreamErrorCodeStillFails protects the failure
// signal itself. Providers are free to answer with an explicit empty "code",
// and a real E2E against Langfuse showed such an attempt arriving as a normal
// generation. The presence of the error, not its code, decides.
func TestFailedAttemptWithEmptyUpstreamErrorCodeStillFails(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	emptyCode := types.WithOpenAIError(
		types.OpenAIError{Message: "Invalid token", Code: ""}, http.StatusUnauthorized)
	require.Empty(t, string(emptyCode.GetErrorCode()), "this fixture only matters while the code is empty")

	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", "", emptyCode)
	fixture.runAttempt(t, "openai-us", "gpt-4o-2", `{"choices":[]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 3)

	assert.Equal(t, codes.Error, ended[1].Status().Code,
		"an upstream failure must reach Langfuse as an error observation")
	assert.NotEmpty(t, ended[1].Status().Description)
	assert.Equal(t, "gpt-4o-1", attributesOf(ended[1])[attrModelName],
		"a failed attempt without usage may name its model")
	assert.Equal(t, codes.Unset, ended[2].Status().Code, "the successful retry is not an error")
}

func TestAllowModelNameFollowsTheCostContract(t *testing.T) {
	cases := []struct {
		name                    string
		hasCost, hasUsage, fail bool
		allowed                 bool
	}{
		{name: "authoritative cost", hasCost: true, hasUsage: true, allowed: true},
		{name: "error without usage", fail: true, allowed: true},
		{name: "error with usage but no cost", hasUsage: true, fail: true, allowed: false},
		{name: "success without cost", allowed: false},
		{name: "success with usage and no cost", hasUsage: true, allowed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allowed, allowModelName(tc.hasCost, tc.hasUsage, tc.fail))
		})
	}
}

func TestRootMetadataCarriesTheDocumentedSummary(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.info.IsPlayground = true
	fixture.recorder.isPlayground = true
	fixture.recorder.username = "alice"
	fixture.recorder.tokenName = "prod-key"
	fixture.recorder.tokenId = 3
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	metadata := decodeMetadata(t, attributesOf(ended[0])[attrObservationMetadata])
	assert.Equal(t, "req-1", metadata["request_id"])
	assert.Equal(t, "openai", metadata["relay_format"])
	assert.Equal(t, "gpt-4o", metadata["origin_model"])
	assert.Equal(t, "/v1/chat/completions", metadata["request_path"])
	assert.Equal(t, "alice", metadata["username"])
	assert.Equal(t, "prod-key", metadata["token_name"])
	assert.Equal(t, float64(3), metadata["token_id"])
	assert.Equal(t, "default", metadata["user_group"])
	assert.Equal(t, "default", metadata["selected_group"])
	assert.Equal(t, float64(1), metadata["attempt_count"])
	assert.Equal(t, float64(0), metadata["retry_count"])
	assert.Equal(t, CaptureStateFull, metadata["capture_state"])
	assert.Equal(t, true, metadata["is_playground"])
	assert.Equal(t, false, metadata["content_truncated"])
	assert.Equal(t, false, metadata["content_redacted"])
	assert.NotContains(t, metadata, "quota", "no settled attempt means no trace level quota")
	assert.NotContains(t, metadata, "billing_source")

	generation := decodeMetadata(t, attributesOf(ended[1])[attrObservationMetadata])
	assert.Equal(t, float64(0), generation["attempt_index"])
	assert.Equal(t, float64(7), generation["channel_id"])
	assert.Equal(t, "azure-eu", generation["channel_name"])
	assert.Equal(t, "client_request", generation["input_source"])
	assert.Equal(t, AttemptEndHandlerReturned, generation["attempt_end_reason"])
	assert.Equal(t, "gpt-4o-1", generation["upstream_model"])
}

func TestFinishClosesALingeringAttemptAsFinalizerCleanup(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	common.SetContextKey(fixture.context, constant.ContextKeyChannelName, "azure-eu")
	BeginAttempt(fixture.context, fixture.info)
	fixture.clock.advance(time.Second)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	generation := decodeMetadata(t, attributesOf(ended[1])[attrObservationMetadata])
	assert.Equal(t, AttemptEndFinalizerClean, generation["attempt_end_reason"])
}

func TestMarkLifecyclePanicClosesTheActiveAttempt(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	common.SetContextKey(fixture.context, constant.ContextKeyChannelName, "azure-eu")
	BeginAttempt(fixture.context, fixture.info)
	fixture.clock.advance(time.Second)

	MarkLifecyclePanic(fixture.context, fixture.info)
	Finish(fixture.context, fixture.info, nil)

	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)
	assert.Equal(t, AttemptEndLifecyclePanic,
		decodeMetadata(t, attributesOf(ended[1])[attrObservationMetadata])["attempt_end_reason"])
	assert.Equal(t, true, decodeMetadata(t, attributesOf(ended[0])[attrObservationMetadata])["lifecycle_panic"])
}

func TestExportedPayloadsAreParsableJSONStrings(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	fixture.recorder.modelParams = map[string]any{"temperature": 0.0, "stream": false}
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1",
		`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	root := attributesOf(ended[0])
	decodeMetadata(t, root[attrObservationMetadata])
	assert.JSONEq(t, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`, root[attrObservationInput])

	generation := attributesOf(ended[1])
	decodeMetadata(t, generation[attrObservationMetadata])
	assert.JSONEq(t, `{"temperature":0,"stream":false}`, generation[attrModelParameters])
	assert.Equal(t, root[attrObservationInput], generation[attrObservationInput],
		"root and every generation export the same sanitized client request")

	var output map[string]any
	require.NoError(t, common.UnmarshalJsonStr(generation[attrObservationOutput], &output))
	assert.Equal(t, "hello", output["content"], "the response is aggregated, not copied verbatim")
}

func decodeMetadata(t *testing.T, encoded string) map[string]any {
	t.Helper()
	require.NotEmpty(t, encoded)
	var metadata map[string]any
	require.NoError(t, common.UnmarshalJsonStr(encoded, &metadata))
	return metadata
}

func TestContentPipelinePanicDegradesToMetadataOnly(t *testing.T) {
	resetCaptureWarnWindows(t)
	warnings := captureWarnings(t)
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runAttempt(t, "azure-eu", "gpt-4o-1", `{"choices":[]}`, nil)

	contentFaultHook = func() { panic("injected sanitizer fault") }
	t.Cleanup(func() { contentFaultHook = nil })

	assert.NotPanics(t, func() { Finish(fixture.context, fixture.info, nil) })

	ended := fixture.spans.Ended()
	require.Len(t, ended, 2, "the trace survives without its payload")
	root := attributesOf(ended[0])
	assert.NotContains(t, root, attribute.Key(attrObservationInput))
	assert.NotContains(t, root, attribute.Key(attrObservationOutput))
	assert.Equal(t, CaptureStatePanic, decodeMetadata(t, root[attrObservationMetadata])["capture_state"])
	assert.Contains(t, warnings.joined(), "content_panic")
	assert.Zero(t, inFlightCaptureBytes.Load())
	assert.Zero(t, fixture.runtime.inFlightLeases())
}

func TestRootMetadataReportsEveryCaptureState(t *testing.T) {
	for _, state := range []string{
		CaptureStateFull, CaptureStateBudget, CaptureStateAdmission,
		CaptureStateDisabled, CaptureStatePanic,
	} {
		t.Run(state, func(t *testing.T) {
			material := recorderMaterial{
				RequestId:    "req-1",
				RelayFormat:  string(types.RelayFormatOpenAI),
				OriginModel:  "gpt-4o",
				CaptureState: state,
			}
			assert.Equal(t, state, buildRootMetadata(&material, nil)["capture_state"])
		})
	}
}

func TestRootMetadataNeverCopiesTheRawSession(t *testing.T) {
	material := recorderMaterial{
		RequestId:   "req-1",
		RelayFormat: string(types.RelayFormatOpenAI),
		Session: SessionIdentity{
			ScopedID:      "42:secret-session",
			RawSessionID:  "secret-session",
			Source:        "x-langfuse-session-id",
			OmittedReason: SessionOmittedTooLong,
		},
		CaptureState: CaptureStateFull,
	}

	metadata := buildRootMetadata(&material, nil)
	assert.Equal(t, "user", metadata["session_scope"])
	assert.Equal(t, "x-langfuse-session-id", metadata["session_source"])
	assert.Equal(t, SessionOmittedTooLong, metadata["session_omitted_reason"])
	for _, value := range metadata {
		assert.NotEqual(t, "secret-session", value, "the raw client session must never be exported as metadata")
	}
}

func TestEncodeMetadataFallsBackToTheDiagnosticKeys(t *testing.T) {
	metadata := map[string]any{
		"request_id":    "req-1",
		"capture_state": CaptureStateFull,
		"token_name":    strings.Repeat("n", maxMetadataBytes),
	}

	decoded := map[string]any{}
	require.NoError(t, common.UnmarshalJsonStr(encodeMetadata(metadata), &decoded))
	assert.Equal(t, true, decoded["metadata_truncated"])
	assert.Equal(t, "req-1", decoded["request_id"])
	assert.Equal(t, CaptureStateFull, decoded["capture_state"])
	assert.NotContains(t, decoded, "token_name", "an oversized value is dropped, not bisected")
}
