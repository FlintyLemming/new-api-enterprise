# Plan 1 — OTel 依赖转 direct 并锁定版本

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §16.1——把 OTel 依赖调整为实现所需的 direct 依赖并整组锁定版本，验证现有 ClickHouse 间接消费者不受影响。本计划不改任何业务代码。

**Architecture:** 只动 `go.mod`/`go.sum` 与一个最小冒烟测试文件。当前 `go.opentelemetry.io/otel`、`otel/trace` 均为 v1.34.0 indirect（经 ClickHouse `ch-go`/`clickhouse-go` 引入），仓库尚无 OTel 直接 import。

**Tech Stack:** Go modules。

## Global Constraints

- 见 `00-master.md`。
- `go.opentelemetry.io/proto/otlp` 只允许被**测试**代码直接 import（解码官方 exporter 的 protobuf 用，见 plan-3/plan-8）。
- 生产代码不得 import `internal/tracetransform` 或自行实现 OTLP 编码。
- 依赖只进根 module；`relaykit/` 的 go.mod 不动。

---

### Task 1: 引入 direct 依赖并 tidy

**Files:**
- Modify: `go.mod`、`go.sum`
- Create: `service/langfuse/deps_smoke_test.go`（临时冒烟，plan-3 会扩充该包）

- [ ] **Step 1: 添加 direct 依赖**

```bash
go get go.opentelemetry.io/otel@v1.34.0 \
  go.opentelemetry.io/otel/trace@v1.34.0 \
  go.opentelemetry.io/otel/sdk@v1.34.0 \
  go.opentelemetry.io/otel/metric@v1.34.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.34.0
go mod tidy
```

若 `go get` 因传递约束提示需要 `go.opentelemetry.io/proto/otlp` 或 `google.golang.org/protobuf`，按提示版本加入（`protobuf v1.36.5` 已在 go.sum 中）；最终以 `go mod tidy` 收敛为准，并核对 `go.mod` 中 otel 家族版本全部为 `v1.34.0`（`otlp` proto 例外，跟随 exporter 需求）。

> **实现期修订（plan-3 执行时）：** 整组版本已从 `v1.34.0` 上调到 `v1.44.0`。plan-3 的 403 `RoundTripper` 需要
> `otlptracehttp.WithHTTPClient`，该 option 自 v1.36.0 才存在。改锁后重新执行了本计划 Task 2 的验证
> （`go build ./...`、`go test ./model/... ./service/...`、`cd relaykit && GOWORK=off go build ./...`）。

- [ ] **Step 2: 冒烟测试（证明 direct import 可用且 sdk 构建无冲突）**

```go
package langfuse

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestOtelDependenciesSmoke(t *testing.T) {
	// 只验证依赖链可编译、SDK 基本可用;不建立任何全局状态。
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	tr := tp.Tracer("smoke")
	var span trace.Span
	func() {
		ctx, s := tr.Start(t.Context(), "smoke-span")
		defer s.End()
		span = s
	}()
	if !span.SpanContext().IsValid() {
		t.Fatal("expected valid span context")
	}
	_ = otel.GetTracerProvider()
}
```

- [ ] **Step 3: 验证**

```bash
go build ./... && go test ./service/langfuse/... -v
cd relaykit && GOWORK=off go build ./... && GOWORK=off go test ./...
```

Expected: PASS；relaykit 不受影响。

- [ ] **Step 4: ClickHouse 间接消费者回归（§16.1 硬性要求）**

```bash
go test ./model/... ./service/...
```

Expected: PASS（`ch-go`/`clickhouse-go` 仍编译）。环境允许时跑完整 `go test ./...`。

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum service/langfuse/deps_smoke_test.go
git commit -m "build(deps): add direct otel sdk/otlptracehttp v1.34.0 dependencies"
```
