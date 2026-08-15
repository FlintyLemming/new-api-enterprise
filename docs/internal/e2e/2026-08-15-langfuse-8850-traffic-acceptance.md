# Langfuse 真实流量验收报告（Task 7）

- **执行时间**：2026-08-15 15:14 – 15:34 UTC
- **被测系统**：new-api（容器 `new-api`，镜像 `new-api:langfuse-750452c0`，端口 8850，`StartedAt=2026-08-15T15:09:25Z`，`RestartCount=0`）
- **观测后端**：Langfuse 4.2.0（端口 8858，project `new-api-8850`，**v4 events_only 模式**）
- **结论**：**5 条通过 / 0 条不通过 / 3 条未覆盖（均有结构性原因，非缺陷）**

> 本报告不含任何请求正文、模型输出正文、凭证、令牌明文、数据库口令或 session 原值。
> 正文一律以「字节数 / 结构骨架 / 字段名」表述。

---

## 0. 验收环境与方法

### 0.1 生效中的 Langfuse 配置（读自 `options` 表，密钥已略）

| key | value |
|---|---|
| `langfuse_setting.enabled` | `true` |
| `langfuse_setting.sample_rate` | `1` |
| `langfuse_setting.send_content` | `true` |
| `langfuse_setting.environment` | `production` |
| `langfuse_setting.host` | `http://langfuse-web:3000` |
| `langfuse_setting.batch_size` / `queue_size` / `flush_interval_seconds` | `16` / `64` / `5` |
| `langfuse_setting.max_content_bytes` | `65536` |
| `langfuse_setting.max_response_bytes` | `524288` |
| `langfuse_setting.max_session_body_bytes` | `65536` |
| `langfuse_setting.max_in_flight_capture_bytes` | `536870912` |
| `langfuse_setting.session_header_names` | `[]` |
| `langfuse_setting.session_body_paths` | `[]` |
| `langfuse_setting.public_key` / `secret_key` | 已设置（值不记录） |

验收令牌：`token_id=700`，`token_name=langfuse-e2e-20260815`（明文不记录）。

### 0.2 断言口径：为什么全部走 API / 只读 SQL，而不是 UI

Langfuse 运行在 **v4 events_only** 模式，v3 的 trace 域端点全部下线：

```
GET /api/public/traces            -> 404 "not available on deployments running in Langfuse v4 events_only mode"
GET /api/public/traces/{traceId}  -> 404 （同上）
GET /api/public/observations      -> 404 （同上）
GET /api/public/sessions          -> 404 （同上）
GET /api/public/v2/traces         -> 404 （不存在）
GET /api/public/api-docs/openapi.yml -> 404
```

实际可用面（自行探测 + `GET /generated/api/openapi.yml` 确认）：

| 端点 | 用途 |
|---|---|
| `GET /api/public/v2/observations` | 列 observation，支持 `fields=core,basic,time,io,metadata,model,usage,metrics,trace_context`、`traceId`、`isRootObservation`、`filter=` 结构化过滤 |
| `GET /api/public/v2/metrics` | 以 `traceName` 等维度聚合——**这是 v4 下唯一能读到"trace 名"解析结果的公共接口** |
| `GET /api/public/health` | 存活探测 |
| `GET /generated/api/openapi.yml` | 完整 OpenAPI（481 KB） |

补充证据来源（均为只读）：

- Langfuse ClickHouse（容器 `8858-langfuse-newapi-clickhouse-1`，库 `default`，表 `events_core` / `events_full`；`traces` 表在 events_only 下为空，`count()=0`）；
- new-api 日志库 ClickHouse（容器 `new-api-clickhouse`，表 `newapi.logs`）用于取权威 quota；
- `langfuse-web` 镜像内编译产物中的视图定义，用于确定 Langfuse 自身的字段解析规则（见 §1.1）。

### 0.3 数据快照（冻结于 2026-08-15T15:34:09Z）

`events_full`：**168 span / 84 trace / 84 root / 84 generation**（root : generation = 1 : 1），
时间跨度 `15:14:06.289` – `15:33:08.205`，覆盖 5 个模型、3 个渠道、真实用户流量 + 验收流量。

---

## 1. Step 1：触发并定位一条 trace —— **通过**

### 1.1 输入形状（脱敏）

```
POST http://127.0.0.1:8850/v1/chat/completions
Authorization: Bearer <e2e token>
Content-Type: application/json
body = 136 B
{"model":"llm-lite","messages":[{"role":"user","content":<str:41>}],"max_tokens":16,"temperature":0}
未带任何 session 标识（无 X-Langfuse-Session-Id、无 X-Session-Id、body 无 session 字段）
```

