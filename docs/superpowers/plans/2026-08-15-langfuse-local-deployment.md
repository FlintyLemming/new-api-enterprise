# 8850 部署切换到 fork 构建并启用 Langfuse — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) 或 superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `/home/flintylemming/appdata/8850-new-api` 运行中的 new-api 从上游镜像 `calciumion/new-api:latest` 切换到 `feature/langfuse-tracing` 的本机构建，接入新建的 `8858-langfuse-newapi` Langfuse 4.2.0 实例，并按"先全采样验收、再降到 0.1"两段式启用 Langfuse。

**Architecture:** 三块互相独立的改动——(A) 新建一套专用 Langfuse compose 栈，只发布 Web UI 端口；(B) 本机 `docker build` 出不可变 tag 的 new-api 镜像，改 `8850-new-api/compose.yaml` 的 `image` 与 `networks` 两处；(C) 通过 root 专用接口 `/api/option/langfuse` 写 DB option 启用。设计文档：`docs/superpowers/designs/2026-08-15-langfuse-local-deployment-design.md`。

**Tech Stack:** Docker Compose v5.1.3、Langfuse 4.2.0（web/worker/clickhouse/minio/redis/postgres）、Go 1.26 + Bun 构建的 new-api 镜像、PostgreSQL 15（new-api 主库）、ClickHouse（new-api 日志库）。

## Global Constraints

- **本计划不改任何 new-api 业务代码**；仓库内只新增/修改 `docs/` 下的文档。仓库工作区 `/mnt/extend/Projects/new-api`，分支 `feature/langfuse-tracing`，起始 HEAD `16894384`，构建用的源码 revision 为 `750452c0`（本计划所有镜像 tag 以此为准）。
- **绝不修改 `/home/flintylemming/appdata/18081-langfuse/`** 的任何文件、容器、网络、数据卷，也不复用它的任何凭证。
- 新 Langfuse 栈只发布一个宿主机端口 `8858`；`langfuse-worker`/`clickhouse`/`minio`/`redis`/`postgres` 的端口映射全部删除。
- 所有涉及正文与凭证的输出一律脱敏：日志、报告、commit message 中只允许出现 trace/observation ID、状态与字节数，**不得**出现请求正文、响应正文、Langfuse public/secret key、Basic header、session 原值、数据库密码。
- git 提交使用一次性身份覆盖，**不得**修改仓库或全局 git config：`git -c user.name=Claude -c user.email=noreply@anthropic.com commit ...`。
- 每个 Task 的失败都是停止点：先按 `superpowers:systematic-debugging` 定位，不得跳过或"先往下走"。
- 回滚锚点：Task 2 产出的 PG 备份与上游镜像 digest 在整个计划期间必须可用。

---

### Task 1: 全量构建验证（切换前的绿色基线）

**Files:**
- 只读：`/mnt/extend/Projects/new-api` 全仓库
- 无文件产出（结果记录在 Task 8 的报告里）

**Interfaces:**
- Consumes: 无
- Produces: "HEAD `750452c0` 的代码可构建、测试全绿"这一结论；Task 4 才允许构建镜像

- [ ] **Step 1: 确认工作区状态**

```bash
cd /mnt/extend/Projects/new-api
git rev-parse --abbrev-ref HEAD
git rev-parse --short HEAD
git status --short
```

Expected: 分支 `feature/langfuse-tracing`；`git status --short` 无输出（工作区干净）。若有未提交改动，先停下来问用户。

- [ ] **Step 2: 分包测试**

```bash
cd /mnt/extend/Projects/new-api
go test ./service/langfuse/... ./service/langfuseconfig/... ./setting/langfuse_setting/... ./relay/constant/...
```

Expected: 全部 `ok`，无 `FAIL`。

- [ ] **Step 3: 并发契约测试**

```bash
cd /mnt/extend/Projects/new-api
go test -race ./service/langfuse/...
```

Expected: `ok`，无 `DATA RACE`。

- [ ] **Step 4: 包边界断言**

```bash
cd /mnt/extend/Projects/new-api
go test ./service/langfuse -run 'Boundar|Import' -v
go list -deps ./service/langfuse | grep -E 'new-api/(model|controller|service/langfuseconfig|relay/channel)' && echo VIOLATION || echo OK
go list -deps ./service/langfuse | grep -E 'new-api/service'
```

Expected: 边界测试 PASS；第二条输出 `OK`；第三条只输出 `github.com/QuantumNous/new-api/service/langfuse` 自身。

