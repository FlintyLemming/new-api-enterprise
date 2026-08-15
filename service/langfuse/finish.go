package langfuse

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// admissionInFlight counts the materialization workers currently admitted. The
// generic goroutine pool provides no admission control of its own, so design
// §5.3 requires this dedicated bound: past it a request degrades to a
// synchronous metadata-only trace instead of queueing unbounded payloads.
var admissionInFlight atomic.Int64

// maxAdmittedWorkers caps the semaphore no matter how generous the configured
// capture budget is.
const maxAdmittedWorkers = 4096

// submitWorker hands one materialization job to the relay goroutine pool. It is
// a variable so a test can run the job synchronously and assert the exported
// spans without racing a pool.
var submitWorker = func(job func()) { common.RelayCtxGo(context.Background(), job) }

// contentFaultHook is the controlled fault injection point design §14.1
// requires for the sanitizer and aggregator stage. It is nil in production.
var contentFaultHook func()

// recorderMaterial is the immutable worker input. It carries values only: no
// gin.Context, no RelayInfo and no capture writer, so a worker can never read
// request state that has already moved on.
type recorderMaterial struct {
	Snapshot Snapshot
	Runtime  *TelemetryRuntime
	Budget   *BudgetReservation

	RequestId     string
	Username      string
	UserGroup     string
	TokenName     string
	SelectedGroup string
	OriginModel   string
	RelayFormat   string
	RequestPath   string
	UserId        int
	TokenId       int

	IsStream     bool
	IsPlayground bool

	RootStart time.Time
	RootEnd   time.Time

	Session            SessionIdentity
	CaptureState       string
	InputOmittedReason string
	InputScanTruncated bool
	LifecyclePanic     bool

	ModelParams map[string]any
	Settlement  *settlementSummary

	Attempts []attemptValue
	Input    *string
	Capture  FrozenCapture

	Failed         bool
	FailureMessage string
}

// materializedContent is the sanitized payload a worker produced. A nil value
// means the trace is metadata-only.
type materializedContent struct {
	Input          []byte
	RootOutput     []byte
	AttemptOutputs map[int][]byte
	Redacted       bool
}

// Finish is the single, idempotent end of a traced request. It snapshots the
// root end time before anything else, restores the writer, freezes the capture
// and either materializes a metadata-only trace synchronously or hands the
// bounded values to a worker. The RelayInfo is not read for telemetry: every
// value the trace needs was snapshotted at Begin or at the attempt hooks.
func Finish(c *gin.Context, info *relaycommon.RelayInfo, finalErr *types.NewAPIError) {
	recorder := FromContext(c)
	if recorder == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture(fmt.Sprintf("finish_panic: %T", recovered))
			recorder.releaseOwnership()
		}
	}()

	// The real business end time, taken before any writer, freeze or admission
	// work can delay it.
	end := recorder.now()

	recorder.mu.Lock()
	if recorder.finished {
		recorder.mu.Unlock()
		return
	}
	recorder.finished = true
	recorder.rootEnd = end
	recorder.mu.Unlock()

	if recorder.writer != nil && !recorder.restoreWriter(c) {
		// A later wrapper replaced the writer. Overwriting it would silently
		// swallow that wrapper, so the violation is reported instead.
		warnCapture("writer_replaced_before_finish")
	}

	material := recorder.freezeMaterial(finalErr)

	// A Gemini request whose final classification left the supported action is
	// discarded whole: no span, and both resources handed straight back.
	if material.RelayFormat == types.RelayFormatGemini {
		for i := range material.Attempts {
			if material.Attempts[i].GeminiFinal != relayconstant.GeminiActionGenerate {
				recorder.releaseOwnership()
				return
			}
		}
	}

	if material.Input == nil && len(material.Capture.Buf) == 0 {
		// Nothing was ever captured, so the bounded metadata is cheap enough
		// for the request goroutine.
		materialize(material, nil)
		recorder.releaseOwnership()
		return
	}

	if !acquireAdmission(material.Snapshot) {
		material.CaptureState = CaptureStateAdmission
		material.Input = nil
		material.Capture = FrozenCapture{}
		materialize(material, nil)
		recorder.releaseOwnership()
		return
	}

	// Ownership of the budget and the lease moves to the worker together with
	// the payload; Finish must not release either from here on.
	submitWorker(func() {
		defer releaseAdmission()
		defer material.Budget.Release()
		defer material.Runtime.Release()
		runWorker(material)
	})
}

// MarkLifecyclePanic closes the active attempt while a business panic unwinds
// the stack. It is the panic counterpart of EndAttempt: the attempt is real and
// must be reported, but its end reason records that no handler returned.
func MarkLifecyclePanic(c *gin.Context, info *relaycommon.RelayInfo) {
	recorder := FromContext(c)
	if recorder == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture(fmt.Sprintf("lifecycle_panic_mark_panic: %T", recovered))
		}
	}()

	recorder.mu.Lock()
	recorder.lifecyclePanic = true
	recorder.mu.Unlock()
	closeActiveAttempt(recorder, c, info, nil, AttemptEndLifecyclePanic)
}