响应：`HTTP 200`，`time_total=0.4956 s`，`size_download=512 B`，`error=null`，
`usage.prompt_tokens=60`、`completion_tokens=16`、`total_tokens=76`。

### 1.2 定位结果

| 项 | 值 |
|---|---|
| new-api `request_id` | `202608151517285601975488268d9d6DAdjs1DY` |
| trace id | `1dbe6947be4f716767a88231f8067e56` |
| root observation | `33b56fa21f4922f6`，`type=SPAN`，`name="openai llm-lite"`，`parentObservationId=null`，`isRootObservation=true` |
| generation | `8d858c5641629957`，`type=GENERATION`，`name="[A6000] Qwen 3.6 27B llm-lite"`，`parentObservationId=33b56fa21f4922f6` |

### 1.3 断言 ①：trace name 由 root span name 回退得到

`events_core.trace_name` 为空串——这是**设计使然**：`buildRootAttributes` 刻意不写任何
`langfuse.trace.*` 属性，由 Langfuse 摄入侧从 span name 回退。

从 `langfuse-web` 编译产物中取出的 **eventsTracesView** 定义（v4 events_only 下 trace 域的真实来源）：

```sql
-- dimension: name
COALESCE(
  nullIf(events_traces.trace_name, ''),
  if(events_traces.parent_span_id = '', nullIf(events_traces.name, ''), NULL)
)
-- aggregationFunction
COALESCE(
  nullIf(argMaxIf(trace_name, event_ts, trace_name <> ''), ''),
  argMaxIf(name, event_ts, parent_span_id = '' AND name <> '')
)
```

即：`trace_name` 为空时**回退到 `parent_span_id=''` 的 root span 的 name**。本 trace 的 root
`parent_span_id` 实测为空串，落在回退分支上。

用 Langfuse 自己的 metrics 接口验证解析结果（`view=observations`，`dimensions=[traceName,type]`，
窗口 15:00–15:40）：

```
{"traceName":"openai deepseek-v4-flash-0731","type":"SPAN","count_count":15}
{"traceName":"openai qwen3.6-27b",          "type":"SPAN","count_count":5}
{"traceName":"openai llm-lite",             "type":"SPAN","count_count":2}
{"traceName":"openai llm-prime",            "type":"SPAN","count_count":2}
{"traceName":null,                          "type":"GENERATION","count_count":24}
```

全量快照复核：84 个 root 中 `name=''` **0 个**、`name LIKE 'unknown%'` **0 个**；
去重后的 root name 为 `openai deepseek-v4-flash-0731`(42) / `openai qwen3.6-27b`(19) /
`openai llm-lite`(18) / `openai llm-prime`(2) / `openai llm-omni`(1)，全部形如 `openai <origin_model>`。

**结论：通过。**

> **记录一处 API 表面差异（非缺陷，供后续查询者避坑）**：
> `GET /api/public/v2/observations` 返回的 `traceName` 字段是 `e.trace_name as "trace_name"`
> 的**原样透传**，因此对本项目所有 observation 都返回空串；同理
> `filter=[{"column":"traceName","operator":"=",...}]` 也匹配不到任何行。
> 只有 metrics / traces 视图会执行上面的 `COALESCE` 回退。查询 trace 名请走 metrics 视图。

### 1.4 断言 ②：trace 列表的预览正文来自 root，且包含 `capture_state`

v4 events_only 下 trace 行的载体就是 root observation，判定式取自同一编译产物：

```js
eventsTableIsRootObservationSql = "(e.parent_span_id = '' OR e.is_app_root = true)"
```

本 trace 的 root 命中前半支（`parent_span_id=''`）。以 root 过滤（`isRootObservation=true`）
读回的行即"trace 列表行"：

```json
{"id":"33b56fa21f4922f6","traceId":"1dbe6947be4f716767a88231f8067e56",
 "name":"openai llm-lite","type":"SPAN","isRootObservation":true,"parentObservationId":null,
 "input_bytes":135,"output_bytes":129,
 "capture_state":"full","request_id_present":true,"user":"1","session":""}
```

root metadata 实际键集（仅键名）：
`request_id, request_path, relay_format, origin_model, is_stream, attempt_count, retry_count,`
`use_time_ms, capture_state, selected_group, username, user_group, token_name, token_id,`
`quota, billing_source, content_truncated, content_redacted`
—— `capture_state="full"`。

