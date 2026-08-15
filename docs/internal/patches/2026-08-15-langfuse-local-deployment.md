# 8850 部署切换到 fork 构建并启用 Langfuse

日期：2026-08-15
执行计划：`docs/superpowers/plans/2026-08-15-langfuse-local-deployment.md`
设计文档：`docs/superpowers/designs/2026-08-15-langfuse-local-deployment-design.md`
验收报告：`docs/internal/e2e/2026-08-15-langfuse-e2e-report.md`

## 1. 变更内容

- `/home/flintylemming/appdata/8850-new-api` 的 `new-api` 容器从上游镜像 `calciumion/new-api:latest`（revision `0ab02020`，2026-08-01 构建）切换到本机构建的 fork 镜像。首次上线为 `new-api:langfuse-750452c0`（image `9c8570cf79f0`，217 MB，代码 revision `750452c0`，领先上游 73 个 commit）；当晚因审计需求抬高正文上限后重建为 **`new-api:langfuse-02dc4189`**（image `07d309327292`，217 MB），这是当前运行的镜像。
- 新建专用 Langfuse 实例 `/home/flintylemming/appdata/8858-langfuse-newapi`（Langfuse 4.2.0，六服务，仅发布 `8858->3000`），org `newapi` / project `new-api-8850`。既有的 `18081-langfuse`（服务 agentgateway 项目）未做任何改动。
- `8850-new-api/compose.yaml` 改动两处：`image` + `pull_policy: never`；`new-api` 服务显式声明 `networks: [default, langfuse]`，顶层新增 external 网络 `8858-langfuse-newapi_default`。
- Langfuse 集成经 root 专用接口 `PUT /api/option/langfuse` 启用：`host=http://langfuse-web:3000`、`environment=production`、`send_content=true`。
- **采样率最终为 `1.0`（全采集）**：本部署是企业内部环境，有审计要求"不得遗漏任何请求的内容"。计划原本的两段式（验收后降到 `0.1`）曾短暂执行，随后按审计口径改回 `1.0`。
- **正文上限由 1 MiB 抬到 4 MiB**（commit `02dc4189`）：改的是 `setting/langfuse_setting/config.go` 的 `maxContentBytes`（1 MiB→4 MiB）与 `queueBodyPlanningLimit`（256 MiB→2 GiB），并同步前端镜像校验、UI 输入框 `max` 属性与 7 个语种的校验文案。镜像重建为 `new-api:langfuse-02dc4189`。运行时容量参数：content=4 MiB、response=512 KiB、queue=128、batch=4、in_flight=8 GiB。

## 2. 部署影响

- **新增 option keys**：`langfuse_setting.*`（含 secret，`GET /api/option/langfuse` 只回 `secret_key_configured` 布尔值，通用 options 接口拒绝这些 key）。
- **schema**：`AutoMigrate` 给 `midjourneys` 加了 `token_id`、`billing_channel_id` 两列（bigint，默认 0）。无新表，`logs`（ClickHouse）结构未变。
- **启动顺序耦合**：`8850-new-api` 现在依赖 external 网络 `8858-langfuse-newapi_default`，Langfuse 栈必须先于 new-api 启动。
- **版本号展示**：`/api/status` 的 `version` 由 `v1.0.0-rc.23` 变为空字符串——仓库 `VERSION` 文件为空（上游行为），fork 构建未注入 ldflags。版本识别以镜像 tag 为准。
- **`docker compose up` 必须带 `--no-deps`**：本次 `up -d new-api` 连带重建了 postgres 与 clickhouse。根因是这两个容器相对当前 compose 存在既有 config-hash 漂移（用切换前的 compose 做 dry-run 同样判定需要重建），非本次编辑引入；数据走 bind mount 未受影响，约 10 秒恢复 healthy。漂移现已消除。

## 3. 容量规划

**审计档（当前生效）**：`max_content_bytes=4 MiB`、`max_response_bytes=512 KiB`、`max_in_flight_capture_bytes=8 GiB`、`queue_size=128`、`batch_size=4`、`sample_rate=1.0`。

| 项 | 值 | 约束上限 |
| --- | --- | --- |
| 单次捕获预留 `2×content+response` | 8,912,896 B（8.50 MiB） | 9,000,000（单 span 包络） |
| 并发捕获槽 `in_flight/预留` | 963 | 峰值并发实测约 34（p90 口径约 72） |
| 正文规划 `(queue+3×batch)×预留` | 1.16 GiB | 2 GiB（`queueBodyPlanningLimit`） |
| 最坏导出批体 `batch×预留` | 34 MiB | 实测 Langfuse OTLP 端点 60 MB 仍返回 200 |

