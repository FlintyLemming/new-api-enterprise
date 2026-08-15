package langfuse

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// BeginAttempt records that the request is about to cross the shared upstream
// HTTP boundary. It is called from exactly one place — right before
// relayClient.Do in the private doRequest — so one real upstream call always
// produces exactly one generation (design §5.2). It is a no-op without a
// Recorder, which is what makes the task, realtime and channel test paths free.
func BeginAttempt(c *gin.Context, info *relaycommon.RelayInfo) {
	recorder := FromContext(c)
	if recorder == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture(fmt.Sprintf("begin_attempt_panic: %T", recovered))
		}
	}()

	// The start time is taken at the hook entry so the generation duration is
	// the real upstream call, not whatever the worker measures later.
	start := recorder.now()
	channelName := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyChannelName))

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.frozen {
		return
	}
	captured, logical := recorder.offsetsLocked()

	if previous := recorder.active; previous != nil && previous.UpstreamCallStarted {
		// The handler entered the shared boundary again without the outer
		// EndAttempt. The previous attempt is closed at this moment with the
		// current offsets and marked superseded; it never receives usage.
		previous.EndTime = start
		previous.EndCaptured, previous.EndLogical = captured, logical
		previous.EndReason = AttemptEndSuperseded
		previous.Superseded = true
		closeAttempt(previous)
		previous.PartialOutput = previous.WroteToClient
		recorder.active = nil
	}

	attempt := &attemptValue{
		Index:               len(recorder.attempts),
		ChannelID:           info.GetChannelID(),
		ChannelType:         info.GetChannelType(),
		ChannelName:         channelName,
		SelectedGroup:       recorder.selectedGroup,
		OriginModel:         info.GetOriginModelName(),
		StartTime:           start,
		StartCaptured:       captured,
		StartLogical:        logical,
		UpstreamCallStarted: true,
	}
	recorder.attempts = append(recorder.attempts, attempt)
	recorder.active = attempt
}

// EndAttempt closes the attempt the handler just returned from. Only an attempt
// that actually reached the outbound boundary is closed here; a handler failure
// before that boundary produces no generation at all (design §5.2).
func EndAttempt(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) {
	recorder := FromContext(c)
	if recorder == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture(fmt.Sprintf("end_attempt_panic: %T", recovered))
		}
	}()
	closeActiveAttempt(recorder, c, info, apiErr, AttemptEndHandlerReturned)
}

// closeActiveAttempt takes the immutable end snapshot of the active attempt.
// The end reason distinguishes the normal handler return from the defensive
// close a lifecycle panic performs; everything else is identical, because a
// panicked attempt is just as real as a returned one.
func closeActiveAttempt(recorder *Recorder, c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError, reason string) {
	// The end time is taken before any attribute work or lock contention.
	end := recorder.now()

	upstreamModel := info.GetUpstreamModelName()
	upstreamFormat := string(info.GetFinalRequestRelayFormat())
	upstreamRequestID := c.GetString(common.UpstreamRequestIdKey)
	var geminiFinal relayconstant.GeminiAction
	if recorder.relayFormat == types.RelayFormatGemini {
		// Model mapping and the adaptor's suffix stripping are done by now, so
		// this is the final outbound model the classification must use.
		geminiFinal = relayconstant.ClassifyGeminiAction(recorder.requestPath, upstreamModel)
	}
	var firstResponse time.Time
	if info != nil && info.HasSendResponse() {
		firstResponse = info.FirstResponseTime
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	attempt := recorder.active
	if attempt == nil || !attempt.UpstreamCallStarted {
		return
	}

	attempt.EndTime = end
	attempt.EndCaptured, attempt.EndLogical = recorder.offsetsLocked()
	attempt.UpstreamModel = upstreamModel
	attempt.UpstreamRelayFormat = upstreamFormat
	attempt.UpstreamRequestID = upstreamRequestID
	attempt.GeminiFinal = geminiFinal
	attempt.EndReason = reason
	if apiErr != nil {
		attempt.Failed = true
		attempt.ErrCode = string(apiErr.GetErrorCode())
		attempt.ErrMessage = apiErr.MaskSensitiveErrorWithStatusCode()
		attempt.HTTPStatus = apiErr.StatusCode
	}
	closeAttempt(attempt)
	attempt.PartialOutput = apiErr != nil && attempt.WroteToClient

	// The first response time belongs to this attempt only when this attempt
	// wrote and the latch fired inside its own window.
	if attempt.WroteToClient && !firstResponse.IsZero() &&
		!firstResponse.Before(attempt.StartTime) && !firstResponse.After(attempt.EndTime) {
		attempt.FirstResponseTime = firstResponse
	}
	recorder.active = nil
}

// closeAttempt derives the byte level facts of a closed attempt. Whether the
// client saw data is decided by the logical delta, because the captured delta
// stops growing at the capture ceiling while the response keeps streaming.
func closeAttempt(attempt *attemptValue) {
	logicalDelta := attempt.EndLogical - attempt.StartLogical
	capturedDelta := attempt.EndCaptured - attempt.StartCaptured
	attempt.WroteToClient = logicalDelta > 0
	attempt.OutputTruncated = logicalDelta > capturedDelta
}

// offsetsLocked reads the shared response offsets. A metadata-only Recorder has
// no writer and reports zeros, which keeps every attempt slice empty instead of
// special casing the caller.
func (r *Recorder) offsetsLocked() (captured, logical int64) {
	if r.writer == nil {
		return 0, 0
	}
	return r.writer.Offsets()
}

// generationName is the stable §6.2 span name. The channel name is only known
// from the request context, so a missing one falls back through the channel ID
// and the channel type before giving up.
func generationName(attempt *attemptValue) string {
	name := attempt.ChannelName
	switch {
	case name != "":
	case attempt.ChannelID > 0:
		name = fmt.Sprintf("channel-%d", attempt.ChannelID)
	case attempt.ChannelType > 0:
		name = fmt.Sprintf("channel-type-%d", attempt.ChannelType)
	default:
		name = "channel-unknown"
	}
	model := attempt.UpstreamModel
	if model == "" {
		model = "model-unknown"
	}
	return name + " " + model
}