> 注：plan-8 里写的 `-run TestPackageBoundaries` 在本仓库没有对应测试函数，`-run` 空匹配会返回退出码 0 并打印 `PASS`（假绿）；其 grep 模式含 `new-api/(service|...)`，会匹配被测包自身而永远输出 `VIOLATION`。以上是修正后的等价检查。

- [ ] **Step 5: 全量测试与构建**

```bash
cd /mnt/extend/Projects/new-api
go build ./...
go test ./...
```

Expected: `go build` 无输出；`go test ./...` 无 `FAIL`（部分包 `no test files` 属正常）。

- [ ] **Step 6: relaykit 独立构建**

```bash
cd /mnt/extend/Projects/new-api/relaykit
GOWORK=off go build ./...
GOWORK=off go test ./...
```

Expected: 无输出错误，测试无 `FAIL`。这是 AGENTS.md 的硬性要求：root 模块构建成功不能替代这一条。

- [ ] **Step 7: 前端验证**

```bash
cd /mnt/extend/Projects/new-api/web
bun install --frozen-lockfile
bun run typecheck
bun run lint
bun run build
```

Expected: 三条命令均退出码 0，`bun run build` 产出 `web/dist`。

- [ ] **Step 8: 记录结果**

把 Step 2–7 的通过情况按条记在本地便签（Task 8 会写进报告）。本 Task 无 commit（未改任何文件）。

---

### Task 2: 备份与回滚锚点

**Files:**
- Create: `/home/flintylemming/appdata/8850-new-api/backups/newapi-pre-langfuse-<UTC>.dump`（`<UTC>` 由命令自动生成，格式 `20260815T093000Z`）
- Create: `/home/flintylemming/appdata/8850-new-api/backups/rollback-anchor-<UTC>.txt`

**Interfaces:**
- Consumes: 无
- Produces: PG 逻辑备份文件路径与上游镜像 digest，供 Task 5 失败时回滚、供 Task 8 报告引用

- [ ] **Step 1: 记录当前运行态与镜像 digest**

```bash
cd /home/flintylemming/appdata/8850-new-api
TS=$(date -u +%Y%m%dT%H%M%SZ)
echo "TS=$TS"
{
  echo "timestamp_utc=$TS"
  echo "container_image=$(docker inspect new-api --format '{{.Config.Image}}')"
  echo "image_id=$(docker inspect new-api --format '{{.Image}}')"
  echo "repo_digest=$(docker image inspect calciumion/new-api:latest --format '{{index .RepoDigests 0}}')"
  echo "image_revision=$(docker image inspect calciumion/new-api:latest --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')"
} | tee "backups/rollback-anchor-$TS.txt"
```

Expected: 输出包含 `container_image=calciumion/new-api:latest`、`image_revision=0ab02020603d22e5613bc4cf46bfab06f8567769` 与一行 `repo_digest=calciumion/new-api@sha256:...`。把这个 `$TS` 值留着，后面两步复用。

- [ ] **Step 2: 主库逻辑备份**

```bash
cd /home/flintylemming/appdata/8850-new-api
docker exec new-api-postgres pg_dump -U root -Fc newapi > "backups/newapi-pre-langfuse-$TS.dump"
ls -lh "backups/newapi-pre-langfuse-$TS.dump"
```

Expected: 文件存在且大小 > 1 MB（对照同目录 `newapi-pre-upgrade-20260805T123940Z.dump` 的量级）。

- [ ] **Step 3: 验证备份可读**

```bash
cd /home/flintylemming/appdata/8850-new-api
docker exec -i new-api-postgres pg_restore -l < "backups/newapi-pre-langfuse-$TS.dump" | head -20
```

Expected: 输出 `;` 开头的归档目录清单（含 `Archive created at ...`），不报 `input file does not appear to be a valid archive`。

- [ ] **Step 4: 无需 commit**

备份文件在部署目录，不进仓库。

---

### Task 3: 新建 8858-langfuse-newapi 栈

**Files:**
- Create: `/home/flintylemming/appdata/8858-langfuse-newapi/docker-compose.yml`（由 18081 模板拷贝后定点修改）
- Create: `/home/flintylemming/appdata/8858-langfuse-newapi/.env`（权限 600，凭证全部新生成）
- Create: `/home/flintylemming/appdata/8858-langfuse-newapi/data/`（compose 首启自动创建子目录）

**Interfaces:**
- Consumes: 无
- Produces: Docker 网络 `8858-langfuse-newapi_default`（Task 5 的 compose 以 external 方式引用）、容器内 DNS 名 `langfuse-web`（端口 3000）、一对固定的 `pk-lf-*` / `sk-lf-*` project key（Task 6 使用）

