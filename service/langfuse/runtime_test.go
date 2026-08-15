package langfuse

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initialBinding is captured during package variable initialization, so the
// startup contract can be asserted no matter which test publishes first.
var initialBinding = LoadBinding()

func enabledSetting() langfuse_setting.LangfuseSetting {
	s := langfuse_setting.DefaultLangfuseSetting
	s.Enabled = true
	s.Host = "https://x.example/langfuse/"
	s.PublicKey = "pk-lf-1"
	s.SecretKey = "sk-lf-1"
	return s
}

// keepBinding restores whatever binding was active before the test, so package
// level state does not leak between tests.
func keepBinding(t *testing.T) {
	t.Helper()
	previous := LoadBinding()
	t.Cleanup(func() { PublishBinding(*previous) })
}

func TestInitialBindingIsDisabledWithoutRuntime(t *testing.T) {
	require.NotNil(t, initialBinding)
	assert.False(t, initialBinding.Snapshot.Enabled)
	assert.Nil(t, initialBinding.Runtime)
	assert.Equal(t, uint64(0), initialBinding.Snapshot.Version)
	assert.Equal(t, "", initialBinding.Snapshot.TracesURL)
}

func TestPublishSnapshotPublishesValidatedSnapshotWithMonotonicVersion(t *testing.T) {
	keepBinding(t)
	before := CurrentVersion()

	require.NoError(t, PublishSnapshot(enabledSetting()))

	binding := LoadBinding()
	require.NotNil(t, binding)
	assert.True(t, binding.Snapshot.Enabled)
	assert.Equal(t, "https://x.example/langfuse/api/public/otel/v1/traces", binding.Snapshot.TracesURL)
	assert.Equal(t, before+1, binding.Snapshot.Version)
	assert.Equal(t, binding.Snapshot.Version, CurrentVersion())

	require.NoError(t, PublishSnapshot(enabledSetting()))
	assert.Equal(t, before+2, CurrentVersion())
}

func TestPublishSnapshotRejectsInvalidSettingAndKeepsBinding(t *testing.T) {
	keepBinding(t)
	require.NoError(t, PublishSnapshot(enabledSetting()))
	published := LoadBinding()

	invalid := enabledSetting()
	invalid.SecretKey = ""
	assert.Error(t, PublishSnapshot(invalid))

	assert.Same(t, published, LoadBinding())
	// A rejected candidate must not consume a version either.
	assert.Equal(t, published.Snapshot.Version, CurrentVersion())
}

func TestBindingLoadAndPublishAreConcurrencySafe(t *testing.T) {
	keepBinding(t)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s := enabledSetting()
			s.SampleRate = float64(i%11) / 10
			assert.NoError(t, PublishSnapshot(s))
		}(i)
		go func() {
			defer wg.Done()
			binding := LoadBinding()
			require.NotNil(t, binding)
			// Read through the binding to catch torn snapshot/runtime pairs.
			_ = binding.Snapshot.TracesURL
			_ = binding.Snapshot.SessionHeaderNames
			_ = binding.Runtime
		}()
	}
	wg.Wait()

	final := LoadBinding()
	assert.True(t, final.Snapshot.Enabled)
	assert.Equal(t, final.Snapshot.Version, CurrentVersion())
}

func TestRuntimeLeaseStopsAtRetirement(t *testing.T) {
	r := newTelemetryRuntime(Snapshot{})
	require.True(t, r.TryAcquire())

	r.Retire()
	assert.False(t, r.TryAcquire(), "a retired runtime must not hand out new leases")

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	assert.False(t, r.WaitIdle(cancelled), "runtime with an outstanding lease is not idle")

	r.Release()
	assert.True(t, r.WaitIdle(t.Context()))
}

