# feature/langfuse-tracing

| 项 | 值 |
| -- | -- |
| 分支 | `feature/langfuse-tracing` |
| 基线 | `origin/main` @ `58d4e9bd3` |
| 合入 commit | 线性合入（无 merge commit），tip `8eb884f4` |
| 合入日期 | 2026-08-16 |
| 状态 | `upstream-pending` |
| 上游 PR | 未提交 |

## 2026-09-09 rc.36 核对

继续保留，上游未提供等价 Langfuse OTLP 配置、导出和 session/usage 归属。转换 sidecar、hosted tools 与 usage 合并修复采用上游实现；内部 `cache_prompt_token_semantic`、summary 的折叠标志及结算点 Recorder 快照继续保留。

`service/text_quota.go` 冲突保留渠道声明分支，同时采用上游 `max` 写法；新增内置价格测试适配四参数 `BuildTieredTokenParams`（OpenAI prompt 为 inclusive）。OTel 依赖组保持 1.44.0，以满足现有 `WithHTTPClient` 用法。retry/finalizer 顺序与唯一 `BeginAttempt` 入口回归通过。新的计费模型标识和日志权限投影不回退。验证见 [同步记录](../sync-rc36-2026-09-09.md)。

## 为什么要这个改动

New API 自身只留结构化消费日志，没有可回看的对话级追踪：一次请求用了哪个渠道、重试了几次、每次尝试的
真实上游模型与耗时、客户端实际收到的正文，出问题时只能靠日志拼。这条 patch 增加**可选**的 Langfuse OTLP
导出：默认关闭，开启后每个受支持的共享 HTTP 对话请求产生一个 trace（root + 每次上游尝试一条 generation），
带 user/session、模型参数、互斥 usage 桶、New API 权威 cost，以及限长脱敏后的 input/output。

不开启时除了少量 nil 检查之外没有任何行为变化；开启后 telemetry 的任何故障（Langfuse 宕机、凭证失效、
队列饱和、内容处理 panic）都不允许改变 relay 的状态码或响应正文。

设计文档：`docs/superpowers/designs/2026-08-12-langfuse-conversation-tracing-design.md`。

## 改动内容

按模块列出行为变化：

- `setting/langfuse_setting/` — 新增配置组 `langfuse_setting`：字段上下界、Host 规范化（scheme/authority/base path）
  与 OTLP traces URL 拼接、整组原子校验（含单 span 正文包络与队列内存规划上限）、不可变 `Snapshot`。
- `service/langfuseconfig/` — 控制面：整组持久化、周期 reconcile、向数据面发布快照。只有它依赖 `model`。
- `service/langfuse/` — 数据面：runtime/exporter（OTLP HTTP + gzip + Basic auth、403 不重试的专用 transport、
  计数装饰器、限频错误处理、退休安全的 lease）、确定性采样与 trace ID 派生、session 身份提取、
  有界捕获 writer、脱敏与合法 JSON 截断、四协议 output 聚合、Recorder 生命周期与异步 materialization。
  该包的项目内直接依赖被 AST/import 测试限制在白名单内。
- `controller/` — 专用 `GET/PUT /api/option/langfuse`；通用 option 端点拒绝一切 `langfuse_setting.*`；
  relay retry loop 统一 finalizer：`Begin` → 每轮 `EndAttempt` → 计费阶段 → `Finish`。
- `relay/` — 共享 `doRequest` 在 `relayClient.Do` 前唯一一次 `BeginAttempt`；`relay/constant` 新增纯函数
  Gemini action/model 分类器（embedding/predict/imagen 不产生 trace）。
- `service/` — `PostTextConsumeQuota` / `PostAudioConsumeQuota` 在结算点把最终 usage/quota 快照给 Recorder。
- `relaykit/`、`relay/common`、`model/` — 计费前置：渠道设置 `cache_prompt_token_semantic`、
  `BuildTieredTokenParams` 改为接收原始 prompt cache 归属标志、summary 增加 fold 感知标志。
- `web/src/features/system-settings/integrations/` — Langfuse 设置区块：与后端一致的校验、首次/重新启用必须
  显式选择 Sample Rate 与 Send Content、容量提示、secret 保留/替换/清除；七语言文案。

## 部署影响

**新增 option key（全部 `langfuse_setting.` 前缀，通用 option 接口读写不到）**：
`enabled`、`host`、`public_key`、`secret_key`、`environment`、`sample_rate`、`send_content`、
`max_content_bytes`、`max_response_bytes`、`max_in_flight_capture_bytes`、`max_session_body_bytes`、
`session_header_names`、`session_body_paths`、`queue_size`、`batch_size`、`flush_interval_seconds`。
无数据库迁移；不配置即保持关闭。

**内存规划（出厂默认值）**：单请求捕获预留 `2*max_content_bytes + max_response_bytes` = 640 KiB，
全局预算 512 MiB ⇒ 约 819 个并发正文捕获槽位。正文常驻基线按
`max_in_flight_capture_bytes + (queue_size + 3*batch_size) * max_queued_span_bytes` 规划，默认约 582 MiB，
另需为 relay 业务流量与 Go runtime 单独留量。调低采样率不是唯一容量旋钮，长流式场景应同时调低内容上限。

