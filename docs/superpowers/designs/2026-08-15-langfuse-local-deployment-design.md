# 8850 本机部署切换到 fork 构建并启用 Langfuse

日期：2026-08-15
状态：已确认，待实施

## 1. 目标

把 `/home/flintylemming/appdata/8850-new-api` 这套运行中的 new-api 从上游镜像切换到 `feature/langfuse-tracing` 的本机构建，接入一套专用的新 Langfuse 实例，并在真实流量下完成 Langfuse 链路验收。

不做（YAGNI）：

- 不修改现有 `18081-langfuse`（属于 agentgateway 项目）；
- 不配公网域名与反向代理；
- 不引入镜像仓库、不改 CI（`.github/workflows` 保持原样）；
- 不在本次改动任何 new-api 业务代码。

## 2. 现状

**部署**：compose 项目 `8850-new-api`，`new-api` 容器运行 `calciumion/new-api:latest`（revision `0ab02020`，构建于 2026-08-01），端口 `8850->3000`；主库 PostgreSQL（`new-api-postgres`），日志库 ClickHouse（`new-api-clickhouse`，经 `LOG_SQL_DSN`），缓存 Redis。网络 `8850-new-api_default`。

**源码**：`/mnt/extend/Projects/new-api`，分支 `feature/langfuse-tracing`，HEAD `750452c0`，领先已部署 revision **73 个 commit / 171 个文件**（Langfuse、`cache_prompt_token_semantic`、若干计费与并发修复）。

**现有 Langfuse**：`18081-langfuse`，`ghcr.io/langfuse/langfuse:4.2.0`，`NEXTAUTH_URL=https://langfuse.ai.liandisys.com.cn`，已初始化 org `liandisys` / project `agentgateway`。本次不复用其凭证、网络与数据。

**配置面**：Langfuse 集成没有环境变量开关，全部是 DB option（`langfuse_setting.*`），经 root 专用接口 `GET/PUT /api/option/langfuse` 与系统设置的集成面板读写；这些 key 被通用 options 接口排除，secret 不出现在任何 GET 响应里。

**schema 影响**：核对 `0ab02020..HEAD` 的 `model/`——`AutoMigrate` 列表未变、无新表，新增列仅 `midjourney.token_id` 与 `midjourney.billing_channel_id`（均带 `default:0`），另有若干 `options` 行；ClickHouse 的 `logs` 表结构未变。因此镜像可直接回滚到上游，多余列被上游忽略。

## 3. 架构与变更面

三块互相独立的改动：

- **A. 新 Langfuse 栈**（新增目录，不触碰 18081）
- **B. new-api 镜像替换**（改 `8850-new-api/compose.yaml` 两处）
- **C. 启用配置**（不改 compose，纯 DB option + UI）

## 4. A：新 Langfuse 栈

目录 `/home/flintylemming/appdata/8858-langfuse-newapi/`，包含 `docker-compose.yml`、`.env`、`data/`。compose 项目名取目录名 `8858-langfuse-newapi`，因此网络为 `8858-langfuse-newapi_default`，容器名为 `8858-langfuse-newapi-<service>-1`，与 18081 那套不冲突（两套 compose 均未写 `container_name`）。

以 `18081-langfuse/docker-compose.yml` 为模板，差异：