func TestRuntimeRetirementNeverGrantsLateLease(t *testing.T) {
	r := newTelemetryRuntime(Snapshot{})

	var retireReturned, lateLease atomic.Bool
	var acquired atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Sampling the flag before the attempt keeps the assertion free of
			// false positives: a lease taken before Retire returned is legal.
			retiredBeforeAttempt := retireReturned.Load()
			if !r.TryAcquire() {
				return
			}
			if retiredBeforeAttempt {
				lateLease.Store(true)
			}
			acquired.Add(1)
			r.Release()
		}()
	}

	close(start)
	r.Retire()
	retireReturned.Store(true)
	wg.Wait()

	assert.False(t, lateLease.Load(), "retirement observed zero in-flight but a later lease still succeeded")
	assert.True(t, r.WaitIdle(t.Context()))
	r.mu.Lock()
	inFlight := r.inFlight
	r.mu.Unlock()
	assert.Zero(t, inFlight, "every granted lease must be accounted for by its Release")
	t.Logf("leases granted before retirement: %d", acquired.Load())
}

func TestRuntimeUnbalancedReleaseKeepsAccountingSane(t *testing.T) {
	r := newTelemetryRuntime(Snapshot{})
	require.True(t, r.TryAcquire())

	r.Release()
	// A duplicated release must neither underflow the counter nor close the
	// idle channel twice; both would surface as a panic or a premature idle.
	r.Release()

	r.Retire()
	assert.True(t, r.WaitIdle(t.Context()))
	r.Release()
	assert.True(t, r.WaitIdle(t.Context()))
}

// blockingCollector accepts OTLP requests but holds each one until release is
// closed, which is how the tests fill the bounded BatchSpanProcessor queue
// without relying on timing.
func blockingCollector(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		server.Close()
	})
	return server, unblock
}

func queuedSnapshot(t *testing.T, host string, queueSize, batchSize int) Snapshot {
	t.Helper()
	setting := enabledSetting()
	setting.Host = host
	setting.QueueSize = queueSize
	setting.BatchSize = batchSize
	snapshot, err := langfuse_setting.BuildSnapshot(setting, 11)
	require.NoError(t, err)
	return snapshot
}

func newRuntimeForTest(t *testing.T, snapshot Snapshot) *TelemetryRuntime {
	t.Helper()
	runtime, err := BuildCandidateRuntime(snapshot)
	require.NoError(t, err)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runtime.Retire()
		runtime.shutdownAndReport(shutdownCtx)
	})
	return runtime
}

func TestBuildCandidateRuntimeAssemblesProviderWithoutNetwork(t *testing.T) {
	// Nothing listens on this port: publishing a configuration must never
	// depend on the ingestion endpoint being reachable (design §10.2).
	snapshot := snapshotForHost(t, "http://127.0.0.1:1")
	runtime := newRuntimeForTest(t, snapshot)

	require.NotNil(t, runtime.provider)
	require.NotNil(t, runtime.tracer)
	require.NotNil(t, runtime.exporter)
	assert.Equal(t, snapshot.Version, runtime.exporter.version)
	assert.Equal(t, snapshot.TracesURL, runtime.Snapshot.TracesURL)
}

func TestPublishSnapshotSwapsRuntimeAndRetiresPrevious(t *testing.T) {
	keepBinding(t)

	first := enabledSetting()
	first.Host = "http://127.0.0.1:1"
	require.NoError(t, PublishSnapshot(first))
	previous := LoadBinding().Runtime
	require.NotNil(t, previous)
	assert.EqualValues(t, first.MaxInFlightCaptureBytes, maxInFlightCaptureBytes.Load(),
		"the capture budget ceiling switches together with the binding")

	second := enabledSetting()
	second.Host = "http://127.0.0.1:2"
	require.NoError(t, PublishSnapshot(second))

	current := LoadBinding().Runtime
	require.NotNil(t, current)
	assert.NotSame(t, previous, current)
	assert.False(t, previous.TryAcquire(), "the replaced runtime must stop accepting work")
	require.True(t, current.TryAcquire())
	current.Release()
}

func TestPublishDisabledSnapshotDropsRuntime(t *testing.T) {
	keepBinding(t)

	enabled := enabledSetting()
	enabled.Host = "http://127.0.0.1:1"
	require.NoError(t, PublishSnapshot(enabled))
	previous := LoadBinding().Runtime
	require.NotNil(t, previous)

	disabled := enabledSetting()
	disabled.Enabled = false
	require.NoError(t, PublishSnapshot(disabled))

	binding := LoadBinding()
	assert.False(t, binding.Snapshot.Enabled)
	assert.Nil(t, binding.Runtime, "a disabled binding must not expose a runtime")
	assert.False(t, previous.TryAcquire())
}

