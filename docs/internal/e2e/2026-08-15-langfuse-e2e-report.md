# Langfuse 对话追踪 — 真实 Langfuse E2E 验收报告

| 项 | 值 |
| -- | -- |
| 日期 | 2026-08-15 |
| 分支 | `feature/langfuse-tracing` @ `7a175d140` |
| 依据 | 设计文档 §14.6 / §15，`plan-8-verification-e2e.md` Task 3 |
| Langfuse | `langfuse/langfuse:4.6.0`（`sha256:2e535d8e163c…`）+ `langfuse/langfuse-worker:4.6.0`（`sha256:fcbf2ee82fa3…`） |
| 依赖 | postgres:17、redis:7、clickhouse/clickhouse-server:25.12、chainguard/minio、nginx:1.27-alpine（base path 前置） |
| 结果 | 13/13 用例组通过；过程中发现并修复 1 个真实缺陷（见「缺陷」一节） |

正文、凭证、Basic header 与 raw session 原值都不出现在本报告中：只记录用例形状、trace/observation ID
与脱敏状态。

## 1. 环境与固定项

### 1.1 revision 固定方式与偏差

设计文档 §17 的核验基线是 Langfuse revision `2aa50493a`。该 revision 是 `v4.6.0` 之后第 57 个 commit
（`git describe` = `v4.6.0-57-g2aa50493a`，2026-08-10），**没有**与之精确对应的已发布镜像；该 revision 自带的
`docker-compose.yml` 只引用浮动的 `:4` tag（当前解析到 ≥ 4.9.0，比基线更远）。

本次按决策使用**最接近的已发布 tag `4.6.0`**，并核对了 v4.6.0 与 `2aa50493a` 之间与本功能相关的全部差异：

| 路径 | 差异 | 对本功能的影响 |
| -- | -- | -- |
| `packages/shared/src/server/otel/OtelIngestionProcessor.ts` | 新增 `ArraySlotBudget`：对**点号展开的数组属性路径**（如 `0.role`、`9999.content`）限制重建的数组槽位总数（上限 10001），超限丢弃该属性并打点 | 无影响。New API 只发送整段 JSON 字符串属性 `langfuse.observation.input/output`，不产生任何点号数组路径 |
| `packages/shared/src/env.ts` | 新增 SSO discovery 白名单变量；删除 in-app agent 执行模式开关 | 无影响。`LANGFUSE_OTEL_MAX_SPAN_BYTES` 默认值在两处都是 `9_500_000`，未变 |
| `web/src/pages/api/public/otel/v1/traces/index.ts` | 无差异 | — |
| `worker/src/services/IngestionService/` | 无差异 | — |
| `packages/shared/src/server/ingestion/types.ts` | 无差异 | — |

结论：ingestion 路由、usage/cost 入库、推价与 root merge/fallback 逻辑在 `v4.6.0` 与 `2aa50493a` 之间**逐字节相同**，
本报告的所有观察对固定 revision 同样成立。

### 1.2 部署形态

- 端口重映射（本机 3000/3001/5432 已被占用）：langfuse-web `127.0.0.1:3100`，base path 前置 `127.0.0.1:3101`。
- 写入模式：镜像默认 `LANGFUSE_MIGRATION_V4_WRITE_MODE=events_only`，该模式下 v1 读 API 全部关闭。为了能从 API 读回
  **trace 域**字段（name/input/output/metadata/userId/sessionId）验证 fallback 与 merge，改用
  `dual` 并开启 `LANGFUSE_MIGRATION_V4_ALLOW_PREVIEW_OPT_IN`。写入模式只影响读回路径，不影响
  `OtelIngestionProcessor` 的 OTLP → ingestion event 转换，即本次验证的对象。
- 固定 project：`e2e-project`，固定 Public/Secret Key（本地一次性凭证，值不在此记录）。
- 固定 model prices（`e2e-test-model`，USD/token）：`input=3e-6`、`output=6e-6`、`input_cached_tokens=1.5e-6`、
  `input_cache_creation=3.75e-6`、`input_image_tokens=4e-6`、`input_audio_tokens=1e-5`、`output_audio_tokens=2e-5`。
- New API：本分支源码构建，SQLite，`PORT=3010`；经专用 `PUT /api/option/langfuse` 启用
  （`sample_rate=1`、`send_content=true`、`session_header_names=["X-Langfuse-Session-Id"]`）。
- 上游：真实 OpenAI 兼容渠道（`https://new-api.mitsea.com`），模型映射 `e2e-test-model → deepseek-v4-flash`。
- 轮询：每用例 deadline 60s，trace 按 `sha256(request_id)[:16]` 直接定位，不做列表扫描。

## 2. 用例与结果