- [ ] **Step 1: 建目录并拷贝 compose 模板**

```bash
mkdir -p /home/flintylemming/appdata/8858-langfuse-newapi
cd /home/flintylemming/appdata/8858-langfuse-newapi
cp /home/flintylemming/appdata/18081-langfuse/docker-compose.yml ./docker-compose.yml
grep -n "ports:" -A 3 docker-compose.yml
```

Expected: 看到六处端口块——worker `127.0.0.1:3030:3030`、web `18081:3000`、clickhouse `127.0.0.1:18123:8123` / `127.0.0.1:19000:9000`、minio `127.0.0.1:19090:9000` / `127.0.0.1:19091:9001`、redis `127.0.0.1:16379:6379`、postgres `127.0.0.1:15432:5432`。

- [ ] **Step 2: 只保留 Web UI 端口**

用编辑器对 `docker-compose.yml` 做以下五处删除、一处替换：

1. `langfuse-worker` 服务下删除这两行：

```yaml
    ports:
      - 127.0.0.1:3030:3030
```

2. `langfuse-web` 服务下把端口改掉：

```yaml
    ports:
      - 8858:3000
```

3. `clickhouse` 服务下删除这三行：

```yaml
    ports:
      - 127.0.0.1:18123:8123
      - 127.0.0.1:19000:9000
```

4. `minio` 服务下删除这三行：

```yaml
    ports:
      - 127.0.0.1:19090:9000
      - 127.0.0.1:19091:9001
```

5. `redis` 服务下删除这两行：

```yaml
    ports:
      - 127.0.0.1:16379:6379
```

6. `postgres` 服务下删除这两行：

```yaml
    ports:
      - 127.0.0.1:15432:5432
```

- [ ] **Step 3: 校验只剩一个发布端口**

```bash
cd /home/flintylemming/appdata/8858-langfuse-newapi
grep -n "ports:" -A 2 docker-compose.yml
docker compose config --format json | jq '[.services|to_entries[]|{svc:.key, ports:(.value.ports//[]|map(.published))}]'
```

Expected: `grep` 只剩 `langfuse-web` 一处；`jq` 输出中只有 `langfuse-web` 的 `ports` 非空且为 `["8858"]`，其余服务 `ports` 为 `[]`。（`docker compose config` 此时会因 `.env` 缺失使用默认值告警，属正常，下一步就补上。）

- [ ] **Step 4: 生成 .env（全部凭证新生成）**

```bash
cd /home/flintylemming/appdata/8858-langfuse-newapi
umask 077
PG_PASS=$(openssl rand -hex 24)
CH_PASS=$(openssl rand -hex 24)
REDIS_PASS=$(openssl rand -hex 24)
MINIO_PASS=$(openssl rand -hex 24)
LF_PK="pk-lf-$(uuidgen)"
LF_SK="sk-lf-$(uuidgen)"
INIT_PASS=$(openssl rand -hex 12)
cat > .env <<EOF
# Langfuse 专用实例：仅服务 8850-new-api。凭证与 18081-langfuse 完全独立。
NEXTAUTH_URL=http://10.0.24.40:8858
NEXTAUTH_SECRET=$(openssl rand -base64 32)
SALT=$(openssl rand -base64 32)
ENCRYPTION_KEY=$(openssl rand -hex 32)
TELEMETRY_ENABLED=false
LANGFUSE_ENABLE_EXPERIMENTAL_FEATURES=false

POSTGRES_USER=postgres
POSTGRES_PASSWORD=$PG_PASS
POSTGRES_DB=postgres
DATABASE_URL=postgresql://postgres:$PG_PASS@postgres:5432/postgres

CLICKHOUSE_USER=clickhouse
CLICKHOUSE_PASSWORD=$CH_PASS
CLICKHOUSE_URL=http://clickhouse:8123
CLICKHOUSE_MIGRATION_URL=clickhouse://clickhouse:9000
CLICKHOUSE_CLUSTER_ENABLED=false

REDIS_HOST=redis
REDIS_PORT=6379
REDIS_AUTH=$REDIS_PASS

MINIO_ROOT_USER=minio
MINIO_ROOT_PASSWORD=$MINIO_PASS
LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID=minio
LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY=$MINIO_PASS
LANGFUSE_S3_MEDIA_UPLOAD_ACCESS_KEY_ID=minio
LANGFUSE_S3_MEDIA_UPLOAD_SECRET_ACCESS_KEY=$MINIO_PASS
LANGFUSE_S3_BATCH_EXPORT_ACCESS_KEY_ID=minio
LANGFUSE_S3_BATCH_EXPORT_SECRET_ACCESS_KEY=$MINIO_PASS
# minio 未发布宿主机端口，媒体预签名地址保持内网；UI 媒体预览不可用是已知限制。
LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT=http://minio:9000
LANGFUSE_S3_BATCH_EXPORT_EXTERNAL_ENDPOINT=http://minio:9000

# 与 18081 同版本(4.2.0)已在运行的 V4 组合保持一致
LANGFUSE_MIGRATION_V4_WRITE_MODE=events_only
LANGFUSE_MIGRATION_V4_NATIVE_OTEL_BEHAVIOUR=direct
LANGFUSE_MIGRATION_V4_ALLOW_PREVIEW_OPT_IN=true

# 无人值守初始化：org/project/key 固定，Task 6 直接使用
LANGFUSE_INIT_ORG_ID=newapi
LANGFUSE_INIT_ORG_NAME=New API
LANGFUSE_INIT_PROJECT_ID=new-api-8850
LANGFUSE_INIT_PROJECT_NAME=new-api-8850
LANGFUSE_INIT_PROJECT_PUBLIC_KEY=$LF_PK
LANGFUSE_INIT_PROJECT_SECRET_KEY=$LF_SK
LANGFUSE_INIT_USER_EMAIL=admin@liandisys.com.cn
LANGFUSE_INIT_USER_NAME=admin
LANGFUSE_INIT_USER_PASSWORD=$INIT_PASS
EOF
chmod 600 .env
echo "生成完成；登录密码与 project key 见 .env，不要贴到任何日志或报告里"
```

