# 8850 部署切换到 fork 构建并启用 Langfuse

日期：2026-08-15（2026-08-16 追加 §9：生产对账与流式截断修复）
执行计划：`docs/superpowers/plans/2026-08-15-langfuse-local-deployment.md`
设计文档：`docs/superpowers/designs/2026-08-15-langfuse-local-deployment-design.md`
验收报告：`docs/internal/e2e/2026-08-15-langfuse-8850-traffic-acceptance.md`

> **这份台账记的是"这套部署怎么上的线"，不是内部 patch 条目。** 特性本身的 patch 台账在
> `docs/internal/patches/feature-langfuse-tracing.md`，那份按 `README.md` 的
> `patches/<分支名>.md` 约定命名并登记在总表里；本文按日期命名，是同一个特性的部署与运维记录，
> 没有进总表。两份内容互补，读的时候一起看：那份讲代码改了什么、要不要提上游，本文讲
> 8850 这套环境实际怎么配、容量怎么算、出过什么问题、怎么回滚。

## 1. 变更内容

- `/home/flintylemming/appdata/8850-new-api` 的 `new-api` 容器从上游镜像 `calciumion/new-api:latest`（revision `0ab02020`，2026-08-01 构建）切换到本机构建的 fork 镜像。首次上线为 `new-api:langfuse-750452c0`（image `9c8570cf79f0`，217 MB，代码 revision `750452c0`，领先上游 73 个 commit）；当晚因审计需求抬高正文上限后重建为 `new-api:langfuse-02dc4189`（image `07d309327292`，217 MB）；次日修复流式响应缓冲区后重建为 `new-api:langfuse-0b05ce5c`（image `fd872dd9091a`）；当天并入远端分支后再次重建为 **`new-api:langfuse-b8120389`**（image `6f52df4326b4`），这是当前运行的镜像，详见 §9。
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

**审计档（2026-08-16 起生效）**：`max_content_bytes=4 MiB`、`max_response_bytes=64 MiB`、`max_in_flight_capture_bytes=32 GiB`、`queue_size=128`、`batch_size=4`、`sample_rate=1.0`。（08-15 的初版是 response=512 KiB、in_flight=8 GiB，为什么改见 §9。）

**两个预算是不同的量，不要再合并**：

- **单 span 正文 = `2×content`**，因为一个 span 只带一份 input 加一份 output，两者都已被 `prepareContent` 压到 `max_content_bytes`。
- **单次捕获预留 = `2×content + response`**，是这个请求占住的内存。`response` 缓冲的是带帧的原始 SSE，worker 聚合成 output 后就丢弃，**从来不进 span**。

| 项 | 表达式 | 值 | 约束上限 |
| --- | --- | --- | --- |
| 单 span 正文 | `2×content` | 8,388,608 B（8.00 MiB） | 9,000,000（`LANGFUSE_OTEL_MAX_SPAN_BYTES` 默认值下的余量） |
| 单次捕获预留 | `2×content+response` | 75,497,472 B（72 MiB） | 仅受 `in_flight` 约束 |
| 并发捕获槽 | `in_flight/预留` | 455 | 峰值并发实测约 34（p90 口径约 72） |
| 正文规划 | `(queue+3×batch)×span正文` | 1.09 GiB | 2 GiB（`queueBodyPlanningLimit`） |

预算耗尽时该请求降级为 metadata-only（`ReserveCapture` 返回 nil），不重试、不影响 relay；455 个槽对 p90 并发有 6 倍余量，正常不会触发。最坏实际内存 `455×72 MiB ≈ 32 GB`，宿主机 2 TB。

**为什么 `queueBodyPlanningLimit` 只到 2 GiB**：理论最大规划量 `(256+3×32)×(2×4 MiB) = 2.75 GiB`。设成 2.75 GiB 以上这条校验就永远触发不了，变成死规则；2 GiB 既放行审计档配置，又保留实际约束力。