`plan-8` Task 3 Step 2 的六组用例逐条对应如下。所有 trace ID 均可在本地 Langfuse 中复查。

### 2.1 trace name fallback 与 root 预览（Step 2.1，§15.1/§15.6）

**输入形状**：一次成功的 `POST /v1/chat/completions`，`X-Langfuse-Session-Id` 携带显式 session；root span 只写
observation 域 input/output/metadata 与 `langfuse.internal.as_root="true"`，不写任何 `langfuse.trace.*`。

- trace `99f7668b8c3465669da8fd557e124ae9`，root observation `d77836532992652f`，generation `6c885b1200f55809`

| 断言 | 观察 |
| -- | -- |
| trace name = root span name | 两者均为 `openai e2e-test-model` |
| trace 列表预览正文来自 root fallback | trace.input/output 与 root observation 的 input/output **逐字段相同** |
| trace metadata 由 root observation metadata merge 得到 | 含 `capture_state=full`、`quota`、`billing_source`、`request_id` 等 |
| root observation 详情保留正文 | input/output 均非空 |
| user/session | `user.id=1`，`session.id=1:<raw>`；raw session 未以裸值出现 |

同一 trace 另按 §15.1 核对：generation 携带真实上游模型 `deepseek-v4-flash`、canonical `input`/`output` 桶、
`total` 精确等于非 total 桶之和、New API 权威 cost（等于 `quota / QuotaPerUnit`）、`billing_source=wallet`，
input/output 均为已脱敏的结构化 JSON。

### 2.2 usage 入库口径（Step 2.2）

**输入形状**：E2E 专用 crafted span（生产映射不会产生的形状），一条 generation 同时携带内建口径与自定义 usage type。

- trace `45241a93db344e72510468a1bca4d1f2`，generation `dc2597553545e25e`

| 发送 key | 入库结果 |
| -- | -- |
| `input_cache_creation=100` | 原样入库（内建口径） |
| `output_audio_tokens=25` | 原样入库（内建口径） |
| `input_cached_tokens=800` | 原样入库 |
| `input_image_tokens=300` | 原样保留（New API 自定义 usage type） |
| `input_audio_tokens=50` | 原样保留（New API 自定义 usage type） |
| 全部 8 个桶 | 一个都没有被改名或丢弃 |

**故意拼错 key（仅 E2E）**：trace `c5ecbb19222728733c32df38fb45bae2`，generation `77ec1379350b6bd7`。
发送 `input_cach_creation=100`，入库后仍是 `input_cach_creation`，Langfuse **不会**纠正为
`input_cache_creation`。这证明生产映射选择 canonical key 是有意为之；该拼错 key 绝不出现在生产 builder 中
（`service/langfuse/usage.go` 只产出 canonical 名称，由 Go 契约测试固定）。

### 2.3 推价抑制（Step 2.3，§15.16）

三条 crafted generation，全部使用上表的 `e2e-test-model` 价格：

| 用例 | trace / observation | 观察 |
| -- | -- | -- |
| `Error + model + 无 usage + 无 cost` | `db6f8237e2ff9bcf6c8be4b7bbd89688` / `9a847ad0cf57334d` | level=ERROR，**未**触发 tokenisation：usageDetails 与 costDetails 都为空 |
| `Error + model + usage + 无 cost` | `e5c29406507c98ea8b63ba55d17e2d4b` / `ab3a141ac50a48ce` | level=ERROR 仍被推价：`input 1000×3e-6 + output 500×6e-6`，入库 `costDetails={"input":0.003,"output":0.003,"total":0.006}` |
| 成功 + usage + model + 权威 cost | `8ccd40731f9e553dd5f9c2b568722530` / `882fef363d147ad2` | cost 保持 New API 值 `{"total":0.000002}`（`Quota=1, QuotaPerUnit=500000`），未被 model prices 覆盖 |

**结论**：Error status 只能阻止「缺 usage 时的 tokenisation」，**阻止不了** model prices × usage 推价。
因此设计文档 §6.2 要求 New API 对 `Error + usage + 无 cost` 省略全部 model 识别属性，是必要防护而非保守选择；
§15.16 的两层要求（Go 契约 + 本 E2E）至此都已满足。

### 2.4 denylist 与多 attempt（Step 2.4，§15.4/§15.17）

**输入形状**：临时加入一个 key 无效、优先级更高的渠道，令第一次上游调用 401 失败后重试到正常渠道成功
（`RetryTimes=2`）。

- trace `6bcb1c6d11f22c5bcc09f670caaf9dd9`，root `9d6ac979f77e1900`，generations `4cfaa4db113174a6`（失败）、
  `a71f58bf4effbb66`（成功）