Expected: `.env` 存在、权限 `-rw-------`。**不要**把 `$LF_SK`、`$INIT_PASS` 等值 echo 到终端记录里。

- [ ] **Step 5: 校验 compose 配置可解析且端口无冲突**

```bash
cd /home/flintylemming/appdata/8858-langfuse-newapi
docker compose config -q && echo CONFIG_OK
ss -ltn | grep -E ':8858\b' || echo "8858 空闲"
```

Expected: `CONFIG_OK`，且 8858 当前空闲。

- [ ] **Step 6: 启动并等待就绪**

```bash
cd /home/flintylemming/appdata/8858-langfuse-newapi
docker compose up -d
sleep 60
docker compose ps
```

Expected: 六个服务均 `running`；`clickhouse`/`minio`/`redis`/`postgres` 显示 `healthy`。若 `langfuse-web` 反复重启，先看 `docker compose logs langfuse-web | tail -50`——若报 V4 迁移相关错误，把 `.env` 里三个 `LANGFUSE_MIGRATION_V4_*` 注释掉再 `docker compose up -d`（设计文档 §4 第 6 条的回退路径）。

- [ ] **Step 7: 验证 Web 与 OTLP 端点**

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8858/api/public/health
curl -sS -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8858/api/public/otel/v1/traces
```

Expected: 第一条 `200`；第二条返回 `401`（无凭证被拒即证明该路由存在并在鉴权，不能是 `404`）。

- [ ] **Step 8: 确认网络名与项目初始化**

```bash
docker network ls --format '{{.Name}}' | grep 8858-langfuse-newapi_default
docker compose -f /home/flintylemming/appdata/8858-langfuse-newapi/docker-compose.yml ps --format '{{.Name}}' | sort
```

Expected: 网络 `8858-langfuse-newapi_default` 存在；容器名均以 `8858-langfuse-newapi-` 开头（与 18081 那套不冲突）。

浏览器打开 `http://10.0.24.40:8858`，用 `.env` 里的 `LANGFUSE_INIT_USER_EMAIL` / `LANGFUSE_INIT_USER_PASSWORD` 登录，确认存在 org `New API` 与 project `new-api-8850`，且 Settings → API Keys 里能看到 `.env` 中那个 public key。

---

### Task 4: 构建 new-api 镜像

**Files:**
- 只读：`/mnt/extend/Projects/new-api`（含 `Dockerfile`、`.dockerignore`）
- 产出：本地镜像 `new-api:langfuse-750452c0`

**Interfaces:**
- Consumes: Task 1 的绿色基线
- Produces: 镜像 tag `new-api:langfuse-750452c0`，Task 5 的 compose 引用它

- [ ] **Step 1: 确认构建源与上次一致**