全量快照：84/84 root 的 `capture_state="full"`，`content_truncated=false` 84/84。

**结论：通过。**

### 1.5 断言 ③：root observation 详情保留客户端视角的 input/output

- root `input` 135 B / `output` 129 B，均非空；
- 全量快照：84 个 root 中 `input=''` **0 个**、`output=''` **0 个**；
- input 为**客户端原始请求**而非上游转换后载荷：generation metadata 恒带
  `input_source="client_request"`，且 root 与 generation 的 input 长度在 **84/84** 对上完全相等
  （`countIf(root.len != gen.len) = 0`），符合"每个 generation 共享同一份净化后的客户端请求"的设计；
- 内容预算：`events_full` 实测 `length(input)` 最大 **63,186 B**、均值 22,614 B；
  `length(output)` 最大 7,144 B。`max_content_bytes=65536`，**0 条**达到或超过上限，
  `content_truncated`/`output_truncated` 全为 `false`。

> 注：`events_core.input_length` / `length(input)` 在 core 表被截到 200，属 Langfuse 的索引列；
> 完整正文在 `events_full`，公共 API 返回的也是完整值。统计口径以 `events_full` 为准。

**结论：通过。**

---

## 2. Step 2：usage 与 cost 口径 —— **通过（cache/image 已覆盖；cache_creation/audio 未覆盖）**

### 2.1 分桶互斥性：全量不变式扫描

契约是 `total == sum(其余所有桶)`（`normalizeUsageBuckets`，桶之间互斥不重叠）。
对快照内 **83 份** 有 usage 的 generation 逐条求和：

```
sum_violations = 0 / 83
```

导出的桶键集在文本口径下恒为：
`input, output, input_cached_tokens, input_cache_creation, input_image_tokens, output_reasoning_tokens, total`
（缺项按 0 显式出现，因此"互斥且完备"在每一份文档上都可直接验证）。

### 2.2 跨库口径核对：Langfuse 桶 ↔ new-api 权威 usage

按 `request_id` 关联 Langfuse `events_core` 与 `newapi.logs`，比较三条不变式
（快照 15:32，共 **79** 对可关联记录）：

| 不变式 | 结果 |
|---|---|
| `input + input_cached_tokens + input_cache_creation + input_image_tokens + input_audio_tokens == logs.prompt_tokens` | **79 / 79** |
| `output + output_reasoning_tokens + output_audio_tokens == logs.completion_tokens` | **79 / 79** |
| `total == logs.prompt_tokens + logs.completion_tokens` | **79 / 79** |

### 2.3 cache 分桶（真实流量，30 份命中）

样本 trace `e08d4de5427edf4ca9c82f2697b399e4` / generation `d60ca3e183444621`
（真实用户流量，`[H200] Deepseek V4 Flash deepseek-v4-flash-0731`）：

```
usage_details = {input:1293, input_cache_creation:0, input_cached_tokens:71168,
                 input_image_tokens:0, output:624, output_reasoning_tokens:0, total:73085}
```

对照 `newapi.logs`（`request_id=202608151520081130279428268d9d61qYN6e2F`）：
`prompt_tokens=72461`、`insight_cache_tokens=71168`、`completion_tokens=624`、`quota=7309`。

- `1293 + 71168 = 72461` → 折叠口径下 base input 已扣除缓存命中，**不重复计数**；
- `1293 + 71168 + 624 = 73085 = total`；
- 快照内 `input_cached_tokens > 0` 的文档 **30** 份，全部满足上述不变式。

### 2.4 image 分桶（主动构造，1 份）

输入形状（脱敏）：

```
POST /v1/chat/completions   body = 940 B
{"model":"llm-omni","max_tokens":32,"temperature":0,
 "messages":[{"role":"user","content":[
    {"type":"text","text":<str:61>},
    {"type":"image_url","image_url":{"url":"data:image/png;base64,<676 chars, 507 B PNG 96x96>"}}]}]}
```

响应 `HTTP 200`，`usage.prompt_tokens=131`、`prompt_tokens_details.image_tokens=64`、`completion_tokens=32`。

trace `f2a0daacbc6a9653ccb1e26d46fbdd3e`，root `71ae906eeb405a22`，generation `ae3f3b0cf64bad95`：

```
usage_details = {input:67, input_cache_creation:0, input_cached_tokens:0,
                 input_image_tokens:64, output:32, output_reasoning_tokens:0, total:163}
```

- `67 + 64 = 131 = prompt_tokens` → image token **从 base input 中扣出**，互斥成立；
- `67 + 64 + 32 = 163 = total`。

