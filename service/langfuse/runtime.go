// Package langfuse is the Langfuse tracing data plane. It only reads an atomic
// binding published by the control plane, so the relay hot path never touches
// the database, the mutable GlobalConfig objects or any reconcile logic.
package langfuse

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	// retirementWarnInterval is how long a retiring runtime may keep in-flight
	// leases before every further wait logs one warning. Design §10.2 forbids
	// shutting the provider down while a worker can still touch it, so the wait
	// continues after the warning.
	retirementWarnInterval = 5 * time.Minute

	// retirementShutdownBudget bounds the drain of a runtime that a
	// configuration switch replaced. It is deliberately larger than the 10s
	// single request timeout so one in-flight export can still finish.
	retirementShutdownBudget = 15 * time.Second

	// langfuseSpanAttributeCountLimit is the locked span attribute budget from
	// design §11: enough for the closed attribute schema plus headroom.
	langfuseSpanAttributeCountLimit = 64

	tracerName = "new-api/langfuse"
)

// maxInFlightCaptureBytes is the global capture budget ceiling. It is published
// together with the binding so the budget and the runtime can never disagree
// about which configuration is active.
var maxInFlightCaptureBytes atomic.Int64

// SetMaxInFlightCaptureBytes updates the ceiling the capture reservations are
// checked against.
func SetMaxInFlightCaptureBytes(limit int64) {
	maxInFlightCaptureBytes.Store(limit)
}

// Snapshot is the immutable configuration the data plane reads. The alias keeps
// every data plane caller on a single type without importing the setting
// package at each call site.
type Snapshot = langfuse_setting.Snapshot

// TelemetryRuntime owns the OTel TracerProvider and the lease accounting that
// keeps a retiring runtime alive until every Recorder that already acquired it
// has finished. A lease is held from a successful Begin until the worker (or
// the synchronous metadata-only materialization) returns, so Finish alone never
// releases it.
type TelemetryRuntime struct {
	Snapshot Snapshot

	tracer   trace.Tracer
	provider *sdktrace.TracerProvider
	exporter *countingExporter

	// Diagnostics from design §11. materialized counts every span whose End
	// returned; together with the exporter counters it bounds how many spans
	// the BatchSpanProcessor silently dropped.
	materialized      atomic.Int64
	droppedLowerBound atomic.Int64
	lastQueueWarn     atomic.Int64

	shutdownAttempted atomic.Bool
	drainComplete     atomic.Bool

	// mu guards the whole retirement state machine. Design §10.2 describes the
	// lease as "increment, then re-check retiring, give it back if retired";
	// taking the retiring flag and the counter under one lock is the same
	// invariant without the window, and retirement therefore never observes a
	// zero count that a later lease can revive.
	mu         sync.Mutex
	retiring   bool
	inFlight   int
	idle       chan struct{}
	idleClosed bool
}

func newTelemetryRuntime(snapshot Snapshot) *TelemetryRuntime {
	return &TelemetryRuntime{Snapshot: snapshot, idle: make(chan struct{})}
}

// TryAcquire takes a lease unless the runtime is retiring. A false return means
// the caller must fall back to the currently published binding instead of using
// this runtime.
func (r *TelemetryRuntime) TryAcquire() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retiring {
		return false
	}
	r.inFlight++
	return true
}

// Release returns a lease. It tolerates an unbalanced call so a defensive
// double release in a worker cleanup path cannot underflow the counter or
// signal idle twice.
func (r *TelemetryRuntime) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == 0 {
		return
	}
	r.inFlight--
	if r.inFlight == 0 && r.retiring {
		r.signalIdleLocked()
	}
}

// Retire stops new leases. Existing ones keep the runtime and its provider
// alive; callers wait with WaitIdle before shutting the provider down.
func (r *TelemetryRuntime) Retire() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retiring = true
	if r.inFlight == 0 {
		r.signalIdleLocked()
	}
}