```bash
cd /mnt/extend/Projects/new-api
git rev-parse --short HEAD
git status --short
```

Expected: `750452c0`（若已包含设计/计划文档提交则可能更新，但**必须**用实际输出替换下面命令里的 tag 后缀，并在 Task 8 报告中记录实际值）；工作区无未提交的代码改动。

- [ ] **Step 2: 构建**

```bash
cd /mnt/extend/Projects/new-api
time docker build -t new-api:langfuse-750452c0 .
```

Expected: 以 `naming to docker.io/library/new-api:langfuse-750452c0 done` 结束，退出码 0。构建分三段（bun 前端 → go 编译 → debian 运行时），本机 192 核，通常几分钟内完成。

- [ ] **Step 3: 校验镜像可用**

```bash
docker image inspect new-api:langfuse-750452c0 --format '{{.Created}} {{.Config.Entrypoint}} {{.Config.ExposedPorts}}'
docker run --rm --entrypoint /bin/sh new-api:langfuse-750452c0 -c 'ls -l /new-api && /new-api --help 2>&1 | head -5' || true
```

Expected: `Entrypoint` 为 `[/new-api]`、暴露 `3000/tcp`；二进制存在且可执行（`--help` 不被支持时打印用法或直接退出，均可接受，只要不是 `exec format error` / `not found`）。

- [ ] **Step 4: 无需 commit**

镜像不进仓库。

---

### Task 5: 切换 8850 的镜像并冒烟

**Files:**
- Modify: `/home/flintylemming/appdata/8850-new-api/compose.yaml`（`new-api` 服务的 `image`、新增 `pull_policy` 与 `networks`；文件末尾新增顶层 `networks`）

**Interfaces:**
- Consumes: Task 3 的网络 `8858-langfuse-newapi_default`、Task 4 的镜像 tag、Task 2 的回滚锚点
- Produces: 运行 fork 构建的 `new-api` 容器，且容器已接入 Langfuse 网络（`langfuse-web` 可解析）

- [ ] **Step 1: 备份 compose 文件**

```bash
cd /home/flintylemming/appdata/8850-new-api
cp compose.yaml "compose.yaml.bak-$(date -u +%Y%m%dT%H%M%SZ)"
ls compose.yaml.bak-*
```

Expected: 备份文件存在。

- [ ] **Step 2: 修改 compose.yaml**

把 `services.new-api` 的第 3 行 `image: calciumion/new-api:latest` 替换为下面三行（`command` 及其后内容保持不动）：

```yaml
    image: new-api:langfuse-750452c0
    # 本地构建的 tag，禁止 compose 去 registry 拉取
    pull_policy: never
```

在同一服务的 `healthcheck` 块之后（缩进 4 空格，与 `healthcheck:` 同级）追加：

```yaml
    networks:
      # 一旦显式声明 networks，隐式 default 就失效，必须一并列出
      - default
      - langfuse
```

在文件末尾（与 `services:` 同级，顶格）追加：

```yaml
networks:
  langfuse:
    external: true
    name: 8858-langfuse-newapi_default
```

- [ ] **Step 3: 校验配置**

```bash
cd /home/flintylemming/appdata/8850-new-api
docker compose config -q && echo CONFIG_OK
docker compose config --format json | jq '.services["new-api"] | {image, pull_policy, networks: (.networks|keys)}'
```

Expected: `CONFIG_OK`；jq 输出 `image` 为 `new-api:langfuse-750452c0`，`networks` 含 `default` 与 `langfuse`。

- [ ] **Step 4: 重建容器**

```bash
cd /home/flintylemming/appdata/8850-new-api
docker compose up -d new-api
docker compose ps new-api
```

Expected: 只有 `new-api` 被 Recreate，其余服务不动；容器 `running`。

- [ ] **Step 5: 检查启动日志与迁移**

```bash
cd /home/flintylemming/appdata/8850-new-api
docker compose logs --since 5m new-api | grep -iE 'error|fatal|panic|failed' | head -20 || echo "no error lines"
docker exec new-api-postgres psql -U root -d newapi -c "\d midjourneys" | grep -E 'token_id|billing_channel_id'
```

Expected: 无 `panic`/`fatal`；`midjourneys` 表出现 `token_id` 与 `billing_channel_id` 两列（AutoMigrate 生效）。若表名不是 `midjourneys`，用 `\dt` 找到实际表名再查。

- [ ] **Step 6: 健康与连通性冒烟**

