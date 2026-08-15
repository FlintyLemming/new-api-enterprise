# Plan 3 — OTel Runtime Manager、Exporter 与 OTLP 集成契约

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §10.2 后半、§11、§16.3 与 §16.7——`TelemetryRuntime`（防退休 `TryAcquire`、lease 计数、retirement drain、诊断计数）、OTLP/HTTP exporter 构建（`WithEndpointURL`、Basic auth、gzip、10s 超时、429 退避、403 non-retryable RoundTripper）、计数型 exporter decorator、全局代理 error handler、确定性 IDGenerator、锁定 SpanLimits、进程 shutdown；以及 §14.2 的 OTLP 集成契约测试。

**Architecture:** `service/langfuse/runtime.go`（manager/lease/计数/shutdown）+ `service/langfuse/exporter.go`（endpoint/exporter/RoundTripper/decorator）+ `service/langfuse/idgen.go`（IDGenerator + `DeriveTraceID`）。控制面 API 不变：`PublishSnapshot(s)` 内部升级为"启用→构建 candidate runtime→发布→退休旧 runtime"。生产代码只调用官方 exporter。

**Tech Stack:** otel sdk v1.44（执行本计划时从 v1.34 上调，因为 403 `RoundTripper` 需要 v1.36.0 才引入的
`otlptracehttp.WithHTTPClient`）、otlptracehttp、`go.opentelemetry.io/proto/otlp`（仅测试）。

## Global Constraints

- 见 `00-master.md` 与 plan-2 的白名单（本计划不新增项目内依赖；测试文件可以 import `go.opentelemetry.io/proto/otlp`）。
- §11 全部锁定值：`AlwaysSample`；`WithRawSpanLimits(SpanLimits{AttributeValueLengthLimit: -1, AttributeCountLimit: 64, EventCountLimit: 0, LinkCountLimit: 0, AttributePerEventCountLimit: 0, AttributePerLinkCountLimit: 0})`；单次 HTTP 请求超时 10s；retirement 5 分钟告警；shutdown 共享 15s 绝对 deadline；Basic header 只从不可变 secret 快照构造且不进日志。
- 测试失败输出不得打印 Basic Authorization 值、secret、正文。

---

### Task 1: `TelemetryRuntime` 与防退休 lease

**Files:**
- Modify: `service/langfuse/runtime.go`（替换 plan-2 的空壳 `TelemetryRuntime`）
- Test: `service/langfuse/runtime_test.go`（追加）

**Interfaces:**
- Produces（plan-5b 消费）:

```go
type TelemetryRuntime struct {
	Snapshot langfuse_setting.Snapshot
	tracer   trace.Tracer        // 经 provider.Tracer("new-api/langfuse")
	provider *sdktrace.TracerProvider
	exporter *countingExporter   // Task 3
	// lease / retirement
	mu       sync.Mutex
	retiring bool
	inFlight int
	idle     chan struct{}       // 归零后关闭一次
	// diagnostics
	materialized atomic.Int64
	// shutdown 状态
	shutdownOnce sync.Once
}
func (r *TelemetryRuntime) TryAcquire() bool   // retiring=false 时 inFlight++ 后复查 retiring;已退休则归还并 false
func (r *TelemetryRuntime) Release()           // inFlight--;归零且 retiring 时关闭 idle(once)
func (r *TelemetryRuntime) Retire()            // mu 下 retiring=true;快照 inFlight;若为 0 立即关 idle
func (r *TelemetryRuntime) WaitIdle(ctx context.Context) bool // 等归零;5 分钟未归零 SysError 告警(含 version/inFlight)后继续等
```

- [ ] **Step 1: 写失败测试**（`-race` 必跑）：
  1. 顺序：acquire→retire→acquire 返回 false→release 后 idle 关闭。
  2. 竞态不变式（§10.2/§14.1 runtime）：200 goroutine 并发 `TryAcquire`，同时主 goroutine `Retire`；断言成功 acquire 的次数 == 最终 Release 总次数，且 `Retire` 返回后到 idle 关闭之间不存在任何新成功 acquire（用一个 `atomic.Bool` 在 retire 返回后置位，所有 acquire 成功路径断言未置位）。
  3. `Release` 幂等性不要求，但重复 Release 不得 panic 于负数（用 `if r.inFlight > 0` 守卫并在归零判定时只触发一次 idle）。
- [ ] **Step 2: 确认失败**。Run: `go test ./service/langfuse -race -run TestRuntime -v`
- [ ] **Step 3: 实现**（标准 retiring-refcount 模式，注释里写明 §10.2 的竞态说明）。此时 `tracer/provider/exporter` 字段允许为 nil（Task 4 填充），`TryAcquire/Release` 不依赖它们。
- [ ] **Step 4: 通过 + Commit**