1. 镜像固定 `ghcr.io/langfuse/langfuse:4.2.0` 与 `ghcr.io/langfuse/langfuse-worker:4.2.0`（本机已有，无需拉取）；其余组件沿用模板镜像。
2. 端口：只保留 `langfuse-web` 的 `8858:3000`。删除 `langfuse-worker` 的 `127.0.0.1:3030`、`clickhouse` 的 `18123/19000`、`minio` 的 `19090/19091`、`redis` 的 `16379`、`postgres` 的 `15432` 全部映射。
3. 凭证全部新生成，不复用 18081 的任何一个：`POSTGRES_PASSWORD`、`CLICKHOUSE_PASSWORD`、`REDIS_AUTH`、`MINIO_ROOT_PASSWORD`（含对应的三组 `LANGFUSE_S3_*_SECRET_ACCESS_KEY`）、`NEXTAUTH_SECRET`、`SALT`、`ENCRYPTION_KEY`（`openssl rand -hex 32`）。
4. `NEXTAUTH_URL=http://10.0.24.40:8858`，必须与浏览器实际访问地址一致，否则登录回跳失败。
5. 无人值守初始化：`LANGFUSE_INIT_ORG_ID=newapi`、`LANGFUSE_INIT_PROJECT_ID=new-api-8850`、固定的 `LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-lf-<uuid>` / `LANGFUSE_INIT_PROJECT_SECRET_KEY=sk-lf-<uuid>`、管理员邮箱与密码。这样 §6 Step 6 可以直接填 key，不必先点 UI 建项目。
6. 保留与 18081 相同的三个 `LANGFUSE_MIGRATION_V4_*` 开关（同版本已在运行的组合），首启后按 §7 确认 OTLP 端点可用；若首启报错则回退到官方默认值。
7. 数据卷仍为 `./data/{postgres,clickhouse,clickhouse-logs,minio,redis}`；`/home/flintylemming/appdata` 落在 `/mnt/extend/appdata`（21T，已用 45%），容量充足。

**已知限制**：`minio` 不再发布端口，`LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT` 保持内网 `http://minio:9000`。服务端上传正常，但 Langfuse UI 的媒体预览由浏览器直连预签名 URL，浏览器解析不到 `minio` 这个名字，媒体预览不可用。本集成上报的是文本 JSON 正文，不受影响；后续若需要预览，再发布 minio 端口并把该 endpoint 改成宿主机地址。

## 5. B：镜像构建与 compose 变更

构建（`.dockerignore` 已排除 `.git`、`docs`、`web/node_modules`）：

```bash
cd /mnt/extend/Projects/new-api
docker build -t new-api:langfuse-750452c0 .
```

`VERSION` 文件在本仓库为空（上游如此，不改），故 `/api/status` 的版本号为空字符串；版本识别以镜像 tag 为准。

`8850-new-api/compose.yaml` 只改两处：

```yaml
services:
  new-api:
    image: new-api:langfuse-750452c0
    pull_policy: never          # 本地 tag，禁止 compose 去 registry 拉
    networks:
      - default                 # 显式声明；一旦写 networks，隐式 default 就失效
      - langfuse

networks:
  langfuse:
    external: true
    name: 8858-langfuse-newapi_default
```

其余服务（redis / postgres / clickhouse）不动，仍只在 `default` 网络。

## 6. C：上线流程

每一步都是一道门，失败即停并按 §8 回滚。

**Step 0 — 全量验证**（plan-8 Task 2 的七条）：`go test` 分包与 `-race`、包边界断言、`go build ./...`、`cd relaykit && GOWORK=off go build ./... && GOWORK=off go test ./...`、`cd web && bun run typecheck && bun run lint && bun run build`。任何失败按 systematic-debugging 定位，不跳过。

**Step 1 — 备份与回滚锚点**：`docker exec new-api-postgres pg_dump -U root -Fc newapi` 输出到 `backups/newapi-pre-langfuse-<UTC 时间戳>.dump`（沿用 `newapi-pre-upgrade-20260805T123940Z.dump` 的命名）；记录 `docker image inspect calciumion/new-api:latest --format '{{index .RepoDigests 0}}'`。

**Step 2 — 起 Langfuse 新栈**：`docker compose up -d`，等六个服务 healthy；浏览器访问 `http://10.0.24.40:8858` 用 init 账号登录，确认 org/project 存在、Settings 里能看到那对固定 key。

**Step 3 — 构建镜像**：见 §5。

**Step 4 — 切镜像**：改 compose 后 `docker compose up -d new-api`（仅重建该容器）。AutoMigrate 会给 `midjourney` 加两列。