```bash
curl -sS http://127.0.0.1:8850/api/status | jq '.success'
docker exec new-api getent hosts langfuse-web || docker exec new-api sh -c 'wget -q -O - http://langfuse-web:3000/api/public/health && echo OK'
```

Expected: `.success` 为 `true`；第二条能解析到 `langfuse-web` 或直接打印健康响应 + `OK`（证明跨 compose 网络连通）。

- [ ] **Step 7: 真实业务冒烟（计费必须验证）**

用 8850 上你自己的测试令牌（没有就在 UI → 令牌 里新建一个）：

```bash
export NEWAPI_TEST_KEY=sk-xxxx   # 替换为你的测试令牌，不要写进任何文件
curl -sS http://127.0.0.1:8850/v1/chat/completions \
  -H "Authorization: Bearer $NEWAPI_TEST_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}],"max_tokens":16}' \
  | jq '{id, model, usage}'
```

Expected: 返回正常响应与 `usage`；随后在 UI 的日志页看到这条记录且扣费金额合理。这一步验证的是"73 个 commit 一次性生效"的整体健康，不只是 Langfuse。若失败——立即执行 Task 5 的回滚（见下一步注记）后再排查。

> **本 Task 的回滚**：`cp compose.yaml.bak-<ts> compose.yaml && docker compose up -d new-api`。`midjourneys` 的两个多余列被上游忽略，不需要恢复数据库。

- [ ] **Step 8: 无需 commit**

改动在部署目录，不进仓库。

---

### Task 6: 启用 Langfuse（第一阶段：全采样）

**Files:**
- 无文件产出；写的是 new-api 主库的 `options` 表（`langfuse_setting.*`）

**Interfaces:**
- Consumes: Task 3 的 `pk-lf-*` / `sk-lf-*`、Task 5 的容器与网络
- Produces: `enabled=true, sample_rate=1.0, send_content=true` 的运行时绑定，供 Task 7 验收

- [ ] **Step 1: 准备 root 访问令牌**

在 8850 用 root 账号登录 → 个人设置 → 生成"系统访问令牌"，然后：

```bash
export NEWAPI_ROOT_TOKEN=<刚生成的访问令牌>
curl -sS http://127.0.0.1:8850/api/option/langfuse -H "Authorization: $NEWAPI_ROOT_TOKEN" | jq '.data | {enabled, host, sample_rate, send_content, secret_key_configured}'
```

Expected: `enabled` 为 `false`，`host` 为空，`secret_key_configured` 为 `false`（未启用的初始态）。若返回 401/403，说明令牌不是 root 用户的。

- [ ] **Step 2: 写入配置（全采样 + 上报正文）**

```bash
export LF_PK=$(grep '^LANGFUSE_INIT_PROJECT_PUBLIC_KEY=' /home/flintylemming/appdata/8858-langfuse-newapi/.env | cut -d= -f2)
export LF_SK=$(grep '^LANGFUSE_INIT_PROJECT_SECRET_KEY=' /home/flintylemming/appdata/8858-langfuse-newapi/.env | cut -d= -f2)
curl -sS -X PUT http://127.0.0.1:8850/api/option/langfuse \
  -H "Authorization: $NEWAPI_ROOT_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg pk "$LF_PK" --arg sk "$LF_SK" '{
        enabled: true,
        host: "http://langfuse-web:3000",
        public_key: $pk,
        secret_key: $sk,
        environment: "production",
        sample_rate: 1.0,
        send_content: true,
        max_content_bytes: 65536,
        max_response_bytes: 524288,
        max_in_flight_capture_bytes: 536870912,
        max_session_body_bytes: 65536,
        queue_size: 64,
        batch_size: 16,
        flush_interval_seconds: 5
      }')" | jq '{success, message}'
```

Expected: `{"success": true, "message": ""}`。若 `success:false`，`message` 会指明是哪个字段或整组包络校验没过，按提示改值重试（不要绕过校验）。

- [ ] **Step 3: 回读确认**

```bash
curl -sS http://127.0.0.1:8850/api/option/langfuse -H "Authorization: $NEWAPI_ROOT_TOKEN" \
  | jq '.data | {enabled, host, environment, sample_rate, send_content, secret_key_configured}'
```

Expected: `enabled:true`、`host:"http://langfuse-web:3000"`、`sample_rate:1`、`send_content:true`、`secret_key_configured:true`；响应中**不含** secret 明文（含则是缺陷，立即停止并上报）。

- [ ] **Step 4: UI 复核**