// freezeMaterial stops all capture state changes and copies everything the
// worker may see. Attempts are copied by value, so a late write to the
// Recorder can no longer reach a submitted job.
func (r *Recorder) freezeMaterial(finalErr *types.NewAPIError) recorderMaterial {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true

	if lingering := r.active; lingering != nil && lingering.UpstreamCallStarted && lingering.EndReason == "" {
		// Only an abnormal exit leaves an attempt open here; the retry loop
		// closes every attempt it started.
		lingering.EndTime = r.rootEnd
		lingering.EndCaptured, lingering.EndLogical = r.offsetsLocked()
		lingering.EndReason = AttemptEndFinalizerClean
		closeAttempt(lingering)
		lingering.PartialOutput = lingering.WroteToClient
	}
	r.active = nil

	material := recorderMaterial{
		Snapshot:           r.snap,
		Runtime:            r.runtime,
		Budget:             r.budget,
		RequestId:          r.requestId,
		Username:           r.username,
		UserGroup:          r.userGroup,
		TokenName:          r.tokenName,
		SelectedGroup:      r.selectedGroup,
		OriginModel:        r.originModel,
		RelayFormat:        r.relayFormat,
		RequestPath:        r.requestPath,
		UserId:             r.userId,
		TokenId:            r.tokenId,
		IsStream:           r.isStream,
		IsPlayground:       r.isPlayground,
		RootStart:          r.rootStart,
		RootEnd:            r.rootEnd,
		Session:            r.session,
		CaptureState:       r.captureState,
		InputOmittedReason: r.inputOmittedReason,
		InputScanTruncated: r.inputScanTruncated,
		LifecyclePanic:     r.lifecyclePanic,
		ModelParams:        r.modelParams,
		Settlement:         r.settlement,
		Input:              r.input,
	}
	if r.writer != nil {
		material.Capture = r.writer.Freeze()
	}

	material.Attempts = make([]attemptValue, 0, len(r.attempts))
	for _, attempt := range r.attempts {
		material.Attempts = append(material.Attempts, *attempt)
	}

	material.Failed = r.lifecyclePanic || finalErr != nil || allAttemptsFailed(material.Attempts)
	if finalErr != nil {
		material.FailureMessage = finalErr.MaskSensitiveErrorWithStatusCode()
	} else if r.lifecyclePanic {
		material.FailureMessage = "relay panicked before the response was finished"
	}
	return material
}

// releaseOwnership gives the capture budget and the runtime lease back. Both
// releases are idempotent, so the synchronous and the worker path can each
// defend themselves without double counting.
func (r *Recorder) releaseOwnership() {
	if r == nil {
		return
	}
	r.budget.Release()
	r.runtime.Release()
}

func allAttemptsFailed(attempts []attemptValue) bool {
	if len(attempts) == 0 {
		return false
	}
	for i := range attempts {
		if !attemptFailed(&attempts[i]) {
			return false
		}
	}
	return true
}

func attemptFailed(attempt *attemptValue) bool {
	return attempt.Superseded || attempt.ErrCode != "" || attempt.EndReason == AttemptEndLifecyclePanic
}

// acquireAdmission takes one worker slot. The capacity follows the same
// reservation arithmetic the capture budget uses, so a deployment can never
// admit more concurrent payloads than its configured budget allows.
func acquireAdmission(snapshot Snapshot) bool {
	limit := int64(maxAdmittedWorkers)
	if reservation := int64(2*snapshot.MaxContentBytes + snapshot.MaxResponseBytes); reservation > 0 {
		if capacity := int64(snapshot.MaxInFlightCaptureBytes) / reservation; capacity < limit {
			limit = capacity
		}
	}
	if limit < 1 {
		limit = 1
	}
	for {
		current := admissionInFlight.Load()
		if current >= limit {
			return false
		}
		if admissionInFlight.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func releaseAdmission() {
	if admissionInFlight.Add(-1) < 0 {
		admissionInFlight.Store(0)
	}
}

// runWorker does the expensive part off the request goroutine: sanitizing the
// frozen input, aggregating each attempt's response slice and creating the
// spans with the historical timestamps the value objects carry.
func runWorker(material recorderMaterial) {
	content := prepareContent(&material)
	defer func() {
		if recovered := recover(); recovered != nil {
			// Spans may already exist; creating a second set would duplicate the
			// observation, so the job is dropped instead.
			warnCapture(fmt.Sprintf("materialize_panic: %T", recovered))
		}
	}()
	materialize(material, content)
}

// prepareContent sanitizes and aggregates the captured payload. Its own panic
// boundary degrades the trace to metadata-only rather than losing it, because
// the diagnostic value of the root does not depend on the body.
func prepareContent(material *recorderMaterial) (content *materializedContent) {
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture(fmt.Sprintf("content_panic: %T", recovered))
			material.CaptureState = CaptureStatePanic
			material.Input = nil
			material.Capture = FrozenCapture{}
			content = nil
		}
	}()
	if contentFaultHook != nil {
		contentFaultHook()
	}

	limit := material.Snapshot.MaxContentBytes
	prepared := &materializedContent{AttemptOutputs: map[int][]byte{}}

	if material.Input != nil {
		prepared.Input = sanitizeContent([]byte(*material.Input), limit)
		// The raw request body is released before the outputs are built, so the
		// worker never holds two full copies at once.
		material.Input = nil
	}

	kind := outputKind(material.RelayFormat)
	prepared.RootOutput = aggregateSlice(kind, material.IsStream, material.Capture.Buf, limit)
	for i := range material.Attempts {
		attempt := &material.Attempts[i]
		slice := captureSlice(material.Capture.Buf, attempt.StartCaptured, attempt.EndCaptured)
		if len(slice) == 0 {
			continue
		}
		prepared.AttemptOutputs[attempt.Index] = aggregateSlice(kind, material.IsStream, slice, limit)
	}
	prepared.Redacted = contentRedacted(prepared)
	return prepared
}

