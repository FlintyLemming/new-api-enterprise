package langfuse

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingStorage is a common.BodyStorage that reports how often an independent
// reader was opened, which is the observable for the "at most one complete body
// identity read per Begin" contract of design §5.1.
type countingStorage struct {
	body    []byte
	size    int64
	readers atomic.Int64
}

func newCountingStorage(body []byte) *countingStorage {
	return &countingStorage{body: body, size: int64(len(body))}
}

func (s *countingStorage) Read([]byte) (int, error)       { return 0, io.EOF }
func (s *countingStorage) Seek(int64, int) (int64, error) { return 0, nil }
func (s *countingStorage) Close() error                   { return nil }
func (s *countingStorage) Bytes() ([]byte, error)         { return s.body, nil }
func (s *countingStorage) Size() int64                    { return s.size }
func (s *countingStorage) IsDisk() bool                   { return false }
func (s *countingStorage) NewReader() (io.ReadCloser, error) {
	s.readers.Add(1)
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

// beginFixture is one traced request under construction: the gin context, the
// relay info and the published binding a Begin call reads.
type beginFixture struct {
	context  *gin.Context
	info     *relaycommon.RelayInfo
	recorder *httptest.ResponseRecorder
	storage  *countingStorage
	runtime  *TelemetryRuntime
}

func newBeginFixture(t *testing.T, setting langfuse_setting.LangfuseSetting, body string) *beginFixture {
	t.Helper()
	keepBinding(t)

	snapshot, err := langfuse_setting.BuildSnapshot(setting, 7)
	require.NoError(t, err)
	var runtime *TelemetryRuntime
	if snapshot.Enabled {
		runtime = newRuntimeForTest(t, snapshot)
	}
	PublishBinding(RuntimeBinding{Snapshot: snapshot, Runtime: runtime})
	setCaptureBudget(t, int64(setting.MaxInFlightCaptureBytes))

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	storage := newCountingStorage([]byte(body))
	c.Set(common.KeyBodyStorage, common.BodyStorage(storage))

	return &beginFixture{
		context:  c,
		recorder: recorder,
		storage:  storage,
		runtime:  runtime,
		info: &relaycommon.RelayInfo{
			RequestId:       "req-1",
			RelayFormat:     types.RelayFormatOpenAI,
			RelayMode:       relayconstant.RelayModeChatCompletions,
			UserId:          42,
			UserGroup:       "default",
			UsingGroup:      "default",
			OriginModelName: "gpt-4o",
			RequestURLPath:  "/v1/chat/completions",
			StartTime:       time.Now(),
		},
	}
}

// alwaysSampled keeps a fixture deterministic without pinning the digest.
func alwaysSampled() langfuse_setting.LangfuseSetting {
	setting := enabledSetting()
	setting.SampleRate = 1
	setting.SendContent = true
	return setting
}

func TestBeginSkipsEveryCheapGuardWithoutTouchingIdentity(t *testing.T) {
	cases := []struct {
		name    string
		setting func(langfuse_setting.LangfuseSetting) langfuse_setting.LangfuseSetting
		mutate  func(*relaycommon.RelayInfo)
	}{
		{
			name: "disabled",
			setting: func(s langfuse_setting.LangfuseSetting) langfuse_setting.LangfuseSetting {
				s.Enabled = false
				return s
			},
		},
		{
			name: "sample rate zero",
			setting: func(s langfuse_setting.LangfuseSetting) langfuse_setting.LangfuseSetting {
				s.SampleRate = 0
				return s
			},
		},
		{
			name:   "unsupported relay mode",
			mutate: func(info *relaycommon.RelayInfo) { info.RelayMode = relayconstant.RelayModeEmbeddings },
		},
		{
			name: "unsupported relay format",
			mutate: func(info *relaycommon.RelayInfo) {
				info.RelayFormat = types.RelayFormatRerank
				info.RelayMode = relayconstant.RelayModeRerank
			},
		},
		{
			name: "gemini embedding is out of scope",
			mutate: func(info *relaycommon.RelayInfo) {
				info.RelayFormat = types.RelayFormatGemini
				info.RelayMode = relayconstant.RelayModeGemini
				info.RequestURLPath = "/v1beta/models/text-embedding-004:embedContent"
				info.OriginModelName = "text-embedding-004"
			},
		},
		{
			name: "gemini predict is out of scope",
			mutate: func(info *relaycommon.RelayInfo) {
				info.RelayFormat = types.RelayFormatGemini
				info.RelayMode = relayconstant.RelayModeGemini
				info.RequestURLPath = "/v1beta/models/imagen-3:predict"
				info.OriginModelName = "imagen-3"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := alwaysSampled()
			setting.SessionBodyPaths = []string{"metadata.session_id"}
			if tc.setting != nil {
				setting = tc.setting(setting)
			}
			fixture := newBeginFixture(t, setting, `{"metadata":{"session_id":"s-1"}}`)
			if tc.mutate != nil {
				tc.mutate(fixture.info)
			}
			fixture.context.Request.Header.Set(LangfuseSessionHeader, "s-header")

			headerReads := failOnHeaderRead(t)
			before := fixture.context.Writer

			assert.Nil(t, Begin(fixture.context, fixture.info, nil))
			assert.Zero(t, headerReads(), "phase 0 must not read any header")
			assert.Zero(t, fixture.storage.readers.Load(), "phase 0 must not read the body")
			assert.Same(t, before, fixture.context.Writer)
			assert.Nil(t, FromContext(fixture.context))
		})
	}
}

// failOnHeaderRead swaps the header reader for one that counts, so a guard that
// reaches the request before it is allowed to is visible.
func failOnHeaderRead(t *testing.T) func() int64 {
	t.Helper()
	previous := headerValue
	var reads atomic.Int64
	headerValue = func(c *gin.Context, name string) string {
		reads.Add(1)
		return previous(c, name)
	}
	t.Cleanup(func() { headerValue = previous })
	return reads.Load
}

func TestBeginReadsTheBodyIdentityOnceEvenWhenNotSampled(t *testing.T) {
	setting := alwaysSampled()
	// A rate this small never hits, but phase 1 still pays for the identity
	// read because a body path is configured and no header carried a candidate.
	setting.SampleRate = 1e-300
	setting.SessionBodyPaths = []string{"metadata.session_id"}
	fixture := newBeginFixture(t, setting, `{"metadata":{"session_id":"s-1"}}`)

	assert.Nil(t, Begin(fixture.context, fixture.info, nil))
	assert.Equal(t, int64(1), fixture.storage.readers.Load())
	assert.Nil(t, FromContext(fixture.context))
	assert.Zero(t, fixture.runtime.inFlightLeases(), "an unsampled request must not take a lease")
	assert.Zero(t, inFlightCaptureBytes.Load(), "an unsampled request must not reserve budget")
}

func TestBeginSkipsTheBodyWhenAHeaderLockedTheSession(t *testing.T) {
	setting := alwaysSampled()
	// Without content capture the identity read is the only thing that can open
	// a reader, so the counter isolates the body path decision.
	setting.SendContent = false
	setting.SessionBodyPaths = []string{"metadata.session_id"}
	fixture := newBeginFixture(t, setting, `{"metadata":{"session_id":"from-body"}}`)
	fixture.context.Request.Header.Set(LangfuseSessionHeader, "from-header")

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Zero(t, fixture.storage.readers.Load(), "a locked header must not trigger a body read")
	assert.Equal(t, "42:from-header", recorder.session.ScopedID)
	assert.Equal(t, strings.ToLower(LangfuseSessionHeader), recorder.session.Source)
}

func TestBeginOmitsSessionWithoutAPositiveUserScope(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{}`)
	fixture.info.UserId = 0
	fixture.context.Request.Header.Set(LangfuseSessionHeader, "s-1")

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Empty(t, recorder.session.ScopedID)
	assert.Equal(t, SessionOmittedUserScopeUnavailable, recorder.session.OmittedReason)
}

func TestBeginInstallsCaptureAndFreezesBoundedInput(t *testing.T) {
	setting := alwaysSampled()
	fixture := newBeginFixture(t, setting, strings.Repeat("a", setting.MaxContentBytes+1))
	original := fixture.context.Writer

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Same(t, recorder, FromContext(fixture.context))
	assert.Equal(t, CaptureStateFull, recorder.captureState)

	require.NotNil(t, recorder.input)
	assert.Len(t, *recorder.input, setting.MaxContentBytes)
	assert.True(t, recorder.inputScanTruncated)
	assert.Empty(t, recorder.inputOmittedReason)

	require.NotNil(t, recorder.writer)
	assert.Same(t, recorder.writer, fixture.context.Writer)
	assert.Same(t, original, recorder.origWriter)
	assert.Equal(t, 1, fixture.runtime.inFlightLeases())
	assert.Equal(t, int64(2*setting.MaxContentBytes+setting.MaxResponseBytes), inFlightCaptureBytes.Load())
}

func TestBeginReportsMissingBodyStorageWithoutLosingCapture(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{}`)
	fixture.context.Set(common.KeyBodyStorage, nil)

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Nil(t, recorder.input)
	assert.Equal(t, InputOmittedStorageUnavailable, recorder.inputOmittedReason)
	assert.Same(t, recorder.writer, fixture.context.Writer)
}