```bash
git add service/langfuse/runtime.go service/langfuse/runtime_test.go
git commit -m "feat(langfuse): telemetry runtime with retirement-safe leases"
```

---

### Task 2: Endpoint helper 与 exporter 构建

**Files:**
- Create: `service/langfuse/exporter.go`
- Test: `service/langfuse/exporter_test.go`、`service/langfuse/otlp_http_test.go`（集成）

**Interfaces:**
- Produces:
  - `func buildExporterOptions(snap langfuse_setting.Snapshot) ([]otlptracehttp.Option, error)`（纯函数：从快照构造全部 options，包括 Basic auth header、gzip、10s timeout client、retry、403 transport）
  - `func basicAuthHeader(publicKey, secretKey string) string`（`base64.StdEncoding.EncodeToString([]byte(pub+":"+sec))` 前缀 `Basic `；**日志/错误信息中禁止出现该值**）

- [ ] **Step 1: 单元测试**（§14.2/§11）：
  - `basicAuthHeader("pk","sk") == "Basic cGs6c2s="`。
  - buildExporterOptions 对 http/https Host 生成等价 options（可通过其构造的 `http.Client` 与 URL 断言间接验证；核心断言放集成测试）。
- [ ] **Step 2: 实现**

```go
func buildExporterOptions(snap langfuse_setting.Snapshot) ([]otlptracehttp.Option, error) {
	if snap.TracesURL == "" {
		return nil, errors.New("langfuse traces url is empty")
	}
	rt := newForbiddenTransport() // Task 3;本任务先返回未包 403 逻辑的同名结构占位
	client := &http.Client{Timeout: 10 * time.Second, Transport: rt}
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(snap.TracesURL), // 一次性覆盖 env 的 scheme/authority/path
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": basicAuthHeader(snap.PublicKey, snap.SecretKey),
		}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{ /* 默认瞬时错误重试 + 上限;按 §11 设置 AsMax/Backoff */
			Enabled: true,
		}),
		otlptracehttp.WithHTTPClient(client),
	}, nil
}
```

（`RetryConfig` 具体字段以 v1.44 API 为准：`otlptracehttp.RetryConfig{Enabled: true, InitialInterval:, MaxInterval:, MaxElapsedTime:}`；选择 Initial 1s、Max 5s、MaxElapsed 30s，并在测试/注释固定。`WithEndpointURL` 在 v1.44 仍存在，无需 §11 的 `WithEndpoint(authority)+WithURLPath(path)+WithInsecure(仅 http)` 替代路径。）

- [ ] **Step 3: 集成测试**（`otlp_http_test.go`，真实 exporter + `httptest.Server`，`t.Cleanup` 关闭 provider）：
  1. `http://127.0.0.1:addr` Host（scheme http）→ 服务端收到 `POST /api/public/otel/v1/traces`，`Content-Encoding: gzip`，`Authorization` 存在且以 `Basic ` 开头（**断言时不得把值写进失败消息**）。
  2. `https://` Host → 指向 `httptest.NewTLSServer`，client 信任其证书（`WithHTTPClient` 的 client 注入 `TLSClientConfig`）→ 收到请求（证明 https 走 TLS）。
  3. base path：Host `http://host`（用 hosts 重写或 `WithEndpointURL` 直接指到 server URL + `/langfuse` 前缀的 mux）→ 命中 `/langfuse/api/public/otel/v1/traces`。
  4. env 覆盖：`t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://evil.example")`、`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://evil.example` → 请求仍发到快照 URL。
- [ ] **Step 4: 通过 + Commit**

Run: `go test ./service/langfuse -run 'TestExporter|TestOtlp' -v`

```bash
git add service/langfuse/exporter.go service/langfuse/exporter_test.go service/langfuse/otlp_http_test.go
git commit -m "feat(langfuse): otlp http exporter options with env-immune endpoint"
```

---

### Task 3: 403 RoundTripper、计数 decorator、代理 error handler

**Files:**
- Modify: `service/langfuse/exporter.go`
- Test: `service/langfuse/exporter_test.go`（追加）

**Interfaces:**
- Produces:
  - `type forbiddenTransport struct{ base http.RoundTripper; warned ... }`：`RoundTrip` 观察目标 endpoint 状态码；403 → 关闭原 body，替换为 `io.NopCloser(strings.NewReader("ingestion forbidden"))`、`ContentLength` 调整，并按 runtime version ≤1 条/30s 记 `common.SysError` 固定摘要（状态码+version+失败 span 计数，无原始 body/URL query/凭证）。
  - `type langfuseExportError struct{ err error; sanitized string }`（`Error()` 返回 sanitized）
  - `type countingExporter struct { sdktrace.SpanExporter; received, exported, failed atomic.Int64 }`：`ExportSpans` 入口 `received += len(spans)`；调内部 exporter，nil → `exported += len`，err → `failed += len` 并返回 `langfuseExportError{err, 固定摘要}`。
  - `func installErrorHandlerOnce()`：`otel.SetErrorHandler(代理)`——`langfuseExportError` 走限频（同 key 30s 一条）`common.SysError`，其余错误转发给安装时的原 handler；`sync.Once` 保证只装一次。

