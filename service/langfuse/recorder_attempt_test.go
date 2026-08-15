package langfuse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClock is the injectable time source of the lifecycle hooks, so attempt
// durations are exact instead of whatever the machine measured.
type testClock struct{ now time.Time }

func (c *testClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// attemptFixture is a Recorder wired to a capture writer, without the binding
// and lease machinery Begin needs: the attempt hooks only depend on the state
// model and the shared response offsets.
type attemptFixture struct {
	recorder   *Recorder
	clock      *testClock
	underlying *recordingWriter
	context    *gin.Context
	info       *relaycommon.RelayInfo
}

func newAttemptFixture(t *testing.T, maxCapture int) *attemptFixture {
	t.Helper()
	c := newTestContext(t)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	underlying := newRecordingWriter(t)
	clock := &testClock{now: time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)}
	recorder := &Recorder{
		captureState:  CaptureStateFull,
		relayFormat:   string(types.RelayFormatOpenAI),
		requestPath:   "/v1/chat/completions",
		selectedGroup: "default",
		clock:         func() time.Time { return clock.now },
	}
	recorder.origWriter = c.Writer
	recorder.writer = NewCaptureWriter(underlying, maxCapture)
	c.Writer = recorder.writer
	common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, recorder)

	return &attemptFixture{
		recorder:   recorder,
		clock:      clock,
		underlying: underlying,
		context:    c,
		info: &relaycommon.RelayInfo{
			OriginModelName: "gpt-4o",
			RelayFormat:     types.RelayFormatOpenAI,
			// The request started before the fake clock base, so any first
			// response time the test assigns satisfies HasSendResponse.
			StartTime: clock.now.Add(-time.Second),
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelId:         7,
				ChannelType:       1,
				UpstreamModelName: "gpt-4o-2024-11-20",
			},
		},
	}
}

func (f *attemptFixture) attempts() []*attemptValue {
	f.recorder.mu.Lock()
	defer f.recorder.mu.Unlock()
	return f.recorder.attempts
}

func TestAttemptHooksAreNoOpWithoutRecorder(t *testing.T) {
	c := newTestContext(t)
	info := &relaycommon.RelayInfo{}

	assert.NotPanics(t, func() {
		BeginAttempt(c, info)
		EndAttempt(c, info, nil)
		BeginAttempt(nil, info)
		EndAttempt(nil, info, nil)
		BeginAttempt(c, nil)
		EndAttempt(c, nil, nil)
	})
	assert.Nil(t, FromContext(c))
}

func TestAttemptRecordsOneGenerationPerBoundaryCrossing(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)
	common.SetContextKey(fixture.context, constant.ContextKeyChannelName, " azure-eu ")

	BeginAttempt(fixture.context, fixture.info)
	fixture.clock.advance(250 * time.Millisecond)
	_, err := fixture.context.Writer.Write([]byte("hello"))
	require.NoError(t, err)
	firstResponse := fixture.clock.now
	fixture.info.FirstResponseTime = firstResponse
	require.True(t, fixture.info.HasSendResponse())
	fixture.clock.advance(250 * time.Millisecond)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 1)
	attempt := attempts[0]

	assert.Equal(t, 0, attempt.Index)
	assert.Equal(t, 7, attempt.ChannelID)
	assert.Equal(t, 1, attempt.ChannelType)
	assert.Equal(t, "azure-eu", attempt.ChannelName, "the channel name is trimmed at the hook")
	assert.Equal(t, "default", attempt.SelectedGroup)
	assert.Equal(t, "gpt-4o", attempt.OriginModel)
	assert.Equal(t, "gpt-4o-2024-11-20", attempt.UpstreamModel)
	assert.Equal(t, AttemptEndHandlerReturned, attempt.EndReason)
	assert.Equal(t, 500*time.Millisecond, attempt.EndTime.Sub(attempt.StartTime))
	assert.True(t, attempt.WroteToClient)
	assert.False(t, attempt.OutputTruncated)
	assert.False(t, attempt.PartialOutput)
	assert.Equal(t, int64(0), attempt.StartCaptured)
	assert.Equal(t, int64(5), attempt.EndCaptured)
	assert.Nil(t, fixture.recorder.active)

	// The first response latch fired inside this attempt's window, so it is
	// attributed to it.
	assert.WithinDuration(t, firstResponse, attempt.FirstResponseTime, 0)
}

func TestAttemptDoesNotClaimAFirstResponseFromAnotherWindow(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)

	// The latch fired before this attempt started, which is what happens when a
	// partially streamed attempt already consumed it.
	fixture.info.FirstResponseTime = fixture.clock.now
	require.True(t, fixture.info.HasSendResponse())

	fixture.clock.advance(time.Second)
	BeginAttempt(fixture.context, fixture.info)
	_, err := fixture.context.Writer.Write([]byte("data"))
	require.NoError(t, err)
	fixture.clock.advance(time.Second)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 1)
	assert.True(t, attempts[0].FirstResponseTime.IsZero())
}