**Step 5 — 冒烟**：容器日志无迁移/启动错误；`/api/status` 返回 `success:true`；发一次真实 relay 请求，确认响应正常、扣费正常、日志正常写入 ClickHouse。此步覆盖的是"73 个 commit 一次性生效"这个风险，不只是 Langfuse。

**Step 6 — 启用 Langfuse（第一阶段：全采样）**：root 登录 8850 → 系统设置 → 集成 → Langfuse：

| 字段 | 值 |
| --- | --- |
| Host | `http://langfuse-web:3000`（拼出 `.../api/public/otel/v1/traces`） |
| Public/Secret Key | Step 2 的固定 key |
| Environment | `production` |
| Sample Rate | `1.0` |
| Send Content | 开 |
| 容量参数 | 全部保持默认 |

默认容量下的内存账：单次捕获预留 `2*64KiB + 512KiB = 640KiB`，全局预算 512MiB → 819 个并发捕获槽；常驻规划值 `512MiB + 64*640KiB ≈ 552MiB`，导出峰值再加 `3*batch` 个 span 体。预算耗尽时该请求降级为 metadata-only（`ReserveCapture` 返回 nil），不重试、不影响 relay。

**Step 7 — 验收**：见 §7。

**Step 8 — 收敛到常态**：验收通过后把 Sample Rate 改回 `0.1`，其余不变。

## 7. 验收清单

在真实流量上核对（plan-8 Task 3 的精简版，输出只记录 trace/observation ID 与状态，不贴正文与凭证）：

1. Langfuse 中出现 trace，且 trace name 由 root span name 回退得到（形如 `openai <model>`）；列表预览正文来自 root。
2. root observation 保留客户端视角的 input/output，`capture_state` 与截断行为符合预期。
3. usage 分桶：cache creation、audio、image 各按既定口径出现在 usage details，互斥不重叠。
4. cost 为 new-api 的权威值（对照 `quota/500000`），Langfuse 未按 model price 二次推价。
5. 一次触发重试/换渠道的请求：多个 attempt 呈现为独立 generation，root 的 user/session/metadata 不被 attempt 覆盖，generation 上无 denylist 属性。
6. 客户端未提供 session 标识时，trace 上不出现伪造的 session。
7. 故障隔离：临时 `docker compose stop langfuse-web`，确认 relay 请求与计费不受影响、错误日志被限流打印；恢复后继续上报。
8. 观察 new-api 容器内存与 Langfuse ingestion 延迟，确认全采样阶段没有持续增长。

结果写入 `docs/internal/e2e/2026-08-15-langfuse-8850-traffic-acceptance.md`（脱敏）。

## 8. 回滚

分三级，按影响面从小到大：

1. **关开关**：UI 里 `enabled=false`。exporter 停止，其余功能不受影响。适用于只有 Langfuse 链路有问题的情况。
2. **回镜像**：`compose.yaml` 的 `image` 改回 `calciumion/new-api:latest`（digest 已在 Step 1 记录），去掉 `pull_policy` 与 `networks` 两处改动，`docker compose up -d new-api`。`midjourney` 的两个多余列被上游忽略，**不需要恢复数据库**。
3. **恢复库**：仅当出现数据异常时，用 Step 1 的 `pg_restore` 备份。

Langfuse 新栈可独立 `docker compose down` 而不影响 new-api；即使 new-api 仍开着开关，导出失败也只是限流日志。

## 9. 风险

- **一次性抬 73 个 commit**：Langfuse 之外的计费与缓存语义改动同时生效。Step 0 的全量测试与 Step 5 的真实计费冒烟是主要防线。
- **全采样阶段**：内存与 Langfuse ClickHouse 写入压力上升；预算耗尽只会降级为 metadata-only，不会拖慢 relay。Step 8 及时收敛。
- **跨 compose 项目的 external 网络**：`8850-new-api` 依赖 `8858-langfuse-newapi_default` 存在，Langfuse 栈必须先于 new-api 启动（`docker compose down` 掉 Langfuse 栈后再重建 new-api 会失败）。这是可接受的耦合，回滚方案 2 会一并解除。
- **媒体预览不可用**：见 §4 已知限制。
