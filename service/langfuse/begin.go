package langfuse

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// beginFaultHook is the controlled fault injection point design §14.1 requires
// for Begin: a test makes the last setup step panic and asserts that the client
// response and the installed writer are left exactly as they were. It is nil in
// production.
var beginFaultHook func()

// headerValue reads one request header. It is a variable so the phase contract
// test of design §14.1 can assert that the cheap phase 0 guards never reach the
// request at all.
var headerValue = func(c *gin.Context, name string) string {
	if c.Request == nil {
		return ""
	}
	return c.Request.Header.Get(name)
}

// sessionScan carries the one body identity read a single Begin is allowed to
// perform (design §5.1). It survives the retirement retry so a second round can
// re-query new paths against the buffer instead of reading the body twice.
type sessionScan struct {
	body []byte
	// read records that a reader was actually opened. A candidate rejected by
	// the content type, the storage or the size guard never opened one, so a
	// second round with a larger limit may still perform that single read.
	read bool
}

// Begin decides whether the current request is traced and, when it is, takes
// the runtime lease, reserves the capture budget, freezes the request body and
// installs the capture writer. It returns nil for every request outside the
// sample, and never propagates a failure of its own into the relay.
func Begin(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) (recorder *Recorder) {
	if c == nil || info == nil || info.RequestId == "" {
		return nil
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			abandon(c, recorder, recovered)
			recorder = nil
		}
	}()

	var scan sessionScan
	binding := LoadBinding()
	session, sampled := evaluateBinding(c, info, binding, &scan)
	if !sampled {
		return nil
	}

	runtime := binding.Runtime
	if !runtime.TryAcquire() {
		// The single allowed reload of design §5.1: the runtime retired between
		// the guard and the lease. Every guard, the identity and the sampling
		// decision are redone against the new snapshot, so an old rate is never
		// paired with a new runtime.
		binding = LoadBinding()
		if session, sampled = evaluateBinding(c, info, binding, &scan); !sampled {
			return nil
		}
		runtime = binding.Runtime
		if !runtime.TryAcquire() {
			return nil
		}
	}

	snapshot := binding.Snapshot
	requestPath, _, _ := strings.Cut(info.RequestURLPath, "?")
	recorder = &Recorder{
		snap:          snapshot,
		runtime:       runtime,
		userId:        info.UserId,
		tokenId:       info.TokenId,
		username:      common.GetContextKeyString(c, constant.ContextKeyUserName),
		userGroup:     info.UserGroup,
		tokenName:     c.GetString("token_name"),
		selectedGroup: info.UsingGroup,
		requestId:     info.RequestId,
		originModel:   info.OriginModelName,
		relayFormat:   string(info.RelayFormat),
		requestPath:   requestPath,
		isStream:      info.IsStream,
		isPlayground:  info.IsPlayground,
		// The root duration shares its base with use_time_ms in the consume log
		// instead of measuring a second start here.
		rootStart:    info.StartTime,
		session:      session,
		captureState: CaptureStateFull,
		modelParams:  extractModelParams(request),
		clock:        time.Now,
	}
	common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, recorder)

	if !snapshot.SendContent {
		recorder.captureState = CaptureStateDisabled
		return recorder
	}
	reservation := ReserveCapture(int64(2*snapshot.MaxContentBytes + snapshot.MaxResponseBytes))
	if reservation == nil {
		recorder.captureState = CaptureStateBudget
		return recorder
	}
	recorder.budget = reservation

	recorder.freezeInput(c)
	recorder.origWriter = c.Writer
	recorder.writer = NewCaptureWriter(c.Writer, snapshot.MaxResponseBytes)
	c.Writer = recorder.writer

	if beginFaultHook != nil {
		beginFaultHook()
	}
	return recorder
}

// evaluateBinding runs the phase 0 guards and, only for a request that survives
// them, the phase 1 identity extraction and sampling decision for one published
// binding. Header and body are untouched while any guard rejects the request,
// which is what keeps a disabled or paused configuration free of privacy cost.
func evaluateBinding(c *gin.Context, info *relaycommon.RelayInfo, binding *RuntimeBinding, scan *sessionScan) (SessionIdentity, bool) {
	snapshot := binding.Snapshot
	if !snapshot.Enabled || binding.Runtime == nil {
		return SessionIdentity{}, false
	}
	if snapshot.SampleRate <= 0 {
		return SessionIdentity{}, false
	}
	if !formatSupported(info.RelayFormat, info.RelayMode) {
		return SessionIdentity{}, false
	}
	if info.RelayFormat == types.RelayFormatGemini &&
		relayconstant.ClassifyGeminiAction(info.RequestURLPath, info.OriginModelName) != relayconstant.GeminiActionGenerate {
		return SessionIdentity{}, false
	}

	session := extractSession(c, snapshot, scan)
	if session.RawSessionID != "" {
		if scoped, ok := scopeSession(info.UserId, session.RawSessionID); ok {
			session.ScopedID = scoped
		} else {
			// A legal client value without a positive user ID may not enter the
			// project wide Langfuse namespace (design §7.2).
			session.OmittedReason = SessionOmittedUserScopeUnavailable
		}
	}
	if !sampleHit(samplingKeyFor(session, info.RequestId), snapshot.SampleRate, sha256.Sum256) {
		return SessionIdentity{}, false
	}
	return session, true
}