func TestBeginWithoutSendContentStaysPassthrough(t *testing.T) {
	setting := alwaysSampled()
	setting.SendContent = false
	setting.SessionBodyPaths = []string{"metadata.session_id"}
	fixture := newBeginFixture(t, setting, `{"metadata":{"session_id":"s-1"}}`)
	original := fixture.context.Writer

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Equal(t, CaptureStateDisabled, recorder.captureState)
	assert.Nil(t, recorder.input)
	assert.Nil(t, recorder.writer)
	assert.Same(t, original, fixture.context.Writer)
	assert.Zero(t, inFlightCaptureBytes.Load())
	// The identity read of phase 1 is independent of the content switch.
	assert.Equal(t, int64(1), fixture.storage.readers.Load())
	assert.Equal(t, "42:s-1", recorder.session.ScopedID)
}

func TestBeginDegradesToMetadataOnlyWhenTheBudgetIsExhausted(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{"model":"gpt-4o"}`)
	setCaptureBudget(t, 1)
	original := fixture.context.Writer

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Equal(t, CaptureStateBudget, recorder.captureState)
	assert.Nil(t, recorder.input)
	assert.Nil(t, recorder.writer)
	assert.Same(t, original, fixture.context.Writer)
	assert.Zero(t, inFlightCaptureBytes.Load())
}

// republishOnFirstHeaderRead swaps the active binding while Begin is between
// its first LoadBinding and its lease attempt, which is the only window the
// bounded retry of design §5.1 exists for.
func republishOnFirstHeaderRead(t *testing.T, replacement RuntimeBinding) {
	t.Helper()
	previous := headerValue
	var swapped atomic.Bool
	headerValue = func(c *gin.Context, name string) string {
		if swapped.CompareAndSwap(false, true) {
			PublishBinding(replacement)
		}
		return previous(c, name)
	}
	t.Cleanup(func() { headerValue = previous })
}

func TestBeginRetryRejectsTheRequestWhenTheNewSnapshotPausedSampling(t *testing.T) {
	setting := alwaysSampled()
	setting.SendContent = false
	setting.SessionBodyPaths = []string{"metadata.session_id"}
	fixture := newBeginFixture(t, setting, `{"metadata":{"session_id":"s-1"}}`)

	paused := setting
	paused.SampleRate = 0
	pausedSnapshot, err := langfuse_setting.BuildSnapshot(paused, 8)
	require.NoError(t, err)
	republishOnFirstHeaderRead(t, RuntimeBinding{Snapshot: pausedSnapshot, Runtime: newRuntimeForTest(t, pausedSnapshot)})

	// The runtime phase 0 read retires before the lease is taken, so the reload
	// happens and the paused rate of the new snapshot decides the request.
	fixture.runtime.Retire()

	assert.Nil(t, Begin(fixture.context, fixture.info, nil))
	assert.Equal(t, int64(1), fixture.storage.readers.Load(),
		"a single Begin performs at most one complete body identity read")
}

func TestBeginRetryReusesTheBodyBufferAndRequeriesTheNewPaths(t *testing.T) {
	setting := alwaysSampled()
	setting.SendContent = false
	setting.SessionBodyPaths = []string{"metadata.session_id"}
	fixture := newBeginFixture(t, setting, `{"metadata":{"session_id":"s-1"}}`)

	replacement := setting
	replacement.SessionBodyPaths = []string{"metadata.other_id"}
	replacementSnapshot, err := langfuse_setting.BuildSnapshot(replacement, 9)
	require.NoError(t, err)
	republishOnFirstHeaderRead(t, RuntimeBinding{Snapshot: replacementSnapshot, Runtime: newRuntimeForTest(t, replacementSnapshot)})

	fixture.runtime.Retire()

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Equal(t, int64(1), fixture.storage.readers.Load())
	// The new snapshot's path finds nothing in the reused buffer, so the old
	// round's session conclusion is dropped instead of being carried over.
	assert.Empty(t, recorder.session.ScopedID)
	assert.Equal(t, SessionBodyOmittedNoSupportedScalar, recorder.session.BodyOmittedReason)
	assert.Equal(t, replacementSnapshot.Version, recorder.snap.Version)
}

func TestBeginContainsItsOwnPanicAndReleasesEverything(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{"model":"gpt-4o"}`)
	original := fixture.context.Writer

	beginFaultHook = func() { panic("injected begin fault") }
	t.Cleanup(func() { beginFaultHook = nil })

	assert.NotPanics(t, func() {
		assert.Nil(t, Begin(fixture.context, fixture.info, nil))
	})
	assert.Same(t, original, fixture.context.Writer, "the installed capture writer must be removed again")
	assert.Nil(t, FromContext(fixture.context))
	assert.Zero(t, inFlightCaptureBytes.Load())
	assert.Zero(t, fixture.runtime.inFlightLeases())
}