预算耗尽时该请求降级为 metadata-only（`ReserveCapture` 返回 nil），不重试、不影响 relay；963 个槽对峰值并发有 13 倍余量，正常不会触发。

**为什么 `queueBodyPlanningLimit` 只到 2 GiB**：理论最大规划量 `(256+3×32)×9,000,000 = 2.95 GiB`。设成 3 GiB 以上这条校验就永远触发不了，变成死规则；2 GiB 既放行审计档配置，又保留实际约束力。

**字节/token 实测**：4.49 B/token（Phase A 四档实测，见 §7）。因此 4 MiB ≈ 93 万 token，覆盖 7 天真实流量的最大值 71.7 万 token；上游模型 `deepseek-v4-flash-0731` 的 1M 上下文硬限会先于捕获上限拒绝请求。

## 4. 回滚

三级，按影响面从小到大：

1. **关开关**：`PUT /api/option/langfuse` 置 `enabled=false`（`secret_key` 传空字符串即保留已存密钥）。exporter 停止，其余功能不受影响。
2. **回镜像**：`compose.yaml` 改回 `image: calciumion/new-api:latest`，去掉 `pull_policy` 与两处 `networks`，`docker compose up -d --no-deps new-api`。`midjourneys` 的两个多余列被上游忽略，**不需要恢复数据库**。
   - 上游镜像锚点：`backups/rollback-anchor-20260815T144457Z.txt`，`calciumion/new-api@sha256:bacbbfbed64b4579213316e0ed78415985223bb20c47fbc24572dd7be5aa1695`；已额外打保险 tag `calciumion/new-api:pre-langfuse-20260815`，防止将来 `pull latest` 后旧镜像变 dangling 被 prune。
3. **恢复库**：`backups/newapi-pre-langfuse-20260815T144457Z.dump`（331 MiB，custom 格式；已用 `pg_restore -l` 与全量 `-f /dev/null` 解压校验，逐表对账 `users` 354 / `tokens` 475 / `channels` 6 / `options` 37 / `logs` 7,633,444 与线上一致）。仅在数据异常时使用。

Langfuse 新栈可独立 `docker compose down` 而不影响 new-api：实测停机 50 秒期间 8/8 relay 请求正常、延迟与基线重叠、扣费三方吻合，导出错误只有一行聚合日志；窗口内 7 个请求有 4 个 trace 恢复后补投成功、3 个 trace（6 span）按设计丢弃。

## 5. 已知限制

- **minio 未发布宿主机端口**，媒体预签名地址保持内网 `http://minio:9000`，Langfuse UI 的媒体预览不可用（本集成上报文本 JSON 正文，不受影响）。
- **Langfuse 跑在 v4 `events_only` 模式**，v3 的 `GET /api/public/traces` 已停用，只能用 `GET /api/public/v2/observations?fromStartTime=&toStartTime=`。且该接口的 `traceName` 字段对本项目恒为空串（trace 名由 root span name 回退，需走 metrics 视图查询），基于该字段的下游查询/告警会失效。
- **Langfuse 数据目录属主**：dockerd 以 root 创建 `data/{minio,clickhouse,clickhouse-logs}`，而 minio 跑 uid 65532、clickhouse 声明 `user: 101:101`，首启前需手动 `chown`。将来 `down -v` 重建需重复这一步。
- **两条验收未覆盖**（真实流量下这两条代码路径从未执行，目前只有单测保障，建议在非生产实例补定向验证）：
  - **多 attempt 归属与 denylist**：本部署 `RetryTimes=0`，attempt 循环上限恒为 1，近 3 天 47,919 条日志中多渠道 0 条；构造需改渠道设置，触碰线上红线。
  - **cache_creation / audio 分桶**：部署内无 Anthropic 语义渠道（7 天 0 条，`claude-opus-5` 实测被映射到 OpenAI 兼容上游），近 30 天 19 个模型中无音频模型。

## 6. 上线前验证结果

| 项 | 结果 |
| --- | --- |
| `go test` 分包（langfuse / langfuseconfig / langfuse_setting / relay·constant） | 全绿 |
| `go test -race ./service/langfuse/...` | 无竞争 |
| 包边界（依赖闭包排除自身） | OK，`new-api/service` 依赖只有 `service/langfuse` 自身 |
| `go build ./...` / `go test ./...` | 90 包 0 失败 |
| `cd relaykit && GOWORK=off go build/test ./...` | 通过 |
| `bun run typecheck` / `bun run build` | 通过 |
| `bun run lint` / `bun test` | **红，但与本分支零交集**：lint 364 error 分布在 163 个既有文件；`bun test` 因 34 个文件用 `node:test` 而 Bun 1.3.14 的 shim 在多文件同跑时误判嵌套 `describe`（只跑 4 个 langfuse 测试文件为 69 pass / 0 fail）。另 `web/src/features/keys/components/__tests__/api-key-group-cell.test.tsx` 单跑也失败 3 条。三项均为既有技术债，建议另开工单。 |

