# Langfuse 对话追踪接入指南

New API 支持把对话级追踪导出到 [Langfuse](https://langfuse.com)：每个受支持的共享 HTTP 对话请求生成一个
trace，包含 user/session、每次上游尝试的 generation（渠道、真实上游模型、耗时、usage 桶、New API 权威成本），
以及限长脱敏后的请求/响应正文。

该功能**默认关闭**，不配置就完全不生效。开启后 telemetry 的任何故障（Langfuse 宕机、凭证失效、队列饱和）都
不会改变 relay 的状态码或响应正文。

本文假设你已经有一套 `docker-compose.yml` 跑着 new-api + PostgreSQL + ClickHouse（ClickHouse 用于
`LOG_SQL_DSN` 日志库），在此基础上部署并接入 Langfuse。

---

## 1. Langfuse 的依赖，以及哪些能和 new-api 共用

Langfuse v4 自身需要 **PostgreSQL + ClickHouse + Redis + S3 兼容对象存储** 四样。下表是在本机实测
（Langfuse `4.6.0`）得到的共用结论，不是推断：

| 组件 | 能否与 new-api 共用 | 实测结论 |
| -- | -- | -- |
| PostgreSQL | ✅ 共用同一个 server，各自独立 database | `postgres:15`（new-api compose 默认版本）上 Langfuse 429 个 Prisma 迁移全部通过 |
| ClickHouse | ⚠️ 共用同一个 server，各自独立 database，**但版本必须 25.x** | `24.8` 直接失败（见下）；`25.12` 上 Langfuse 13 张表与 new-api 的 `new_api_logs.logs` 正常共存，new-api 日志写入正常 |
| Redis | ✅ 共用同一个实例，Langfuse 用不同 db index | `REDIS_CONNECTION_STRING=redis://:密码@redis:6379/3` 正常工作 |
| S3 / MinIO | ❌ 必须新增 | Langfuse v3+ 强制要求对象存储存放 ingestion 事件 |

### ClickHouse 版本是硬门槛

new-api 的 `docker-compose.yml` 注释里给的 ClickHouse 是 `24.8`。Langfuse 4.6 在这个版本上**迁移会失败**：

```
error: migration failed in line 0: CREATE TABLE IF NOT EXISTS events_full
  (details: code: 80, message: Only literals can be skip index arguments)
```

原因是 `events_full` 表声明了 `INDEX ... TYPE text(tokenizer = splitByNonAlpha)` 这种带命名参数的全文索引，
24.8 的语法不支持。失败后 golang-migrate 会把迁移表标记为 dirty，容器进入
`Dirty database version 39` 重启循环，必须清库重来。

**结论**：想共用 ClickHouse，就得把它升到 25.x（Langfuse 官方 compose 用的是 `25.12`）。如果你不接受动现有
日志库的 ClickHouse 版本，就给 Langfuse 单独起一个 ClickHouse 容器，其他三样（PG/Redis）照样共用。

> ⚠️ 上面验证的是**全新 25.12 上 new-api 日志表能正常创建和写入**，不是 `24.8 → 25.12` 的原地升级。
> 升级已有 ClickHouse 数据前请按 ClickHouse 官方升级说明操作并先备份。

---

## 2. 建库

Langfuse 的迁移工具**不会自动建库**：PostgreSQL 和 ClickHouse 的目标 database 都必须先存在。

新建 `init/` 目录，放两个初始化脚本（仅对**全新**数据卷生效；已经跑起来的库直接手工执行同样的 SQL）：

`init/postgres-langfuse-db.sql`：

```sql
CREATE DATABASE langfuse;
```

`init/clickhouse-langfuse-db.sql`：

```sql
CREATE DATABASE IF NOT EXISTS langfuse;
```

已有数据卷的话，直接执行：

```bash
docker compose exec postgres psql -U root -d postgres -c "CREATE DATABASE langfuse;"
```

```bash
docker compose exec clickhouse clickhouse-client --user default --password 123456 --query "CREATE DATABASE IF NOT EXISTS langfuse"
```

---

## 3. compose 改动

### 3.1 给现有服务挂初始化脚本

```yaml
  postgres:
    image: postgres:15
    volumes:
      - pg_data:/var/lib/postgresql/data
      - ./init/postgres-langfuse-db.sql:/docker-entrypoint-initdb.d/10-langfuse.sql:ro   # 新增

  clickhouse:
    image: clickhouse/clickhouse-server:25.12   # ⚠️ 由 24.8 升到 25.x，见第 1 节
    volumes:
      - clickhouse_data:/var/lib/clickhouse
      - ./init/clickhouse-langfuse-db.sql:/docker-entrypoint-initdb.d/10-langfuse.sql:ro  # 新增
```

ClickHouse 还需要健康检查，否则 Langfuse 会在它就绪前启动并反复重启：

```yaml
    healthcheck:
      test: wget --no-verbose --tries=1 --spider http://localhost:8123/ping || exit 1
      interval: 5s
      timeout: 5s
      retries: 20
      start_period: 5s
```

### 3.2 新增三个服务

把下面这段加进 `services:`，注意全部挂在 new-api 已有的 `new-api-network` 上。所有 `CHANGEME` 都要改。

```yaml
  minio:
    image: cgr.dev/chainguard/minio
    container_name: langfuse-minio
    restart: always
    entrypoint: sh
    command: -c 'mkdir -p /data/langfuse && minio server --address ":9000" --console-address ":9001" /data'
    environment:
      MINIO_ROOT_USER: minio
      MINIO_ROOT_PASSWORD: miniosecret   # CHANGEME
    ports:
      - "127.0.0.1:9090:9000"   # 仅为浏览器加载 Langfuse 里的多模态附件所需
    volumes:
      - minio_data:/data
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 5s
      timeout: 5s
      retries: 10
      start_period: 5s
    networks:
      - new-api-network

  langfuse-worker:
    image: langfuse/langfuse-worker:4.6.0
    container_name: langfuse-worker
    restart: always
    depends_on: &langfuse-depends-on
      postgres: {condition: service_healthy}
      clickhouse: {condition: service_healthy}
      minio: {condition: service_healthy}
      redis: {condition: service_started}
    environment: &langfuse-env
      NEXTAUTH_URL: http://localhost:3100          # 对外访问 Langfuse 的地址
      SALT: mysalt                                  # CHANGEME
      NEXTAUTH_SECRET: mysecret                     # CHANGEME
      ENCRYPTION_KEY: "0000000000000000000000000000000000000000000000000000000000000000"  # CHANGEME: openssl rand -hex 32，必须加引号
      TELEMETRY_ENABLED: "false"

      # 与 new-api 共用 PostgreSQL，独立 database
      DATABASE_URL: postgresql://root:123456@postgres:5432/langfuse   # CHANGEME

      # 与 new-api 共用 ClickHouse，独立 database；单机部署必须显式关掉 cluster
      CLICKHOUSE_URL: http://clickhouse:8123
      CLICKHOUSE_MIGRATION_URL: clickhouse://clickhouse:9000
      CLICKHOUSE_USER: default
      CLICKHOUSE_PASSWORD: "123456"                 # CHANGEME
      CLICKHOUSE_DB: langfuse
      CLICKHOUSE_CLUSTER_ENABLED: "false"

      # 与 new-api 共用 Redis，独立 db index
      REDIS_CONNECTION_STRING: redis://:123456@redis:6379/3   # CHANGEME

      LANGFUSE_S3_EVENT_UPLOAD_BUCKET: langfuse
      LANGFUSE_S3_EVENT_UPLOAD_REGION: auto
      LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID: minio
      LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY: miniosecret   # CHANGEME
      LANGFUSE_S3_EVENT_UPLOAD_ENDPOINT: http://minio:9000
      LANGFUSE_S3_EVENT_UPLOAD_FORCE_PATH_STYLE: "true"
      LANGFUSE_S3_EVENT_UPLOAD_PREFIX: events/
      LANGFUSE_S3_MEDIA_UPLOAD_BUCKET: langfuse
      LANGFUSE_S3_MEDIA_UPLOAD_REGION: auto
      LANGFUSE_S3_MEDIA_UPLOAD_ACCESS_KEY_ID: minio
      LANGFUSE_S3_MEDIA_UPLOAD_SECRET_ACCESS_KEY: miniosecret   # CHANGEME
      LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT: http://minio:9000
      LANGFUSE_S3_MEDIA_UPLOAD_FORCE_PATH_STYLE: "true"
      LANGFUSE_S3_MEDIA_UPLOAD_PREFIX: media/
      LANGFUSE_S3_BATCH_EXPORT_ENABLED: "false"
    networks:
      - new-api-network

  langfuse-web:
    image: langfuse/langfuse:4.6.0
    container_name: langfuse-web
    restart: always
    depends_on: *langfuse-depends-on
    ports:
      - "3100:3000"        # Langfuse 控制台；生产环境建议只绑 127.0.0.1 并走反向代理
    environment:
      <<: *langfuse-env
      LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT: http://localhost:9090   # 浏览器可达的 MinIO 地址
    networks:
      - new-api-network
```

卷声明补上：

```yaml
volumes:
  pg_data:
  clickhouse_data:
  minio_data:      # 新增
```

几个容易踩的点：

- `ENCRYPTION_KEY` **必须加引号**。全零的 64 位十六进制串不加引号会被 YAML 解析成数字 `0`，
  Langfuse 启动时报 `ENCRYPTION_KEY must be 256 bits, 64 string characters in hex format`。
- `CLICKHOUSE_CLUSTER_ENABLED` 在 Langfuse 的配置里**默认是 `true`**，单机部署必须显式写 `"false"`，
  否则会去跑集群版迁移。
- Langfuse 的镜像 tag 建议钉死小版本（如 `4.6.0`），不要用浮动的 `4`。

启动：

```bash
docker compose up -d
```

`langfuse-web` 首次启动会跑完 PostgreSQL 与 ClickHouse 迁移，约需 1 分钟。确认就绪：

```bash
curl -s http://127.0.0.1:3100/api/public/health
```

返回 `{"status":"OK","version":"4.6.0"}` 即可。

---

## 4. 建 project 并拿 API Key

浏览器打开 `http://<你的地址>:3100`，注册账号 → 建 Organization → 建 Project → 在 **Settings → API Keys**
创建一对 key，记下 Public Key（`pk-lf-…`）和 Secret Key（`sk-lf-…`）。Secret Key 只显示一次。

也可以在 `langfuse-web` 的 environment 里用初始化变量自动建好（仅首次启动生效）：

```yaml
      LANGFUSE_INIT_ORG_ID: my-org
      LANGFUSE_INIT_ORG_NAME: My Org
      LANGFUSE_INIT_PROJECT_ID: new-api
      LANGFUSE_INIT_PROJECT_NAME: New API
      LANGFUSE_INIT_PROJECT_PUBLIC_KEY: pk-lf-xxxxx      # CHANGEME
      LANGFUSE_INIT_PROJECT_SECRET_KEY: sk-lf-xxxxx      # CHANGEME
      LANGFUSE_INIT_USER_EMAIL: admin@example.com        # CHANGEME
      LANGFUSE_INIT_USER_NAME: Admin
      LANGFUSE_INIT_USER_PASSWORD: change-me             # CHANGEME
```

**可选**：如果希望 Langfuse 自己也能按模型算价（New API 有权威成本时不需要，Langfuse 会直接采用），
在 Settings → Models 里配置模型价格。

---

## 5. 在 New API 侧启用

用**管理员**账号进入「系统设置 → 运维 → Langfuse 追踪」（`/system-settings/operations/langfuse`），填写：

| 字段 | 值 |
| -- | -- |
| Host | `http://langfuse-web:3000` —— 同一 compose 网络内用服务名，明文 HTTP 即可 |
| Public Key | `pk-lf-…` |
| Secret Key | `sk-lf-…` |
| Sample Rate | 采样率，首次启用**必须显式选择** |
| Send Content | 是否上报请求/响应正文，首次启用**必须显式选择** |

Host 也支持带子路径的部署（如 `https://obs.example.com/langfuse`），New API 会自动拼成
`<你的 Host>/api/public/otel/v1/traces`。

也可以走 API：

```bash
curl -X PUT http://127.0.0.1:3000/api/option/langfuse -H "Authorization: Bearer <管理员 token>" -H 'Content-Type: application/json' -d '{"enabled":true,"host":"http://langfuse-web:3000","public_key":"pk-lf-xxxxx","secret_key":"sk-lf-xxxxx","environment":"default","sample_rate":1,"send_content":true,"max_content_bytes":65536,"max_response_bytes":524288,"max_in_flight_capture_bytes":536870912,"max_session_body_bytes":65536,"session_header_names":["X-Langfuse-Session-Id"],"session_body_paths":[],"queue_size":64,"batch_size":16,"flush_interval_seconds":5}'
```

关于配置行为，有几点是刻意设计的：

- 整组配置**原子保存**，不能逐项改。通用的 `/api/option/` 接口会拒绝一切 `langfuse_setting.*` 键。
- Secret Key **永远不会被任何接口返回**，读接口只告诉你「是否已配置」。
- 首次启用或重新启用时，Sample Rate 和 Send Content 必须显式给出，缺任一项都不会保存，避免默认值造成
  「以为没开正文上报、其实开了」。
- 想按会话把多个请求归到一个 Langfuse session，让客户端带 `X-Langfuse-Session-Id` 请求头
  （或在 Session Body Paths 里配置从请求体读取的精确路径）。session 会自动按用户隔离，
  不同用户带相同的 session 值不会串到一起。

### 验证链路

发一个正常的对话请求，然后在 Langfuse 控制台的 Tracing 里应该能看到一条 trace，名字形如
`openai <你的模型名>`，下面挂着一条 generation，带 usage 和成本。

---

## 6. 容量与内存规划

正文采集是这个功能唯一显著吃内存的部分，必须按下面的口径规划，**不能只靠调采样率**。

**单请求预留**（与重试次数无关的常量）：

```
单请求预留 = 2 × max_content_bytes + max_response_bytes
```

出厂默认 `max_content_bytes=64 KiB`、`max_response_bytes=512 KiB`，即 **640 KiB/请求**。

**并发槽位**：预留从请求开始一直持有到异步 worker 完成，所以它是并发槽位而不是吞吐配额：

```
并发槽位 = max_in_flight_capture_bytes ÷ 单请求预留
```

默认 512 MiB ÷ 640 KiB ≈ **819 个槽位**。按平均捕获生命周期 30 秒估算，约支撑 **27 已采样 RPS**；
若 `sample_rate=0.1` 且流量分布均匀，约对应 270 总 RPS。超出后新请求会降级为「只有元数据、没有正文」
（`capture_state=budget`），不会丢 trace，也不会影响 relay。

**正文常驻内存规划值**（默认配置）：

| 项 | 公式 | 默认值 |
| -- | -- | -- |
| 捕获预算 | `max_in_flight_capture_bytes` | 512 MiB |
| 单 span 正文包络 | `2 × max_content_bytes + max_response_bytes` | 640 KiB |
| 常驻基线 | `捕获预算 + queue_size × 单 span 包络` | ≈ 552 MiB |
| 导出阶段规划值 | `捕获预算 + (queue_size + 3 × batch_size) × 单 span 包络` | ≈ 582 MiB |

这只是**正文这一层**的规划值，不是进程总内存承诺；relay 业务流量和 Go runtime 仍需另外留量。

调优方向：

- 内存吃紧：优先调低 `max_content_bytes` / `max_response_bytes`（直接线性降低单请求预留），
  或调低 `sample_rate`。
- 长流式响应多：捕获生命周期变长，槽位周转变慢，应同时调低内容上限，而不是只加大预算。
- 完全不要正文：把 Send Content 关掉。此时不预留预算、不包装 writer、不读取正文，
  user/session、耗时、模型、usage、成本仍然完整保留。

**Langfuse 自身的资源占用**（本机实测、空载稳定状态，仅供估算）：

| 容器 | 内存 |
| -- | -- |
| langfuse-web | ≈ 970 MiB |
| langfuse-worker | ≈ 860 MiB |
| clickhouse | ≈ 610 MiB |
| minio | ≈ 100 MiB |

也就是说，光 Langfuse 这套就要预留 **2.5 GiB 以上**内存，实际写入量上来后 ClickHouse 还会更高。

---

## 7. 反向代理与 ingress body limit

同一 compose 网络内直连 `http://langfuse-web:3000` 时不存在 ingress 限制，本节可以跳过。

如果 New API 与 Langfuse 之间隔着 nginx / 网关（例如 Langfuse 部署在另一台机器，Host 填
`https://obs.example.com`），必须放开请求体大小限制：OTLP 是**批量**发送的，一个请求里可能有
`batch_size` 个 span，每个 span 都带正文。

按下式配置**解压后**的 body 上限：

```
最坏单请求体积 = batch_size × (2 × max_content_bytes + max_response_bytes)
```

出厂默认（`batch_size=16`）约为 **10 MB**。实测数据（真实 exporter 出网字节）：

| 配置 | 单请求 protobuf | gzip 后（不可压缩正文） |
| -- | -- | -- |
| 默认：content 64 KiB / response 512 KiB / batch 16 | 2,112,371 | 1,592,265 |
| content 1 MiB / response 64 KiB / batch 1 | 2,098,180 | 1,580,439 |
| content 4 KiB / response 8 MiB / batch 1 | 9,209 | 3,997 |

注意 `max_response_bytes` 只影响**响应捕获缓冲**，不会整块变成 span 属性——所以第三行的配置公式算出来
8.4 MB，实际单 span 只有约 9 KB。按公式配是安全的高估。

nginx 示例：

```nginx
location /api/public/otel/ {
    proxy_pass http://langfuse-web:3000;
    client_max_body_size 32m;
    proxy_request_buffering off;
}
```

New API 发出的请求带 `Content-Encoding: gzip`，代理层若按压缩后大小限制，可以适当调小；但**不要**按
Langfuse 日志里那两个警告阈值（单 span 9,500,000 字节、请求体 16 MiB）来推算 ingress 限制——它们只记录
警告，不拒绝数据，也不是 Langfuse 的请求体限制。

---

## 8. 本文实测环境

| 项 | 版本 |
| -- | -- |
| Langfuse | `langfuse/langfuse:4.6.0`、`langfuse/langfuse-worker:4.6.0` |
| PostgreSQL | `postgres:15`（new-api compose 默认版本） |
| ClickHouse | `clickhouse/clickhouse-server:25.12`（`24.8` 实测不可用） |
| Redis | `redis:latest`，Langfuse 使用 db index 3 |
| 对象存储 | MinIO |

验证内容：Langfuse 迁移在共用 PostgreSQL/ClickHouse 上全部通过；new-api 的 `new_api_logs` 日志库与
Langfuse 的 `langfuse` 库在同一个 ClickHouse 实例上共存且写入正常；一次真实对话请求经
`http://langfuse-web:3000` 完成 ingestion，trace 含正确的 user/session、usage 桶与权威成本。