顺带确认脱敏链路：该 root 的 `content_redacted=true`，导出的 input 结构骨架为

```json
{"max_tokens":"int","temperature":"int","model":"<str:8>",
 "messages":[{"role":"<str:4>","content":[
   {"type":"<str:4>","text":"<str:61>"},
   {"type":"<str:9>","image_url":{"url":{"_langfuse_redacted":"bool",
                                         "approx_bytes":"int","media_type":"<str:9>"}}}]}]}
```

—— base64 图像载荷被替换为 `{_langfuse_redacted, approx_bytes, media_type}` 三元组，未进入观测后端。

### 2.5 cost 等于 new-api 权威值，Langfuse 未二次推价

**逐条核对（全量）**：以 root metadata 的 `quota` 与 generation 的 `total_cost` 配对，
断言 `quota == round(total_cost × 500000)`：

```
mismatches = 0 / 79
```

**Langfuse 侧未推价的直接证据**（快照 84 份 generation）：

| 列 | 含义 | 结果 |
|---|---|---|
| `calculated_total_cost != 0` | Langfuse 按 model price 自算的成本 | **0 份** |
| `model_id != ''` | 命中 Langfuse 内置 model 目录（当前 87 条） | **0 份** |
| `total_cost == provided_cost_details['total']` | 最终成本取自我们上报的值 | **84 / 84** |
| `inputPrice` / `outputPrice` / `totalPrice`（API） | Langfuse 单价 | `null` |

举例（Step 1 那条）：`provided_cost_details={'total':0.000152}`、`cost_details={'total':0.000152}`、
`calculated_total_cost=0`、`total_cost=0.000152`；`newapi.logs.quota=76`，`76 / 500000 = 0.000152` ✅。

**模型名 / 成本硬契约的反向验证**：另发一条上游必拒的请求
（`{"model":"llm-lite", ..., "top_p":5}`，body 92 B），上游返回 400
（new-api 侧 `record error log ... status_code=400`，上游拒绝了越界的 `top_p`）。
trace `e194bcddeee197f74f3a6f25643ef62f`：

| observation | level | usage_details | cost_details | cost_source | cost_omitted_reason | provided_model_name |
|---|---|---|---|---|---|---|
| root `429bb70d98604104` (SPAN) | `ERROR` | `{}` | `{}` | — | — | `''` |
| gen `e6a7e0f1a9220ac7` (GENERATION) | `ERROR` | `{}` | `{}` | `unavailable` | `attempt_failed` | `llm-lite` |

符合 `allowModelName(hasCost=false, hasUsage=false, isError=true) = true`：无 usage 的失败 attempt
才允许写模型名，且此时既无 usage 也无 cost，Langfuse 无从推价（`calculated_total_cost=0`）。
root 的 output 保留了客户端可见的错误体（136 B），generation output 为空——错误归属清晰。

**结论：通过。**

### 2.6 未覆盖子项

| 子项 | 结论 | 原因（已实测，非推测） |
|---|---|---|
| `input_cache_creation`（Anthropic 缓存写入） | **未覆盖** | 本部署近 7 天 `newapi.logs` 中 `other LIKE '%cache_creation%'` 命中 **0 条**。`claude-opus-5` 虽在令牌白名单内，但实测被模型映射到 OpenAI 兼容上游（日志 `is_model_mapped=true`、`upstream_model_name=deepseek-v4-flash-0731`、`request_conversion=["OpenAI Compatible"]`），不产生 Anthropic usage 语义。要覆盖必须新增/改渠道 —— 触红线，未做。该桶键在每份文本口径文档中恒以 `0` 出现，互斥性已随 §2.1 的求和不变式一并验证。 |
| `input_audio_tokens` / `output_audio_tokens` | **未覆盖** | 音频分桶来自 `service/quota.go` 的音频结算路径（`/v1/audio/*`）。本部署近 30 天出现过的全部模型（19 个）中无任何音频模型，验收令牌的 11 个可用模型也全部是 chat/embedding/rerank。无可触发路径。 |

---

## 3. Step 3：多 attempt 与 denylist —— **部分通过（多 attempt 未覆盖，denylist 与不覆写通过）**

### 3.1 多 attempt：未覆盖，原因是配置上不可达

按红线要求，不通过改渠道构造，只在真实流量里找天然重试。结果：