func TestAttemptSupersedesAnUnclosedPredecessor(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)

	BeginAttempt(fixture.context, fixture.info)
	_, err := fixture.context.Writer.Write([]byte("partial"))
	require.NoError(t, err)
	fixture.clock.advance(time.Second)

	// The handler enters the shared boundary again, e.g. the second relay of
	// chatCompletionsViaResponses.
	BeginAttempt(fixture.context, fixture.info)
	fixture.clock.advance(time.Second)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 2)

	first, second := attempts[0], attempts[1]
	assert.True(t, first.Superseded)
	assert.Equal(t, AttemptEndSuperseded, first.EndReason)
	assert.True(t, first.WroteToClient)
	assert.True(t, first.PartialOutput)
	assert.Equal(t, first.EndTime, second.StartTime, "the successor's entry time closes the predecessor")

	assert.False(t, second.Superseded)
	assert.Equal(t, AttemptEndHandlerReturned, second.EndReason)
	assert.Equal(t, 1, second.Index)
	assert.Same(t, second, fixture.recorder.attempts[1])
	assert.Nil(t, fixture.recorder.active)
}

func TestAttemptSlicesDoNotOverlapAcrossTheCaptureCeiling(t *testing.T) {
	fixture := newAttemptFixture(t, 250)

	BeginAttempt(fixture.context, fixture.info)
	_, err := fixture.context.Writer.Write(make([]byte, 100))
	require.NoError(t, err)
	EndAttempt(fixture.context, fixture.info, nil)

	BeginAttempt(fixture.context, fixture.info)
	_, err = fixture.context.Writer.Write(make([]byte, 200))
	require.NoError(t, err)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 2)
	first, second := attempts[0], attempts[1]

	assert.Equal(t, int64(0), first.StartCaptured)
	assert.Equal(t, int64(100), first.EndCaptured)
	assert.False(t, first.OutputTruncated)

	assert.Equal(t, int64(100), second.StartCaptured, "the successor starts where the predecessor stopped")
	assert.Equal(t, int64(250), second.EndCaptured)
	assert.True(t, second.WroteToClient)
	assert.True(t, second.OutputTruncated, "the logical delta exceeds the captured delta")
	assert.Equal(t, int64(300), second.EndLogical)

	// The client still received every byte.
	assert.Len(t, fixture.underlying.bytes(), 300)
}

func TestAttemptSnapshotsMaskedUpstreamFailure(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream exploded"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	BeginAttempt(fixture.context, fixture.info)
	_, err := fixture.context.Writer.Write([]byte("half"))
	require.NoError(t, err)
	EndAttempt(fixture.context, fixture.info, apiErr)

	attempts := fixture.attempts()
	require.Len(t, attempts, 1)
	attempt := attempts[0]

	assert.Equal(t, string(types.ErrorCodeBadResponseStatusCode), attempt.ErrCode)
	assert.Equal(t, http.StatusBadGateway, attempt.HTTPStatus)
	assert.Equal(t, apiErr.MaskSensitiveErrorWithStatusCode(), attempt.ErrMessage)
	assert.True(t, attempt.PartialOutput, "bytes already sent stay with the failed attempt")
}

func TestAttemptWithoutBoundaryIsNotClosed(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)

	EndAttempt(fixture.context, fixture.info, nil)
	assert.Empty(t, fixture.attempts())
	assert.Nil(t, fixture.recorder.active)
}

func TestAttemptUsesNilSafeChannelAccessors(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)
	fixture.info.ChannelMeta = nil

	BeginAttempt(fixture.context, fixture.info)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 1)
	assert.Zero(t, attempts[0].ChannelID)
	assert.Zero(t, attempts[0].ChannelType)
	assert.Empty(t, attempts[0].UpstreamModel)
}

func TestGenerationNameFallsBackThroughChannelIdentity(t *testing.T) {
	cases := []struct {
		name     string
		attempt  attemptValue
		expected string
	}{
		{
			name:     "channel name from context",
			attempt:  attemptValue{ChannelName: "azure-eu", ChannelID: 7, ChannelType: 1, UpstreamModel: "gpt-4o"},
			expected: "azure-eu gpt-4o",
		},
		{
			name:     "channel id fallback",
			attempt:  attemptValue{ChannelID: 7, ChannelType: 1, UpstreamModel: "gpt-4o"},
			expected: "channel-7 gpt-4o",
		},
		{
			name:     "channel type fallback",
			attempt:  attemptValue{ChannelType: 1, UpstreamModel: "gpt-4o"},
			expected: "channel-type-1 gpt-4o",
		},
		{
			name:     "unknown channel and model",
			attempt:  attemptValue{},
			expected: "channel-unknown model-unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, generationName(&tc.attempt))
		})
	}
}

func TestAttemptReclassifiesGeminiWithTheFinalUpstreamModel(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)
	fixture.recorder.relayFormat = types.RelayFormatGemini
	fixture.recorder.requestPath = "/v1beta/models/gemini-2.5-flash:generateContent"
	fixture.info.RelayFormat = types.RelayFormatGemini
	fixture.info.ChannelMeta.UpstreamModelName = "text-embedding-004"

	BeginAttempt(fixture.context, fixture.info)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 1)
	assert.Equal(t, relayconstant.GeminiActionEmbedding, attempts[0].GeminiFinal,
		"a generate path mapped onto an embedding model is embedding")
}

func TestAttemptLeavesGeminiClassificationUnsetForOtherFormats(t *testing.T) {
	fixture := newAttemptFixture(t, 1024)
	fixture.info.ChannelMeta.UpstreamModelName = "text-embedding-004"

	BeginAttempt(fixture.context, fixture.info)
	EndAttempt(fixture.context, fixture.info, nil)

	attempts := fixture.attempts()
	require.Len(t, attempts, 1)
	assert.Equal(t, relayconstant.GeminiActionUnknown, attempts[0].GeminiFinal,
		"a non Gemini inbound format is never reclassified")
}