func TestShutdownAllSkipsRuntimeWithOutstandingLease(t *testing.T) {
	keepBinding(t)
	warnings := captureWarnings(t)

	setting := enabledSetting()
	setting.Host = "http://127.0.0.1:1"
	require.NoError(t, PublishSnapshot(setting))
	runtime := LoadBinding().Runtime
	require.NotNil(t, runtime)
	require.True(t, runtime.TryAcquire())
	t.Cleanup(runtime.Release)

	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	ShutdownAll(expired)

	assert.False(t, LoadBinding().Snapshot.Enabled, "process shutdown must stop new telemetry first")
	assert.False(t, runtime.shutdownAttempted.Load(),
		"a provider a worker may still use must not be shut down")
	assert.Contains(t, warnings.joined(), fmt.Sprintf("version %d", runtime.Snapshot.Version))
}

func TestQueueDropLowerBoundGrowsAndWarnsUnderPressure(t *testing.T) {
	server, unblock := blockingCollector(t)
	runtime := newRuntimeForTest(t, queuedSnapshot(t, server.URL, 16, 1))
	warnings := captureWarnings(t)

	// The collector is blocked, so the bounded queue fills and the standard
	// BatchSpanProcessor silently drops the rest.
	for i := 0; i < 200; i++ {
		_, span := runtime.tracer.Start(t.Context(), "materialized")
		span.End()
		runtime.recordMaterialized(1)
	}

	assert.Positive(t, runtime.queueDroppedLowerBound(), "dropped spans must remain diagnosable")
	assert.Contains(t, warnings.joined(), "queue_dropped")
	unblock()
}

func TestRetirementSummaryReportsExactDropAfterSuccessfulDrain(t *testing.T) {
	server, unblock := blockingCollector(t)
	runtime := newRuntimeForTest(t, queuedSnapshot(t, server.URL, 16, 1))

	for i := 0; i < 200; i++ {
		_, span := runtime.tracer.Start(t.Context(), "materialized")
		span.End()
		runtime.recordMaterialized(1)
	}
	require.Positive(t, runtime.queueDroppedLowerBound())

	warnings := captureWarnings(t)
	unblock()
	runtime.Retire()
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.True(t, runtime.shutdownAndReport(shutdownCtx))

	exact := runtime.materialized.Load() - runtime.exporter.received.Load()
	summary := warnings.joined()
	assert.Contains(t, summary, "drain complete")
	assert.Contains(t, summary, fmt.Sprintf("queue_dropped=%d", exact))
}

func TestRetirementSummaryStaysIncompleteWhenDrainTimesOut(t *testing.T) {
	server, _ := blockingCollector(t)
	runtime := newRuntimeForTest(t, queuedSnapshot(t, server.URL, 16, 1))

	for i := 0; i < 200; i++ {
		_, span := runtime.tracer.Start(t.Context(), "materialized")
		span.End()
		runtime.recordMaterialized(1)
	}

	warnings := captureWarnings(t)
	runtime.Retire()
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	assert.False(t, runtime.shutdownAndReport(shutdownCtx))

	summary := warnings.joined()
	assert.Contains(t, summary, "drain incomplete")
	assert.Contains(t, summary, fmt.Sprintf("queue_dropped>=%d", runtime.queueDroppedLowerBound()),
		"an unfinished drain may only report the lower bound")
}

func TestExportFailuresAreNotCountedAsQueueDrops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	runtime := newRuntimeForTest(t, queuedSnapshot(t, server.URL, 16, 1))
	for i := 0; i < 5; i++ {
		_, span := runtime.tracer.Start(t.Context(), "failing")
		span.End()
		runtime.recordMaterialized(1)
	}
	require.NoError(t, runtime.provider.ForceFlush(t.Context()))

	assert.Positive(t, runtime.exporter.failed.Load())
	assert.Zero(t, runtime.queueDroppedLowerBound(), "export failures are not queue drops")
}