1. **配置层面已禁用重试**：`options` 表中 `key ILIKE '%retry%'` **零行**，
   `common.RetryTimes` 保持代码默认 `0`。`controller/relay.go` 的 attempt 循环是

   ```go
   for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
       ...
       langfuse.EndAttempt(c, relayInfo, newAPIError)   // 每轮闭合一个 attempt
       ...
       if !shouldRetry(c, newAPIError, common.RetryTimes-retryParam.GetRetry()) { break }
   }
   ```

   `RetryTimes=0` 时循环体只能执行一次（`0<=0` 成立，自增后 `1<=0` 不成立），
   因此**任何请求的 attempt 数上限就是 1**，与 `shouldRetry` 的返回值无关。

2. **历史流量佐证**：近 3 天 `newapi.logs` 消费日志 **47,919** 条，
   其中 `admin_info.use_channel` 数组长度 > 1 的 **0 条**。

3. **本次窗口佐证**：快照 84 个 root，`attempt_count` 分布为 `{"1": 84}`，无一条多 attempt。

**结论：未覆盖。** 原因：该部署 `RetryTimes=0`，多 attempt 在配置上不可能发生；
制造它必须修改系统设置或渠道状态，属本次明令禁止的操作。**这是环境限制，不是实现缺陷。**

> 建议：若要闭合这一条，应在**非生产实例**上把 `RetryTimes` 设为 ≥1 并制造一次上游 5xx，
> 而不是在 8850 上做。

### 3.2 每次上游调用是独立 generation —— 在单 attempt 深度上通过

快照 84 trace / 84 root / 84 generation，严格 1:1，每个 generation 的
`parent_span_id` 都指向本 trace 的 root。generation 名为 `<渠道名> <上游模型名>`，
与 root 的 `openai <客户端模型名>` 相互独立——`llm-prime`（2 个 root）在上游被映射为
`deepseek-v4-flash-0731`，其 generation 名如实记为
`[H200] Deepseek V4 Flash deepseek-v4-flash-0731`，证明 attempt 维度的渠道/模型身份
不是从 root 抄来的，而是各自独立采集。

### 3.3 root 的 user/session/metadata 不被后续 attempt 覆盖 —— 通过

Langfuse v4 的 trace 级字段聚合规则（同一编译产物，eventsTracesView）：

```sql
userId    : argMaxIf(events_traces.user_id,    event_ts, user_id    <> '')
sessionId : argMaxIf(events_traces.session_id, event_ts, session_id <> '')
name      : COALESCE(nullIf(trace_name,''), argMaxIf(name, event_ts, parent_span_id='' AND name<>''))
```

即"最后一个**非空**值获胜"。因此只要 generation 从不写这些字段，就不可能覆盖 root。
全量实测（84 份 generation）：

| 字段 | 携带该字段的 generation 数 |
|---|---|
| `user_id` | **0** |
| `session_id` | **0** |
| `trace_name` | **0** |
| `release` | **0** |
| `tags` | **0** |
| `version` | **0** |
| `is_app_root` | **0** |

对照组：84 份 root 全部携带 `user_id`，其中 1 份携带 `session_id`（§4 的控制组）。

**结论：通过。**

### 3.4 generation 上没有 denylist 属性 —— 通过

generation 的 metadata 键全集（84 份聚合，`arrayJoin(metadata_names)` 去重）：

```
attempt_index, attempt_end_reason, channel_id, channel_name, channel_type,
relay_format, upstream_relay_format, upstream_model, origin_model, selected_group,
is_stream, input_source, partial_output, output_truncated,
billing_source, cost_source
（+ Langfuse 自动回填的 attributes.* / resourceAttributes.* / scope.name）
```

**不含** `user`、`session`、`trace_*`、`token_name`、`username`、`quota`、`capture_state`
等任何 trace 域或请求级键——这些只出现在 root 上。
`buildGenerationAttributes` 的"构造即合规"在真实数据上成立。

**结论：通过。**

---

## 4. Step 4：session 不伪造 —— **通过**

设计上 `session_header_names=[]`、`session_body_paths=[]`，唯一合法来源是
`X-Langfuse-Session-Id`。设计了一组对照实验。

### 4.1 诱饵组（不应产生 session）

输入形状（脱敏）：

```
POST /v1/chat/completions   body = 168 B
headers: X-Session-Id: <str:17>, X-Conversation-Id: <str:18>     ← 均未配置为来源
body:    {"model":"llm-lite","messages":[{"role":"user","content":<str:4>}],
          "max_tokens":8,"temperature":0,
          "user":<str:14>,                                        ← OpenAI 风格用户字段
          "metadata":{"session_id":<str:17>}}                     ← body 里的 session_id 诱饵
```

