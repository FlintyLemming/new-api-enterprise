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
)

// retirementWarnInterval is how long a retiring runtime may keep in-flight
// leases before every further wait logs one warning. Design §10.2 forbids
// shutting the provider down while a worker can still touch it, so the wait
// continues after the warning.
const retirementWarnInterval = 5 * time.Minute

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
}

var manager = newRuntimeManager()

func newRuntimeManager() *runtimeManager {
	m := &runtimeManager{}
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

// PublishSnapshot validates a candidate configuration, assigns it the next
// monotonic version and publishes it without a runtime. A rejected candidate
// leaves the active binding and the version counter untouched.
func PublishSnapshot(s langfuse_setting.LangfuseSetting) error {
	snapshot, err := langfuse_setting.BuildSnapshot(s, 0)
	if err != nil {
		return err
	}
	snapshot.Version = manager.versionCounter.Add(1)
	PublishBinding(RuntimeBinding{Snapshot: snapshot})
	return nil
}

// CurrentVersion reports the active snapshot version. Versions exist for
// diagnostics only; configuration switching happens at the publish point.
func CurrentVersion() uint64 {
	return LoadBinding().Snapshot.Version
}