**单 span 包络为什么没有运行时检查**：`max_content_bytes` 的字段上限（4 MiB）本身就保证了 `2×content ≤ 9,000,000`，等 `Validate` 跑到包络那一步，该条件已恒成立，写成运行时检查就是死代码。改为编译期声明 `const _ = uint(maxQueuedSpanBytesLimit - 2*maxContentBytes)`——将来谁抬 `maxContentBytes` 会直接编译失败。

**字节/token 实测**：4.49 B/token（Phase A 四档实测，见 §7）。因此 4 MiB ≈ 93 万 token，覆盖 7 天真实流量的最大值 71.7 万 token；上游模型 `deepseek-v4-flash-0731` 的 1M 上下文硬限会先于捕获上限拒绝请求。

## 4. 回滚

三级，按影响面从小到大：

1. **关开关**：`PUT /api/option/langfuse` 置 `enabled=false`（`secret_key` 传空字符串即保留已存密钥）。exporter 停止，其余功能不受影响。
2. **回上一版 fork 镜像**（只撤 §9 的流式缓冲区改动）：`compose.yaml` 改回 `image: new-api:langfuse-02dc4189`（`07d309327292`，仍在本地），`docker compose up -d --no-deps new-api`，再把两个 option 回写成 `max_response_bytes=524288`、`max_in_flight_capture_bytes=8589934592`（旧值存于 `backups/langfuse-pre-streambuffer-20260816T113133Z.txt`）。**顺序不能反**：旧镜像的校验器会拒绝 64 MiB 的 `max_response_bytes`。中间版本 `new-api:langfuse-0b05ce5c`（`fd872dd9091a`）也还在本地，但它缺远端的 attempt 分类修复，除非专门要隔离 §9.6 的合并，否则不要回到它。
3. **回上游镜像**：`compose.yaml` 改回 `image: calciumion/new-api:latest`，去掉 `pull_policy` 与两处 `networks`，`docker compose up -d --no-deps new-api`。`midjourneys` 的两个多余列被上游忽略，**不需要恢复数据库**。
   - 上游镜像锚点：`backups/rollback-anchor-20260815T144457Z.txt`，`calciumion/new-api@sha256:bacbbfbed64b4579213316e0ed78415985223bb20c47fbc24572dd7be5aa1695`；已额外打保险 tag `calciumion/new-api:pre-langfuse-20260815`，防止将来 `pull latest` 后旧镜像变 dangling 被 prune。
4. **恢复库**：`backups/newapi-pre-langfuse-20260815T144457Z.dump`（331 MiB，custom 格式；已用 `pg_restore -l` 与全量 `-f /dev/null` 解压校验，逐表对账 `users` 354 / `tokens` 475 / `channels` 6 / `options` 37 / `logs` 7,633,444 与线上一致）。仅在数据异常时使用。

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

1. **Langfuse 不可用期间、以及 new-api 自身重启时，在途的 span 会丢**。导出器是内存队列 + 有界重试，设计上"绝不阻塞 relay"，因此没有本地持久化缓冲。实测 Langfuse 停机 50 秒，窗口内 7 个请求有 3 个 trace（6 span）丢弃；08-16 换镜像重启也丢了 1 条（`202608161130528060176708268d9d69IJM8JRD`，重启瞬间正在进行的流式请求，new-api 日志有、Langfuse 无）。**每次换镜像都要按这个量级预期。** 若审计要求"零丢失"，需要在 new-api 与 Langfuse 之间加一层带磁盘持久化队列的 OpenTelemetry Collector（`otlpreceiver` 支持自定义 `traces_url_path`，可原样接住 `/api/public/otel/v1/traces`），由它负责断点续传。
2. **超过 4 MiB 的请求正文仍会截断**（指输入侧）。7 天真实流量中无此类请求（最大 71.7 万 token ≈ 3.2 MB）。继续抬需要同时抬 `LANGFUSE_OTEL_MAX_SPAN_BYTES`——08-16 实测该值只触发告警不拦截（见 §9），所以不是硬墙，但会让一个原版 Langfuse 开始记 warn 日志。
   - ~~流式响应超过 512 KiB 原始 SSE 会丢最终答案~~ —— 08-16 已修复，见 §9。