// WaitIdle blocks until every lease is returned, reporting false when ctx ends
// first. A runtime that stays busy past retirementWarnInterval is reported but
// still waited on, because a forced shutdown would race live workers.
func (r *TelemetryRuntime) WaitIdle(ctx context.Context) bool {
	if r == nil {
		return true
	}
	warn := time.NewTimer(retirementWarnInterval)
	defer warn.Stop()
	for {
		select {
		case <-r.idle:
			return true
		case <-ctx.Done():
			return false
		case <-warn.C:
			r.mu.Lock()
			inFlight := r.inFlight
			r.mu.Unlock()
			common.SysError(fmt.Sprintf("langfuse runtime version %d still has %d in-flight lease(s) after %s of retirement",
				r.Snapshot.Version, inFlight, retirementWarnInterval))
			warn.Reset(retirementWarnInterval)
		}
	}
}

func (r *TelemetryRuntime) signalIdleLocked() {
	if r.idleClosed {
		return
	}
	r.idleClosed = true
	close(r.idle)
}

func (r *TelemetryRuntime) inFlightLeases() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight
}

// recordMaterialized is called once per span whose End returned. It also keeps
// the queue drop estimate current, because the SDK BatchSpanProcessor drops
// spans without any callback.
func (r *TelemetryRuntime) recordMaterialized(count int64) {
	if r == nil || count <= 0 {
		return
	}
	materialized := r.materialized.Add(count)
	if r.exporter == nil {
		return
	}

	// Spans that were materialized but never handed to the exporter are either
	// still queued or already dropped; only the amount above one full queue
	// plus one batch is certainly dropped.
	unexported := materialized - r.exporter.received.Load()
	queueSize := int64(r.Snapshot.QueueSize)
	lowerBound := unexported - queueSize - int64(r.Snapshot.BatchSize)
	if lowerBound < 0 {
		lowerBound = 0
	}
	grew := false
	for {
		previous := r.droppedLowerBound.Load()
		if lowerBound <= previous {
			break
		}
		if r.droppedLowerBound.CompareAndSwap(previous, lowerBound) {
			grew = true
			break
		}
	}
	if grew || unexported*4 > queueSize*3 {
		r.warnQueuePressure()
	}
}

// queueDroppedSpansLowerBound reports the monotonic lower bound of spans the
// BatchSpanProcessor dropped for this runtime.
func (r *TelemetryRuntime) queueDroppedLowerBound() int64 {
	return r.droppedLowerBound.Load()
}

func (r *TelemetryRuntime) warnQueuePressure() {
	last := r.lastQueueWarn.Load()
	now := time.Now()
	if last != 0 && now.Sub(time.Unix(0, last)) < alertInterval {
		return
	}
	if !r.lastQueueWarn.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	emitWarning(fmt.Sprintf("langfuse runtime version %d under export pressure: materialized=%d exporter_received=%d exported=%d failed=%d queue_dropped>=%d queue_size=%d batch_size=%d sample_rate=%v",
		r.Snapshot.Version, r.materialized.Load(), r.exporter.received.Load(), r.exporter.exported.Load(),
		r.exporter.failed.Load(), r.queueDroppedLowerBound(), r.Snapshot.QueueSize, r.Snapshot.BatchSize, r.Snapshot.SampleRate))
}