func TestBeginDefendsAgainstMissingRequestIdentity(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{}`)
	assert.Nil(t, Begin(nil, fixture.info, nil))
	assert.Nil(t, Begin(fixture.context, nil, nil))

	fixture.info.RequestId = ""
	assert.Nil(t, Begin(fixture.context, fixture.info, nil))
}

func TestBeginRecordsExplicitModelParameters(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{}`)
	zero := 0.0
	stream := false
	maxTokens := uint(0)
	request := &dto.GeneralOpenAIRequest{
		Model:           "gpt-4o",
		Temperature:     &zero,
		MaxTokens:       &maxTokens,
		Stream:          &stream,
		ReasoningEffort: "low",
		Tools:           []dto.ToolCallRequest{{Type: "function"}},
	}

	recorder := Begin(fixture.context, fixture.info, request)
	require.NotNil(t, recorder)
	assert.Equal(t, map[string]any{
		"temperature":      0.0,
		"max_tokens":       uint(0),
		"stream":           false,
		"reasoning_effort": "low",
		"tool_count":       1,
	}, recorder.modelParams)
	assert.NotContains(t, recorder.modelParams, "top_p", "an absent parameter must stay absent")
}

func TestBeginKeepsGeminiGenerateInScope(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{}`)
	fixture.info.RelayFormat = types.RelayFormatGemini
	fixture.info.RelayMode = relayconstant.RelayModeGemini
	fixture.info.RequestURLPath = "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	fixture.info.OriginModelName = "gemini-2.5-flash"

	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)
	assert.Equal(t, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", recorder.requestPath,
		"metadata must not carry the query string, which can hold an API key")
}

func TestBeginStoresRecorderUnderTheTypedContextKey(t *testing.T) {
	fixture := newBeginFixture(t, alwaysSampled(), `{}`)
	recorder := Begin(fixture.context, fixture.info, nil)
	require.NotNil(t, recorder)

	stored, ok := common.GetContextKey(fixture.context, constant.ContextKeyLangfuseRecorder)
	require.True(t, ok)
	assert.Same(t, recorder, stored)
}
