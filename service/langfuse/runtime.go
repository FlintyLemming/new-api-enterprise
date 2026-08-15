// Package langfuse is the Langfuse tracing data plane. It only reads an atomic
// binding published by the control plane, so the relay hot path never touches
// the database, the mutable GlobalConfig objects or any reconcile logic.
package langfuse

import (
	"sync/atomic"

	"github.com/QuantumNous/new-api/setting/langfuse_setting"
)

// Snapshot is the immutable configuration the data plane reads. The alias keeps
// every data plane caller on a single type without importing the setting
// package at each call site.
type Snapshot = langfuse_setting.Snapshot

// TelemetryRuntime owns the OTel TracerProvider and its lease accounting. It is
// an inert placeholder until the runtime manager lands; no methods are exported
// before its fields are settled.
type TelemetryRuntime struct {
	version uint64
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