浏览器 root 登录 8850 → 系统设置 → 集成 → `Langfuse Tracing`，确认面板显示的启用状态、Host、容量提示（预留 640 KiB/次、并发槽 819、常驻约 552 MiB）与上面一致。

- [ ] **Step 5: 确认导出通路已建立**

```bash
cd /home/flintylemming/appdata/8850-new-api
docker compose logs --since 3m new-api | grep -i langfuse | head -20 || echo "no langfuse log lines"
```

Expected: 没有反复出现的 `403`/`connection refused`/`forbidden` 之类导出错误。零日志也是正常的（成功导出不打日志）。

---

### Task 7: 真实流量验收

**Files:**
- Create: `/mnt/extend/Projects/new-api/docs/internal/e2e/2026-08-15-langfuse-8850-traffic-acceptance.md`

**Interfaces:**
- Consumes: Task 6 的启用态
- Produces: 验收报告；通过后 Task 8 才允许降采样

- [ ] **Step 1: 触发并定位一条 trace**

发一次 Task 5 Step 7 那样的真实请求，然后在 `http://10.0.24.40:8858` 的 project `new-api-8850` 里找到对应 trace。核对：

1. trace name 由 root span name 回退得到（形如 `openai <model>`），不是空或 `unknown`；
2. trace 列表的预览正文来自 root，且包含 `capture_state`；
3. root observation 详情保留客户端视角的 input/output。

- [ ] **Step 2: usage 与 cost 口径**

在同一条（或另找一条命中缓存/音频/图片的）trace 的 generation 上核对：

1. usage details 里 cache creation、audio、image 各按既定口径出现，互斥不重叠；
2. cost 等于 new-api 的权威值——用日志页该请求的 quota 对照 `quota/500000`（美元），Langfuse 不应按 model price 二次推价。

- [ ] **Step 3: 多 attempt 与 denylist**

找一条发生了重试/换渠道的请求（可临时把某个渠道置为不可用来构造）。核对：

1. 每次上游调用是独立 generation；
2. root 的 user/session/metadata 没有被后续 attempt 覆盖；
3. generation 上没有 denylist 属性。

- [ ] **Step 4: session 不伪造**

发一条不带任何 session 标识的请求，确认 trace 上不出现被"猜"出来的 session。

- [ ] **Step 5: 故障隔离**

```bash
cd /home/flintylemming/appdata/8858-langfuse-newapi
docker compose stop langfuse-web
# 立刻发 2-3 次真实 relay 请求，确认全部正常返回且扣费正常
docker compose start langfuse-web
```

Expected: Langfuse 停机期间 relay 不受影响（不变慢、不报错）；new-api 日志里的导出错误是被限流的，不刷屏；恢复后新请求继续上报。

- [ ] **Step 6: 资源观察**

```bash
docker stats --no-stream new-api 8858-langfuse-newapi-langfuse-worker-1 8858-langfuse-newapi-clickhouse-1
```

Expected: `new-api` 内存稳定（全采样下的规划常驻约 552 MiB 增量，不应持续单调增长）；观察 10 分钟后再取一次对比。

- [ ] **Step 7: 写报告并提交**

创建 `docs/internal/e2e/2026-08-15-langfuse-8850-traffic-acceptance.md`，逐条记录 Step 1–6 的：输入形状（脱敏）、trace/observation ID、观察结果、结论；未覆盖项写明原因。**报告中不得出现正文、凭证、session 原值。**

```bash
cd /mnt/extend/Projects/new-api
git add docs/internal/e2e/2026-08-15-langfuse-8850-traffic-acceptance.md
git -c user.name=Claude -c user.email=noreply@anthropic.com commit -m "docs(langfuse): real langfuse E2E verification report

Co-Authored-By: Claude <noreply@anthropic.com>"
```

Expected: 提交成功。若任一条验收不通过——停止，不要进入 Task 8，按 `superpowers:systematic-debugging` 定位；必要时按设计文档 §8 的一级回滚（UI 关开关）先止血。

---

### Task 8: 收敛到常态采样并留痕

**Files:**
- Create: `/mnt/extend/Projects/new-api/docs/internal/patches/2026-08-15-langfuse-local-deployment.md`（`docs/internal/` 在本分支尚不存在，本 Task 一并新建该目录；结构按下面 Step 4 列出的六个小节）

**Interfaces:**
- Consumes: Task 7 通过的结论
- Produces: `sample_rate=0.1` 的常态配置与部署台账

- [ ] **Step 1: 降采样**