- [ ] **Step 1: 失败测试**：
  1. httptest 返回 403 + 敏感 body：exporter（经 BSP flush/直接调 `ExportSpans`）后断言——请求只发生 1 次（不重试）、`failed` 计数 == span 数、`http.Server` 侧原 body 已被消费关闭（用 `httptest` + 自定义 handler 记录 `w.(http.Flusher)`/body 读取探测不可靠，改为断言 exporter 收到的错误不是原始 body：`err.Error()` 为固定摘要）。
  2. 429 + `Retry-After: 1`：允许最终失败/成功，断言请求次数 ≥2（重试发生）且重试不阻塞调用 goroutine 超过合理界（BSP worker 内执行）。
  3. decorator 计数：fake SpanExporter 返回 nil / err 两轮，断言 received/exported/failed。
  4. 代理 handler：`langfuseExportError` 触发限频告警（注入告警函数便于断言，30s 窗口第二条被吞）；非 Langfuse 错误透传给原 handler（注入 fake 原断言收到）。
- [ ] **Step 2/3: 实现并跑绿**。Run: `go test ./service/langfuse -run 'TestForbidden|TestCounting|TestErrorHandler' -v`
- [ ] **Step 4: Commit**

```bash
git add service/langfuse/exporter.go service/langfuse/exporter_test.go
git commit -m "feat(langfuse): 403 non-retryable transport, counting exporter decorator, rate-limited error handler"
```

---

### Task 4: TracerProvider 组装、candidate 构建/发布/退休、IDGenerator、SpanLimits

**Files:**
- Create: `service/langfuse/idgen.go`
- Modify: `service/langfuse/runtime.go`（manager 升级）
- Test: `service/langfuse/runtime_test.go`、`service/langfuse/idgen_test.go`、`service/langfuse/otlp_limits_test.go`

**Interfaces:**
- Produces（plan-5b 消费）:
  - `func DeriveTraceID(requestID string) trace.TraceID`：`traceIDHash([]byte(requestID))` 前 16 字节原序；全零则末字节 `0x01`；包级 `var traceIDHash = sha256.Sum256` 可注入测试。
  - `type langfuseIDGenerator struct{}`：`NewIDs(ctx)` 优先 `ctx.Value(traceIDCtxKey{})`（`trace.TraceID`，合法则用），SpanID 用 `crypto/rand` 8 字节；`NewSpanID(ctx, _)` 同源随机。
  - `func BuildCandidateRuntime(snap langfuse_setting.Snapshot) (*TelemetryRuntime, error)`：buildExporterOptions → otlptracehttp.New → countingExporter 包装 → `sdktrace.NewTracerProvider(WithSampler(AlwaysSample), WithRawSpanLimits(固定 64/-1/0), WithBatcher(bsp, MaxQueueSize=snap.QueueSize, MaxExportBatchSize=snap.BatchSize, BatchTimeout=flush), WithResource(service.name/version/environment), WithIDGenerator)`。
  - `manager.PublishSnapshot(s)` 升级：enabled→BuildCandidateRuntime→成功才原子发布新 binding 并对旧 runtime `Retire()`+异步 `drainThenShutdown`；失败→关闭 candidate、保留旧 binding、返回 error。disabled→发布 `{Snapshot: disabled快照, Runtime: nil}` 并退休旧 runtime。**发布过程不做网络操作**。发布同时调用 `SetMaxInFlightCaptureBytes(s.MaxInFlightCaptureBytes)`（plan-4 暴露的全局预算上限），使预算上限与 binding 同一切换。
  - `func ShutdownAll(deadline time.Time)`：原子禁止新 telemetry（发布 disabled binding + 标记所有已知 runtime retiring），在共享绝对 deadline（15s，>10s 单请求超时）内等 lease 归零并 shutdown 已归零 provider；超时跳过仍被引用的 provider 并 `SysError` 记录 in-flight 数。
  - `runtime.recordMaterialized(n)`：`materialized += n`；`queueDroppedLowerBound()` = `max(0, materialized-received-queue-batch)`，单调 max 维护 + 压力告警（差值持续 >75% queue 容量时每 30s 一条，含四计数/queue/batch/采样率，无正文）。