| 断言 | 观察 |
| -- | -- |
| 每次尝试各一条 generation | 2 条，顺序与 attempt 顺序一致 |
| 失败 attempt 为 Error | level=ERROR（**修复后**，见第 3 节） |
| usage / cost 只属于成功 attempt | 各只有 1 条 generation 携带 |
| root 的 user/session 不被 attempt 覆盖 | `user.id=1`、`session.id=1:<raw>` 保持不变 |
| root metadata 保留结算摘要 | `attempt_count=2`，`quota` 为整数 |
| generation 无 denylist 属性 | 两条 generation 都没有 `user.id`/`session.id`/`langfuse.trace.*` |
| root trace name 不被覆盖 | 仍为 `openai e2e-test-model` |

### 2.5 两种部署路径（Step 2.5，§15.9）

| 部署形态 | Host 配置 | 结果 |
| -- | -- | -- |
| HTTP 根路径 | `http://127.0.0.1:3100` | 全部上述用例，明文 HTTP 完成真实 ingestion |
| base path | `http://127.0.0.1:3101/langfuse` | trace `52a714867ac96afc70b636f070d788c0` 完成真实 ingestion |

固定 revision 的官方镜像是预构建产物，`NEXT_PUBLIC_BASE_PATH` 是构建期变量，因此**镜像本身不支持子路径部署**。
本次用 nginx 前置：`/langfuse/...` 剥离前缀后转发到 langfuse-web，New API 的 exporter 实际发往
`/langfuse/api/public/otel/v1/traces`，由代理还原为 `/api/public/otel/v1/traces`。这验证了 exporter 侧的
base path 拼接与真实 ingestion 的连通性；Langfuse 应用自身在子路径下的行为不在本次覆盖范围内，由 plan-3 的
exporter HTTP 集成测试（精确命中 `/langfuse/api/public/otel/v1/traces`）覆盖。

### 2.6 验收标准端到端抽查（Step 2.6）

| §15 条目 | 观察 |
| -- | -- |
| 1 成功请求的完整 trace | 见 2.1：user/session、上游模型、canonical 桶、权威 cost、脱敏正文齐备 |
| 2 session 归组与作用域 | 同一 user+session 的 4 个请求在 `sample_rate=0.5` 下**整组命中**（4/4）；两个不同用户携带**相同 raw session** 得到不同 `session.id`（各自以自己的 userId 为前缀），trace `2554a81428c6b3f8573bb58e74d9ce5a` 与 `db73d3bad4c5708ae21cc6087619b555` |
| 4 重试与结算归属 | 见 2.4 |
| 6 root 正文与 as_root 语义 | 见 2.1：root 只写 observation 域，trace 域内容全部由 Langfuse fallback/merge 得到 |
| 9 配置原子性与密钥保护 | 通用 `PUT /api/option/` 修改 `langfuse_setting.enabled` 被拒绝；通用 `GET /api/option/` 不含任何 `langfuse_setting.*`；专用 GET 只返回 `secret_key_configured=true`，从不返回密钥；`http://` Host 保持明文 HTTP |

**额外（不在 Step 2 清单内）**：`send_content=false` 时 trace `9057d9be2a509305169a771bd42a2ecf` 为
metadata-only：`capture_state=disabled`，root 与 trace 都没有 input/output，而 user/session 与 generation 的
usage 仍保留（§15.7）。

## 3. 缺陷：失败 attempt 未被标记为 Error（已修复）

**发现方式**：2.4 首次执行时，401 失败的 attempt 在 Langfuse 中显示为 level=DEFAULT 的普通 generation，
而 New API 日志明确记录了 `channel error (channel #2, status code: 401)` 且实际发生了重试
（`use_channel: ["2","1"]`）。

**根因**：`service/langfuse/finish.go` 的 `attemptFailed()` 用 `ErrCode != ""` 判定失败，而 `ErrCode` 取自上游
响应体的 `error.code`。`relaykit/types` 的 `WithOpenAIError` 只在 code 为 nil 或非字符串时兜底为
`unknown_error`，**显式的空字符串 `"code": ""` 会原样通过**。该上游正是这种形状，于是失败 attempt 的
`ErrCode` 为空，OTel Error status 从未被设置。

**影响**：不涉及计费安全——`allowModelName(hasCost=false, hasUsage=false, isError=false)` 反而更保守地省略了
model 属性，不会引入非权威推价。真正的影响是可观测性：失败尝试在 Langfuse 中看起来像成功，违反设计
§14.2 的 Error status 映射与 §15.4。