响应 `HTTP 200`。trace `2e5c795f50525cbdf1145bbef2412080`，root `79717f6ea1a0becf`：

| 字段 | 观测值 |
|---|---|
| `session_id` | 空串 |
| `metadata.session_scope` | 键不存在 |
| `metadata.session_source` | 键不存在 |
| `metadata.session_omitted_reason` | 键不存在 |
| `metadata.session_body_omitted_reason` | 键不存在 |

三个诱饵（自定义 header ×2、body `user`、body `metadata.session_id`）**全部未被采纳**，
也**没有**编造一个"猜出来的" session。

### 4.2 控制组（应产生 session，证明链路本身可用）

```
POST /v1/chat/completions   body = 98 B
headers: X-Langfuse-Session-Id: <str:18>
body:    {"model":"llm-lite","messages":[{"role":"user","content":<str:4>}],"max_tokens":8,"temperature":0}
```

trace `bb953dab784d31e61c5935c1f65f8835`，root `714f444a97aa92ba`：

| 字段 | 观测值 |
|---|---|
| `session_id` | 非空，长度 20，前缀为 `"<userId>:"`（用户域限定，原值不记录） |
| `metadata.session_scope` | `user` |
| `metadata.session_source` | `x-langfuse-session-id` |
| `metadata.session_omitted_reason` | 键不存在（未发生拒绝） |

`ScopedID = "{userId}:{raw}"` 的用户域隔离成立；raw 值本身未作为任何属性导出。

### 4.3 全量佐证

快照 84 个 root 中携带 `session_id` 的仅 **1** 个——即上面的控制组。
包含全部真实用户流量在内的其余 83 条均无 session，未出现任何被推断出来的会话。
84 份 generation 全部无 `session_id`。

**结论：通过。**

---

## 5. Step 5：故障隔离 —— **通过**

### 5.1 停机窗口

```
15:28:40  docker compose stop langfuse-web  （目录 /home/flintylemming/appdata/8858-langfuse-newapi）
15:28:50  Stopped
15:29:39  docker compose start langfuse-web
15:29:40  Started
15:29:51  GET /api/public/health -> 200（首次探测即就绪）
```

**停机时长 50 秒**，在约定的 2 分钟窗口内。

### 5.2 停机期间的 relay 表现

| 请求 | HTTP | time_total | error | usage |
|---|---|---|---|---|
| 基线-1（停机前） | 200 | 0.2911 s | null | 53/8 |
| 基线-2（停机前） | 200 | 0.2773 s | null | 53/8 |
| 基线-3（停机前） | 200 | 0.2758 s | null | 53/8 |
| 停机-1 | 200 | 0.3152 s | null | 53/8 |
| 停机-2 | 200 | 0.2887 s | null | 53/8 |
| 停机-3 | 200 | 0.2693 s | null | 53/8 |
| 停机-4 | 200 | 0.2840 s | null | 53/8 |
| 停机-5 | 200 | 0.2675 s | null | 53/8 |

停机期间 5 条全部 200、无报错，延迟区间 0.267–0.315 s，与停机前基线
0.276–0.291 s 完全重叠，**无可观测的变慢**。同窗口内真实用户流量（token_name 非本次验收令牌）
也正常成交 2 条。

**扣费正常**：验收令牌 `remain_quota / used_quota` 由 `2499216 / 784` 变为 `2498911 / 1089`，
差额 **-305 / +305**；5 条请求各 61 quota，`5 × 61 = 305`，三方吻合。
`record consume log` 行逐条落账，`subscription_consumed=61`。

### 5.3 导出错误被限流，不刷屏

停机窗口内 new-api 日志共 42 行，其中 Langfuse 导出错误 **1 行**：

```
[SYS] 2026/08/15 - 23:29:24 | langfuse export failed: export_failed (runtime version 1, 6 span(s))
```

- 单行聚合了 6 个 span 的失败，**没有逐 span 刷屏**；
- 整个停机窗口 `grep -c 'langfuse export failed'` = **1**；
- 全程 `panic|fatal` 命中 **0**。

### 5.4 恢复后继续上报

- 恢复后首条验收请求（15:29:56，`request_id=202608151529567773476378268d9d6jNky7ZzS`）
  正常落库：trace `cd0075369c3f374a53a6b24f37dc1051`（root `c0811ef1c3161775` + gen `b95d233b508a278c`）；
- 随后真实用户流量继续入库（15:30:06 `1ff65cbf16d969f67fbd3b74e5318775`、
  15:30:08 `9531af5916a59e83592406d459b3c6e9` …）。