- [ ] **Step 1: 失败测试**：
  1. `DeriveTraceID("abc")` 等于 sha256 前 16 字节；注入 `traceIDHash = func(...)[32]byte{全 1}`→返回的 ID 前 15 字节为 0、末字节 0x01；`requestID=""` 的行为同上（sha256("") 不全零，走正常路径）。
  2. **环境变量覆盖测试（§14.2 硬要求）**：构建 provider 前 `t.Setenv` 把 `OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT=1`、`OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT=1`、`OTEL_ATTRIBUTE_COUNT_LIMIT=1`、`OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT=1`；创建 1 root + 2 generation 形状的 span（属性 ≥10 个、其中一个为 100KB JSON string），经真实 exporter 发到 httptest，解码 protobuf（`proto/otlp`）：全部属性存在、值完整、无截断；生产属性 builder 最大数 ≤64（此处先以固定 fixture 断言 ≥10 且 ≤64）。
  3. **root 先入队顺序**：用测试 SpanProcessor（记录 `OnEnd` 顺序）替代 BSP 构建一个 runtime 变体（`BuildCandidateRuntimeForTest` 或导出注入点），materialize root+2 generations（先 End root 再 End generation 的顺序由调用方保证——本测试只断言 processor 收到顺序 root, g1, g2）。
  4. 极小 queue（QueueSize=16, BatchSize=1）+ 快速 End 200 个 span → `queueDroppedLowerBound() > 0` 且告警函数被调用（注入告警 hook）。
  5. `PublishSnapshot` 失败路径（非法配置）保留旧 binding；成功路径 Version 单调、旧 runtime `Retire` 被调用（注入 retire hook 或观察 `TryAcquire` 返回 false）。
  6. barrier 用例（§14.1 runtime）：worker 已 acquire 尚未 release 时切换配置→旧 runtime 不会被 shutdown（`ShutdownAll` 前置条件断言 in-flight>0 时 15s 预算内跳过其 shutdown 并告警）。
  7. retirement drain 摘要（§11/§14.2 末条）：drain 在预算内成功后输出该 runtime 的**精确** queue drop 数（== `materialized-received`）并更新最终计数；drain 超时/失败时摘要标记 incomplete、只保留 lower-bound（用可控 queue + 阻塞 exporter 注入两种路径断言）；export 最终错误只增加 failed 计数、不混入 drop。
- [ ] **Step 2: 实现并跑绿**

Run: `go test ./service/langfuse -race -v`
（proto 解码测试 import `go.opentelemetry.io/proto/otlp/...`，确认仍是测试专用。）

- [ ] **Step 3: Commit**

```bash
git add service/langfuse/
git commit -m "feat(langfuse): tracer provider assembly, deterministic idgen, publish/retire/shutdown"
```

---

### Task 5: 进程 shutdown wiring

**Files:**
- Modify: `main.go`（`srv.Shutdown(ctx)` 成功返回后、`model.SaveQuotaDataCache()` 附近）

- [ ] **Step 1: 接线**

```go
	// Langfuse telemetry drain: 共享 15s 绝对 deadline,严格大于单次 exporter 10s 超时;
	// 不阻塞 relay shutdown,超时仅告警。必须在 server 关闭后调用,保证请求 goroutine 已结束。
	langfuseShutdownCtx, langfuseShutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	_ = langfuseShutdownCancel
	langfuse.ShutdownAll(langfuseShutdownCtx)
```

（`ShutdownAll` 签名按 Task 4 落地为准——若定义为接收 `context.Context` 则直接传；保持"所有 provider 共用同一绝对 deadline、不另起 15s 计时器"。）

- [ ] **Step 2: 验证 + Commit**

Run: `go build ./...`

```bash
git add main.go
git commit -m "feat(langfuse): bounded 15s shared shutdown drain in process exit"
```

---

### Task 6: 收尾验证

- [ ] Run:

```bash
go test ./service/langfuse/... -race -v && go test ./service/langfuseconfig ./setting/... && go build ./... && cd relaykit && GOWORK=off go build ./...
```

Expected: 全绿；`go list -deps ./service/langfuse` 闭包仍满足 plan-2 Task 3 的禁用名单（新 import 只有 OTel 家族与标准库）。

---

## Self-Review 已核对

- §11 的 endpoint 五步（authority/URLPath/finalURL/WithEndpointURL/环境免疫）、gzip/Basic/10s/retry、AlwaysSample、RawSpanLimits、429/403、计数诊断、15s shutdown 均有任务；§10.2 的防退休 TryAcquire、candidate 失败保留旧 runtime、drain 不与 worker 竞争在 Task 1/4/6；§14.2 中不依赖 Recorder 的条目（env 覆盖、顺序、计数、URL/auth、429/403、边界 protobuf 大小——最后一条最坏转义正文用例在本计划 Task 4 Step1.2 顺带覆盖 100KB JSON，完整 9,000,000 包络断言归 plan-8 验证计划统一执行）。依赖 Recorder 的 §14.2 条目（deterministic trace ID 全链路、attempt 时长、denylist、usage/cost 属性）显式留给 plan-5b/6，不在此重复。
