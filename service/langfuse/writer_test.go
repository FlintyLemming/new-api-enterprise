package langfuse

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingWriter is the underlying gin.ResponseWriter every capture writer test
// wraps. It records exactly what the client would have received and can hold the
// capture writer's critical section open on demand.
type recordingWriter struct {
	gin.ResponseWriter

	mu      sync.Mutex
	written []byte

	entered chan struct{}
	block   chan struct{}
}

func newRecordingWriter(t *testing.T) *recordingWriter {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return &recordingWriter{ResponseWriter: c.Writer}
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	if w.entered != nil {
		w.entered <- struct{}{}
	}
	if w.block != nil {
		<-w.block
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written = append(w.written, b...)
	return len(b), nil
}

func (w *recordingWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *recordingWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte{}, w.written...)
}

// deadlineWriter adds the SetWriteDeadline support relay/helper/stream_scanner.go
// reaches for through http.NewResponseController.
type deadlineWriter struct {
	*recordingWriter
	deadline atomic.Pointer[time.Time]
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline.Store(&deadline)
	return nil
}

// resetCaptureWarnWindows clears the process wide capture rate limiter so a
// test observes its own warnings no matter what ran before it.
func resetCaptureWarnWindows(t *testing.T) {
	t.Helper()
	for i := range captureWarnWindows {
		captureWarnWindows[i].Store(0)
	}
}

func setCaptureBudget(t *testing.T, limit int64) {
	t.Helper()
	previous := maxInFlightCaptureBytes.Load()
	SetMaxInFlightCaptureBytes(limit)
	t.Cleanup(func() {
		SetMaxInFlightCaptureBytes(previous)
		inFlightCaptureBytes.Store(0)
	})
}

func TestCaptureWriterPassesEverythingThroughWhileBoundingTheBuffer(t *testing.T) {
	underlying := newRecordingWriter(t)
	writer := NewCaptureWriter(underlying, 250)

	for i := 0; i < 3; i++ {
		n, err := writer.Write([]byte(strings.Repeat(string(rune('a'+i)), 100)))
		require.NoError(t, err)
		require.Equal(t, 100, n)
	}

	assert.Len(t, underlying.bytes(), 300, "the client must receive every byte")
	captured, logical := writer.Offsets()
	assert.Equal(t, int64(250), captured)
	assert.Equal(t, int64(300), logical)

	frozen := writer.Freeze()
	assert.Equal(t, underlying.bytes()[:250], frozen.Buf)
	assert.Equal(t, int64(300), frozen.TotalWritten)
	assert.True(t, frozen.Truncated)
}

func TestCaptureWriterDelegatesResponseWriterBehaviour(t *testing.T) {
	underlying := newRecordingWriter(t)
	writer := NewCaptureWriter(underlying, 1024)

	writer.WriteHeader(http.StatusAccepted)
	n, err := writer.WriteString("hello")
	require.NoError(t, err)
	assert.Equal(t, 5, n)

	assert.Equal(t, http.StatusAccepted, writer.Status())
	assert.Equal(t, []byte("hello"), underlying.bytes())
	assert.Equal(t, []byte("hello"), writer.Freeze().Buf)
	assert.Same(t, underlying, writer.Original())
}

func TestCaptureWriterUnwrapReachesTheWriteDeadline(t *testing.T) {
	t.Run("supported", func(t *testing.T) {
		underlying := &deadlineWriter{recordingWriter: newRecordingWriter(t)}
		writer := NewCaptureWriter(underlying, 1024)

		deadline := time.Now().Add(37 * time.Second)
		require.NoError(t, http.NewResponseController(writer).SetWriteDeadline(deadline))

		stored := underlying.deadline.Load()
		require.NotNil(t, stored)
		assert.True(t, deadline.Equal(*stored))
	})

	t.Run("unsupported", func(t *testing.T) {
		underlying := newRecordingWriter(t)
		writer := NewCaptureWriter(underlying, 1024)

		assert.ErrorIs(t, http.NewResponseController(writer).SetWriteDeadline(time.Now()), http.ErrNotSupported)

		_, err := writer.Write([]byte("still streaming"))
		require.NoError(t, err)
		assert.Equal(t, []byte("still streaming"), underlying.bytes())
	})
}

func TestCaptureWriterFreezeIsIdempotentAndStopsCapturing(t *testing.T) {
	resetCaptureWarnWindows(t)
	warnings := captureWarnings(t)
	underlying := newRecordingWriter(t)
	writer := NewCaptureWriter(underlying, 1024)

	_, err := writer.Write([]byte("before-freeze"))
	require.NoError(t, err)

	first := writer.Freeze()
	second := writer.Freeze()
	assert.Equal(t, first, second)

	_, err = writer.Write([]byte("after-freeze"))
	require.NoError(t, err)
	_, err = writer.Write([]byte("after-freeze-again"))
	require.NoError(t, err)

	assert.Equal(t, []byte("before-freezeafter-freezeafter-freeze-again"), underlying.bytes(),
		"late writes must still reach the client")
	assert.Equal(t, first, writer.Freeze(), "the frozen capture must not change")

	late := 0
	for _, warning := range warnings.all() {
		if strings.Contains(warning, "late_write_after_freeze") {
			late++
		}
		assert.NotContains(t, warning, "after-freeze", "warnings must never carry body bytes")
	}
	assert.Equal(t, 1, late, "the late write warning is rate limited")
}