**有界丢失的实测量化**：停机窗口内 new-api 共处理 7 个请求（本次验收 5 条 + 真实用户 2 条），
其中 **4 个 trace（8 span）在恢复后由队列补投成功**（`8d940c688cf0c512f2cbfe9165ed8be6`、
`907dfe44620789840e8bd67c9830640f`、`6efa0ec38c0304ab99539bb2c7f88ad3`、
`953ef3df6c87381c70a87a8383db2800`），**3 个 trace（6 span）被丢弃**——恰好等于那条
`export failed ... 6 span(s)` 日志所报的数量。丢弃有界、有日志、可审计，且未回压到 relay。

**结论：通过。**

---

## 6. Step 6：资源观察 —— **通过**

`docker stats --no-stream`（同一命令，四次采样，跨度 17 分 26 秒）：

| 时间 (UTC) | `new-api` MEM | `langfuse-worker` MEM | `langfuse-clickhouse` MEM |
|---|---|---|---|
| 15:16:13（T0） | **52.39 MiB** | 891.6 MiB | 532.7 MiB |
| 15:26:19（T0+10m06s） | **46.38 MiB** | 893.5 MiB | 583.9 MiB |
| 15:31:02 | **53.60 MiB** | 894.5 MiB | 676.2 MiB |
| 15:33:39（T0+17m26s） | **54.05 MiB** | 893.0 MiB | 640.3 MiB |

- `new-api` 常驻在 **46–54 MiB** 之间上下波动（T0→T0+10m 实际是**下降** 6 MiB），
  **无单调增长**；`/proc/1/status` 末次读数 `VmRSS=95,536 kB`、`Threads=65`；
- 该值远低于设计文档给出的全采样容量上限（`max_in_flight_capture_bytes = 512 MiB`，
  规划常驻增量 ~552 MiB）——说明在当前 ~6 req/min、单条正文 ≤63 KiB 的负载下，
  在途捕获预算几乎没有被占用；
- `langfuse-worker` 稳定在 891–895 MiB（波动 < 0.4%）；
- `langfuse-clickhouse` 在 533–676 MiB 间波动，属摄入/合并期的正常起伏，无单调趋势；
- `new-api` 容器 `RestartCount=0`，全程未重启；日志 `panic|fatal` 命中 0。

**结论：通过。**

---

## 7. 汇总

| # | 验收要点 | 结论 | 关键证据 |
|---|---|---|---|
| 1 | trace name 由 root span name 回退而来，非空/非 unknown | **通过** | eventsTracesView 的 `COALESCE(trace_name, if(parent_span_id='', name))`；metrics 视图解析出 `openai <model>`；84/84 root name 非空非 unknown |
| 2 | trace 预览正文来自 root，且带 `capture_state` | **通过** | `isRootObservation` 判定式命中 `parent_span_id=''`；84/84 root `capture_state=full` |
| 3 | root observation 保留客户端视角 input/output | **通过** | 84/84 root input/output 非空；`input_source=client_request`；root 与 gen input 长度 84/84 相等 |
| 4 | usage 分桶（cache/image）互斥不重叠 | **通过** | 求和不变式 83/83 无违例；跨库口径 79/79 三项全中；cache 30 份、image 1 份实证 |
| 4b | usage 分桶 cache_creation / audio | **未覆盖** | 部署内无 Anthropic 语义渠道（7 天 0 条）、无音频模型（30 天 0 个）；构造需改渠道，触红线 |
| 5 | cost 等于 new-api 权威值，非 Langfuse 推价 | **通过** | `quota == cost×500000` 79/79；`calculated_total_cost!=0` 0/84；`model_id!=''` 0/84；失败 attempt 无 cost 且 `cost_omitted_reason=attempt_failed` |
| 6 | 多 attempt 各自独立 generation | **未覆盖** | `RetryTimes=0`，attempt 上限为 1；3 天 47,919 条中多渠道 0 条；84/84 `attempt_count=1` |
| 7 | root 的 user/session/metadata 不被 attempt 覆盖 | **通过** | trace 级聚合为"最后一个非空值获胜"；84/84 generation 不写 user/session/trace_name/release/tags/version |
| 8 | generation 上无 denylist 属性 | **通过** | generation metadata 键全集不含任何 trace 域/请求级键 |
| 9 | 无客户端 session 时不伪造 | **通过** | 诱饵组（自定义 header ×2 + body `user` + body `metadata.session_id`）无 session 且无 omit 原因；控制组 `X-Langfuse-Session-Id` 正常产出用户域 scoped id；83/84 root 无 session |
| 10 | Langfuse 停机期间 relay 不受影响 | **通过** | 停机 50 s，8 条请求全 200，延迟与基线重叠；扣费 -305/+305 三方吻合 |
| 11 | 导出错误被限流不刷屏 | **通过** | 停机窗口 42 行日志中导出错误仅 1 行（聚合 6 span）；恢复后补投 4 trace、丢弃 3 trace，有界可审计 |
| 12 | new-api 内存无持续增长 | **通过** | 17m26s 四次采样 46–54 MiB 波动，T0→T0+10m 反而下降 |