**ingress body limit 必须由部署者按实测值配置**。实测（真实 exporter 出网字节，见
`service/langfuse/otlp_envelope_test.go` 与 `docs/internal/e2e/2026-08-15-langfuse-e2e-report.md` 第 4 节）：

| 配置 | 单次 OTLP 请求 protobuf | gzip（不可压缩正文） | 最坏投影 `batch*(2*content+response)` |
| -- | -- | -- | -- |
| 出厂默认（content 64KiB / response 512KiB / batch 16） | 2,112,371 | 1,592,265 | 10,485,760 |
| content 1MiB / response 64KiB / batch 1 | 2,098,180 | 1,580,439 | 2,162,688 |
| content 4KiB / response 8MiB / batch 1 | 9,209 | 3,997 | 8,396,800 |

按最坏投影配置解压后 body 上限即可；Langfuse 自身的 9,500,000 单 span warning 与 16 MiB 请求 warning 都只
记录不拒绝，不能当作 ingress 依据。

**启用步骤**：`PUT /api/option/langfuse` 或管理界面填写 Host/Public Key/Secret Key，并显式选择 Sample Rate
与 Send Content（缺任一不保存）。Secret Key 永远不会被任何接口返回。

**完整部署说明**见 `docs/langfuse.md`（面向部署者，含 compose 增量、共用 pg/clickhouse/redis 的实测结论、
容量规划与 ingress 配置）。其中一条硬约束：Langfuse 4.6 的 ClickHouse 迁移需要 25.x，new-api compose 注释
里建议的 24.8 会在 `0039_create_events_full` 失败（`code: 80, Only literals can be skip index arguments`）。

## 与上游的冲突风险

- `controller/relay.go` 的 retry loop 与统一 finalizer 是上游活跃文件。同步上游时保留内部的
  `Begin`/`EndAttempt`/`Finish` 调用点，并重新确认计费阶段顺序仍是
  `NormalizeViolationFeeError → Refund → ChargeViolationFeeIfNeeded → 写响应 → Finish`。
- `relay/channel/*/` 的共享 `doRequest`：`BeginAttempt` 必须保持「`relayClient.Do` 前唯一一次」，
  上游若拆分该函数需要重新定位调用点，由 `TestAttemptHookHasExactlyOneCallSite` 兜底。
- `service/text_quota.go` / 音频结算路径：上游改 usage 组装时要重新确认结算点快照仍在同一位置。
- `relaykit/dto/channel_settings.go` 与前端渠道抽屉、七个 locale 文件属于机械冲突，取并集即可（rc.30 起该结构多了上游的 `task_plugin_key` 字段）。
- `go.mod` 的 OpenTelemetry 依赖组（v1.44.0）已转 direct，上游若引入自己的 OTel 版本需要统一。
- 2026-09-01 同步 rc.30 实况：上游把 web 测试运行器换成 vitest，本条目的四个前端测试文件已从 `node:test` 转为 vitest 导入。

## 验证方式

```bash
go test ./service/langfuse/... ./service/langfuseconfig/... ./setting/langfuse_setting/... ./relay/constant/...
```

```bash
go test -race ./service/langfuse/...
```

```bash
go list -deps ./service/langfuse | grep -E '^github.com/QuantumNous/new-api/(service|model|controller|relay/channel)' | grep -vx 'github.com/QuantumNous/new-api/service/langfuse'
```

```bash
go test ./... && go build ./...
```

```bash
cd relaykit && GOWORK=off go build ./... && GOWORK=off go test ./...
```

```bash
cd web && bun run typecheck && bun run build && bun test src/features/system-settings/integrations/__tests__/
```

手工验证：对真实 Langfuse 的端到端验收见
`docs/internal/e2e/2026-08-15-langfuse-e2e-report.md`（13/13 用例组通过，含 trace name fallback、usage 入库
口径、推价抑制、多 attempt/denylist、根路径与 base path 两种部署）。

这条特性另有一套**部署侧**的记录，是在 8850 生产环境上线并跑真实流量得来的，与上面的代码级验收互补：
`docs/internal/patches/2026-08-15-langfuse-local-deployment.md`（容量规划、回滚路径、压测、上线后全量对账）
与 `docs/internal/e2e/2026-08-15-langfuse-8850-traffic-acceptance.md`（真实流量验收）。两者跑的 Langfuse
版本不同（4.2.0 对 4.6.0），出现结论差异时以各自记录的环境为准。

注意两条与计划书不一致但已确认的执行细节：

- `plan-8` 里的 `go test ./service/langfuse -run TestPackageBoundaries` 匹配不到任何用例，实际测试名为
  `TestDataPlaneDirectImportsStayInsideWhitelist` 等五个（见 `package_boundaries_test.go`）。
- `plan-8` 里的 `go list -deps | grep -E 'new-api/(service|…)'` 会把被测包 `service/langfuse` 自己算作违规，
  必须像上面那样排除自身。

`go test ./...` 依赖 `web/dist` 存在（根包 `embed`），需要先跑一次前端构建。

## 退出条件

上游合并等价的 Langfuse/OTLP 导出能力后，确认其配置键名、采样与 session 作用域语义、usage/cost 归属规则与
本实现一致，改用上游实现并归档本条。若上游明确拒绝该功能，状态改为 `internal-only` 长期保留。