func TestCaptureWriterSerializesConcurrentWriters(t *testing.T) {
	const rounds = 40
	prefixes := []string{"REQ-", "PING-", "DATA-"}

	underlying := newRecordingWriter(t)
	writer := NewCaptureWriter(underlying, 1<<20)

	var offsetsRegressed atomic.Bool
	sampling := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var lastCaptured, lastLogical int64
		for {
			select {
			case <-sampling:
				return
			default:
			}
			captured, logical := writer.Offsets()
			if captured < lastCaptured || logical < lastLogical {
				offsetsRegressed.Store(true)
			}
			lastCaptured, lastLogical = captured, logical
		}
	}()

	var writers sync.WaitGroup
	for _, prefix := range prefixes {
		writers.Add(1)
		go func(prefix string) {
			defer writers.Done()
			for i := 0; i < rounds; i++ {
				chunk := fmt.Sprintf("%s%03d;", prefix, i)
				n, err := writer.Write([]byte(chunk))
				assert.NoError(t, err)
				assert.Equal(t, len(chunk), n)
			}
		}(prefix)
	}
	writers.Wait()
	close(sampling)
	<-sampled

	frozen := writer.Freeze()
	assert.False(t, offsetsRegressed.Load(), "captured and logical offsets must be monotonic")
	assert.False(t, frozen.Truncated)
	assert.Equal(t, underlying.bytes(), frozen.Buf, "capture must be byte identical to what the client received")

	for _, prefix := range prefixes {
		for i := 0; i < rounds; i++ {
			chunk := fmt.Sprintf("%s%03d;", prefix, i)
			assert.Equal(t, 1, strings.Count(string(frozen.Buf), chunk), "chunk %s must appear exactly once", chunk)
		}
	}
}

func TestCaptureWriterFreezeWaitsForTheWriteInFlight(t *testing.T) {
	underlying := newRecordingWriter(t)
	underlying.entered = make(chan struct{}, 1)
	underlying.block = make(chan struct{})
	writer := NewCaptureWriter(underlying, 1024)

	written := make(chan struct{})
	go func() {
		defer close(written)
		_, err := writer.Write([]byte("LAST-WRITE"))
		assert.NoError(t, err)
	}()

	<-underlying.entered // the writer now holds the capture writer's lock

	frozen := make(chan FrozenCapture, 1)
	go func() { frozen <- writer.Freeze() }()

	close(underlying.block)
	<-written

	captured := <-frozen
	assert.Equal(t, []byte("LAST-WRITE"), captured.Buf, "Freeze must observe the write it raced with")
	assert.Equal(t, int64(len("LAST-WRITE")), captured.TotalWritten)
}

func TestCaptureWriterIsolatesBookkeepingPanics(t *testing.T) {
	resetCaptureWarnWindows(t)
	warnings := captureWarnings(t)
	underlying := newRecordingWriter(t)
	writer := NewCaptureWriter(underlying, 1024)

	panics := atomic.Int64{}
	captureFaultHook = func() {
		if panics.Add(1) == 1 {
			panic("injected capture failure")
		}
	}
	t.Cleanup(func() { captureFaultHook = nil })

	n, err := writer.Write([]byte("first"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)

	n, err = writer.Write([]byte("second"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)

	assert.Equal(t, []byte("firstsecond"), underlying.bytes(), "the client must not notice the capture failure")
	assert.Contains(t, warnings.joined(), "capture_append")
	assert.NotContains(t, warnings.joined(), "first")
}

func TestBudgetReservationsNeverExceedTheCeiling(t *testing.T) {
	setCaptureBudget(t, 1000)

	first := ReserveCapture(600)
	require.NotNil(t, first)
	assert.Nil(t, ReserveCapture(600), "a reservation past the ceiling must fail")

	second := ReserveCapture(300)
	require.NotNil(t, second)

	first.Release()
	first.Release()
	assert.Equal(t, int64(300), inFlightCaptureBytes.Load(), "release must be idempotent")

	second.Release()
	assert.Zero(t, inFlightCaptureBytes.Load())
}

func TestBudgetReservationsAreSafeUnderConcurrency(t *testing.T) {
	setCaptureBudget(t, 1000)

	var overshoot atomic.Bool
	var granted atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 200; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			reservation := ReserveCapture(10)
			if reservation == nil {
				return
			}
			granted.Add(1)
			if inFlightCaptureBytes.Load() > 1000 {
				overshoot.Store(true)
			}
			reservation.Release()
		}()
	}
	workers.Wait()

	assert.False(t, overshoot.Load(), "the in flight capture budget must never exceed its ceiling")
	assert.Positive(t, granted.Load())
	assert.Zero(t, inFlightCaptureBytes.Load())
}