**通过 10 条 / 不通过 0 条 / 未覆盖 2 条**（表中 4b 与 6；4b 含 cache_creation 与 audio 两个子项，
按验收要点计为 1 条）。

---

## 8. 顾虑与后续建议

1. **两条未覆盖项都是环境限制，不是实现缺陷**，但也意味着这两条代码路径
   （Anthropic cache_creation 分桶、音频结算分桶、多 attempt 归属与 denylist）
   **在真实流量下从未被执行过**。它们目前只有单元测试保障。
   建议在非生产实例上补一次定向验证，尤其是多 attempt 的 root 不被覆写这一条——
   §3.3 目前是"结构性证明 + 单 attempt 实证"，不是多 attempt 实证。

2. **内容体积已逼近上限**：真实请求的导出 input 最大已达 **63,186 B**，
   而 `max_content_bytes = 65536`（96.4%）。当前 0 条触发截断，但更长的对话
   一定会开始命中 `content_truncated=true`。这是设计内行为，降采样前建议确认
   下游消费方能接受截断语义；如需保全长上下文，应显式调高该值而不是等它静默截断。

3. **`langfuse.internal.as_root` 未被 Langfuse 采纳为 `is_app_root`**：
   我们写的是字符串 `"true"`，ClickHouse `events_core.is_app_root` 实测恒为 `false`
   （84/84）。当前无影响——root 的 `parent_span_id` 为空，命中判定式
   `(parent_span_id = '' OR is_app_root = true)` 的前半支。
   但如果将来 root 变成"逻辑根但有物理父"（例如接受客户端传入的 traceparent），
   这个属性就必须真正生效。建议后续确认 Langfuse 期望的是 boolean 而非字符串。

4. **`/api/public/v2/observations` 的 `traceName` 恒为空串**（§1.3 注），
   任何基于该字段的下游查询/告警都会失效，需改走 metrics 视图。

5. **`output_reasoning_tokens` 与部分上游的 `reasoning_tokens` 口径不一致**：
   Step 1 的响应体里 `usage.reasoning_tokens=16`，但导出的
   `output_reasoning_tokens=0`（该值取自 `CompletionTokenDetails.ReasoningTokens`）。
   总和不变式不受影响（该 16 tokens 仍完整计在 `output` 里，79/79 与
   `logs.completion_tokens` 精确相等），属于上游把 reasoning 计数放在非标准
   顶层字段导致的细分缺失，不是计费或互斥性问题。降采样前不阻塞，但值得跟踪。

6. **降采样（Task 8）的前置判断**：本报告未发现任何阻塞性问题。
   sample_rate 从 1.0 降到 0.1 后，上述基于全量统计的不变式将只能在抽样子集上复核，
   建议保留本报告作为全采样基线。

---

## 附：验收中使用的只读查询入口（不含任何凭证）

```bash
# Langfuse 公共 API（Basic auth，密钥从 8858 栈 .env 读取，不落盘、不回显）
GET /api/public/v2/observations?traceId=<id>&fields=core,basic,time,io,metadata,model,usage,metrics,trace_context
GET /api/public/v2/observations?isRootObservation=true&fromStartTime=<ISO>&toStartTime=<ISO>
GET /api/public/v2/metrics?query=<urlencoded JSON, view=observations, dimensions=[traceName,type]>
GET /generated/api/openapi.yml

# Langfuse ClickHouse（只读）
docker exec 8858-langfuse-newapi-clickhouse-1 clickhouse-client --query "SELECT ... FROM events_full ..."

# new-api 日志库（只读，权威 quota）
docker exec new-api-clickhouse clickhouse-client --query "SELECT request_id, quota, prompt_tokens, completion_tokens FROM newapi.logs WHERE ..."
```

**本次验收未修改任何渠道、系统设置、compose 文件，未重启 new-api。**
唯一的写操作是 Step 5 约定的 `docker compose stop/start langfuse-web`（8858 栈，50 秒）。
