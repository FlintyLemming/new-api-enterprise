# Plan 8 — 全量验证与真实 Langfuse E2E

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) 或 superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §14.5（构建验证）、§14.2 的边界包络集成测试与 §14.6/§16.9（固定 revision `2aa50493a` 的真实 Langfuse E2E）——发布验收项，不以 Go ingestion fixture 替代。

**Architecture:** 两个收尾任务组：(A) 代码内验证——补齐跨计划才能写的边界测试（满 batch 最坏转义正文 → protobuf/gzip 实测包络）+ 全套命令；(B) 环境级 E2E——docker 起 Langfuse web/worker + 依赖，跑真实 OTLP ingestion，按验收清单核对。

## Global Constraints

- 见 `00-master.md`。E2E 失败输出只允许 trace/observation ID 与脱敏状态，**不得**输出正文、凭证、Basic header、session 原值。
- E2E 不是 `go test ./...` 的一部分：不得在 Go 中复制 `OtelIngestionProcessor`/`IngestionService` 制造伪 Langfuse fixture；也不要求 Go 测试启动 Node/TypeScript worker。

---

### Task 1: 边界 span/batch 包络集成测试（§14.2 倒数第二条）

**Files:**
- Create: `service/langfuse/otlp_envelope_test.go`

- [ ] **Step 1: 测试内容**：
  1. 用满足整组校验的边界配置（`max_content_bytes=1MiB, max_response_bytes=64KiB, queue=16, batch=1` 与 `max_content_bytes=4KiB, max_response_bytes=8MiB, queue=16, batch=1` 两组）分别构造**最大 root span**（input/output 各接近 `max_content_bytes` 的最坏转义正文：不可压缩 ASCII、全引号/反斜杠/控制字符、多字节 UTF-8 三种素材）与**满 batch**（`batch_size` 个同类 span）。
  2. 经真实 exporter 发送（httptest 接收），服务端解 gzip、用 `proto/otlp` 解码：断言结构化 input/output 均为合法 JSON、逐属性 `jsonEscapedLen` 贡献 ≤ `max_content_bytes`、配置包络 `2*content+response <= 9_000_000`。
  3. 记录（`t.Logf`）实际 protobuf 与 gzip 字节数作为 ingress body-limit 配置依据；断言不把 protobuf 大小冒充 `JSON.stringify(span)` eventBytes（即测试只声明 wire 实测，不断言 9,500,000 阈值行为）。
  4. 正文与凭证不得进入测试日志（`t.Logf` 只打字节数与属性名）。
- [ ] **Step 2: 跑绿 + Commit**

Run: `go test ./service/langfuse -run TestOtlpEnvelope -v`

```bash
git add service/langfuse/otlp_envelope_test.go
git commit -m "test(langfuse): worst-case escaping span/batch wire envelope"
```

---

### Task 2: 全量构建验证（§14.5）

- [ ] **Step 1: 逐条执行并记录结果**

```bash
# 1. focused
go test ./service/langfuse/... ./service/langfuseconfig/... ./setting/langfuse_setting/... ./relay/constant/... -v
# 2. race(writer/recorder 并发契约所在包)
go test -race ./service/langfuse/...
# 3. 包边界(AST + go list 闭包)
go test ./service/langfuse -run TestPackageBoundaries -v
go list -deps ./service/langfuse | grep -E 'new-api/(service|model|controller|service/langfuseconfig|relay/channel)' && echo VIOLATION || echo OK
# 4. 最广 suite(环境允许)
go test ./...
# 5. 构建
go build ./...
# 6. relaykit 独立
cd relaykit && GOWORK=off go build ./... && GOWORK=off go test ./...
# 7. 前端
cd ../web && bun run typecheck && bun run lint && bun run build
```

Expected: 全绿；第 3 条无 VIOLATION。任何失败先按 superpowers:systematic-debugging 定位，禁止跳过。

- [ ] **Step 2: 台账收尾**——按 `docs/internal/patches/_template.md` 为 `feature/langfuse-tracing` 补 patch 文档（含部署影响：新 option keys、内存规划值、ingress body limit 需部署者按 Task 1 实测值配置）与索引行，`docs(internal)` 单独提交。

---

### Task 3: 真实 Langfuse E2E（§14.6）

**Files:**
- Create: `docs/internal/e2e/2026-XX-XX-langfuse-e2e-report.md`（执行时填写）

- [ ] **Step 1: 环境准备**
  - 以固定 revision `2aa50493a` 拉起 Langfuse web + worker 及其依赖（db/redis/minio——用该 revision 的 `docker-compose.yml`；子路径部署若该 revision 不支持，报告中明确该限制，子路径正确性由 plan-3 的 exporter HTTP 集成测试覆盖）。
  - 创建 project，固定 Public/Secret Key；在 Langfuse 中配置测试 model prices（如 `test-model: input=3, output=6, cached=...` USD/Mtok）。
  - New API 侧通过专用 PUT 启用 Langfuse：Host 指向 compose 暴露地址、`sample_rate=1`、`send_content=true`。
- [ ] **Step 2: 用例执行与轮询断言**（经 Langfuse API 轮询 trace 列表/详情，deadline 60s/用例；失败只输出 trace/observation ID 与脱敏状态）：
  1. **trace name fallback**：发送 root（span name=`openai test-model`，observation input/output/metadata、`as_root="true"`、无 `langfuse.trace.*`）→ trace name 等于 span name；trace 列表预览正文来自 root fallback（内容一致、含 `capture_state`）；root observation 详情保留正文。
  2. **usage 入库口径**：`input_cache_creation`/`output_audio_tokens` 按内建口径出现在 usage details；`input_image_tokens`/`input_audio_tokens` 作为自定义 usage type 原样保留；另发一条**仅 E2E 的故意拼错 key**（如 `input_cach_creation`）→ 原样入库不被纠正（证明生产映射的 canonical key 是有意选择；拼错 key 绝不出现在生产 builder）。
  3. **推价抑制**：`Error + model + 无 usage/无 cost` → 不触发 tokenisation；`Error + model + usage + 无 cost` → Langfuse 仍按 model prices × usage 推价（证明 New API 对该组合省略 model 是必要防护）；成功 `usage + model + authoritative cost` → cost 为 New API 值（对照 `{"total": quota/500000}`）。
  4. **denylist/多 attempt**：两个 generation 的 trace → root 的 user/session/metadata 不被 attempt 覆盖；generation 无 denylist 属性。
  5. **两种部署路径**：HTTP 根路径至少完成一次真实 ingestion；（环境支持时）带 base path 的部署再完成一次。
  6. 顺手核对验收标准 §15 的 1/2/4/6/9（端到端视角）。
- [ ] **Step 3: 报告**——把每条用例的输入形状（脱敏）、trace/observation ID、观察结果与结论写入报告；标注未覆盖项与原因。

- [ ] **Step 4: Commit**

```bash
git add docs/internal/e2e/
git commit -m "docs(langfuse): real langfuse E2E verification report"
```

---

## Self-Review 已核对

- §14.5 七条逐条对应；§14.2 包络测试补齐（plan-3 只覆盖了 100KB 样例）；§14.6 六项验证 + 固定 revision/keys/轮询 deadline/脱敏输出全部落实；台账收尾在 Task 2。
