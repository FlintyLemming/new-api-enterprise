package langfuse

import (
	"hash/fnv"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// CaptureWriter forwards every byte to the client and keeps a bounded copy for
// telemetry. One buffer is shared by the whole request: attempts only record
// offsets into it, so a retry never duplicates response bytes (design §9.1).
//
// The write path is deliberately minimal. Inside the lock it forwards, appends
// and updates offsets; it never parses, redacts or touches the gin.Context, and
// a failure in its own bookkeeping can neither panic into the handler nor hide
// the underlying writer's result.
type CaptureWriter struct {
	// The embedded writer is the original one Begin replaced, so Unwrap and
	// Original hand back the same object and every gin.ResponseWriter behaviour
	// (Flush, Hijack, CloseNotify, Status, Size, Pusher) stays delegated.
	gin.ResponseWriter

	mu           sync.Mutex
	buf          []byte
	totalWritten int64
	truncated    bool
	frozen       bool
	maxCapture   int
}

// FrozenCapture is the immutable view of a request's captured response. The
// buffer is handed over at Freeze; the writer stops appending to it, so the
// worker can read it without copying half a megabyte per request.
type FrozenCapture struct {
	Buf          []byte
	TotalWritten int64
	Truncated    bool
}

// captureFaultHook is the controlled fault injection point design §14.1
// requires: a test makes the capture bookkeeping panic and asserts the client
// still receives a byte identical response. It is nil in production.
var captureFaultHook func()

func NewCaptureWriter(orig gin.ResponseWriter, maxCapture int) *CaptureWriter {
	return &CaptureWriter{ResponseWriter: orig, maxCapture: maxCapture}
}

func (w *CaptureWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.ResponseWriter.Write(b)
	w.captureLocked(b, n, err)
	return n, err
}

func (w *CaptureWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *CaptureWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}

func (w *CaptureWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.Flush()
}

// Unwrap exposes the underlying writer to http.NewResponseController.
// gin.ResponseWriter does not declare Unwrap, so embedding alone would make
// relay/helper/stream_scanner.go lose the connection and silently give up on
// the streaming write deadline (design §13).
func (w *CaptureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Original returns the writer Begin replaced. Finish restores it only when
// c.Writer is still this capture writer.
func (w *CaptureWriter) Original() gin.ResponseWriter { return w.ResponseWriter }

// Offsets reports the captured and the logical byte counts. An attempt records
// them before and after the upstream call to claim its slice of the shared
// buffer.
func (w *CaptureWriter) Offsets() (captured, logical int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return int64(len(w.buf)), w.totalWritten
}

// Freeze stops capturing and hands the buffer to the caller. It is idempotent
// and, because it takes the same lock as Write, it cannot observe a partially
// applied write.
func (w *CaptureWriter) Freeze() FrozenCapture {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frozen = true
	// Capping the slice makes the handover safe without a copy: no append can
	// reach these bytes again once frozen is set.
	return FrozenCapture{Buf: w.buf[:len(w.buf):len(w.buf)], TotalWritten: w.totalWritten, Truncated: w.truncated}
}

// captureLocked updates the capture state for a write that already reached the
// client. Any failure here is contained: the caller still returns the
// underlying writer's own (n, err).
func (w *CaptureWriter) captureLocked(b []byte, n int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture("capture_append")
		}
	}()
	if captureFaultHook != nil {
		captureFaultHook()
	}
	if err != nil {
		return
	}
	if w.frozen {
		// The response outlived Finish. The bytes still belong to the client;
		// telemetry just stops at the frozen boundary.
		warnCapture("late_write_after_freeze")
		return
	}

	w.totalWritten += int64(n)
	remaining := w.maxCapture - len(w.buf)
	if remaining <= 0 {
		w.truncated = w.truncated || n > 0
		return
	}
	if n > len(b) {
		n = len(b)
	}
	if n > remaining {
		w.buf = append(w.buf, b[:remaining]...)
		w.truncated = true
		return
	}
	w.buf = append(w.buf, b[:n]...)
}

// captureWarnWindows rate limits capture anomalies. A fixed bucket array keeps
// the limiter lock free and bounded no matter how many distinct stages report;
// a hash collision only suppresses one warning, which is exactly what a rate
// limiter is allowed to do.
const captureWarnBuckets = 64

var captureWarnWindows [captureWarnBuckets]atomic.Int64

// warnCapture reports a capture side anomaly at most once per window per stage.
// Only stage identifiers are ever passed in: a warning must never carry request
// or response bytes (design §9.2).
func warnCapture(stage string) {
	digest := fnv.New32a()
	_, _ = digest.Write([]byte(stage))
	window := &captureWarnWindows[digest.Sum32()%captureWarnBuckets]

	now := time.Now()
	last := window.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < alertInterval {
		return
	}
	if !window.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	emitWarning("langfuse capture anomaly: " + stage)
}

// inFlightCaptureBytes is the process wide capture budget of design §9.1. The
// reservation is taken once per request before any content is copied, so the
// Write hot path never competes for a global counter.
var inFlightCaptureBytes atomic.Int64

// BudgetReservation owns a slice of the global capture budget from Begin until
// the worker (or a degraded path) finishes with the frozen buffers.
type BudgetReservation struct {
	bytes   int64
	release sync.Once
}

// ReserveCapture takes n bytes of the global budget with a single CAS, or
// returns nil when the ceiling would be exceeded. A nil result means the
// request degrades to metadata-only; it must not retry inside the same request.
func ReserveCapture(n int64) *BudgetReservation {
	if n <= 0 {
		return nil
	}
	limit := maxInFlightCaptureBytes.Load()
	for {
		current := inFlightCaptureBytes.Load()
		if current > limit || limit-current < n {
			return nil
		}
		if inFlightCaptureBytes.CompareAndSwap(current, current+n) {
			return &BudgetReservation{bytes: n}
		}
	}
}

// Release gives the reservation back. It is idempotent so the capture and
// runtime ownership paths can both defend themselves without double counting.
func (b *BudgetReservation) Release() {
	if b == nil {
		return
	}
	b.release.Do(func() { inFlightCaptureBytes.Add(-b.bytes) })
}