**修复**（commit `7a175d140`）：在 attempt 快照错误的同一处记录独立的 `Failed` 标志，`attemptFailed()` 改用该
标志；错误码继续只作为 metadata。回归测试
`TestFailedAttemptWithEmptyUpstreamErrorCodeStillFails` 构造空 code 的上游错误，断言 generation 为
`codes.Error`、status message 非空、且按 §6.2 允许写 model 名（该场景已由 2.3 第一条证明不会被推价）。
已确认该测试在修复前失败、修复后通过。修复后 2.4 全部断言通过。

## 4. ingress body limit 实测依据（Task 1）

来自 `service/langfuse/otlp_envelope_test.go`（真实 exporter → httptest，解 gzip 后用 `proto/otlp` 解码）。
这些是 **wire 实测值**，不等于 Langfuse TypeScript 侧 `JSON.stringify(span)` 的 `eventBytes`，也不构成对
`9_500_000` 阈值行为的断言。

最大单 span（input/output 各取满 `max_content_bytes` 的最坏转义正文）：

| 配置 | 配置包络 `2*content+response` | 单 span protobuf | 请求 protobuf | 请求 gzip |
| -- | -- | -- | -- | -- |
| content=1MiB, response=64KiB（不可压缩 ASCII） | 2,162,688 | 2,098,173 | 2,098,349 | 1,580,533 |
| content=1MiB, response=64KiB（引号/反斜杠/控制符） | 2,162,688 | 1,499,002 | 1,499,175 | 4,602 |
| content=1MiB, response=64KiB（多字节 UTF-8） | 2,162,688 | 2,098,154 | 2,098,330 | 5,052 |
| content=4KiB, response=8MiB（三种素材） | 8,396,800 | ≤ 9,207 | ≤ 9,377 | ≤ 4,060 |
| 出厂默认 content=64KiB, response=512KiB | 655,360 | ≤ 132,093 | ≤ 264,207 | ≤ 199,149 |

满 batch（同类 span 填满 `batch_size`）：

| 配置 | batch_size | 请求内 span 数 | 请求 protobuf | 请求 gzip | 最坏投影 `batch*包络` |
| -- | -- | -- | -- | -- | -- |
| content=1MiB, response=64KiB | 1 | 1 | 2,098,180 | 1,580,439 | 2,162,688 |
| content=4KiB, response=8MiB | 1 | 1 | 9,209 | 3,997 | 8,396,800 |
| 出厂默认 | 16 | 16 | 2,112,371 | 1,592,265 | 10,485,760 |

两点部署结论：

1. `max_response_bytes` 只影响**响应捕获缓冲**，不会直接成为 span 属性——`content=4KiB, response=8MiB` 的配置
   包络是 8.4 MB，实际单 span 只有约 9 KB。按包络公式配置 ingress 是安全的高估，不是实际值。
2. 出厂默认下，单次 OTLP 请求最坏投影约 10 MB（未压缩 protobuf）；不可压缩正文时 gzip 后约 1.6 MB。
   部署者应按 `batch_size * (2*max_content_bytes + max_response_bytes)` 配置 ingress 的解压后 body 上限，
   并为压缩流量单独留量。

## 5. 未覆盖项与原因

| 未覆盖 | 原因 |
| -- | -- |
| Langfuse 应用自身在子路径下运行 | 固定 revision 的官方镜像预构建，`NEXT_PUBLIC_BASE_PATH` 为构建期变量。exporter 侧的子路径拼接由 plan-3 集成测试精确覆盖，本次以反向代理完成了真实 ingestion 连通性验证 |
| `2aa50493a` 精确构建 | 无对应发布镜像；已逐文件核对该 revision 与 `v4.6.0` 在 ingestion 相关路径上无差异（§1.1） |
| 流式请求、Claude/Gemini/Responses 协议的 E2E | 本次上游渠道为 OpenAI 兼容非流式。四协议的聚合与属性映射由 `aggregate_test.go` 的真实 fixture 与 OTLP 契约测试覆盖 |
| ingestion-suspended 403、队列饱和、shutdown 超时 | 需要构造 Langfuse 侧的配额/故障状态；已由 plan-3 的 exporter 集成测试（403 不重试、限频告警、固定脱敏错误、恢复后继续）覆盖 |
| AWS SDK / Xunfei 旁路 | 设计文档 §15.4 明确的 v1 已知例外，无可用渠道，且行为由 Go 契约测试固定 |

## 6. 复现方式

E2E 编排脚本与 compose 文件位于未跟踪的 `.local-tests/langfuse-e2e/`（`.gitignore` 中的本地探针目录）：

```bash
cd .local-tests/langfuse-e2e && docker compose up -d
```

```bash
ADMIN_TOKEN=... RELAY_KEY=... RELAY_KEY_USER_B=... python3 .local-tests/langfuse-e2e/run_e2e.py
```

```bash
go test ./service/langfuse -run TestOtlpEnvelope -v
```