3. **`/v1/embeddings` 与 `/v1/rerank` 不产生 trace**（7 天各 86 / 85 条，占 0.14%）。`formatSupported` 只放行 OpenAI ChatCompletions、Claude Messages、OpenAI Responses、Gemini 四类。若审计范围包含嵌入/重排的输入文本，需要扩展该白名单。
4. §5 里那两条"真实流量下从未执行过"的代码路径（多 attempt 归属、Anthropic cache/音频分桶）依然只有单测保障。

## 9. 2026-08-16：生产对账与流式截断修复

上线满一天后按审计口径做了一次全量对账，发现一个 §7 的压测没能暴露的缺口，当天修复并重新上线。

### 9.1 对账方法与结果

`DeriveTraceID`（`service/langfuse/idgen.go:19`）是确定性的 `SHA256(request_id)[:16]`，`logs` 表又有 `request_id` 列，所以两库可以逐条精确对账，不靠时间窗猜：

```sql
-- Langfuse 侧（ClickHouse，events_only 模式下读 events_full）
SELECT metadata_values[indexOf(metadata_names,'request_id')]
FROM events_full WHERE has(metadata_names,'attributes.langfuse.internal.as_root')
-- new-api 侧
SELECT request_id FROM newapi.logs WHERE type IN (2,5)
-- 推导关系可直接在 ClickHouse 里验：
--   trace_id = lower(hex(substring(SHA256(request_id),1,16)))
```

配置定型（08-15 16:30 UTC）之后到 08-16 11:00 UTC，**117 个连续 10 分钟桶全部 100.0% 覆盖**，5,319 条请求里只差 6 条 embedding/rerank（§8.3 的已知设计限制）。反向也干净：Langfuse 里没有任何一条 trace 在 new-api 日志里找不到对应。

16:30 之前的 296 条未采集是上线过程本身造成的——15:14 前尚未启用，15:35–16:25 处于 §1 提到的 `sample_rate=0.1` 试验期，覆盖率在那两段分别是 0% 和 5%–15%，与采样率吻合。

### 9.2 发现的缺口：流式响应丢的正是最终答案

稳定窗口内 5,269 条 trace 有 **43 条 `content_truncated=true`**，全部 `is_stream=true`。其中 **39 条的 `content` 字段完全为空**——不是"答案被截了一截"，是**一个字都没有**：

| 模型 | 条数 | `content` 为空 | reasoning 存了 | content 存了 |
| --- | --- | --- | --- | --- |
| llm-lite | 31 | 30 | 231,648 B | **365 B** |
| deepseek-v4-flash-0731 | 11 | 8 | 219,671 B | 36,541 B |
| llm-prime | 1 | 1 | 26,092 B | 0 B |

对照组（未截断的流式）里 `content` 为空的 1,012 条中有 988 条带 `tool_calls`，属正常；这 42 条截断的里连 `tool_calls` 都没有。

**根因是两层：**

1. `CaptureWriter` 缓冲的是**带帧的原始 SSE 字节**，聚合是流结束后在这个缓冲区上跑的。实测框架开销约 **67 原始字节 / 输出字符**（每 token 一帧，每帧一整个 chunk JSON 信封），所以 512 KiB 只换回约 7,800 字符正文。触发阈值约 2,400 completion token（观测最小值 2,439）。
2. **reasoning 先于 content 流出**，预算被推理过程吃光时，答案的 delta 一个都还没到。这就是为什么丢的恰恰是最想留的那部分。

`max_response_bytes` 当时被卡在 512 KiB 不是偶然：`Validate` 把**单 span 包络**和**单请求内存预留**当成同一个表达式 `2×content+response` 校验，content 一到 4 MiB，包络 9,000,000 就只剩 611 KiB 留给缓冲区。但原始 SSE 字节被 worker 聚合成 output 后就丢弃了，聚合结果由 `max_content_bytes` 约束——**那些字节从来没进过 span**。把内存量算进了 span 包络，这是这个缺口的直接来源。

