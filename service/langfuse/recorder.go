package langfuse

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

// Closed capture_state enum from design §8.1. Exactly one of these is always
// exported, and the degradation priority is panic > admission > budget >
// disabled > full.
const (
	CaptureStateFull      = "full"
	CaptureStateBudget    = "budget"
	CaptureStateAdmission = "admission"
	CaptureStateDisabled  = "disabled"
	CaptureStatePanic     = "panic"
)

// Closed attempt_end_reason enum from design §8.1. Every created attempt gets
// exactly one of these.
const (
	AttemptEndHandlerReturned = "handler_returned"
	AttemptEndSuperseded      = "replaced_by_next_upstream_call"
	AttemptEndFinalizerClean  = "finalizer_cleanup"
	AttemptEndLifecyclePanic  = "lifecycle_panic"
)

// Closed input_omitted_reason enum from design §8.1. An empty body writes no
// reason at all, and an oversized one is reported with input_scan_truncated.
const (
	InputOmittedStorageUnavailable  = "body_storage_unavailable"
	InputOmittedStorageTypeMismatch = "body_storage_type_mismatch"
	InputOmittedReadFailed          = "body_read_failed"
)

// UsageKind separates the two settlement paths that produce usage. Text keeps
// completion audio tokens inside output, audio exports them as their own
// bucket (design §8.4).
type UsageKind string

const (
	UsageKindText  UsageKind = "text"
	UsageKindAudio UsageKind = "audio"
)

// UsageRecord is the normalized settlement result one attempt received. It is a
// pure value: the settlement call site resolves every semantic flag, so neither
// Finish nor the worker ever reads billing state back off the RelayInfo.
type UsageRecord struct {
	Kind      UsageKind
	Available bool
	ModelName string
	// InputExcludesCache reports whether InputTokens already has the cached
	// tokens folded out, which decides how the mutually exclusive buckets are
	// built (design §8.4).
	InputExcludesCache   bool
	UsageSemanticUnknown bool

	InputTokens           int
	OutputTokens          int
	InputCachedTokens     int
	InputCacheWriteTokens int
	InputImageTokens      int
	InputAudioTokens      int
	OutputAudioTokens     int
	OutputReasoningTokens int

	Quota         int
	QuotaPerUnit  float64
	BillingSource string
	Settled       bool
	// SettlementFailed separates the two ways Settled can be false: a
	// SettleBilling error, which is reported as settlement_error, and a text
	// request that simply had nothing billable (design §8.2).
	SettlementFailed bool
}

// attemptValue is the immutable-by-convention snapshot of one upstream call.
// Everything the worker needs is copied in at BeginAttempt/EndAttempt, so
// materialization never touches the gin.Context or the RelayInfo (design §5.2).
type attemptValue struct {
	Index       int
	ChannelID   int
	ChannelType int

	ChannelName         string
	SelectedGroup       string
	OriginModel         string
	UpstreamModel       string
	UpstreamRelayFormat string
	UpstreamRequestID   string

	StartTime         time.Time
	EndTime           time.Time
	FirstResponseTime time.Time

	// Captured offsets slice the shared response buffer; logical offsets count
	// what the underlying writer actually emitted, including bytes past the
	// capture ceiling.
	StartCaptured int64
	StartLogical  int64
	EndCaptured   int64
	EndLogical    int64

	ErrCode    string
	ErrMessage string
	HTTPStatus int

	UpstreamCallStarted bool
	WroteToClient       bool
	PartialOutput       bool
	OutputTruncated     bool

	EndReason   string
	GeminiFinal relayconstant.GeminiAction
	Superseded  bool

	Usage *UsageRecord
}

// settlementSummary is the trace level quota pair root metadata may copy. It is
// only filled when exactly one attempt settled (design §8.1).
type settlementSummary struct {
	Quota         int
	BillingSource string
}

// Recorder holds everything one traced request owns: the runtime lease, the
// capture budget, the frozen input and the attempt value objects. The request
// path never creates an OTel span; spans are materialized in Finish or in the
// async worker with explicit historical timestamps (design §5).
//
// mu is the request level state mutex of design §5.2. It guards the attempt
// list, the active pointer and the frozen flag; the capture writer serializes
// its own buffer and offsets behind the same discipline, and offsets are only
// ever read through its Offsets/Freeze methods while this lock is held, which
// keeps the lock order Recorder -> writer.
type Recorder struct {
	mu sync.Mutex

	snap    Snapshot
	runtime *TelemetryRuntime
	budget  *BudgetReservation

	writer     *CaptureWriter
	origWriter gin.ResponseWriter

	// input is frozen once in Begin and shared by root and every generation, so
	// a retry never copies the request body again.
	input              *string
	inputOmittedReason string
	inputScanTruncated bool

	attempts []*attemptValue
	active   *attemptValue
	frozen   bool
	finished bool
	// lifecyclePanic records that a business panic unwound through the
	// finalizer, which marks the root failed and the active attempt closed
	// without a handler return.
	lifecyclePanic bool

	userId  int
	tokenId int

	username      string
	userGroup     string
	tokenName     string
	selectedGroup string
	requestId     string
	originModel   string
	relayFormat   string
	requestPath   string

	isStream     bool
	isPlayground bool

	rootStart time.Time
	rootEnd   time.Time

	session      SessionIdentity
	captureState string
	// usageUnattributed records that a settlement arrived without an attempt to
	// own it, which is the documented degradation of the bypass paths.
	usageUnattributed bool

	modelParams map[string]any

	// clock is the single time source of the lifecycle hooks so tests can pin
	// span durations without sleeping.
	clock func() time.Time
}

func (r *Recorder) now() time.Time {
	if r == nil || r.clock == nil {
		return time.Now()
	}
	return r.clock()
}

// RecordUsage attributes one settlement result to the attempt that produced it.
// The settlement functions call it right next to SettleBilling, so the quota and
// the conversion rate the record carries are the exact values this request was
// charged with, and no worker ever reads billing state back off a global.
//
// A record that arrives without an active attempt is dropped rather than moved
// onto the root: the AWS SDK and Xunfei v1 bypasses never open an attempt, and
// their usage may not be attributed to a trace that did not make that call
// (design §8.4).
func (r *Recorder) RecordUsage(rec UsageRecord) {
	if r == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture(fmt.Sprintf("record_usage_panic: %T", recovered))
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	if r.active == nil || !r.active.UpstreamCallStarted {
		r.usageUnattributed = true
		return
	}
	r.active.Usage = &rec
}

// FromContext returns the Langfuse Recorder of the current request. A missing
// key, a mismatched type and a typed nil all yield nil, so every settlement
// function and attempt hook can call it without its own assertion.
func FromContext(c *gin.Context) *Recorder {
	if c == nil {
		return nil
	}
	value, ok := common.GetContextKey(c, constant.ContextKeyLangfuseRecorder)
	if !ok || value == nil {
		return nil
	}
	recorder, ok := value.(*Recorder)
	if !ok || recorder == nil {
		return nil
	}
	return recorder
}