// captureSlice returns one attempt's bytes out of the shared response buffer.
// Offsets are clamped because a degraded Recorder may carry zeroed offsets.
func captureSlice(buf []byte, start, end int64) []byte {
	if start < 0 || end <= start || start >= int64(len(buf)) {
		return nil
	}
	if end > int64(len(buf)) {
		end = int64(len(buf))
	}
	return buf[start:end]
}

// aggregateSlice folds one captured response into the compact structure of
// design §9.3 and then applies the same size and redaction rules the input
// uses.
func aggregateSlice(kind string, isStream bool, body []byte, limit int) []byte {
	if len(body) == 0 {
		return nil
	}
	aggregated, ok := AggregateOutput(kind, isStream, body)
	if !ok {
		return sanitizeContent(body, limit)
	}
	encoded, err := common.Marshal(aggregated)
	if err != nil {
		return sanitizeContent(body, limit)
	}
	return sanitizeContent(encoded, limit)
}

// sanitizeContent produces the exported representation of one payload: a
// redacted, reduced JSON document when the bytes parse, a UTF-8 safe truncation
// otherwise, and never a bisected JSON prefix.
func sanitizeContent(raw []byte, limit int) []byte {
	if sanitized, ok := SanitizeJSON(raw, limit); ok {
		return sanitized
	}
	return SanitizeRawText(raw, limit)
}

// outputKind maps the inbound relay format onto the aggregation §9.3 defines.
// An unmapped format keeps its raw captured body.
func outputKind(relayFormat string) string {
	switch relayFormat {
	case string(types.RelayFormatOpenAI):
		return OutputKindOpenAI
	case types.RelayFormatClaude:
		return OutputKindClaude
	case types.RelayFormatOpenAIResponses:
		return OutputKindResponses
	case types.RelayFormatGemini:
		return OutputKindGemini
	}
	return ""
}

// materialize creates the whole observation tree with explicit historical
// timestamps. The root is ended first so it reaches the batch queue before any
// generation: under queue pressure the trace identity and the full input/output
// must survive, not the last attempt.
func materialize(material recorderMaterial, content *materializedContent) {
	if material.Runtime == nil || material.Runtime.tracer == nil {
		return
	}

	ctx := contextWithTraceID(context.Background(), DeriveTraceID(material.RequestId))
	rootCtx, root := material.Runtime.tracer.Start(ctx,
		material.RelayFormat+" "+material.OriginModel, trace.WithTimestamp(material.RootStart))
	root.SetAttributes(buildRootAttributes(&material, content)...)
	if material.Failed {
		root.SetStatus(codes.Error, material.FailureMessage)
	}

	generations := make([]trace.Span, 0, len(material.Attempts))
	for i := range material.Attempts {
		attempt := &material.Attempts[i]
		_, generation := material.Runtime.tracer.Start(rootCtx,
			generationName(attempt), trace.WithTimestamp(attempt.StartTime))
		generation.SetAttributes(buildGenerationAttributes(attempt, &material, content)...)
		if attemptFailed(attempt) {
			generation.SetStatus(codes.Error, attemptStatusMessage(attempt))
		}
		generations = append(generations, generation)
	}

	root.End(trace.WithTimestamp(material.RootEnd))
	material.Runtime.recordMaterialized(1)
	for i, generation := range generations {
		generation.End(trace.WithTimestamp(material.Attempts[i].EndTime))
		material.Runtime.recordMaterialized(1)
	}
}

// attemptStatusMessage is the stable, payload free status of a failed
// generation. A superseded attempt gets its own wording so it never pretends
// the provider returned an error.
func attemptStatusMessage(attempt *attemptValue) string {
	switch {
	case attempt.Superseded:
		return "attempt superseded by the next upstream call"
	case attempt.EndReason == AttemptEndLifecyclePanic:
		return "attempt ended by a relay lifecycle panic"
	case attempt.ErrMessage != "":
		return attempt.ErrMessage
	default:
		return "attempt failed"
	}
}