> 计划里抄自 plan-8 的包边界命令有缺陷已就地修正：`-run TestPackageBoundaries` 在本仓库无对应函数（`-run` 空匹配会打印 `PASS` 造成假绿），且其 grep 模式含 `new-api/service` 会匹配被测包自身而永远输出 `VIOLATION`。

## 7. 压力测试（2026-08-15 深夜，模型 `deepseek-v4-flash-0731`）

审计口径的核心问题是"会不会漏"。用专用令牌 `langfuse-stress-20260815`（用后即删）跑了三个阶段，全程 `sample_rate=1.0`、4 MiB 审计档。

**Phase A — 正文尺寸边界**（顺序发送，`max_tokens=1`）：

| 请求体 | 上游 HTTP | 耗时 | Langfuse 捕获字节 | `input_scan_truncated` |
| --- | --- | --- | --- | --- |
| 102,299 B | 200（22,795 tok） | 1.0 s | 102,291 | false |
| 1,048,475 B | 200（233,056 tok） | 11.5 s | 1,048,467 | false |
| 3,145,627 B | 200（699,089 tok） | 48.0 s | 3,145,619 | false |
| 5,242,779 B | 400（超 1M 上下文） | 3.9 s | 4,194,282（= 4 MiB 上限） | **true** |

3 MB 正文完整入库是这次抬上限的直接目的；5 MB 那条同时验证了截断标志与错误请求仍被记录（`level=ERROR`）。

**Phase B — 小正文高并发**：180 请求 / 并发 60 / 正文约 2 KB → 全部 HTTP 200，4.6 s 完成，**39.5 req/s**（生产峰值 2.4 req/s 的 16 倍）。

**Phase C — 大正文并发**：24 请求 / 并发 8 / 正文 1 MiB → 全部 HTTP 200，237 s。

**完整性核对**：

| 指标 | 值 |
| --- | --- |
| 发出请求 | 4 + 180 + 24 = **208** |
| Langfuse root span | **208**（零遗漏） |
| `capture_state=full` | 208 / 208（无预算降级） |
| 输入被截断 | 1（即故意超限的 5 MB） |
| 捕获正文总量 / 单条最大 | 34.0 MB / 4,194,282 B |
| new-api 计费记录 | 207（少的一条是被上游 400 拒绝的 5 MB 请求，无扣费，符合预期） |
| 导出器计数（runtime v2 退休汇总） | `materialized=552 exporter_received=552 exported=552 failed=0 queue_dropped=0` |

**结论**：在 16 倍于生产峰值的速率、以及 8 路并发 1 MiB 正文下，捕获与导出**零丢弃、零降级**。

## 8. 审计完整性的剩余缺口

参数已经调到"正常运行不漏"，但有一类缺口不是调参能消除的，需要知悉：

1. **Langfuse 不可用期间的 span 会丢**。导出器是内存队列 + 有界重试，设计上"绝不阻塞 relay"，因此没有本地持久化缓冲。实测停机 50 秒，窗口内 7 个请求有 3 个 trace（6 span）丢弃。若审计要求"零丢失"，需要在 new-api 与 Langfuse 之间加一层带磁盘持久化队列的 OpenTelemetry Collector（`otlpreceiver` 支持自定义 `traces_url_path`，可原样接住 `/api/public/otel/v1/traces`），由它负责断点续传。
2. **超过 4 MiB 的请求仍会截断**。7 天真实流量中无此类请求（最大 71.7 万 token ≈ 3.2 MB），但这是硬上限：单 span 包络 9,000,000 B 是 Langfuse 9.5 MB 告警线换来的余量，继续抬需要重新评估。
3. **`/v1/embeddings` 与 `/v1/rerank` 不产生 trace**（7 天各 86 / 85 条，占 0.14%）。`formatSupported` 只放行 OpenAI ChatCompletions、Claude Messages、OpenAI Responses、Gemini 四类。若审计范围包含嵌入/重排的输入文本，需要扩展该白名单。
4. §5 里那两条"真实流量下从未执行过"的代码路径（多 attempt 归属、Anthropic cache/音频分桶）依然只有单测保障。