```bash
curl -sS -X PUT http://127.0.0.1:8850/api/option/langfuse \
  -H "Authorization: $NEWAPI_ROOT_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true,"host":"http://langfuse-web:3000","public_key":"'"$LF_PK"'","secret_key":"","environment":"production","sample_rate":0.1,"send_content":true,"max_content_bytes":65536,"max_response_bytes":524288,"max_in_flight_capture_bytes":536870912,"max_session_body_bytes":65536,"queue_size":64,"batch_size":16,"flush_interval_seconds":5}' \
  | jq '{success, message}'
```

Expected: `success:true`。`secret_key` 传空字符串表示保留已存密钥（见 `UpdateRequest.SecretKey` 的语义），不需要再次提交密钥。

- [ ] **Step 2: 回读确认**

```bash
curl -sS http://127.0.0.1:8850/api/option/langfuse -H "Authorization: $NEWAPI_ROOT_TOKEN" \
  | jq '.data | {enabled, sample_rate, send_content, secret_key_configured}'
```

Expected: `sample_rate: 0.1`、`enabled:true`、`secret_key_configured:true`。

- [ ] **Step 3: 观察 10 分钟**

```bash
sleep 600
cd /home/flintylemming/appdata/8850-new-api
docker compose logs --since 10m new-api | grep -i langfuse | head -20 || echo "no langfuse log lines"
docker stats --no-stream new-api
```

Expected: 无持续导出错误；内存回落到低于全采样阶段的水平。Langfuse UI 中 trace 仍在持续产生（约为总请求量的十分之一）。

- [ ] **Step 4: 写部署台账**

新建 `docs/internal/patches/2026-08-15-langfuse-local-deployment.md`（目录一并创建），用以下六个小节：

- 变更内容：8850 从上游 `0ab02020` 切到 fork `750452c0`（镜像 `new-api:langfuse-750452c0`）；新增 `8858-langfuse-newapi` Langfuse 4.2.0 实例。
- 部署影响：新增 option keys `langfuse_setting.*`；`midjourneys` 新增两列；`8850-new-api` 的 compose 依赖 external 网络 `8858-langfuse-newapi_default`（Langfuse 栈必须先于 new-api 启动）。
- 容量规划：全采样与 0.1 采样下的预留/常驻数值（640 KiB、819 槽、约 552 MiB）。
- 回滚：三级回滚步骤与 Task 2 的备份/digest 文件路径。
- 已知限制：minio 未发布端口 → Langfuse UI 媒体预览不可用。
- Task 1 的七条验证结果。

```bash
cd /mnt/extend/Projects/new-api
git add docs/internal/patches/
git -c user.name=Claude -c user.email=noreply@anthropic.com commit -m "docs(internal): langfuse local deployment ledger

Co-Authored-By: Claude <noreply@anthropic.com>"
```

Expected: 提交成功。

- [ ] **Step 5: 收尾核对**

```bash
docker ps --format '{{.Names}}\t{{.Image}}\t{{.Status}}' | grep -E 'new-api|8858-langfuse'
curl -sS http://127.0.0.1:8850/api/status | jq '.success'
```

Expected: `new-api` 跑在 `new-api:langfuse-750452c0` 且 healthy；八个相关容器（new-api 四个 + langfuse 六个中的可见项）状态正常；`/api/status` 返回 `true`。

---

## Self-Review 已核对

- **设计文档覆盖**：§4（新 Langfuse 栈）→ Task 3；§5（镜像与 compose）→ Task 4/5；§6 Step 0–8 → Task 1/2/3/4/5/6/7/8 一一对应；§7 验收 8 条 → Task 7 Step 1–6（第 8 条资源观察拆到 Step 6）；§8 三级回滚 → Task 5 注记 + Task 8 台账；§9 风险 → Task 5 Step 7（计费冒烟）、Task 7 Step 5（故障隔离）、Task 8 Step 3（降采样后观察）。
- **占位符扫描**：无 TBD/TODO。两处需操作者提供的值（`NEWAPI_TEST_KEY`、`NEWAPI_ROOT_TOKEN`）已写明获取方式；`$TS` 与镜像 tag 后缀均由命令输出决定并注明替换规则。
- **一致性**：镜像 tag `new-api:langfuse-750452c0` 在 Task 4/5/8 一致；网络名 `8858-langfuse-newapi_default` 在 Task 3/5/8 一致；Host `http://langfuse-web:3000` 在 Task 6/8 一致；`UpdateRequest` 字段名与 `service/langfuseconfig/service.go:41-59` 逐一对齐（`secret_key` 传空保留旧值的语义已在 Task 8 Step 1 说明）。
