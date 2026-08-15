# 8850 部署切换到 fork 构建并启用 Langfuse

日期：2026-08-15
执行计划：`docs/superpowers/plans/2026-08-15-langfuse-local-deployment.md`
设计文档：`docs/superpowers/designs/2026-08-15-langfuse-local-deployment-design.md`
验收报告：`docs/internal/e2e/2026-08-15-langfuse-e2e-report.md`

## 1. 变更内容

- `/home/flintylemming/appdata/8850-new-api` 的 `new-api` 容器从上游镜像 `calciumion/new-api:latest`（revision `0ab02020`，2026-08-01 构建）切换到本机构建的 `new-api:langfuse-750452c0`（image `9c8570cf79f0`，217 MB，源自 `feature/langfuse-tracing` 的代码 revision `750452c0`，领先上游 73 个 commit）。
- 新建专用 Langfuse 实例 `/home/flintylemming/appdata/8858-langfuse-newapi`（Langfuse 4.2.0，六服务，仅发布 `8858->3000`），org `newapi` / project `new-api-8850`。既有的 `18081-langfuse`（服务 agentgateway 项目）未做任何改动。
- `8850-new-api/compose.yaml` 改动两处：`image` + `pull_policy: never`；`new-api` 服务显式声明 `networks: [default, langfuse]`，顶层新增 external 网络 `8858-langfuse-newapi_default`。
- Langfuse 集成经 root 专用接口 `PUT /api/option/langfuse` 启用：`host=http://langfuse-web:3000`、`environment=production`、`send_content=true`、容量参数全部取默认值。采样率先以 `1.0` 完成验收，随后收敛到常态 `0.1`。

## 2. 部署影响

- **新增 option keys**：`langfuse_setting.*`（含 secret，`GET /api/option/langfuse` 只回 `secret_key_configured` 布尔值，通用 options 接口拒绝这些 key）。
- **schema**：`AutoMigrate` 给 `midjourneys` 加了 `token_id`、`billing_channel_id` 两列（bigint，默认 0）。无新表，`logs`（ClickHouse）结构未变。
- **启动顺序耦合**：`8850-new-api` 现在依赖 external 网络 `8858-langfuse-newapi_default`，Langfuse 栈必须先于 new-api 启动。
- **版本号展示**：`/api/status` 的 `version` 由 `v1.0.0-rc.23` 变为空字符串——仓库 `VERSION` 文件为空（上游行为），fork 构建未注入 ldflags。版本识别以镜像 tag 为准。
- **`docker compose up` 必须带 `--no-deps`**：本次 `up -d new-api` 连带重建了 postgres 与 clickhouse。根因是这两个容器相对当前 compose 存在既有 config-hash 漂移（用切换前的 compose 做 dry-run 同样判定需要重建），非本次编辑引入；数据走 bind mount 未受影响，约 10 秒恢复 healthy。漂移现已消除。

## 3. 容量规划

默认参数下（`max_content_bytes=64 KiB`、`max_response_bytes=512 KiB`、`max_in_flight_capture_bytes=512 MiB`、`queue_size=64`、`batch_size=16`）：

| 项 | 值 |
| --- | --- |
| 单次捕获预留 | `2×64 KiB + 512 KiB = 640 KiB` |
| 并发捕获槽 | `512 MiB / 640 KiB = 819` |
| 常驻规划值 | `512 MiB + 64×640 KiB ≈ 552 MiB` |
| 导出峰值 | 常驻值 + `3×batch_size` 个 span 体 |

预算耗尽时该请求降级为 metadata-only（`ReserveCapture` 返回 nil），不重试、不影响 relay。全采样阶段 17 分钟四次采样，new-api 常驻内存 46–54 MiB，无单调增长。

**待观察**：真实请求实测最大 input 为 63,186 B，已达 `max_content_bytes` 的 96.4%。当前 0 条截断，更长对话会开始命中 `content_truncated`。若不接受截断，把 `max_content_bytes` 提到 128 KiB 即可（单次预留升到 768 KiB，槽位降到 682）。

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