// shutdownAndReport shuts the provider down once and reports the retirement
// summary. It reports whether the drain finished inside ctx; only then is
// materialized minus exporter_received the exact number of dropped spans.
func (r *TelemetryRuntime) shutdownAndReport(ctx context.Context) bool {
	if r == nil {
		return true
	}
	if !r.shutdownAttempted.CompareAndSwap(false, true) {
		return r.drainComplete.Load()
	}

	complete := true
	if r.provider != nil {
		if err := r.provider.Shutdown(ctx); err != nil {
			complete = false
		}
	}
	r.drainComplete.Store(complete)
	manager.forget(r)

	var received, exported, failed int64
	if r.exporter != nil {
		received, exported, failed = r.exporter.received.Load(), r.exporter.exported.Load(), r.exporter.failed.Load()
	}
	materialized := r.materialized.Load()
	if complete {
		// Nothing is in flight after a successful drain, so the difference is
		// exactly what the queue dropped.
		dropped := materialized - received
		if dropped < 0 {
			dropped = 0
		}
		emitWarning(fmt.Sprintf("langfuse runtime version %d retired, drain complete: materialized=%d exporter_received=%d exported=%d failed=%d queue_dropped=%d",
			r.Snapshot.Version, materialized, received, exported, failed, dropped))
		return true
	}
	emitWarning(fmt.Sprintf("langfuse runtime version %d retired, drain incomplete: materialized=%d exporter_received=%d exported=%d failed=%d queue_dropped>=%d",
		r.Snapshot.Version, materialized, received, exported, failed, r.queueDroppedLowerBound()))
	return false
}

// RuntimeBinding pairs a validated snapshot with the runtime built from it.
// Configuration and runtime must be published through this single struct: two
// independent atomic pointers would let a request pair a new configuration with
// a retired exporter during a switch.
type RuntimeBinding struct {
	Snapshot Snapshot
	Runtime  *TelemetryRuntime
}

type runtimeManager struct {
	binding        atomic.Pointer[RuntimeBinding]
	versionCounter atomic.Uint64

	// live tracks every published runtime that has not finished its shutdown,
	// so process exit can retire runtimes that a configuration switch already
	// replaced but whose workers are still draining.
	mu   sync.Mutex
	live map[*TelemetryRuntime]struct{}
}

var manager = newRuntimeManager()

func newRuntimeManager() *runtimeManager {
	m := &runtimeManager{live: map[*TelemetryRuntime]struct{}{}}
	// Defaults are validated by the setting package; the zero snapshot only
	// guards against a future default that stops validating, so LoadBinding
	// still never returns nil.
	disabled, err := langfuse_setting.BuildSnapshot(langfuse_setting.DefaultLangfuseSetting, 0)
	if err != nil {
		disabled = langfuse_setting.Snapshot{}
	}
	m.binding.Store(&RuntimeBinding{Snapshot: disabled})
	return m
}

// LoadBinding returns the active binding. It never returns nil, so callers can
// read the snapshot without a nil check on the relay hot path.
func LoadBinding() *RuntimeBinding {
	return manager.binding.Load()
}

// PublishBinding atomically swaps in a new binding. Publishing must not perform
// any operation that can fail, so callers build and validate the candidate
// first.
func PublishBinding(b RuntimeBinding) {
	manager.binding.Store(&b)
}

// PublishSnapshot validates a candidate configuration, builds the runtime it
// needs and publishes both as one binding. A rejected configuration or a
// candidate that fails to build leaves the active binding untouched; the
// previous runtime is retired only after the new binding is visible.
func PublishSnapshot(s langfuse_setting.LangfuseSetting) error {
	snapshot, err := langfuse_setting.BuildSnapshot(s, 0)
	if err != nil {
		return err
	}
	snapshot.Version = manager.versionCounter.Add(1)

	var candidate *TelemetryRuntime
	if snapshot.Enabled {
		if candidate, err = BuildCandidateRuntime(snapshot); err != nil {
			return err
		}
	}

	previous := LoadBinding()
	SetMaxInFlightCaptureBytes(int64(snapshot.MaxInFlightCaptureBytes))
	PublishBinding(RuntimeBinding{Snapshot: snapshot, Runtime: candidate})
	manager.track(candidate)
	retireInBackground(previous.Runtime)
	return nil
}

// CurrentVersion reports the active snapshot version. Versions exist for
// diagnostics only; configuration switching happens at the publish point.
func CurrentVersion() uint64 {
	return LoadBinding().Snapshot.Version
}