### 9.3 修复前对 Langfuse 上限做的实测

抬上限之前先验证不违背 Langfuse 的设计。三层证据：

- **官方文档**：[API limits](https://langfuse.com/faq/all/api-limits) 自托管写明 "No hard limits"（Cloud 是 5MB/请求），对 observation 正文大小**零建议、零劝阻**；[Scaling](https://langfuse.com/self-hosting/configuration/scaling) 唯一提到大正文的地方在 "Increasing Disk Usage"，把它当**容量与保留策略问题**处理，给的是 retention policy / blob 生命周期 / ClickHouse TTL。
- **代码演进方向**：读侧上限 v3 是写死的 `PAYLOAD_SIZE_LIMIT = 4e6`，本部署跑的 4.2.0 已换成 env `LANGFUSE_API_TRACE_OBSERVATIONS_SIZE_LIMIT_BYTES`，**默认 `8e7` = 80 MB**。写侧 `LANGFUSE_OTEL_MAX_SPAN_BYTES` 默认 `95e5` = 9,500,000，schema 是 `.positive()` 无上限。
- **本机实测**（合成 OTLP span，正文尾部埋标记逐条校验，测完已按 §9.5 清除）：

  | 正文 | 落库字节 | 尾部标记 | Langfuse 反应 |
  | --- | --- | --- | --- |
  | 1 MB / 8 MB / 9.4 MB | 完整 | ✓ | 无 |
  | 10 MB | 10,485,756 | ✓ | `warn "OTEL oversized span detected"` |
  | 20 MB | 20,971,516 | ✓ | 同上 + web `warn "OTEL request body exceeds 16MB"` |

  **超过 `LANGFUSE_OTEL_MAX_SPAN_BYTES` 只是告警，不丢弃、不截断。** 那个 9,500,000 是可调阈值，不是协议常量——我们把它固化成了自己的硬上限。

- **UI 可读性**（无头浏览器实测）：三条大 trace 加载 2.4–2.9 s、堆内存 96–134 MB、无崩溃。Langfuse 对大字符串有**一等公民的处理**：页面显示 `Large string — 4.2M characters, truncated to keep the tab responsive` 加一个 **Download full value** 按钮。点下载实测拿到真实 trace 的 4,194,282 B（141 ms）与 20 MB 合成 trace 的 20,971,516 B（346 ms），尾部标记完好。**它不是勉强容忍大正文，是专门为大正文建了预览 + 完整下载的路径。**

### 9.4 改动与上线

代码 commit `0b05ce5c`（rebase 后为 `84db94f6`），首次上线镜像 `new-api:langfuse-0b05ce5c`（`fd872dd9091a`）。当天并入远端分支后重新构建部署，见 §9.6。

- `setting/langfuse_setting/config.go`：拆开两个预算的校验（span 包络与队列规划用 `2×content`，预留只受 `in_flight` 约束）；`maxResponseBytes` 上限 8 MiB → 64 MiB；包络检查改为编译期声明（理由见 §3）。
- 前端 `langfuse-schema.ts` 镜像同步；`langfuse-capacity.ts` 新增 `spanBodyBytes`，队列规划从 reservation 切到 span body（否则 64 MiB 缓冲会让"导出中的 span 正文"虚报到 9 GiB）；UI 输入框 `max` 属性与 7 个语种的三条文案同步。
- 上线：`docker compose up -d --no-deps new-api`（11:33:13 UTC），约 1 分钟 healthy；`PUT /api/option/langfuse` 把 `max_response_bytes` 设为 67108864、`max_in_flight_capture_bytes` 设为 34359738368，回读确认；`langfuse runtime version 1 retired, drain complete: materialized=4 exported=4 failed=0 queue_dropped=0`。
- 重启代价：丢 1 条在途 trace，已记入 §8.1。重启后至 13:40 UTC 的 **265 条请求 100% 有 trace**。

**上线后的验证仍不完整**：低谷期没有等到超过旧阈值（约 2,400 token）的流式响应。已观测到的最大值是一条 1,499 token 的流式 deepseek（`content` 4,940 B + reasoning 1,022 B，`content_truncated=false`）和一条 4,732 token 的**非流式** llm-lite（`content` 8,280 B + reasoning 7,682 B，完整）——两条都不经过出问题的那条路径。**要在高峰期（16:40–02:00 UTC）复核一次**：查 `content_truncated=true` 应恒为 0，且 completion_tokens > 3,000 的流式请求 `content` 非空。

### 9.5 对账中踩到的坑

- **root span 与 generation span 携带的字段不同**：`usage_details` / `cost_details` 只在 generation span 上，root span 恒为空。按 root span 查 usage 会得到"全空"的假象，误判成回归。区分靠 `has(metadata_names,'attributes.langfuse.internal.as_root')`。
- **span 命名**：root 是 `relayFormat + " " + originModel`（如 `openai deepseek-v4-flash-0731`），generation 是渠道名 + 模型（如 `[H200] Deepseek V4 Flash deepseek-v4-flash-0731`）。别按名字前缀猜哪个是 root。
- **测试数据清理**：§9.3 的 5 条合成 trace 写在 `environment=size-test`（与 `production` 隔离，不污染审计集），验证后用 `DELETE /api/public/traces`（Basic 认证，body `{"traceIds":[...]}`）删除，实测该接口在 v4 仍可用，返回 `Traces deleted successfully`；删后 `environment=size-test` 剩 0 行，production 11,708 行未受影响。

### 9.6 并入远端分支后重新部署

§9.4 那次上线的镜像是从一条**落后于远端**的本地分支构建的。推送时才发现 `feature/langfuse-tracing` 已经分叉：本机自 `750452c0` 推送后就没再 fetch，而在这之后 7.5 小时（08-15 07:05–09:45 UTC）另一个环境往同一分支推了 4 个提交。两条线都从 `750452c0` 出发，互不知情。

**运维后果**：`new-api:langfuse-0b05ce5c` 缺 `7a175d14 fix(langfuse): classify an attempt by its error, not by the provider's code`——一个 `service/langfuse` 的真实修复。所以那个镜像只在生产上跑了约 4 小时就被替换。

整合方式是 rebase 到远端之上（9 个提交一次都没推过，重写无风险），冲突只有一处：双方各自新建了 `docs/internal/e2e/2026-08-15-langfuse-e2e-report.md`，内容是两份不同的报告。本机那份改名为 `2026-08-15-langfuse-8850-traffic-acceptance.md`，远端那份保持原路径。两份报告与两份台账现在互相交叉引用，说明各自验的是什么。

另外修了一处 git 看不见的破坏：远端的 `service/langfuse/otlp_envelope_test.go` 里 `queuedSpanBytes()` 写死 `2*content+response`，正是本次删掉的混淆模型；它那张"per-field 边界"表也已过期两轮（content 写 1 MiB、response 写 8 MiB）。改成 `2*content` 并把两行边界调到真实上限后，记录到的最大 span protobuf 从约 2 MB 变成 **8,389,635 B**，仍在 9,000,000 包络内——旧表把入口要承受的尺寸低估了 4 倍。代价是该包测试从约 9s 变成约 36s。

**重新部署**：镜像 `new-api:langfuse-b8120389`（`6f52df4326b4`），15:38:03 UTC 启动，约 1 分钟 healthy。

- **配置无需重下**：两个 option 存在库里，新校验器照常接受，重启后回读 `max_response_bytes=67108864`、`max_in_flight_capture_bytes=34359738368` 均在位，`secret_key_configured=true`。
- **本次重启零丢失**：窗口内 3 个请求（15:37:06 / 15:37:08 / 15:38:08）全部有完整的 root + generation 双 span。与 §9.4 那次丢 1 条的差别在于这 3 条都是短的非流式请求，能在进程退出前跑完并 drain 完；§8.1 那条限制本身没有改变。
- 重启后至 15:47 UTC 的请求 100% 有 trace。


## 10. 2026-09-09 rc.36 镜像上线

- 源码：`internal-custom` / `2422942fc`，包含合并提交 `a72e8c116`，上游基线 `v1.0.0-rc.36` / `ea7cb0ba4`。补丁取舍见 [rc.36 同步记录](../sync-rc36-2026-09-09.md)。
- 旧镜像：`new-api:rc30-d921bc998`，image `sha256:3cf5fe1d624b5bd5c5281129a7bed8bf61880dd7a81fbbe02193c3b925648e3d`，仍保留本地。
- 新镜像：`new-api:rc36-2422942fc`，image `sha256:82eaafb9b0fe2d9cb1ad77ed5d72ffb93ec47a583a57fafc3a893bd49436c9e2`。
- 用 `git archive HEAD` 导出干净的构建上下文到 `/tmp/new-api-rc36-image-2422942fc`，仅在构建上下文写入 `VERSION=v1.0.0-rc.36-internal-2422942fc`。仓库 VERSION 和用户未跟踪文件不变。沿用仓库 Dockerfile 的固定 digest 多阶段构建，注入 OCI revision/version 标签。构建日志 `/tmp/new-api-rc36-image-build.log`。
- 备份目录：`/home/flintylemming/appdata/8850-new-api/backups/rc36-20260909/`。包含 `compose.before.yaml`、PostgreSQL 完整逻辑备份 `postgres-before.dump`（429543992 字节，`pg_restore --list` 成功）、`postgres-contents.txt`、ClickHouse schema、切换前容器 ID；目录权限 700、文件 600。ClickHouse 原有日志数据没有导出副本；本次启动创建独立 audit 表。
- 仅将 `/home/flintylemming/appdata/8850-new-api/compose.yaml` 中应用服务的 image 替换为新 tag，保留 `pull_policy: never`、端口、挂载、网络和配置。
- 执行 `docker compose -f /home/flintylemming/appdata/8850-new-api/compose.yaml up -d --no-deps --wait --wait-timeout 180 new-api`。应用于 10:07 UTC 启动，数据库迁移及服务初始化约 2.5 秒完成，随后 healthcheck healthy。
- 验证：`http://127.0.0.1:8850/api/status` 返回 success=true、version=`v1.0.0-rc.36-internal-2422942fc`；首页和 HTML 引用的 7 个静态资源 HTTP 200。新容器 `02e90d30a67a`，重启次数 0。PostgreSQL、ClickHouse、Redis、8858 Langfuse web/worker 的容器 ID 与切换前一致。
- 本次执行构建、迁移、健康及静态资源验证，没有发起付费模型探测或重新进行 Langfuse E2E。代码级回归与三数据库矩阵见同步记录。本次部署已授权执行；没有推送 Git 或镜像到远端。

回滚应用镜像：将 compose 的 `image: new-api:rc36-2422942fc` 改回 `image: new-api:rc30-d921bc998`，再执行同一 `up -d --no-deps --wait` 命令。不要自动恢复数据库备份：升级期间新写入的数据需要保留，schema 降级应单独核对；完整逻辑备份是恢复依据，不是无损即时回滚的承诺。


## 11. 2026-09-12 重置卡 + 缓存统计口径镜像上线

- 源码：`internal-custom` / `e6b8fc10d`（merge 提交），在 rc36 镜像（`2422942fc`）之上新增 21 个提交：订阅重置卡全链路（model/controller/web，12 个提交，`feat/subscription-reset-card` 已全部并入）+ 用量统计缓存口径（`feature/usage-stats-cache-caliber`，5 个提交，本次以 `--no-ff` 并入，冲突仅 2 个自动生成的 i18n untranslated 报告，已用 `bun run i18n:sync` 重新生成解决）。
- 旧镜像：`new-api:rc36-2422942fc`，image `sha256:82eaafb9b0fe2d9cb1ad77ed5d72ffb93ec47a583a57fafc3a893bd49436c9e2`，仍保留本地。
- 新镜像：`new-api:rc36-e6b8fc10d`，image `sha256:a63c1c2698c3ab36c97ef7ebf0352ae6dbcf4ad17d0d8f7f77e31c826cf36dd2`。沿用 §10 方式：`git archive HEAD` 导出干净上下文到 `/tmp/new-api-rc36-image-e6b8fc10d`，仅上下文内写入 `VERSION=v1.0.0-rc.36-internal-e6b8fc10d`，注入 OCI revision/version 标签。构建日志 `/tmp/new-api-rc36-image-build-e6b8fc10d.log`。
- 数据库验证（部署前彩排）：生产 PG（postgres:15，8851）上建 `newapi_migtest` 库并导入生产 schema-only 拷贝（37 表）；用本次代码本地构建的二进制连该库启动两次（幂等），均一次迁移成功无报错。结果：仅新增 `subscription_reset_cards` 表（含 pkey + 2 个普通索引）；public schema 的 UNIQUE 约束与生产完全一致（生产侧 pgloader 遗留 `idx_<id>_*` 约束未被触碰）；彩排库多出的 `audit_logs` 系彩排未设 `LOG_SQL_DSN` 导致审计表落到主库，生产走 ClickHouse 不受影响。彩排后 `newapi_migtest` 已删除。本次无 go.mod/go.sum 变更，无 GORM/驱动版本变化。
- 合并后回归：`go build ./...` 通过；`go test ./service/ ./setting/... ./model/ ./controller/` 全绿；前端 typecheck 通过、受影响测试（caliber 2 文件 5 用例、reset-card 2 文件 5 用例）通过、`bun run build` 成功。`bun run lint` 有 2 个上游既存 error（`scripts/sync-i18n.mjs`、`src/features/rankings/index.tsx`），与本次变更无关，未处理。
- 备份目录：`/home/flintylemming/appdata/8850-new-api/backups/rc36-e6b8fc10d-20260912/`。包含 `compose.before.yaml`、PostgreSQL 完整逻辑备份 `postgres-before.dump`（434487782 字节，`pg_restore --list` 成功，722 个对象）、`postgres-contents.txt`、切换前容器 ID；目录权限 700、文件 600。ClickHouse 数据未导出（本次不变更日志库）。
- 仅将 `/home/flintylemming/appdata/8850-new-api/compose.yaml` 中应用服务的 image 替换为新 tag，其余配置不动。执行 `docker compose -f ... up -d --no-deps --wait --wait-timeout 180 new-api`。应用于 10:46 UTC 启动，迁移约 2 秒完成，healthcheck healthy。
- 验证：`/api/status` 返回 version=`v1.0.0-rc.36-internal-e6b8fc10d`；生产库确认 `subscription_reset_cards` 已建；首页与 7 个静态资源 HTTP 200；新容器 `6f82c4b263f0`，重启次数 0；PostgreSQL、ClickHouse、Redis 未被重建。未发起付费模型探测；重置卡端到端核销未在真实数据上演习。本次部署已授权执行；没有推送 Git 或镜像到远端。

回滚应用镜像：将 compose 的 `image: new-api:rc36-e6b8fc10d` 改回 `image: new-api:rc36-2422942fc`，再执行同一 `up -d --no-deps --wait` 命令。注意：回滚镜像不会删除已创建的 `subscription_reset_cards` 表（旧代码不感知该表，保留无害）；数据库逻辑备份是恢复依据，不是无损即时回滚的承诺。


## 12. 2026-09-14 镜像发布改走 GHCR（重置卡持卡人用户名）

**这一节改的是"镜像从哪来"，不是一次上线记录。** 截至写下本节，镜像尚未构建、尚未部署，没有 digest 可记；实际上线后另起一节按 §10/§11 的格式补。

- 代码：重置卡管理列表展示持卡人用户名（原来只有 `user_id`）。分支 `claude/reset-card-list-display-7d7qek`（`286e1bd`）已 `--no-ff` 合入 `internal-custom`，合并提交 `fcf8e49`，已推送远端。后端 `SubscriptionResetCard` 新增瞬态字段 `Username`（tag 为 `gorm:"-"`，只序列化不落库），由 `GetAllSubscriptionResetCards` / `SearchSubscriptionResetCards` 按当前页批量补全；**无表结构、无迁移、无索引变更**，回滚镜像不涉及 schema 处理。
- 发布方式变更：`.github/workflows/docker-build.yml`（tag 触发）与 `.github/workflows/docker-image-branch.yml`（手动指定分支）原本推上游的公开仓库 `calciumion/new-api`，本 fork 既没有推送权限，推上去也不合适。两个 workflow 均改为推本仓库自己的 GHCR：`ghcr.io/flintylemming/new-api-enterprise`。镜像名由 `$GITHUB_REPOSITORY` 小写化后拼出，不再硬编码。
- 认证与权限：登录从 `secrets.DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` 换成 `ghcr.io` + `github.actor` + 内置 `secrets.GITHUB_TOKEN`，**不再需要配置 Docker Hub secrets**。构建与 manifest 两个 job 都补上了 `packages: write`（manifest job 原先只有 `id-token: write`，推 GHCR 会 403）。cosign keyless 签名保留，签名同样写进 GHCR。
- tag 规则不变：分支构建产出 `<分支名>`（如 `internal-custom`）与 `<分支名>-<日期>-<短 sha>`（如 `internal-custom-20260914-fcf8e49`）两个 manifest，外加每架构的 `-amd64` / `-arm64`；tag 构建产出 `<tag>` 与 `latest`。
- 部署机取镜像：GHCR 包随私有仓库默认私有，8850 那台机器需要先 `docker login ghcr.io -u <用户名> -p <PAT>`（PAT 至少 `read:packages`），再 `docker pull ghcr.io/flintylemming/new-api-enterprise:<tag>`。compose 里 `pull_policy: never` 是按本机构建镜像设的，改用 GHCR 后要么先手动 `docker pull` 再保持 `never`，要么把该服务的 `pull_policy` 放开；两者都不要顺手改动其他服务。
- §10/§11 的本机 `git archive` + `docker build` 方式继续有效，作为 CI 不可用或不想走 registry 时的兜底；那种方式产出的 tag 形如 `new-api:rc36-<短 sha>`，与 GHCR 的 tag 命名不冲突。
- 本次没有触发任何构建：远程会话的出网策略拒绝了 Docker Hub 的 blob CDN（`production.cloudfront.docker.com:443` 返回 403），基础镜像拉不下来，本地构建在第一层就失败，因此镜像改由 CI 或部署机产出。

**首次 GHCR 构建（2026-09-14 14:29–14:37 UTC）**：手动触发 `Publish Docker image (manual branch)`，输入分支 `internal-custom`，源码 `5564680`（即上面 `fcf8e49` 加本次 workflow 改动）。[run 34855970732](https://github.com/FlintyLemming/new-api-enterprise/actions/runs/34855970732) 四个 job 全部成功：arm64 约 4 分 20 秒、amd64 约 6 分 20 秒（首次构建无 gha 缓存），manifest 合并与 cosign 签名正常。产出 tag：

- `ghcr.io/flintylemming/new-api-enterprise:internal-custom`
- `ghcr.io/flintylemming/new-api-enterprise:internal-custom-20260914-5564680`
- 以及两者的 `-amd64` / `-arm64` 单架构 tag

各架构 image digest 见该 run 的 summary。GHCR 登录、推送、`packages: write` 权限、cosign keyless 签名均已在真实运行中验证通过，不再需要 Docker Hub secrets。**镜像尚未部署到 8850**，本次只做构建。