// formatSupported is the closed §2.1 scope check. Gemini passes here because
// its real guard is the pure function action classifier the caller applies
// next; every other format and mode is out of scope for v1.
func formatSupported(format types.RelayFormat, mode int) bool {
	switch format {
	case types.RelayFormatOpenAI:
		return mode == relayconstant.RelayModeChatCompletions
	case types.RelayFormatClaude:
		return true
	case types.RelayFormatOpenAIResponses:
		return mode == relayconstant.RelayModeResponses
	case types.RelayFormatGemini:
		return true
	}
	return false
}

// extractSession applies the §7.2 source priority for one snapshot: headers
// first, and the configured body paths only when no header carried a raw
// non-empty candidate.
func extractSession(c *gin.Context, snapshot Snapshot, scan *sessionScan) SessionIdentity {
	session := extractHeaderSession(func(name string) string { return headerValue(c, name) }, snapshot)

	if session.Source != "" || len(snapshot.SessionBodyPaths) == 0 {
		return session
	}
	if scan.read && scan.body == nil {
		// The single allowed read already failed; telemetry does not retry it.
		return session
	}

	bodySession, body := extractBodySession(c, snapshot, scan.body)
	scan.body = body
	scan.read = scan.read || body != nil ||
		bodySession.BodyOmittedReason == SessionBodyOmittedReadFailed ||
		bodySession.BodyOmittedReason == SessionBodyOmittedReadIncomplete
	return bodySession
}

// freezeInput copies the bounded request body once, from the storage the relay
// already cached. Telemetry never creates that storage itself, so a request
// whose body was never cached simply reports the omission and keeps capturing
// the response.
func (r *Recorder) freezeInput(c *gin.Context) {
	cached, exists := c.Get(common.KeyBodyStorage)
	if !exists || cached == nil {
		r.inputOmittedReason = InputOmittedStorageUnavailable
		return
	}
	storage, ok := cached.(common.BodyStorage)
	if !ok {
		r.inputOmittedReason = InputOmittedStorageTypeMismatch
		return
	}
	reader, err := storage.NewReader()
	if err != nil {
		r.inputOmittedReason = InputOmittedReadFailed
		return
	}
	defer reader.Close()

	limit := int64(r.snap.MaxContentBytes)
	// Reading one byte past the limit is how an oversized body is detected
	// without keeping the excess.
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		r.inputOmittedReason = InputOmittedReadFailed
		return
	}
	if int64(len(body)) > limit {
		body = body[:limit]
		r.inputScanTruncated = true
	}
	if len(body) == 0 {
		return
	}
	frozen := string(body)
	r.input = &frozen
}

// abandon is the Begin recovery path of design §5.2. It restores the writer
// only when the current one is still the instance Begin installed, gives back
// the budget and the lease and reports the panic without any payload.
func abandon(c *gin.Context, recorder *Recorder, recovered any) {
	defer func() {
		// A failure inside the cleanup itself must not reach the relay either.
		_ = recover()
	}()

	if recorder != nil {
		recorder.restoreWriter(c)
		recorder.budget.Release()
		recorder.runtime.Release()
	}
	if c != nil {
		common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, (*Recorder)(nil))
	}
	warnCapture(fmt.Sprintf("begin_panic: %T", recovered))
}

// restoreWriter puts the original writer back when c.Writer is still the exact
// capture writer this Recorder installed. Comparing the concrete instance, not
// the dynamic type, is what keeps a later wrapper from being silently dropped.
func (r *Recorder) restoreWriter(c *gin.Context) bool {
	if r == nil || c == nil || r.writer == nil {
		return false
	}
	current, ok := c.Writer.(*CaptureWriter)
	if !ok || current != r.writer {
		return false
	}
	c.Writer = r.origWriter
	return true
}