// BuildCandidateRuntime assembles a TracerProvider for a validated snapshot.
// Nothing here touches the network, so publishing a configuration cannot fail
// because Langfuse is momentarily unreachable.
func BuildCandidateRuntime(snapshot Snapshot) (*TelemetryRuntime, error) {
	installErrorHandlerOnce()

	transport := newForbiddenTransport()
	options, err := buildExporterOptions(snapshot, transport)
	if err != nil {
		return nil, err
	}
	client, err := otlptracehttp.New(context.Background(), options...)
	if err != nil {
		return nil, err
	}

	runtime := newTelemetryRuntime(snapshot)
	runtime.exporter = newCountingExporter(client, transport, snapshot.Version)
	runtime.provider = sdktrace.NewTracerProvider(
		// Sampling already happened in Begin; a second SDK level decision would
		// break the session and request scoped sampling contract.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		// Raw limits keep the structured JSON attributes intact and make the
		// OTEL_*ATTRIBUTE* environment variables irrelevant for this provider.
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit:   -1,
			AttributeCountLimit:         langfuseSpanAttributeCountLimit,
			EventCountLimit:             0,
			LinkCountLimit:              0,
			AttributePerEventCountLimit: 0,
			AttributePerLinkCountLimit:  0,
		}),
		sdktrace.WithIDGenerator(langfuseIDGenerator{}),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("new-api"),
			semconv.ServiceVersion(common.Version),
			semconv.DeploymentEnvironmentName(snapshot.Environment),
		)),
		sdktrace.WithBatcher(runtime.exporter,
			sdktrace.WithMaxQueueSize(snapshot.QueueSize),
			sdktrace.WithMaxExportBatchSize(snapshot.BatchSize),
			sdktrace.WithBatchTimeout(time.Duration(snapshot.FlushIntervalSeconds)*time.Second),
		),
	)
	runtime.tracer = runtime.provider.Tracer(tracerName)
	return runtime, nil
}

// ShutdownAll stops new telemetry and drains every known runtime inside one
// shared deadline. Runtimes a worker still holds are skipped and reported
// rather than shut down underneath that worker (design §10.2).
func ShutdownAll(ctx context.Context) {
	disabled := LoadBinding().Snapshot
	disabled.Enabled = false
	PublishBinding(RuntimeBinding{Snapshot: disabled})

	runtimes := manager.liveRuntimes()
	for _, runtime := range runtimes {
		runtime.Retire()
	}
	for _, runtime := range runtimes {
		if !runtime.WaitIdle(ctx) {
			emitWarning(fmt.Sprintf("langfuse runtime version %d still has %d in-flight lease(s) at process exit, skipping shutdown",
				runtime.Snapshot.Version, runtime.inFlightLeases()))
			continue
		}
		runtime.shutdownAndReport(ctx)
	}
}

// retireInBackground stops new leases immediately and drains the runtime off
// the caller's goroutine. The wait is unbounded on purpose: a configuration
// switch must never block, and shutting a provider down while a worker can
// still call span.End would race that worker.
func retireInBackground(runtime *TelemetryRuntime) {
	if runtime == nil {
		return
	}
	runtime.Retire()
	go func() {
		runtime.WaitIdle(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), retirementShutdownBudget)
		defer cancel()
		runtime.shutdownAndReport(ctx)
	}()
}

func (m *runtimeManager) track(runtime *TelemetryRuntime) {
	if runtime == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live[runtime] = struct{}{}
}

func (m *runtimeManager) forget(runtime *TelemetryRuntime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, runtime)
}

func (m *runtimeManager) liveRuntimes() []*TelemetryRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtimes := make([]*TelemetryRuntime, 0, len(m.live))
	for runtime := range m.live {
		runtimes = append(runtimes, runtime)
	}
	return runtimes
}
