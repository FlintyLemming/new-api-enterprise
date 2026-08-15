# Langfuse 对话追踪设计

日期：2026-08-12
状态：已修订；Langfuse 实现计划等待 tiered billing 前置改动恢复绿色基线后再编写

## 1. 目标

为 New API 增加可选的 Langfuse 集成，把 LLM 对话请求导出为 trace，便于运维人员按 New API 用户和客户端明确提供的会话标识检索日志。

功能必须满足以下要求：

- 保留客户端视角的请求与响应内容；
- 每次上游渠道调用单独呈现，能够看清重试和失败过程；
- 以 New API 最终结算后的 usage 和 quota 作为计费事实来源；
- 不等待导出、不在请求路径执行同步网络 I/O；正文聚合、脱敏和大对象序列化异步执行，Langfuse 故障不得影响
  relay 的返回；
- 关闭或未命中采样时保持极低开销；
- 客户端没有提供 session 时不推测、不伪造。

## 2. 功能范围

### 2.1 范围内

- OpenAI Chat Completions：`RelayFormatOpenAI` + `RelayModeChatCompletions`。
- Claude Messages：`RelayFormatClaude`。
- OpenAI Responses：`RelayFormatOpenAIResponses` + `RelayModeResponses`。
- Gemini `generateContent` / `streamGenerateContent`：`RelayFormatGemini` 中由独立的、纯函数的 Gemini
  action/model 分类器明确判定为 generation 的请求。该分类器（例如
  `relayconstant.ClassifyGeminiAction(path, modelName)`）必须先作为独立提交/PR 设计和验证，Langfuse PR
  只读消费其结果，不借此修改现有的 `controller.geminiRelayHandler`、`GetAndValidateRequest`、Gemini
  adaptor `GetRequestURL` 或 `DoResponse` 分支。当前四处分支并不一致：handler 使用
  `strings.Contains(path, "embed")`，request validator 匹配 `:embedContent`/`:batchEmbedContents`，
  `GetRequestURL` 按上游 model 前缀选择 action，而 `DoResponse` 在 `RelayModeGemini` 下只看 path；此外
  `/v1/engines/:model/embeddings` 也进入 `RelayFormatGemini`，但 path 没有 `:action`。这是一项
  已知的生产分发不一致，尤其 `:generateContent` 映射到 `text-embedding*` 时可能使用 embedding URL 却用
  text-generation handler 解析响应，不能作为 Langfuse PR 的隐藏修复。

  独立分类器的规则必须完整覆盖，并按“最终 model 前缀优先于 path action”的顺序判定：`imagen*` ->
  `predict`；`text-embedding*`、`embedding*` 和 `gemini-embedding*` -> `embedding`（上游 action 按
  `embedContent`/`batchEmbedContents` 区分）；model 不属于上述前缀时，显式 `:embedContent`/
  `:batchEmbedContents` -> `embedding`，显式 `:predict` -> `predict`，其他已知
  `:generateContent`/`:streamGenerateContent` -> `generate`；`/v1/engines/:model/embeddings` 没有可识别
  `:action`，因此在非 embedding/Imagen model 下归为 `unknown`；其他无法识别的 action 也归为 `unknown`。因此，入站
  path 为 `:generateContent` 但 alias 被渠道映射成任一 embedding 前缀时，最终分类必须为 embedding；映射成
  `imagen*` 时必须为 predict。只有最终分类为 generate 的
  `:generateContent`/`:streamGenerateContent` 进入 Langfuse；embedding、predict、unknown 和任何上述
  排除前缀均不进入。

  `Begin` 用 `relayInfo.OriginModelName` 和入站 path 做首轮只读判定；明确为 embedding、predict 或 unknown
  时直接返回 no-op。对首轮可能为 generate 的请求，请求路径只保留有界 Recorder 值对象，不创建 OTel span。
  每个到达 outbound HTTP 边界的 attempt 在 `EndAttempt` 通过 nil-safe 的
  `relayInfo.GetUpstreamModelName()` 读取 handler 已完成映射后的最终模型并重新分类；若到达 outbound 边界但最终模型缺失或无法分类，则按 `unknown`
  处理；不得在 handler 前调用带副作用的 `ModelMappedHelper`，也不要求一个无法取得最终映射结果的前置复核。
  对 Gemini，`Finish` 仅在入站初判为 generate、且所有到达 outbound 边界的 attempt 最终分类都是 generate 时
  才 materialize telemetry；非 Gemini 格式不执行 Gemini 分类复核。这意味着一个非 Gemini 格式的请求即使
  被路由到 Gemini 渠道、并因最终 model mapping 走了 embedding 或 `imagen*` URL，仍按其原始非 Gemini
  范式导出对话 trace；这是本设计明确接受的已知行为，不能在 Langfuse PR 中隐式改变。任一 Gemini attempt
  最终为 embedding、predict、unknown 或 `imagen*` 时丢弃整个 Recorder 并释放捕获预算与 runtime lease，不产生 root 或 generation。没有发生上游 attempt 的入站 generate
  请求仍可产生 metadata-only root。请求路径不维护任何 OTel span 生命周期状态，也不需要可取消 span 或
  BSP 前过滤器；只保留未 materialize 的值对象，不存在待结束 span，且不修改现有 Gemini handler。
- 上述接口的流式与非流式形态。
- 已成功生成 `RelayInfo` 的请求，包括之后发生的预扣费失败、选渠道失败和上游失败。
- `controller.Playground` 最终调用 `controller.Relay`，且 `GenRelayInfo` 会设置 `IsPlayground=true`；只要其
  OpenAI 请求的 mode 属于上述支持范围，就按普通生产请求采样并产生 trace。root metadata 必须显式写
  `is_playground=true`，避免管理员把 Playground 流量误认为普通客户端流量。
- 每个命中采样且最终分类属于范围的客户端请求产生一个 trace；除 §5.2 明确列出的 AWS SDK/Xunfei v1 已知
  降级外，每次实际发起的上游渠道尝试产生一个 generation observation。

### 2.2 范围外

- 第一版不支持旧版 OpenAI Completions。
- Embedding、Rerank、Moderation、图像（包括 Gemini Imagen `:predict`）、独立音频接口、异步任务和
  Midjourney relay。Chat Completions 或 Responses 只要按各自现有路由条件走
  `PostAudioConsumeQuota` 就仍属于范围内；两者的判定并不相同：Chat Completions 要求 prompt/completion
  任一 audio token 为正且模型配置了 input/output audio ratio，Responses 则要求
  `OriginModelName` 以 `gpt-4o-audio` 开头。不能把“存在 audio token”写成两条路径的共同充分条件。
- OpenAI Realtime/WebSocket、Responses Compaction、Alpha Search。
- 在 `controller.Relay` 之前发生的鉴权失败。
- 在 `RelayInfo` 创建之前发生的请求解析或校验失败。
- Channel test。`controller/channel-test.go` 不经过 `controller.Relay`，因此不会调用 `Begin` 或创建
  Recorder；即使后续复用 relay handler、共享 `doRequest` 或结算函数，窄 hook 也只能看到 nil Recorder，
  永远不产生 root 或 generation。
- 多租户 Langfuse 项目、score、dataset、prompt 和 evaluation。
- 与客户端或上游供应商之间传递分布式 trace context。

## 3. 核心决策

| 决策项 | 结论 |
|---|---|
| 传输 | OpenTelemetry Go SDK + OTLP/HTTP protobuf + gzip |
| Langfuse 端点 | `POST {host}/api/public/otel/v1/traces`，使用项目 Public Key/Secret Key 做 Basic Auth |
| 内容采集点 | 在下游边界包装有上限的 `gin.ResponseWriter` |
| 重试模型 | 一个客户端请求对应一个 root span；共享 HTTP 边界的每次真实上游尝试对应一个 generation 子 span；AWS SDK/Xunfei v1 旁路为已知 root-only 降级 |
| 用户标识 | `user.id` 使用稳定的 New API 数值用户 ID；username 只放 metadata |
| 会话标识 | 只接受客户端显式标识作为原始值；导出前用 New API 用户 ID 做 project 内作用域隔离，不使用内容 hash、缓存键等启发式规则 |
| Usage 与 Cost | 使用 New API 最终结算数据，归属到成功的 generation |
| 配置 | 管理员全局配置；通过 `GlobalConfig`/options 持久化，热路径正常只 load 一次 atomic `RuntimeBinding`，仅退休竞态允许一次有界重试 |
| 故障隔离 | 导出错误限频记录，不改变 relay 状态和响应内容 |

选择下游 `ResponseWriter` 边界，是因为所有受支持的 provider 和格式转换最终都要通过 Gin 向客户端写数据。各 adaptor 里的响应 builder 分散且带条件，无法构成完整、稳定的采集边界。

选择标准 OTel SDK 而不是手写 OTLP exporter，是因为 SDK 已经提供协议编码、批处理、有界队列、瞬时错误重试、span 生命周期和 shutdown 语义，不应在项目内维护第二套追踪实现。

## 4. 总体架构

```text
controller.Relay
  |
  +-- 校验请求并创建 RelayInfo
  +-- langfuse.Begin(...)
  |     +-- Phase 0：读取已发布 binding，执行 enabled/config、sample_rate>0、format/mode 和 Gemini 首轮分类
  |     +-- Phase 1：按 Header -> 已缓存 body 提取显式 session，完成用户作用域化和确定性采样
  |     +-- Phase 2（仅命中后）：取得 runtime lease，冻结 root/config 值快照
  |     +-- send_content=true 且预算允许时冻结 input 并用有限缓冲 writer 包装 c.Writer
  |     +-- 否则保留 metadata-only Recorder 并写 capture_state；请求路径始终不创建 OTel span
  |
  +-- retry loop
  |     +-- 选择渠道
  |     +-- getChannel -> tiered billing -> GetBodyStorage
  |     +-- relay handler
  |     |     +-- 完成 Gemini 模型映射；共享 HTTP outbound 边界在真实调用前调用 BeginAttempt
  |     |     +-- 调用上游并通过 capture writer 写客户端响应
  |     |     +-- DoResponse 成功后同步执行 PostTextConsumeQuota 或 PostAudioConsumeQuota
  |     |           +-- 完成最终 usage/quota 结算
  |     |           +-- 通过 c 取回 Recorder，RecordUsage(最终 usage 与 quota)
  |     +-- EndAttempt(error)，快照真实结束时间、最终模型和 Gemini 最终分类
  |
  +-- 统一 finalizer
        +-- 达到计费阶段时：NormalizeViolationFeeError + Refund + ChargeViolationFeeIfNeeded
        +-- SetMessage(MessageWithRequestId) + 写最终客户端错误响应
        +-- langfuse.Finish(final error)，立即快照真实 root 结束时间
        +-- Gemini 最终分类不在范围时丢弃值对象并释放捕获预算与 runtime lease
        +-- 其余请求冻结不可变捕获缓冲并交给有界 gopool
              +-- 聚合 JSON/SSE、脱敏、序列化
              +-- 先按历史时间创建 root，再以其 SpanContext 创建 generation
              +-- 设置属性，先按历史结束时间 End root，再 End 各 generation，交给 BatchSpanProcessor

OTel BatchSpanProcessor
  +-- 有界内存队列
  +-- 批量、重试、gzip
  +-- Langfuse OTLP endpoint
```

### 4.1 模块划分

```text
setting/langfuse_setting/
  config.go       配置、默认值、校验、不可变快照

service/langfuse/
  runtime.go      数据面 TracerProvider 生命周期、RuntimeBinding load/publish
  recorder.go     请求/尝试生命周期和 OTel 属性映射
  writer.go       有上限的下游响应捕获
  aggregate.go    四种协议的 JSON/SSE 聚合
  content.go      base64 替换和安全截断
  identity.go     显式 user/session 提取

service/langfuseconfig/
  service.go      控制面整组读取、事务持久化、reconcile 和 runtime 发布
```

导入边界必须明确：`controller/relay.go` 导入数据面包 `service/langfuse`，负责在 `RelayInfo` 创建后调用
`Begin`、在 retry loop 每次 relay handler 返回后且成功 return/渠道错误处理/下一轮重试之前调用
`EndAttempt`，并在统一 finalizer 调用 `Finish`；`service/text_quota.go` 和 `service/quota.go` 导入该包记录最终 usage/cost；
`relay/channel/api_request.go` 也导入该包，只在共享 `doRequest` 内调用一次 `BeginAttempt`。任务调用方虽然
复用 `doRequest`，但因范围外且没有 Recorder 而自然 no-op；Realtime 的 `DoWssRequest` 不经过该边界且明确
排除。绕过共享 HTTP 边界的 AWS SDK 和 Xunfei WebSocket 在 v1 不导入数据面包，按 §5.2 的已知限制降级。
`service/langfuse` 只能依赖
`relay/common`、setting 快照等窄值对象，明确禁止导入根 `service` 和 `model`；因此热路径保持
`relay/channel -> service/langfuse -> relay/common`，不会新增 `relay/channel -> service/langfuse -> model`
这条脆弱依赖。当前 `model` 已导入 `relay/common`，所以这一禁止项必须由包依赖测试固定，不能只以“当前尚未
成环”为理由接受。

依赖测试采用两层规则，不能把二者混成一个无法维护的全闭包白名单：

- `service/langfuse` 的 New API **直接 import 白名单**为 `common`、`constant`、`relay/common`、
  `relay/constant`、`relaykit/dto`、`relaykit/types` 和 `setting/langfuse_setting`；未列出的根 module 包不得
  直接导入。这里的包名按完整 module path 区分：relay format、`NewAPIError` 及其
  `MaskSensitiveErrorWithStatusCode()` 来自 `relaykit/types`，客户端校验后请求的具体 DTO 来自
  `relaykit/dto`。根 `dto` 当前只承载 Midjourney/Suno/task/video 等本功能范围外 DTO，根 `types` 当前只承载
  host 侧价格/集合工具；二者都不是数据面直接依赖，不能因短包名相同而加入白名单，只允许作为
  `relay/common` 的既有传递依赖出现。`setting/config` 同样只允许作为
  `setting/langfuse_setting` 的传递依赖，数据面不得直接读取其可变 `GlobalConfig` 注册对象。标准库、Gin、
  OTel 等外部 module 不受这份项目内白名单限制。
- `go list -deps ./service/langfuse` 使用**闭包禁用名单**，至少禁止根 `service`、`model`、`controller`、
  `service/langfuseconfig` 和任何 `relay/channel/**`。闭包中允许 `relay/common` 已有的必要传递依赖，包括根
  `constant`、根 `dto`、根 `types`、`relaykit/**`、`pkg/billingexpr`、`setting/config`、
  `setting/model_setting` 和 `setting/operation_setting`；不能因为这些既有闭包成员把测试写成会随
  `relay/common` 内部实现变化而频繁误报的完整白名单。所有 `relay/**` 包另行断言其闭包不含
  `service/langfuseconfig`。

管理员专用 GET/PUT、启动加载和周期 reconcile 使用控制面包 `service/langfuseconfig`。该包可以导入
`model` 和 `service/langfuse`，负责事务及调用数据面公开的 candidate build/publish API；只有
`controller/option.go` 和启动/同步 wiring 导入它，任何 `relay/**` 包都不得导入。结算调用点仅通过小型值对象
向数据面包传数据。所有代码都位于根 Go module。`relaykit/` 不做改动，继续保持独立构建。

## 5. 请求与尝试生命周期

### 5.1 创建 trace

`Begin` 在 `GenRelayInfo` 成功后立即执行。它严格分为三个阶段；阶段顺序是热路径和隐私成本契约，不能把
session/body 提取重新放回“采样命中后”的列表：

**Phase 0：无条件的廉价守卫。**

1. 执行一次 `binding := manager.LoadBinding()`，从同一个 immutable binding 读取 snapshot/runtime；检查
   enabled 及 Host/Public Key/Secret Key 等已验证状态，无效时返回 no-op；
2. 检查 `snapshot.SampleRate`；`sample_rate <= 0` 时立即返回 no-op，使管理员可保留其余配置并零成本暂停采集；
3. 检查 relay format/mode；对 Gemini 用入站 path 和 `relayInfo.OriginModelName` 做首轮纯函数分类，明确排除
   embedding、predict、unknown 和 `imagen*`。

`relayInfo.RequestId` 是 `GenRelayInfo` 的后置条件，不是 Phase 0 的业务分支。`Begin` 只使用该字段，并可保留
“意外为空则 no-op”的内部防御断言；生产路径不会命中，不为它建立独立 telemetry 状态或功能测试。

Phase 0 不读取 Header/body session，不访问 `BodyStorage`，不读取或复制内容，不申请 runtime lease/捕获预算，
也不分配 attempt 列表或创建 OTel span。

生产 HTTP 路径已在 `controller.Relay` 之前安装 `middleware.RequestId()`，中间件无条件生成 ID 并写入
`common.RequestIdKey`；`GenRelayInfo` 从同一 context key 初始化 `relayInfo.RequestId`，且在非标准测试或
调用路径缺失中间件时自行补齐。因此 `Begin` 统一读取已生成的 `relayInfo.RequestId`，不再二次读取
`gin.Context`；为空才返回 no-op。trace ID 和 metadata 都使用这一份值，避免同一请求出现两个 ID 来源。

**Phase 1：身份提取与采样。**

1. 按 §7.2 的固定优先级检查标准 Header 和配置的额外 Header；首个 raw 非空 Header 候选即锁定来源，再执行
   trim/合法性校验。该候选即使非法也不继续降级到后续 Header 或 body，避免低优先级来源绕过显式高优先级
   身份；只有所有 Header 都没有 raw 非空候选、且 snapshot 配置了 body path 时，才检查
   `common.KeyBodyStorage`；
2. body session 只从已缓存且类型正确的 `BodyStorage` 读取。先检查 `Size()`，完整 body 不超过 snapshot 的
   `max_session_body_bytes` 时才通过独立 reader 做一次完整读取和 gjson 查询；不得主动调用
   `GetBodyStorage`/`GetRequestBody`，不得读取截断 JSON；
3. 对合法 raw session 使用正数 `relayInfo.UserId` 生成用户作用域 `session.id`，并以 scoped session ID 作为
   采样键；未得到 session、body storage 缺失/无效或没有正数用户 ID 时，回落到 request ID。采样映射固定为：
   `digest = sha256([]byte(samplingKey))`，`bucket = binary.BigEndian.Uint64(digest[0:8])`。Phase 0 已排除
   `sample_rate <= 0`；`sample_rate >= 1` 全部命中，其余情况计算精确整数阈值
   `threshold = floor(sample_rate * 2^64)`，并仅在 `bucket < threshold` 时命中。阈值必须从快照中已验证的
   IEEE-754 `float64` 值按其精确值计算（可用 `math/big` 或等价整数算法），禁止先把 `bucket` 转成
   `float64`，也禁止改用其他摘要字节、字节序或取模。trace ID 始终只从 request ID 派生，不能改用 session
   ID；
4. 未命中时立即返回 no-op，不包装 writer、不读取或复制**内容采集**所需正文、不创建 span、不申请 lease/
   捕获预算，也不分配 attempt 列表。

配置了 session body path 且没有合法 Header session 时，Phase 1 为了做 session 级判定会承担一次最多 64 KiB
的完整身份读取；这是所有进入 Phase 1 的请求成本，包括最终未采样或 `send_content=false` 的请求。管理 UI
必须明确展示这一点。该身份读取与内容采集完全独立，不能放宽到 1 MiB，也不能让 telemetry 成为第一个消费
原始 request body 的组件。

**Phase 2：仅采样命中后取得资源并安装捕获。**

1. 对 Phase 0 读取的 runtime 调用防退休 `TryAcquire`。若因该 runtime 已退休而失败，只允许重新 load 当前
   binding 一次，并以新 snapshot 从 Phase 0 重新执行所有 enabled/config/format/Gemini 守卫和 Phase 1 身份
   配置、作用域化及采样判定；Header 必须按新配置重查。已经完整读出的最多 64 KiB body 身份
   缓冲必须复用并按新 paths 重查；若第一次因旧上限未读取而新上限允许，才执行那唯一一次完整读取；第一次
   读取失败/不完整时不为 telemetry 重试读取。跨两轮判定，单次 `Begin` 对 body 的完整身份读取总计仍至多
   一次、至多 64 KiB。不能复用依赖旧 Header/path 配置的 raw session 结论，也不能混用旧 snapshot 的
   SampleRate/SendContent/size 与新 runtime。第二次未命中、disabled 或 acquire 失败时返回 no-op；
2. lease 成功后固定本请求使用的配置快照、`relayInfo.RequestId` 和 root 值快照。root 开始时间直接使用
   `relayInfo.StartTime`，使 duration 与日志中的 `use_time_ms`、`first_response_ms` 使用同一基准；不在
   `Begin` 另测一份时间，也不调用 `tracer.Start`；
3. 创建 Recorder，把 runtime lease 所有权交给它，并通过 §8.4 定义的类型化 context key 挂到当前
   `gin.Context`。OTel root/generation 统一推迟到 `Finish` 的 metadata-only 路径或异步 worker，以显式历史
   时间创建；
4. `send_content=false` 时设置 `capture_state=disabled` 并返回 passthrough Recorder：不读取内容采集 input、
   不预留捕获预算、不创建 capture writer、不替换 `c.Writer`；Phase 1 的 session body 身份读取不受此开关
   影响；
5. `send_content=true` 时，在读取/复制 input、创建 capture writer 或替换 `c.Writer` 之前，按 §9.1 用 CAS
   一次性预留全局捕获预算；失败则设置 `capture_state=budget` 并返回 metadata-only Recorder；
6. 仅从已经缓存的 `BodyStorage` 通过独立 reader 冻结有界原始 input；storage 不存在时省略 input 并记录
   `input_omitted_reason=body_storage_unavailable`，仍可捕获 output。请求 goroutine 不执行脱敏；
7. 创建 capture writer 并替换 `c.Writer`。reservation、原始 writer 引用和全部值快照属于本次 Recorder，
   不能放入可由后续请求复用的全局可变状态。

trace ID 从 `relayInfo.RequestId` 确定性派生：取 `sha256([]byte(requestId))` 的前 16 字节，字节顺序保持摘要
原序。若这 16 字节全为零，则把最后一个字节固定改为 `0x01`，避免生成 OTel invalid TraceID；测试通过可注入
摘要函数覆盖该防御分支，不尝试寻找真实 SHA-256 原像。materialization 时通过 Recorder 专用 context 把派生
ID 交给自定义 OTel `IDGenerator`；span ID 继续随机生成。

### 5.2 记录上游尝试

对于经过 `controller.Relay` 且 Recorder 非 nil 的范围内请求，只有在以下前置步骤全部成功后才可能记录
attempt：`getChannel` 返回具体渠道、`PrepareTieredBillingForSelectedGroup` 成功、
`common.GetBodyStorage(c)` 成功并已重新设置 `c.Request.Body`。controller 和 wrapper 都不直接调用
`BeginAttempt`。普通 HTTP adaptor 的唯一共享边界是
`relay/channel/api_request.go` 的私有 `doRequest`：只在其中的 `relayClient.Do(req)` 紧前调用一次
`BeginAttempt(c, info)`。`DoApiRequest`、`DoFormRequest`、导出的 `DoRequest` 以及任何其他调用 wrapper 都不得
再调用 hook，否则一次真实 HTTP 调用会产生两个 attempt，并错误触发
`replaced_by_next_upstream_call`。这一位置也覆盖同一 handler 内再次发起的真实共享 HTTP 调用。

共享文件中的其他调用方/出口必须显式记录如下，避免把清单误读为 wrapper 穷举：

- `DoTaskApiRequest` 也调用 `doRequest`，但异步任务不经过 `controller.Relay` 的 Langfuse `Begin` 且属于 §2.2
  范围外，因此 `FromContext(c)` 返回 nil，hook 是安全 no-op，不产生 trace 或 generation。这也是 hook 必须放在
  `doRequest` 而不是三个 wrapper 的直接理由。
- `DoWssRequest` 是独立的 WebSocket outbound 路径，不调用 `doRequest`；其 OpenAI Realtime 场景明确属于 §2.2
  范围外，v1 不接入 attempt hook。
- AWS Bedrock `ClientModeApiKey` 仍经 `DoApiRequest -> doRequest`，正常记录 attempt。`ClientMode !=
  ClientModeApiKey` 则绕过共享 HTTP 边界，真实调用位于 AWS SDK 的三个 `InvokeModel*` 调用点。v1 不为此在
  AWS SDK 私有链路增加 hook；命中采样时仍可产生 root，但没有 generation，结算记录因没有 active attempt 被
  丢弃，并写 `usage_unattributed=true`、`usage_omitted_reason=no_active_attempt`。这是明确的 v1 已知限制，
  后续独立 PR 再评估三个 SDK 调用点的一致接入。
- Xunfei 的 `Adaptor.DoRequest` 只返回 dummy `http.Response`，真实 outbound 是
  `xunfeiMakeRequest` 中的 WebSocket `Dial`。当前 `c/info` 没有贯穿
  `xunfeiStreamHandler`/`xunfeiHandler` 到 `xunfeiMakeRequest`；v1 不为 tracing 改造这条私有签名链。其命中
  采样时与 AWS SDK 旁路采用相同的 root-only、`no_active_attempt` 降级，后续以独立 PR 接入。
- Coze 的创建请求通过 `DoApiRequest`，因此只在创建调用进入 `doRequest` 时开始一个 attempt。其
  `relay/channel/coze/relay-coze.go` 的私有 `doRequest` 轮询和消息详情请求不要再次调用 `BeginAttempt`；创建
  attempt 保持 active 到 handler 返回，时长有意覆盖整个轮询周期。这是一个调用级 attempt，不是每个轮询 HTTP
  请求一个 generation。

上述每个 hook 只记录值对象、channel 快照、响应 offset 和真实开始时间，不创建 OTel span，也不调用
`ModelMappedHelper`、`request.SetModelName` 或任何映射 helper。Gemini 的 handler 已在进入共享 HTTP 边界前
完成现有模型映射，因此这里无需 preflight。若 adaptor 自己修改了 outbound 模型字段，hook 不得凭该修改反向
触发映射逻辑。

body storage 读取失败（包括 413/400）不会产生 generation；没有选到渠道或 tiered billing 准备失败同样只
把 root 标记为失败。对于范围内格式，`GetAndValidateRequest` 通常已经通过 `common.GetBodyStorage` 创建并
缓存 storage，retry loop 的调用会走缓存分支，因此这里的 413/400 是防御性契约而非预期主路径。

每个 Recorder 在任一时刻至多有一个 active attempt。Recorder 与 capture writer 必须共享同一把请求级
state mutex，由它统一保护缓冲、offset、attempt 列表、active 指针和 frozen 状态；metadata-only Recorder
也使用同一状态模型，只是没有正文缓冲。`BeginAttempt` 必须在这把锁内完成检查和创建；若一次 handler 尚未
调用外层 `EndAttempt` 就再次经过共享 outbound 边界，则第二次
`BeginAttempt` 先以第二次调用入口的时间和当前响应 offset 收口前一个 attempt，写
`attempt_end_reason=replaced_by_next_upstream_call`，再创建新的 generation 值对象。active 只表示前一个
attempt 尚未被生命周期 hook 收口，不推测其 `client.Do` 此刻是否仍在执行；该状态仍按上述确定顺序处理，
不能 panic、复用同一 attempt 或静默覆盖。只有最新 attempt 保持 active，并接收之后的 output 和最终
usage/cost；handler 返回后的
`EndAttempt` 只收口这个最新 active attempt。这样每次真实经过共享 outbound 边界都有确定 generation，
`no_active_attempt` 仍只表示从未成功 Begin、全部已提前收口或生命周期 hook 缺失。Coze 私有轮询明确不经过
共享 hook，因此不触发这条规则。`compatible_handler.go` 的 `chatCompletionsViaResponses` 是同一 handler
内会进入 `DoApiRequest` 的二次 relay 路径，必须纳入该不变式和回归测试；不能因它不是顶层 retry loop 而漏记。
被后续 outbound 调用替换的 attempt 明确定义为“未成功完成的 superseded attempt”：它不接收 usage/cost，
materialize 时设置 OTel `Error` status 和稳定、无敏感信息的 status message，并写
`usage_omitted_reason=usage_unavailable`、`cost_omitted_reason=attempt_superseded`。这个 Error 只用于明确
observation 生命周期和满足 §6.2 的防推价契约，不冒充上游返回了 provider error；若它已经写出字节，仍按
offset 保留 output 并标记 `partial_output`。

`BeginAttempt` 快照 handler 执行前已经确定、且重试会覆盖的数据：

- attempt index 和在共享 outbound 边界 `BeginAttempt` 入口立即快照的开始时间；worker 后续以
  `trace.WithTimestamp(attemptStartTime)` 创建 generation，不能使用 worker 墙钟；
- 渠道 ID、名称、类型；ID/type 必须通过 `relayInfo.GetChannelID()`、
  `relayInfo.GetChannelType()` 读取。渠道名称不在 `RelayInfo`/`ChannelMeta` 上；`BeginAttempt` 收到非 nil
  `*gin.Context` 时，通过 `common.GetContextKeyString(c, constant.ContextKeyChannelName)` 读取、trim 并立即快照，
  context 为 nil 时直接按名称缺失处理。不得从 `ChannelMeta` 臆造字段，也不得让 worker 再访问 context。名称
  为空时 generation name 使用稳定 fallback：
  channel ID 为正时用 `channel-{id}`，否则 channel type 为正时用 `channel-type-{type}`，两者都不可用时用
  `channel-unknown`；metadata 的 `channel_name` 只在真实 context 值非空时写；
- 当前使用分组；
- 客户端原始模型；
- 共享响应缓冲区的开始 captured offset，以及底层 writer 实际写出的开始 logical offset；两者都在 state
  mutex 下快照。

`EndAttempt` 只对已经由 outbound 边界标记 `upstream_call_started=true` 的 attempt 生效，在 handler 返回后、
下一轮重试修改上下文之前完成不可变快照。没有到达该边界的 handler 失败不产生 generation。任何
Recorder、panic recover、finalizer 和 metadata 组装代码都不得直接访问 `RelayInfo` 从 `*ChannelMeta` 提升的
字段；模型、channel ID/type 分别统一使用 `GetUpstreamModelName()`、`GetChannelID()`、`GetChannelType()`，
渠道名称按上文从 context 快照，其他确实只存在于 `ChannelMeta` 的 metadata 先检查 `HasChannelMeta()`。该规则
即使“正常路径通常已有 ChannelMeta”也不放宽。
此时模型映射和
格式转换已经结束，因此记录：

- `EndAttempt` 入口处通过 `time.Now()`（测试中使用可注入 clock）取得的 attempt 结束时间；必须先取得
  此时间再做任何属性整理、锁竞争或异步提交；
- 实际上游模型；必须通过 `relayInfo.GetUpstreamModelName()` 读取，不能直接解引用嵌入的
  `*ChannelMeta`；对 Gemini 用入站 path 和这个最终模型执行纯函数分类，把分类结果写进 attempt 值对象；
  `GetRequestURL` 在共享 `doRequest` 之前执行，Gemini adaptor 可能就地剥掉 `-thinking-<budget>`、
  `-thinking`、`-nothinking` 或 effort 后缀，所以此处读取的是剥写后的最终 outbound model。它适合最终分类和
  `upstream_model` telemetry，但不保证等于管理员 channel model mapping 表中的配置名；metadata 中的
  `upstream_model` 明确表示最终 outbound 名称，不应被解释为配置别名。
- 最终上游 relay format；
- upstream request ID；
- 响应缓冲区结束 captured offset 和实际写出的 logical offset；两者都在 state mutex 下快照；
- `relayInfo.FirstResponseTime` 的快照；只有本 attempt 确实写出数据、且该时间位于 attempt 开始和结束时间
  之间（含边界）时才归属给本 attempt；
- 错误 code/message、HTTP status；
- 本次尝试是否已向客户端写出数据。

捕获 writer 只记录客户端实际收到的单一字节流。每次 attempt 的起止 offset 用于把字节归属到对应 generation。通常可重试失败不会写客户端响应；如果已经写出部分流式内容，则这段内容保留在失败 generation 中，root 始终以客户端真实收到的完整字节流为采集来源，并按 §9.1 导出其限长表示。

capture writer 使用上述共享 state mutex 统一串行化所有下游写入和捕获状态；不能依赖
`stream_scanner.go` 的局部 `writeMutex`，因为后者只覆盖 scanner 的 ping/data handler，不覆盖 finalizer 的
`c.JSON`、其他 handler 的 `helper.ObjectData`，也不覆盖 `api_request.go` 在 `client.Do` 期间启动的 ping。
`Write`/`WriteString` 在同一临界区内调用底层 writer，并只按底层实际成功写入的字节数追加有界缓冲、推进
`totalWritten` 和更新截断标记；`WriteHeader`、`Flush` 及会改变响应顺序的委托方法也使用这把锁，保证客户端
字节顺序、capture 顺序和 offset 使用同一全序。每次 attempt 在 Begin/End 时都在锁内保存
`capturedOffset=len(buffer)` 和 `logicalOffset=totalWritten`：output 只切片 captured offset 区间，是否实际写出
数据由 logical delta 判断；logical delta 大于 captured delta 时标记 `output_truncated=true`。禁止通过未同步的
`len(buffer)` 或单独 atomic 长度计算 offset。达到捕获上限后仍推进 logical offset，后续 attempt 的 output
为空但能正确标记已写出且被截断，不能把前一 attempt 的内容错误归给后一 attempt。

`Finish` 的冻结存在明确 happens-before：普通 stream handler 返回前，`StreamScannerHandler.cleanup()` 已经
执行 `wg.Wait()`，确保 ping、data handler 和 scanner goroutine 全部退出；共享 `doRequest` 返回前，其 defer
也会 `stopPinger()` 并等待 `<-pingerDone`。其他 handler 若启动任何可能写 `c.Writer` 的 goroutine，也必须在
handler 返回前 join。外层 handler 返回并执行 `EndAttempt` 后，finalizer 在请求 goroutine 同步写完最终
`c.JSON`/协议错误，才可调用 `Finish`。`Finish` 随后取得 state mutex，先检查 writer 身份：仅当
`c.Writer` 仍与本 Recorder 安装的 capture writer 是同一实例时，才把 `c.Writer` 恢复为进入 `Begin` 时保存的
原始 writer；若已被后续 handler/middleware 替换，则保留当前 `c.Writer`，并按 request ID/阶段输出一条限频、
不含正文的 `writer_replaced_before_finish` 生命周期违规告警。身份判断必须使用接口底层实例身份，不能仅比较
动态类型；禁止无条件回写原始 writer，以免静默吞掉未来 handler 安装的 wrapper。随后设置 `frozen=true`，
复制不可变缓冲和 offset/attempt 快照，再释放锁并提交 worker；冻结后禁止任何捕获状态变化。若未知代码仍持有
capture writer 的旧引用并在冻结后调用 Write，writer 只可继续原样委托底层 writer，不能修改已冻结状态；
这种晚写属于生命周期违规并限频告警，但 telemetry 不能改变其返回值。这个 join ->
finalizer write -> mutex freeze 顺序是最后写者到不可变 worker 输入的 happens-before，不能用 sleep、原子长度
或“通常已经结束”替代。已知写 goroutine 未 join 时不得调用 `Finish`。

捕获 writer 的 `Write` 是响应热路径上的硬约束：除把字节原样转发给下游、在有界缓冲中追加字节和
更新偏移/截断标记外，不做 JSON/SSE 解析、脱敏、聚合、序列化、span 操作或任何 `gin.Context` 访问。
捕获侧的追加/偏移 bookkeeping 若发生 panic，`Write` 必须在自己的保护边界内释放 mutex、记录限频告警、
放弃本次捕获并继续返回下游写入结果；底层 `ResponseWriter` 的正常错误/状态不能被吞掉，也不能为了
telemetry 改变客户端可见写入。这样切断 capture writer 内部 nil 解引用或切片越界到 relay 的传播路径。
`Begin`、`BeginAttempt`、`EndAttempt` 和 `Finish` 各自必须有最外层 `defer recover()`；panic 被转换为
本次 telemetry 的降级/省略状态，按 request ID 限频记录不含正文的告警，绝不能沿调用栈传播到 relay。
`Begin` 的恢复路径若已经安装 capture writer，也只在 `c.Writer` 仍是该实例时恢复进入前的原始 writer；若
身份已变化则保留当前 writer 并按与 `Finish` 相同的规则限频告警。随后幂等释放已经预留的全局预算以及可能
已经取得的 runtime lease，并返回只透传的 no-op 或设置 `capture_state=panic` 的 metadata-only Recorder。
`BeginAttempt`/`EndAttempt` 的恢复路径只关闭或省略对应值对象；因为请求路径尚未创建 span，不存在未结束 span
泄漏。`Finish` 或 worker 在 materialization 期间 panic 时，必须结束本地已经创建的有限 span 集合或丢弃尚未
创建的值对象，把可导出的 root 标记为 `capture_state=panic`，并在最外层收口释放捕获预算和 runtime lease，
不能留下半初始化对象供后续请求复用。
限频器本身必须是无锁/低争用的有界实现（例如按固定时间窗和哈希桶），且其内部异常也不能向 relay
传播；日志字段只允许 request ID、阶段和 panic 类型/摘要，不允许 request/response bytes。
脱敏与聚合等独立函数也必须有同样的 panic 边界，worker panic 只能丢弃该 telemetry 作业并释放其捕获预算与 runtime lease。

### 5.3 正确的结束顺序

当前 `controller.Relay` 在 Realtime 路径先注册 `defer ws.Close()`，随后注册错误响应 defer；Go 的 LIFO 顺序
保证 `helper.WssError` 先写入仍打开的连接，再关闭 WebSocket。实现时必须把错误响应、计费收口和 Recorder
收口合并为同一个幂等 finalizer，并替换当前错误响应 defer 及后续计费 defer。finalizer 必须注册在当前错误
响应 defer 原本所在的位置，即 WebSocket upgrade 成功且 `defer ws.Close()` 已注册之后，不能在函数入口注册。
这样 finalizer 在 Realtime 中执行 `WssError -> Recorder.Finish`，最后才由较早注册的 `ws.Close` 关闭连接；
不得再额外保留会改变这个顺序的错误响应或 Recorder defer。

为使这个注册位置可编译且阶段语义明确，必须把当前函数后部才以 `:=` 声明的状态提升到函数开头现有
`var` 块：至少声明 `relayInfo *relaycommon.RelayInfo`、`recorder *langfuse.Recorder` 和
`billingPhaseReached bool`，与 `newAPIError`/`ws` 同级。`GenRelayInfo` 调用从
`relayInfo, err := ...` 改成 `relayInfo, err = ...`，错误分支保持立即 return；`Begin` 成功后再给 recorder
赋值。统一 finalizer 闭包只捕获这些已声明变量，不能在注册之后依赖一个尚未进入作用域的短变量声明。

`billingPhaseReached` 只能在当前 `if priceData.FreeModel { ... } else { PreConsumeBilling(...) }` 整个分支
成功结束后的统一位置设置为 true：免费模型明确跳过预扣后置位，非免费模型仅在 `PreConsumeBilling` 返回 nil
后置位；不得分别在两个分支中遗漏或提前设置。这样 `GetAndValidateRequest`、`GenRelayInfo` 或预扣本身失败时，
`relayInfo` 可能为 nil 或尚未进入计费阶段，finalizer 绝不调用 `Refund` 或
`ChargeViolationFeeIfNeeded`。顺序固定为：

1. 失败且 `billingPhaseReached && relayInfo != nil` 时，先执行 `NormalizeViolationFeeError`，再对非 nil
   `relayInfo.Billing` 执行 `Refund`，最后执行 `ChargeViolationFeeIfNeeded`；未达到该阶段时完整跳过这组
   操作，保持当前 defer 的可达语义；后续步骤使用适用时已归一化的 `newAPIError`；
2. 对最终错误调用 `SetMessage(common.MessageWithRequestId(...))`，记录错误并按客户端协议写出最终响应；
3. Recorder 非 nil 时调用 `langfuse.Finish`。

这个顺序既保留现有退款和违规费语义，又保证 root output 捕获的是客户端实际收到的、已经归一化且附带
request ID 的最终错误 JSON；不得在费用处理或错误序列化之前冻结 Recorder 或 materialize root。

统一 finalizer 还必须保留业务 panic 语义：入口调用 `recover()` 检测正在展开的 panic；存在 panic 时不伪造
`newAPIError` 或写 controller 错误 JSON，只把 active attempt 以 `attempt_end_reason=lifecycle_panic` 防御性收口，
把 root 标为 error，调用 `Finish` 恢复/冻结 writer，最后用原 panic value 重新 `panic`，交给外层 Gin Recovery。
不存在 panic 时不得调用 `panic`。这个 recover -> telemetry cleanup -> re-panic 只为保留阶段信息，不能吞掉 panic
或让 Recorder 捕获 Recovery 随后写出的 500；具体一致性例外见 §13。

handler 返回后必须先对已标记 upstream call 的 attempt 调用 `EndAttempt`，再判断成功 return、记录渠道错误
或进入下一轮；成功 attempt 不能留给外层 finalizer 才结束。finalizer 只防御性结束因未知 panic 等异常仍处于
active 的 attempt，并负责 root。`Finish` 必须幂等，防御性重复调用不能导出重复 observation。
`Finish` 的第一步是在任何 writer 恢复、缓冲冻结或 admission 操作之前，通过 `time.Now()`（测试中使用
可注入 clock）快照 root 的真实结束时间。随后按 §5.2 的 writer 身份规则有条件恢复、冻结不可变缓冲和复制必要值对象。请求 goroutine
不得同步执行正文聚合、脱敏或大对象序列化；允许的同步工作仅限结束时间/状态快照、writer 恢复、冻结有界
缓冲、metadata-only Recorder 的有界 metadata JSON 序列化与 span materialization，以及调用 `span.End` 触发
BSP 的无阻塞入队。这里的“异步”保证是不等待 exporter、不执行同步 telemetry 网络 I/O，并不表示请求路径
完全没有有界 CPU 工作。

`Finish` 先检查 Gemini 的最终分类：仅当入站格式为 Gemini 且任一已到达 outbound 边界的 attempt 不为
 generate 时，直接丢弃整个 Recorder 值对象并幂等释放捕获预算和 runtime lease，不创建任何 span。
`disabled`/`budget` 等从未持有正文的 Recorder 不需要进入 worker；请求 goroutine 只序列化有界 metadata，然后先用
`trace.WithTimestamp(finish.RootStartTime)` 创建 root，再从返回的 root SpanContext 创建全部 generation；
属性设置完成后，先以 `finish.RootEndTime` 结束 root，使它第一个进入 BSP 队列，再按 attempt 顺序以各自
`EndTime` 结束 generation。显式时间戳仍保证 root 的业务结束时间覆盖所有 attempt，父 span 在子 span 前调用
`End` 不改变父子关系或历史 duration。持有正文时，提交前
使用专用的有界 semaphore/队列做 admission control，不能假定通用 `gopool` 自身提供本功能。
admission 失败时丢弃冻结正文、设置 `capture_state=admission`，走同一 metadata-only materialization 路径，
在全部 `span.End` 返回后释放捕获预算和 runtime lease。同步路径只触发 `BatchSpanProcessor.OnEnd` 的无阻塞
入队，不直接调用 exporter。

admission 成功后把捕获预算和 runtime lease 的所有权连同重活一起移交给 `gopool`；`Finish` 返回时不能释放
二者。worker 只接收不可变缓冲及值类型元数据，严禁再引用
`gin.Context`、`http.Request`、`RelayInfo`、可变配置结构体或 capture writer。worker 完成聚合、脱敏和
序列化后，先用 `trace.WithTimestamp(finish.RootStartTime)` 创建 root，并设置
`langfuse.internal.as_root="true"`；再以 root 返回的 context 为 parent，按每个 attempt 的 StartTime 创建
全部 generation。属性设置完成后，先调用
`root.End(trace.WithTimestamp(finish.RootEndTime))`，再按 attempt 顺序调用
`generation.End(trace.WithTimestamp(attempt.EndTime))`；root 因而先进入 BSP 队列，队列压力优先牺牲后续
attempt，而不是承载 `user.id`、`session.id` 和完整 input/output 的 trace 身份。结束时间仍使用业务快照，
不受 worker 排队延迟影响。worker 在最外层 defer 中负责收口：正常完成时在全部 `span.End` 返回后释放捕获预算和 runtime lease；panic
时也优先使用值对象中的显式时间戳收口已创建的 root，再收口已经创建的 generation，随后幂等释放二者，
绝不能用 worker 的墙钟作为业务时间。runtime 的 in-flight 因而统计 materialization worker，而不是已经结束
的请求 goroutine 或已经提交作业的 Recorder。
这样即使 worker 排队或乱序执行，失败 attempt 的结束时间也不会漂移到后续 attempt 的开始之后，span
duration 和 `completion_start_time` 的相对关系仍然有效。`Finish` 不等待已成功 admission 的 worker 或
exporter；同步降级路径只允许结束时间快照、writer 恢复、状态冻结、有界 metadata 设置、span
materialization 和 `span.End`。

## 6. Langfuse Trace 模型

每个命中采样且最终属于范围的请求产生：

- 一个 `langfuse.observation.type=span` 的 root observation；
- 零到多个 `langfuse.observation.type=generation` 的子 observation。

Gemini embedding、predict、unknown 和 `imagen*` 请求，以及任一已到达 outbound 边界的 attempt 最终模型
复核属于这些类别的请求，不 materialize root 或 generation。root 表示“客户端到 New API”的契约；generation 表示
“New API 到一次上游渠道”的尝试。

### 6.1 Root span

| 字段 | 值 |
|---|---|
| span name | `{relay_format} {origin_model}`；同时作为 Langfuse trace name 的唯一来源 |
| `langfuse.observation.type` | `span` |
| `user.id` | 十进制 `relayInfo.UserId` |
| `session.id` | 存在合法显式原始 session 且 `user.id > 0` 时，写入 `{userId}:{rawSessionId}` 用户作用域 ID |
| `langfuse.observation.input` | 脱敏、按 §9.1 保持合法 JSON/UTF-8 的限长客户端请求表示 |
| `langfuse.observation.output` | 脱敏、按 §9.1 保持合法 JSON/UTF-8 的限长客户端可见响应表示 |
| `langfuse.internal.as_root` | 字符串 `"true"`，显式锁定 root 语义 |
| `langfuse.environment` | 配置的 environment |
| `langfuse.release` | `common.Version` |
| OTel status | 仅整体 relay 失败时为 `Error` |

root 只写 observation 域的属性，不设置 `langfuse.trace.name`，也不重复写
`langfuse.trace.input/output`。固定 Langfuse revision 在 root 创建 trace 时使用
`attributes[TRACE_NAME] ?? extractName(span.name, attributes)`，因此 span name 已是 trace name 的稳定来源；
额外写 `langfuse.trace.name` 是冗余的，还会无意义命中 `hasTraceUpdates`。创建 root 时 `parentSpanId` 必须为空，
并额外设置 `langfuse.internal.as_root="true"` 锁定 root 意图。该 revision 的 ingestion 源码还表明，trace 域缺少 `TRACE_INPUT/TRACE_OUTPUT` 会回落读取
`OBSERVATION_INPUT/OBSERVATION_OUTPUT`，因此 trace 列表和 root observation 详情都会有内容。Go 契约测试只
断言发出的 OTLP 中 parent 为空、`as_root` 为字符串 `"true"`、observation 正文存在且 trace 域副本不存在；
实际 fallback 展示由 §14.6 的真实 Langfuse E2E 验证。反过来只写 trace 域属性会导致 root observation 详情
为空，这是外部 ingestion 不变式而非 Go 单测要复刻的逻辑。

### 6.2 Generation span

| 字段 | 值 |
|---|---|
| span name | `{channel_name_or_fallback} {upstream_model_or_fallback}`；channel fallback 按 §5.2，模型为空时固定用 `model-unknown` |
| parent | root span |
| `langfuse.observation.type` | `generation` |
| `langfuse.observation.input` | 脱敏后的客户端请求 |
| `langfuse.observation.output` | 本 attempt 产生的客户端可见响应的脱敏、限长表示 |
| `langfuse.observation.model.name` | 只允许在“存在有效权威 `cost_details`”或“完全没有 `usage_details` 且 OTel status 为 `Error`”时写；其他情况省略全部模型识别属性 |
| `langfuse.observation.model.parameters` | 客户端明确提供的受支持参数 |
| `langfuse.observation.usage_details` | 仅成功 attempt 的最终标准化 usage |
| `langfuse.observation.cost_details` | 从最终 quota 换算的权威结算 cost；订阅场景是额度消耗的定价等值 |
| `langfuse.observation.completion_start_time` | `relayInfo.HasSendResponse()` 为 true 时写 `FirstResponseTime` 的 UTC RFC3339Nano 字符串，否则省略 |
| OTel status | provider/relay 失败或被下一次 outbound 调用替换时为 `Error` 并带脱敏 status message；成功时不设置 Error |

这里有一条不能依赖 Langfuse 实现细节隐式维持的硬契约：**`langfuse.observation.model.name` 只能在
`cost_details` 已存在，或 `usage_details` 完全不存在且 OTel span status code 为 `codes.Error` 时写入。**当前
Langfuse 的 `getUsageUnits` 只会用 `level=ERROR` 跳过“缺 usage/cost 时”的 tokenisation；后续
`calculateUsageCosts` 会在 `provided_cost_details` 为空时直接使用 model prices × `usage_details` 推价，完全
不检查 level。因此 Error status 本身不能保护“有 usage、无权威 cost”的 generation。成功 attempt 缺权威
cost 时必须省略全部 model 识别属性；失败或 superseded attempt 只有在同样省略 usage 时才可依赖
`model + Error` 例外。测试必须覆盖“Error + model + usage + no cost”这一负向用例，证明实现会省略 model，
防止模型目录产生非权威 cost。

`user.id`、`session.id` 和 §13 固定 denylist 中的其他 trace-update 属性只写 root，generation 不得复制。

generation input 有意使用脱敏后的客户端原始协议请求，并非应用参数覆盖和协议转换后的上游原始报文。因此
metadata 必须写 `input_source: "client_request"`，避免产生错误语义。v1 不把入站 Gemini `contents`、Claude
`system + messages` 或 Responses items 另行归一化成统一 OpenAI `messages` 形状；与 §9.3 已归一化的 output
相比，input 在 Langfuse UI 中可能只按通用 JSON 展示，不能保证识别为 chat 消息列表。这是保留客户端协议
原貌、避免第二套有损请求转换的明确取舍。精确捕获 provider-specific 上游报文会扩大隐私面并侵入大量 adaptor，
不在第一版范围内。

`completion_start_time` 继承 `RelayInfo.SetFirstResponseTime` 的全请求单次闩锁语义：如果 attempt 1
已经写出部分流后失败、attempt 2 才成功，首次响应时间已经被 attempt 1 消耗，成功 generation
不会再得到自己的 first-token 时间。这个结果是当前规则的明确后果，不视为采集 bug；成功 generation
仍可通过 `partial_output`、attempt 状态和 root 输出判断重试过程。

没有真实上游调用时，trace 只有 root。两次失败重试后成功时，结果是一个 root、两个失败 generation 和一个成功 generation；usage/cost 只属于最后的成功 generation。

## 7. User 与 Session 语义

### 7.1 User

`relayInfo.UserId > 0` 时，`user.id = strconv.Itoa(relayInfo.UserId)`；否则省略 `user.id`。数值 ID 在用户名或邮箱变化时保持稳定，也不会与客户端传入的 OpenAI `user` 字段混淆。

非空时在 metadata 中包含 `username` 和 `user_group`。默认不导出邮箱，因为它是直接个人信息且排障收益有限。请求体的 `user` 字段只可能作为已采集 input 的一部分存在，绝不覆盖 trace identity。

### 7.2 Session

准确性优先于覆盖率。先按以下顺序读取客户端显式提供的原始 session：

1. 请求 Header `X-Langfuse-Session-Id`；
2. 管理员配置的额外 Header 名称，按顺序；
3. 管理员配置的 JSON body path，按顺序。

首个 raw 非空值锁定来源并生效，再执行 trim 和合法性校验；锁定的 Header 候选非法时省略 session，不继续
尝试低优先级 Header/body。body path 内则忽略不存在、空值和不支持的 scalar 类型，首个支持的非空
string/number 候选锁定来源；该候选后续校验失败时同样不继续尝试其他 path。额外 Header 和 body path 默认均
为空。`X-Session-Id` 是含义不统一的通用 Header，默认不读取；只有管理员确认其在当前部署中的语义并把它加入
额外 Header 列表后，才把它作为 session 来源。

严禁通过以下信息推导 session：

- 对话文本或内容 hash；
- `user.id`、username、email 或请求体 `user`；
- prompt cache key；
- 未由管理员明确配置具体路径的任意 metadata 字段。

提取值先 trim；不是合法 UTF-8、包含 CR/LF 或其他控制字符、或超过 200 字节时整项拒绝。除 trim 外不改写原始值，避免改变客户端 ID 的稳定性；超长值不截断，防止不同原始 ID 被截成同一个 session。处理后为空则不设置 `session.id`。

Langfuse session 在 project 内是全局命名空间，不能直接写客户端可控的原始值。原始值合法且
`relayInfo.UserId > 0` 时，导出 `session.id = strconv.Itoa(relayInfo.UserId) + ":" + rawSessionId`；例如用户
`42` 的原始 `default` 导出为 `42:default`。数值用户 ID 使用规范十进制表示，分隔符固定，因此不同 New API
用户不会因为相同客户端默认值合并。这里的用户前缀是明确的租户作用域，不是从用户字段猜测 session；原始值
本身仍严格来自上述显式来源，不做 hash 或截断。没有正数用户 ID 时省略 `session.id` 并写
`session_omitted_reason=user_scope_unavailable`，不能退化成 project 全局 raw session。采样同样使用作用域 ID，
不能只 hash 原始值。metadata 可写 `session_scope="user"` 和不含原始值的 `session_source` 供诊断，但不得再
复制一份原始 session。

body path 使用项目已有的 `gjson`，只对 JSON 请求生效，只接受 string 或 JSON number；array、object、bool 和 null 均忽略，不序列化为 ID。由于 `gjson` 的 path 查询要求完整 JSON，body path 提取必须有独立的 `max_session_body_bytes` 上限，默认值和硬上限均为 64 KiB，不能复用允许截断的 `max_content_bytes` 前缀：`Begin` 只读取 `gin.Context` 中已经存在的 `common.KeyBodyStorage`，不得调用 `GetBodyStorage`/`GetRequestBody` 创建缓存；storage 存在时先用 `BodyStorage.Size()` 判断，只有完整 body 不超过该上限，才通过独立 reader 读取全量并执行 path 查询。storage 不存在、超限、大小未知、读取不完整或 JSON 无效时，跳过全部 body path，写 `session_body_omitted_reason`，继续保留此前的 Header 提取结果；不得调用 `BodyStorage.Bytes()`，也不得对截断 JSON 运行 `gjson`。

省略原因是稳定 telemetry schema，不允许实现时自由拼接字符串。闭合集合如下：

- `session_omitted_reason`：`empty_after_trim`、`invalid_utf8`、`invalid_control_character`、`too_long`、
  `user_scope_unavailable`。完全未提供 session 时不写该字段；来源按上文规则锁定后，写该唯一候选的拒绝
  原因，不继续寻找低优先级候选。
- `session_body_omitted_reason`：`non_json_content_type`、`body_storage_unavailable`、
  `body_storage_type_mismatch`、`body_size_unknown`、`body_too_large`、`body_read_failed`、
  `body_read_incomplete`、`invalid_json`、`no_supported_scalar`。只有配置了 body paths、完全没有 raw 非空 Header
  候选，且 body 因访问/完整性/JSON/类型问题无法产生支持的非空 scalar 时才写；多个失败条件按上述顺序取
  第一个。若 body 已产生支持的 scalar、但该候选在 trim/UTF-8/control/长度校验中失败，只写对应的
  `session_omitted_reason`，不再写 `session_body_omitted_reason`。

## 8. 属性映射

OTel attribute 在线路上只能使用标量或同类型标量数组，不能直接承载 JSON 对象。本文后续的 JSON 代码块描述的是反序列化后的逻辑值；实现必须先用项目 `common.Marshal` 序列化，再通过 `attribute.String` 写入。以下属性必须是合法 JSON object 字符串，不能写成 OTel map、数组或逐字段臆造的属性：

- `langfuse.observation.metadata`；
- `langfuse.observation.model.parameters`；
- `langfuse.observation.usage_details`；
- `langfuse.observation.cost_details`。

Langfuse 对 usage/cost 的非 string 属性会按坏 JSON 丢弃；model parameters 会对 string 执行 `JSON.parse`；metadata 也以 JSON 字符串解析。所有 marshal 失败都只省略对应属性并限频记录脱敏错误，不得影响 relay。`langfuse.observation.completion_start_time` 例外：它是普通 ISO-8601 字符串；仅在 `relayInfo.HasSendResponse()` 为 true 且该时间可归属当前 attempt 时，使用 `relayInfo.FirstResponseTime.UTC().Format(time.RFC3339Nano)`，否则省略。不得写初始化哨兵值、Unix 秒或毫秒数，也不得让 writer 另测一份首包时间。input/output 若是结构化值，也先序列化成 JSON 字符串以符合 OTel attribute 类型约束。

### 8.1 Root metadata

统一只写为 root 的 `langfuse.observation.metadata` JSON object 字符串，不再重复设置
`langfuse.trace.metadata`。当前 Langfuse ingestion 创建 root trace 时，会把 observation metadata
`spanAttributeMetadata` 合并进 trace metadata；root 本身又因 `isRootSpan` 无条件创建 trace，因此省略 trace
域副本不会影响 trace 或 observation 展示，也不会改变 `hasTraceUpdates` 的行为。该规则与 §6.1 的 root
input/output fallback 使用同一 ingestion 机制，并把 root metadata 的 OTLP 字节和属性预算从两份降为一份：

```json
{
  "request_id": "...",
  "username": "...",
  "token_id": 123,
  "token_name": "...",
  "user_group": "...",
  "selected_group": "...",
  "request_path": "/v1/chat/completions",
  "relay_format": "openai",
  "origin_model": "...",
  "is_stream": true,
  "is_playground": true,
  "attempt_count": 2,
  "retry_count": 1,
  "first_response_ms": 182,
  "use_time_ms": 934,
  "quota": 615,
  "billing_source": "wallet",
  "capture_state": "full",
  "content_truncated": false,
  "content_redacted": false,
  "session_scope": "user",
  "session_source": "x-langfuse-session-id",
  "session_omitted_reason": "invalid_control_character",
  "session_body_omitted_reason": "body_too_large"
}
```

`quota` 和 `billing_source` 是可选且必须成对出现的 trace 级结算摘要，不是 root 自己拥有的 usage/cost。
它们只能从冻结 attempt 值对象中的 `UsageRecord` 生成：当且仅当全部 attempt 中恰好一个 attempt 的最终
`UsageRecord.Settled == true`，且该 record 的 `Quota >= 0` 时，root 才复制该 record 的 `Quota` 和已在
`RecordUsage` 调用点归一化的 `BillingSource`；不得在 `Finish`/worker 中重新读取 `RelayInfo.BillingSource`、
预扣值或全局计费状态。失败和 superseded attempt 不是候选；`UsageRecord.Available` 只控制 usage 是否可导出，
不改变这条 settlement 选择规则。没有候选、存在多个 settled 候选或唯一候选的 quota 非法时，root 同时省略
`quota` 和 `billing_source`，不能任选一次 attempt 或做求和。

因此，两次失败重试后成功且只有最终 attempt 结算时，root 可复制最终 attempt 的摘要；AWS SDK/Xunfei
`no_active_attempt` 降级和结算失败时没有 `Settled=true` 候选，必须省略二者。root 在任何情况下都不写
`langfuse.observation.usage_details` 或 `langfuse.observation.cost_details`；上述 metadata 摘要仅用于 trace
筛选，usage/cost 的 observation 归属仍严格保留在唯一成功 attempt。

`is_playground` 仅在 `relayInfo.IsPlayground` 为 true 时写入 true；普通生产请求可省略。除 §7.2 的 session
原因外，其余字符串原因也都是闭合 schema：

- `input_omitted_reason`：`body_storage_unavailable`、`body_storage_type_mismatch`、`body_read_failed`；空 body
  不写该字段，超限 input 使用 `input_scan_truncated=true` 而不是冒充完全省略。
- `attempt_end_reason`：`handler_returned`、`replaced_by_next_upstream_call`、`finalizer_cleanup`、
  `lifecycle_panic`。每个已创建 attempt 必须恰好写一个；正常的外层 `EndAttempt` 使用 `handler_returned`。
- `cost_omitted_reason`：`attempt_failed`、`attempt_superseded`、`settlement_unavailable`、
  `settlement_failed`、`no_billable_usage`、`invalid_quota`、`invalid_quota_per_unit`。只有没有写 `cost_details` 时才写，
  优先级按此列表；`attempt_superseded` 只用于 `replaced_by_next_upstream_call`，不能冒充 provider failure；
  `settlement_unavailable` 表示成功 attempt 从未收到结算记录，不能替代一个已知的具体失败原因。
- `usage_omitted_reason`：`usage_unavailable`、`no_active_attempt`、`invalid_source`、`arithmetic_overflow`、
  `semantic_unknown`。clamp 后仍可导出的 usage 不写 omission reason。

`capture_state` 必须始终存在，枚举为：

- `full`：正文采集、worker 处理和属性设置完成；正文仍可能按配置上限截断；
- `budget`：全局捕获预算不足，未读取内容采集 input、未包装 writer；
- `admission`：已捕获，但异步 worker admission 失败，降级为 metadata-only；
- `disabled`：`send_content=false`，未执行内容采集；
- `panic`：capture writer、sanitizer、aggregator 或生命周期内部失败/被 recover，正文不再可信并省略。

若多个降级原因同时出现，优先级为 `panic > admission > budget > disabled > full`。截断不是互斥生命周期
状态：`content_truncated`、`input_scan_truncated` 和 generation 的 `output_truncated` 独立记录细节，因此
正文截断可以与 `admission` 等状态同时发生。示例中的 `session_omitted_reason` 和
`session_body_omitted_reason` 仅在对应省略发生时写入；metadata 不包含被拒绝的 session 原值。

不包含 token key、provider API key、Authorization、Cookie、完整请求 Header 和用户邮箱。

### 8.2 Generation metadata

以下对象序列化后写入 `langfuse.observation.metadata`：

```json
{
  "attempt_index": 0,
  "channel_id": 12,
  "channel_name": "...",
  "channel_type": 1,
  "selected_group": "default",
  "relay_format": "openai",
  "upstream_relay_format": "openai_responses",
  "upstream_request_id": "...",
  "is_stream": true,
  "input_source": "client_request",
  "cost_source": "new_api_settlement",
  "billing_source": "wallet",
  "partial_output": false,
  "output_truncated": false
}
```

当未得到权威结算 cost 时，generation metadata 仍保留实际模型信息：写 `upstream_model` 和
`origin_model`，同时令 `cost_source: "unavailable"` 并从 §8.1 的闭合集合写明确的
`cost_omitted_reason`。只有 `SettleBilling` 返回错误时写 `settlement_error=true` 和
`cost_omitted_reason="settlement_failed"`；文本 `summary.hasBillableUsage() == false` 时写
`settlement_error=false` 和 `cost_omitted_reason="no_billable_usage"`；quota/rate 非法则写对应的
`invalid_quota` 或 `invalid_quota_per_unit`，不能伪装成结算错误。

成功 attempt 没有权威 cost 时，不要设置 `langfuse.observation.model.name`，也不要设置
`gen_ai.response.model`、`gen_ai.request.model`、`ai.model.id`、`llm.response.model`、`llm.model_name` 或通用
`model` 等 Langfuse model fallback 属性，以免仍被识别模型并推价。失败或 superseded attempt 可写
`langfuse.observation.model.name` 的前提是同时省略 `usage_details`、满足 §6.2 的 OTel Error status 硬契约，
并分别写 `cost_omitted_reason="attempt_failed"` 或 `"attempt_superseded"`；只要存在 usage 而没有权威 cost，
即使 status 已是 Error 也必须省略 model。其他 model fallback 属性始终禁止。

### 8.3 模型参数

`langfuse.observation.model.parameters` 写为 JSON object 字符串。只记录校验后 DTO 已暴露且客户端明确提供的参数，并保留显式 `0`/`false`：

- `temperature`；
- `top_p`；
- `max_tokens` 或 `max_completion_tokens`；
- `stream`；
- `tool_count`；
- 已经标准化且可可靠获取的 thinking/reasoning 设置。

不批量复制未知 passthrough 字段或参数覆盖内容。

### 8.4 Usage

Recorder 在 `gin.Context` 中的存取是结算接入的显式 API，而不是各调用点自选字符串 key。实现必须在
`constant/context_key.go` 增加 `ContextKeyLangfuseRecorder constant.ContextKey = "langfuse_recorder"`；
`Begin` 只通过 `common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, recorder)` 保存非 nil Recorder。
`service/langfuse` 暴露 `FromContext(c *gin.Context) *Recorder`：`c == nil`、key 不存在、值为 typed nil 或类型
不匹配时一律返回 nil，不 panic、不记录正文，也不创建 Recorder。`PostTextConsumeQuota`、
`PostAudioConsumeQuota` 和窄 attempt hook 统一使用该 getter；不得直接 `c.Get`、重复类型断言或使用包内私有
string key。getter 与 typed-nil 行为必须有单元测试。

usage/cost 在成功 handler 内完成结算后记录，而不是在 `controller.Relay` 外层记录。范围内的两条结算路径都
必须接入：`PostTextConsumeQuota` 和 `PostAudioConsumeQuota`。两者都保留 `SettleBilling` 返回值。文本路径
只有 `SettleBilling` 成功且 `summary.hasBillableUsage()` 为 true 时才传 `Settled=true` 和 authoritative
cost；`SettleBilling` 为完成预扣退款/归零等生命周期收口而成功，但没有 billable usage 时，必须传
`Settled=false` 并省略 cost，不能把“billing session 已收口”等同于“发生了本次实际计费”。音频路径维持
`totalTokens == 0 -> quota = 0` 的现有自洽规则，只有结算成功时才传 `Settled=true`。结算失败仍可传已确认的
usage，但必须省略 cost，并在 generation metadata 标记 `settlement_error=true`；文本无 billable usage 的
正常省略则按 §8.2 标记 `no_billable_usage`，不写结算错误。错误正文不得包含账户或凭证信息。Recorder 由
结算函数通过 `gin.Context` 取回；结算函数必须把 usage/cost 归属到当前仍 active 的 attempt，而不是 root。
若 Recorder 为 nil，保持现有范围外调用的忽略行为；若 Recorder 非 nil 但没有 active attempt（包括旁路 hook
漏接、调用已被 `EndAttempt` 收口，或该结算调用不在受支持的上游 attempt 内），必须丢弃本次 usage 和 cost，
并在 Recorder metadata 写入 `usage_unattributed=true` 及 `usage_omitted_reason=no_active_attempt`。不得把这份
usage/cost 落到 root，也不得等待未来 attempt 再归属；这次被丢弃的 record 不构成 §8.1 的 settled 候选，root
必须同时省略 `quota` 和 `billing_source`。普通共享 HTTP 路径中当前 attempt 尚未 `EndAttempt`，
因此 usage/cost 无歧义地归属该成功 attempt；AWS SDK/Xunfei v1 旁路按 §5.2 明确采用上述降级。`UsageRecord.Quota` 和 `UsageRecord.QuotaPerUnit` 必须在调用
`SettleBilling(ctx, relayInfo, quota)` 的同一请求 goroutine、同一调用点相邻快照：文本路径要在 tiered 结果及
其他覆盖完成后取 `summary.Quota`，音频路径要在 tiered 覆盖完成后取 `quota`，同时读取当时的
`common.QuotaPerUnit`。不得读取预扣 quota、原始计算 quota、tiered 覆盖前的中间值，或让异步 worker 再读
全局 `common.QuotaPerUnit`；SettleBilling 失败时仍保留该最终调用参数和换算快照用于诊断，但不据此生成
authoritative cost。

Langfuse 的 usage details 是互斥桶：`input` 不包含 cache read/cache creation/image；文本路径只要
`billingUsage.PromptTokensDetails.AudioTokens > 0`，就把 audio 从基准 `input` 移出并写入
`input_audio_tokens`，没有 audio prompt token 时不导出该桶。这个边界是 usage 语义而不是价格查询命中：
telemetry 的 audio 桶边界有意不同于真实计费中只有在
`GetGeminiInputAudioPricePerMillionTokens(model) > 0 && !PriceData.UsePrice` 时才从 `baseTokens` 扣除
音频的条件。未配置 audio 价格时，telemetry 仍拆出 audio 桶，而计费仍按基础倍率计算这些 token；这是有意
选择 usage 语义的结果。
本文统一使用 `input_cache_creation` 作为 cache creation 桶的 canonical key；表格、示例、Recorder 字段映射
和实现说明中的“cache creation”均指这个 key，不得另造 `cache_creation`、`input_cache_write` 或
`input_cache_creation_input_tokens` 变体。选择该 key 的依据是 Langfuse 对隐式 `gen_ai.usage.*`/
`llm.token_count.*` 属性执行 alias 归一化时，会把 cache creation 输出为 `input_cache_creation`，其 UI、查询和
既有数据因而按该 canonical key 对齐。`output_audio_tokens` 同样是该归一化器产出的内建 key。

`input_image_tokens` 和 `input_audio_tokens` **不是**当前 Langfuse 隐式归一化器的 canonical key，也没有会
将 provider alias 映射到它们的内建规则。New API 为保持自身互斥 usage 口径仍显式发送这两个名称，但它们是
有意的、永久自定义 usage types；UI、查询、价格配置和迁移不能假设它们会自动并入 Langfuse 内建 input 桶或
未来被改名。若 Langfuse 后续增加正式 canonical key，必须先做数据兼容设计，不能静默替换历史名称。

New API 显式写整个 `langfuse.observation.usage_details` JSON object。固定 Langfuse revision 的源码契约表明，
ingestion 对该属性解析成功后原样返回并短路归一化器，不会替 New API 折叠 alias、纠正拼写或改写 bucket 名。
Go 契约测试只验证 New API 发出的 OTLP JSON 使用精确 canonical/custom keys，且生产映射不会发出已知拼写错误；
“自定义 key 原样入库、拼错 key 不会自动纠正”作为外部 ingestion 不变式，由 §14.6 的真实 Langfuse E2E 验证，
不在 Go 测试中复刻 TypeScript worker。
`output` 不包含 reasoning/audio 等细分 output，`total` 必须等于所有已导出非 `total` 桶之和。调用点不得把语义字符串
传给 Recorder；文本路径计算并复用 fold 后的 `InputExcludesCache`，同时把 image/audio 的标准数值放入 summary，
供标准文本计费分支和 Recorder 共用：

```go
type textQuotaSummary struct {
    // ... normalized billing fields ...
    IsClaudeUsageSemantic             bool
    UpstreamPromptTokensIncludeCache  bool // fold 前的 raw upstream 口径，仅供 tiered billing
    InputExcludesCache                bool // fold 后 summary.PromptTokens 的实际口径，仅供 Recorder
}

promptTokensIncludeCache, declaredCacheSemantic := resolvePromptCacheInclusion(relayInfo, billingUsage)
summary.UpstreamPromptTokensIncludeCache = promptTokensIncludeCache

if promptTokensIncludeCache && (declaredCacheSemantic || isOpenRouterClaudeBilling) {
    // 现有 fold：从 summary.PromptTokens 扣除 cache read/cache creation，并做既有 clamp。
    // ...
    promptTokensIncludeCache = false
}
summary.InputExcludesCache = !promptTokensIncludeCache
```

`resolvePromptCacheInclusion` 是 raw upstream prompt 口径的权威来源，channel 的
`cache_prompt_token_semantic` 声明优先于 usage semantic 自动判定。fold 执行后局部变量
`promptTokensIncludeCache` 已被更新为当前 `summary.PromptTokens` 的实际口径，因此
`InputExcludesCache = !promptTokensIncludeCache` 对以下路径都成立：声明 `prompt_includes_cache` 的非 Claude
渠道在 fold 后为 true；声明 `prompt_excludes_cache` 的非 Claude 渠道不执行 fold但本来就是 true；普通
inclusive OpenAI/Gemini 保持 false；Anthropic、legacy Claude-derived 和已 fold 的 OpenRouter Claude 为 true。
Recorder 不得再读取 `UsageSemantic`、channel 声明或 `UpstreamPromptTokensIncludeCache` 重新推导。

`legacyClaudeDerived` 仍可由 `isLegacyClaudeDerivedOpenAIUsage(relayInfo, billingUsage)` 计算，但只保留给现有
cache creation 计价档位选择。`text_quota.go` 当前唯一的
`!summary.IsClaudeUsageSemantic && !legacyClaudeDerived` 判断控制扁平 `CacheCreationRatio` 与 Claude 5m/1h
拆分倍率，不是在判断 prompt 是否包含 cache。不得将它替换为 `!summary.InputExcludesCache`；否则声明
`prompt_excludes_cache` 的 OpenAI 渠道会错误进入 Claude 5m/1h 计价分支。Langfuse 接入只新增/读取 summary
字段，不改这个计费分支。

当前工作树的 tiered billing 前置改动尚未落地完整：`PostTextConsumeQuota` 已用
`BuildTieredTokenParams(billingUsage, summary.IsClaudeUsageSemantic,
summary.UpstreamPromptTokensIncludeCache, tieredUsedVars)` 四个参数调用，但函数签名、其他调用点和测试仍是三个
参数，当前 `go test ./service` 因此无法编译。Langfuse 实现必须以该独立计费改动完成并恢复绿色基线为前提，
再按落地后的最终签名和回归结果对齐；不得在 Langfuse PR 中顺手完成或重新设计 tiered 行为。
`UpstreamPromptTokensIncludeCache` 表示 fold 前 raw usage 口径，与 Recorder 的 fold 后
`InputExcludesCache` 不同，二者不得互传。计费前置改动必须自行覆盖 `PostTextConsumeQuota`、
`PostAudioConsumeQuota`、`controller/channel-test.go`、cache/image/audio、5m/1h 和 `len`，并在落地后重算其
表驱动期望值；Langfuse 的 usage 表只验证 Recorder 的 fold 后互斥桶，不把 tiered quota 结果锁进同一组用例。

```go
type UsageRecord struct {
    Kind                         UsageKind // text 或 audio，决定归一化规则
    Available                    bool
    ModelName                    string
    InputExcludesCache           bool
    UsageSemanticUnknown         bool
    InputTokens                  int
    OutputTokens                 int
    InputCachedTokens            int
    InputCacheWriteTokens        int
    InputImageTokens            int
    InputAudioTokens            int
    OutputAudioTokens            int
    OutputReasoningTokens        int
    Quota                        int
    QuotaPerUnit                 float64 // 与 Quota 在 SettleBilling 调用点一起快照
    BillingSource                string
    Settled                      bool
}
```

`Available` 只能在调用点确实拿到上游归一化 usage 时设为 true。文本路径要求
`billingUsage != nil`；缺少上游 usage 时，即使结算代码为内部计费生成了 prompt estimate，也不得把该
estimate 冒充 provider usage。音频路径使用传入 `PostAudioConsumeQuota` 的真实 `usage`。`Available=true`
且所有 token 都为 0 是合法的真实全零 usage，必须导出核心的三个 0。cost 是否可导出由
`Settled`、quota 和同一 `UsageRecord` 内的 `QuotaPerUnit` 快照决定；usage 是否可用仍是独立维度，但文本
`Settled` 还受上文 `summary.hasBillableUsage()` 约束。

文本归一化规则：

- `InputExcludesCache=false` 表示 fold 后的 `InputTokens` 仍是 cache-inclusive，不论 provider/usage semantic：
  `input = InputTokens - InputCachedTokens - InputCacheWriteTokens - InputImageTokens`；当
  `InputAudioTokens > 0` 时再减 `InputAudioTokens`；audio 桶边界由 usage 语义决定，Recorder
  不在 `UsePrice` 分支中重新判断；`output = OutputTokens - OutputReasoningTokens`。`Kind=text` 不拆分
  `OutputAudioTokens`，该字段即使为正也不再从 `output` 扣除或导出 `output_audio_tokens`；因此文本路径的
  `output` 保留 completion audio token 所在的上游总 output 口径。只有 `Kind=audio` 才使用并拆分该字段。
- `InputExcludesCache=true` 表示 fold 后的 `InputTokens` 已排除 cache read/cache creation，来源可以是上游
  原生 exclusive 语义、channel 声明或 summary fold；因此不再减 cache 桶，但仍无条件减
  `InputImageTokens`，并在
  `InputAudioTokens > 0` 时减 `InputAudioTokens`。这是按 OpenAI usage 语义拆分互斥桶的规则，
  不是为了复刻 `text_quota.go` 的 `UsePrice` 分支：该分支在 `PriceData.UsePrice=true` 时完全跳过 token
  扣减块，`AudioInputPrice` 也会保持 0，但 Recorder 仍必须完成互斥桶拆分，不能把 `UsePrice` 作为是否
  扣减的条件，否则会破坏 `total == sum(all non-total buckets)`。Recorder 必须在 `UsePrice` 分支之外完成该互斥桶拆分，不能因为价格查询未命中而把 audio 合并回 base。Claude output 若确实提供独立 reasoning detail，仍从 output 扣除。
- 文本路径的 `InputImageTokens` 固定取 `summary.ImageTokens`。`InputAudioTokens` 固定取
  `summary.AudioTokens`，只要该值大于 0 就从 base 扣除并导出 `input_audio_tokens`。价格查询仍只发生在
  文本结算计算中，Recorder 不调用 `GetGeminiInputAudioPricePerMillionTokens`、不读取价格设置，也不自行
  解释 model/price；它只按 usage 语义拆分 audio。image 桶即使为 0 也属于文本路径的已知标准细分桶；audio
  为 0 时不导出 audio 桶。
- `InputCacheWriteTokens` 必须匹配当前标准文本计费的 base-token 扣减口径。inclusive 分支使用
  `summary.CacheCreationTokens`，因为 `text_quota.go` 实际从 `baseTokens` 减去的是
  `dCachedCreationTokens`；不得改用 `cacheWriteTokensTotal(summary)` 的口径，后者在 5m+1h 大于通用总数
  时会让导出的 base input 比计费口径更小。exclusive 分支不从 input 扣 cache，可按
  `cacheWriteTokensTotal` 的 `max(CacheCreationTokens, checked(5m+1h))` 语义导出 Claude 5m/1h 的完整物理
  总数；Recorder 必须使用 checked addition，不能直接依赖当前 helper 内的裸 `int` 加法。该选择只影响
  telemetry 值对象，不修改现有计费计算。
- OpenAI cache-write 前缀计数可能与 cached tokens 重叠，`cached + cache_write > prompt` 是现有计费
  明确兼容的正常形态。inclusive 减法结果小于 0 时把 `input` clamp 为 0，继续导出其余桶，并在
  generation metadata 写 `usage_input_clamped=true`；不得因此省略整组 usage。output detail 大于父桶时
  同样把 `output` clamp 为 0，并写 `usage_output_clamped=true`。
- 只有源 token 本身为负、checked addition/subtraction 溢出，或 semantic 无法映射时，才省略整组
  `usage_details`，写脱敏告警：负源值写 `usage_invalid=true` 和
  `usage_omitted_reason=invalid_source`，checked arithmetic 溢出写 `usage_invalid=true` 和
  `usage_omitted_reason=arithmetic_overflow`，semantic 无法映射写 `usage_semantic_unknown=true` 和
  `usage_omitted_reason=semantic_unknown`。文本结算调用点必须在调用 `calculateTextQuotaSummary` 之前，直接检查
  `billingUsage` 的原始 `PromptTokens`、`CompletionTokens`、`PromptTokensDetails.CachedTokens`、
  `CachedCreationTokens`、`CacheWriteTokens`、image/audio details、Claude 5m/1h 字段和 completion details，
  并把结果保存为仅供 telemetry 使用的 `usageSourcesValid`；这里的 `billingUsage` 是
  `effectiveBillingUsage(usage)` 的返回对象，若存在 `Usage.BillingUsage` 则是该对象映射出的归一化值，
  但校验必须发生在 `CacheCreationTokensTotal()` 之前、直接检查其中仍保留的原始 cache-write/cache-creation
  字段。计费仍按现有流程继续；Recorder 在该值为 false 时省略 usage。不得用 `summary.CacheCreationTokens`
  做源值校验，因为它已经来自会把负值归零的 `CacheCreationTokensTotal()`，无法发现负的原始字段。

音频结算路径不套用 cache inclusive/exclusive 推导。它直接使用当前 `PostAudioConsumeQuota` 的计费输入：
`InputTokens=usage.PromptTokensDetails.TextTokens`、
`OutputTokens=usage.CompletionTokenDetails.TextTokens`、
`InputAudioTokens=usage.PromptTokensDetails.AudioTokens`、
`OutputAudioTokens=usage.CompletionTokenDetails.AudioTokens`，导出 `input`、`output`、
`input_audio_tokens` 和 `output_audio_tokens` 四个互斥桶。`TextTokens` 是为与音频计费路径一致而刻意选择的
来源；如果上游没有填充该细分字段，`input` 或 `output` 为 0 是预期结果，不是 Recorder 漏采集或需要用
其他 token 字段猜测的 bug。文本路径的 image/audio-input 分支则按上面的 summary image/audio 数值导出
`input_image_tokens`/`input_audio_tokens`。这样 Chat Completions/Responses 的 audio 请求同时得到 usage、
权威 quota/cost 和 model 属性，不会退化成空 generation。

`total` 不复用 `summary.TotalTokens` 或 `usage.TotalTokens`，而是在归一化后以 checked addition 计算所有
实际导出的非 `total` 桶。文本路径为 `input + input_cached_tokens + input_cache_creation + input_image_tokens + output +
output_reasoning_tokens`，并在 `InputAudioTokens > 0` 时再包含 `input_audio_tokens`；音频结算路径
包含 `input + input_audio_tokens + output + output_audio_tokens`。Recorder 的硬契约是
`total == sum(all non-total buckets)`。

文本 `Available=true` 时其余字段固定来自 `effectiveBillingUsage` 之后的数据：
`InputTokens=summary.PromptTokens`、`OutputTokens=summary.CompletionTokens`、
`InputCachedTokens=summary.CacheTokens`、`InputImageTokens=summary.ImageTokens`、
`InputAudioTokens=summary.AudioTokens`（audio 大于 0 时始终拆分，不受 `UsePrice` 分支控制）、
`OutputReasoningTokens=billingUsage.CompletionTokenDetails.ReasoningTokens`、
`InputExcludesCache=summary.InputExcludesCache`。`Kind=text` 时，已知的 cache/image/reasoning 标准细分桶
即使为 0 也保留，audio input 桶仅在 `InputAudioTokens > 0` 时导出；`OutputAudioTokens` 在该 Kind 下忽略，
不导出 `output_audio_tokens`，completion audio token 已留在 `output`。`Kind=audio` 时，已知的 text/audio 桶即使为
0 也保留，但不臆造 cache/image/reasoning 桶。`Available=false` 时不访问 usage。metadata 的
`billing_source` 优先使用非空的
`relayInfo.BillingSource`；明确免费时写 `free`，其他确实无法判定的兼容路径写 `unknown`，不能把空
字符串冒充钱包扣费。

映射后用 `common.Marshal` 写成 `langfuse.observation.usage_details` JSON object 字符串，例如：

```json
{
  "input": 200,
  "output": 50,
  "total": 1100,
  "input_cached_tokens": 800,
  "input_cache_creation": 0,
  "input_image_tokens": 0,
  "output_reasoning_tokens": 50
}
```

上例对应 OpenAI 语义的 `prompt=1000/cached=800/completion=100/reasoning=50`。已知值即使为 0 也保留；
Langfuse 会保留包含 `input: 0`/`output: 0` 的对象。只有确实不可用的细分字段才省略，但核心
`input`、`output`、`total` 必须成组出现。所有值必须是非负整数，浮点数会被 Langfuse 静默过滤。

### 8.5 Cost

New API quota 是货币核算单位；`common.QuotaPerUnit` 个 quota 等于 1 USD，但它是可由
`model/option.go` 在运行时修改的 `var`，不是编译期常量。`Settled=true`、quota 非负且同一结算调用点快照的
`UsageRecord.QuotaPerUnit` 为有限正数时，把以下对象序列化为 JSON string 后写入 cost details：

```json
{"total": 0.00123}
```

其中 `total = UsageRecord.Quota / UsageRecord.QuotaPerUnit`。worker 只能使用值对象中的这两个快照；即使配置
在请求结算后、worker 执行前发生切换，也不能用新汇率换算旧 quota。这里的 `quota` 必须是 §8.4 所述在
`SettleBilling` 调用点取到的最终结算参数，而不是预扣值或 tiered 覆盖前的中间值。文本路径还必须满足
`summary.hasBillableUsage()`；单纯成功执行 billing session 收口不生成 cost。当
`BillingSource == wallet`（以及无 BillingSession 的兼容钱包路径）时，cost 表示应用分组倍率、tiered
billing、自定义模型价格和工具附加费后的本次最终计价额度；当 `BillingSource == subscription` 时，quota 是
订阅额度消耗的定价等值，并非从用户钱包实际扣取的 USD。两种情况都必须在 trace 和 generation metadata 写
`billing_source`，订阅场景不得表述为“网关实际收费”。

有权威 New API 结算价时不让 Langfuse 根据模型目录另行推价。有 billable usage 的免费请求明确写
`{"total": 0}`；Langfuse 会把 `0` 当作已提供 cost，从而抑制推价。成功 attempt 在文本没有 billable usage、
结算失败或 quota/rate 无效时省略 cost，并按 §8.2 区分原因；此时必须省略 Langfuse 能识别的全部 model 属性，
只把 `upstream_model` 放到 metadata，避免 Langfuse 根据 model + usage 自动算出一笔并非 New API 权威结算的
成本。失败或 superseded attempt 仅可使用 §6.2/§8.2 的“无 usage + model + Error status”例外；只要仍有
usage 而没有权威 cost，就必须省略 model。任何情况下都不导出负成本。

cost 的 JSON number 表示由 `common.Marshal`（当前为 Go 标准库 `encoding/json`）统一生成，不做
`FormatFloat`、字符串 cost 或手工小数拼接。默认 `QuotaPerUnit=500000` 的边界必须进入契约：最小正整数
`Quota=1` 精确编码为 `{"total":0.000002}`，`Quota=0` 精确编码为 `{"total":0}`；两者反序列化后的 `total`
必须分别严格等于 `1.0/500000.0` 和 `0`，并能被 Langfuse 当作 Number 处理。`QuotaPerUnit` 可运行时变化，
因此其他有限正快照可能合法地产生科学计数法；测试不能禁止合法 exponent，也不能把 number 改成 JSON string。

## 9. 内容捕获、脱敏与聚合

### 9.1 大小控制

- `Begin` 只从 `gin.Context` 中已经缓存的 `BodyStorage` 读取 input，不调用 `GetBodyStorage`/`GetRequestBody`；使用独立 reader，最多读取 `max_content_bytes + 1`，不得调用 `Bytes()` 把超大磁盘缓存请求整体读回内存。storage 不存在时省略 input，不能主动消费原始 body。
- session body path 是另一条要求完整 JSON 的读取路径：仅当缓存的 `BodyStorage.Size() <= max_session_body_bytes` 时通过独立 reader 完整读取；`max_session_body_bytes` 默认且硬上限为 64 KiB。storage 不存在或超限时直接跳过 body path，只保留 Header session 来源。它不允许读取截断前缀，也不得扩大 input 内容采集上限。
- 捕获 writer 每个请求只维护一份共享响应缓冲，最多保存 `max_response_bytes`，默认 512 KiB；attempt 只保存
  这份缓冲的起止 offset，不复制响应字节。
- 进程维护一个独立的原子 `inFlightCaptureBytes` 和 `max_in_flight_capture_bytes` 全局预算（默认 512 MiB，
  上限由部署容量决定）。第一版明确选择提高默认预算而不是让 capture writer 增量抢占预算：长流式请求的
  `Write` 热路径仍不执行全局 CAS，代价是按最坏响应上限预留。单请求 reservation 是与 attempt 数无关的常量：
  `2 * max_content_bytes + max_response_bytes`，分别覆盖 input 有界副本、共享 response 缓冲和一份
  `max_content_bytes` 的脱敏/聚合结果余量；使用 checked arithmetic 计算。实现必须把请求 input 物化为一个
  不可变的请求级 `*string`（其底层是同一个 Go string），所有 generation 的 `observation.input` 都复用同一
  对象；attempt 只保存该对象的引用和 offset，不得为每个 generation 重新复制或 materialize input。Recorder
  创建 generation 时必须断言共享 `*string` 引用未变化（不能只用字符串内容相等代替）；断言失败按
  `capture_state=panic` 丢弃不可信正文。worker 必须先生成脱敏 input，清除对原始 input 的引用后再聚合
  output，并通过缓冲复用或及时释放中间值确保任一时刻只有一份额外的 `max_content_bytes` 结果缓冲，不得按
  `common.RetryTimes + 1` 或实际 attempt 数放大 reservation。每个实际 attempt 的小型值对象和 span metadata
  不复制 input/response bytes，因此不计入正文捕获字节 reservation；worker admission 和 OTel span queue
  仍分别使用自身的条数上限。默认配置下每请求 reservation 为 640 KiB，512 MiB 预算最多允许约 819 个正文
  捕获作业同时处在“请求处理或 worker 尚未结束”的阶段；这只描述 capture/worker 这一层，绝不代表整个
  telemetry pipeline 的内存上限。由于 reservation 从 `Begin` 持有到 worker 完成，它是并发槽位而不是吞吐
  配额：`capture_slots = floor(max_in_flight_capture_bytes / reservation)`，稳态可承载的已采样正文请求吞吐近似
  `capture_slots / average_capture_lifetime_seconds`。默认值在平均生命周期 30 秒时约为 27.3 sampled RPS；当
  `sample_rate=0.1` 且其他请求分布近似均匀时，约对应 273 total RPS。管理员仍必须结合实际流式时长调低内容
  上限/采样率或提高预算，不能把 sample rate 当作唯一容量旋钮。提高到 512 MiB 是为了避免典型 100 total RPS、
  10% 采样流量在默认配置下成片退化；它不是进程总内存承诺。span 结束后正文属性会转移到 BSP queue，必须再
  计入下述独立预算。
- reservation 通过 CAS 一次性预留，禁止先递增再检查导致超限。capture writer 的 `Write` 只消费已经
  预留的额度，不在每个 chunk 上重新竞争全局计数。重试只增加 offset metadata；无论运行期
  `common.RetryTimes` 如何变化，都不会额外消耗捕获字节，也不存在“attempt 超过 reservation 上限后降级”
  的规则。
- `send_content=false` 不申请 reservation。`Begin` 无法原子预留完整 reservation 时，该请求设置对应的
  `capture_state` 并降级为 metadata-only：继续原样透传响应，不读取或复制内容采集 input、不创建 capture
  writer、不做 input/output 聚合；Recorder 仍保留不含正文的 root/attempt metadata。请求结束前不得重新
  尝试抢预算，避免高并发形成惊群。
- 预算所有权一直持有到异步 worker 完成或 admission/panic 降级路径丢弃冻结缓冲后才释放；持有预算的
  路径必须在使用显式业务时间 materialize 并调用 `span.End(trace.WithTimestamp(...))` 返回后执行一次幂等
  释放。此时 OTel SDK 已复制/接管不可变 span 数据，Recorder 可以释放 capture reservation，但这些正文仍由
  BSP queue/batch 持有直到导出或丢弃；不能把 `span.End()` 当作进程内正文内存已经释放。BSP 队列满、exporter
  429/错误或进程 shutdown 的丢弃不再引用 Recorder 的捕获缓冲，也不需要不存在的逐条 exporter 回调。
- 所有非正文 string/bytes 属性（metadata JSON、模型参数、错误摘要、名称等）合计必须截断在每 span
  64 KiB 的内部上限内；截断只保留诊断必要字段并设置 `metadata_truncated=true`，不能截坏 JSON。root
  metadata 仅写 observation 域一份。TracerProvider 还必须按 §11 显式锁定 `SpanLimits`，使进程级
  `OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT`、`OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT`、
  `OTEL_ATTRIBUTE_COUNT_LIMIT` 或 `OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT` 不能在 SDK 编码前截断 JSON string 或
  丢弃属性；本文的结构化缩减才是 attribute 长度的唯一控制层。配置校验用 checked arithmetic 计算保守的单 span 正文包络
  `max_queued_span_bytes = 2 * max_content_bytes + max_response_bytes`；该公式覆盖 observation input/output、
  response 捕获相关余量，并有意高估最终每项已截断到 `max_content_bytes` 的 span 正文。
- 当前 Langfuse revision 有两个容易混淆的可观测性阈值：ingestion processor 对单个 span 的
  `JSON.stringify(span)` UTF-8 `eventBytes > LANGFUSE_OTEL_MAX_SPAN_BYTES` 记录 warning/metric，默认阈值是十进制
  `9,500,000` 字节；OTLP HTTP 路由还会在解 gzip 后的整批 protobuf body 超过 `16 * 1024 * 1024` 字节时记录
  request warning。两者都不拒绝数据，也都不是 Langfuse 内建 request body limit。New API 只把前者作为单
  span 容量规划信号：要求 `max_queued_span_bytes <= 9,000,000` 字节，为 JSON/OTLP envelope 和默认 span warning
  留出至少 500,000 字节保守余量；不得把后者升级成 15 MiB splitter 或发送正确性约束。部署 ingress 对压缩
  on-wire body 或解压后 body 的限制仍必须由部署者独立配置，不能从上述 warning 推导。
- BSP 的 `queue_size` 单位是 span 而不是 trace/request，且标准 BSP 只按条数限界，所以它仍是 telemetry 的
  主要内存风险。正文常驻基线按
  `max_in_flight_capture_bytes + queue_size * max_queued_span_bytes` 规划。exporter 正在处理一个 batch 时，BSP
  worker 不再消费 queue，queue 可重新填满；考虑当前 batch、未压缩 protobuf、gzip/request buffer 可同时存在，
  容量规划使用
  `max_in_flight_capture_bytes + (queue_size + 3 * batch_size) * max_queued_span_bytes + metadata/runtime overhead`，
  并在配置校验中要求括号后的正文项不超过 256 MiB。这个 256 MiB 是 New API 的本地保守上限，不是 Langfuse
  body limit；64 KiB metadata、OTLP envelope、HTTP/TLS 和 Go runtime 仍需部署余量。默认值下
  `max_queued_span_bytes=640 KiB`，正文常驻基线约 552 MiB，导出阶段正文侧规划值约 582 MiB，外加上述开销；
  管理 UI 和部署文档必须显式展示这一默认内存理由与规划值，部署者还必须为 relay 业务流量和 Go runtime
  预留独立余量。
  Go 集成测试要编码真实边界 span/batch，断言本地保守包络不超过 9,000,000 字节，并记录官方 exporter 产出的
  protobuf/gzip 大小供 ingress 配置使用；它不能把 protobuf 大小冒充 Langfuse TypeScript
  `JSON.stringify(span)` 的 `eventBytes`。真实 revision 的 warning/ingestion 表现只在 §14.6 E2E 观察，项目内
  不复刻官方 internal 的 `ReadOnlySpan -> tracepb.ResourceSpans` 或 Langfuse event 转换。若 heap 测试证明该
  包络不能可靠约束进程内存，则必须改用按字节限界的自定义 SpanProcessor，不能仅扩大标准 BSP 的条数队列。
- 最终 input/output 每项最多占用 `max_content_bytes`，默认 64 KiB。该上限按最终 OTel string attribute 在
  raw OTLP span 的 JSON 表示中的 UTF-8 贡献计量，而不是只数转义前的 Go string 字节。对成功解析或聚合得到的
  JSON，禁止在 `common.Marshal` 结果上做字节切片；必须先在结构层按确定顺序缩减：优先把超长字符串叶子在
  UTF-8 边界替换为带省略字节数的字符串占位符，再从尾部丢弃完整的低优先级 array element/message/content
  block，最后丢弃低优先级 object field。顶层 object 写 `_langfuse_truncated=true` 和可得的
  `_langfuse_omitted_bytes`；顶层 array 在尾部追加只含这些 marker 的 object；顶层 scalar 超限则直接使用下述
  envelope。每轮缩减后重新 `common.Marshal` 并按 JSON string 的引号/反斜杠/控制字符转义口径计量；如果最小
  保真结构仍超限，则退化为小型合法 JSON object
  `{"_langfuse_truncated":true,"_langfuse_original_bytes":N}`。最终结构化属性必须始终能被
  `common.Unmarshal` 解析，且其转义后贡献不超过上限。对本来就无法解析为 JSON/SSE 的 raw fallback，才允许把
  它作为普通文本在 UTF-8 边界截断并追加省略标记；不得把该文本标称为结构化 JSON。这样即使正文含大量引号、
  反斜杠或控制字符，`max_queued_span_bytes` 也不会低估 Langfuse `JSON.stringify(span)` 使用的 `eventBytes`；
  测试必须覆盖最坏转义输入和截断后的 JSON 可解析性。若已知 JSON 请求因 input 前缀捕获上限而不完整，或已知
  JSON/SSE 响应因 response 捕获上限而无法恢复完整 event，不能把破碎前缀作为 raw text 导出；执行严格
  base64/data URL fallback 清理后，只导出上述合法 truncation envelope。原始 request/response 捕获缓冲仍分别
  按读取前缀和 `max_response_bytes` 限界，不能因为最终属性编码更短而继续读取更多原始字节。
- 达到原始捕获上限后停止继续捕获，但所有字节仍原样写给客户端；结构化结果再按上一条执行合法 JSON 包络。
- 所有文本截断必须保证 UTF-8 完整，并在可得时追加省略字节数标记。
- 超大 input 只处理有界前缀并标记 `input_scan_truncated=true`。若前缀结束于未闭合的 data URL/base64 字段，
  丢弃该未闭合字段的剩余前缀，绝不把半段 base64 发送出去；已知 JSON 前缀不完整时最终只导出合法 truncation
  envelope，不把清理后的破碎前缀伪装成原请求。

### 9.2 脱敏

导出前优先结构化解析 JSON，并替换以下二进制内容：

- `data:image/...;base64,...`；
- `data:audio/...;base64,...`；
- 已知的 `input_audio.data` 和等价 inline data 字段。

占位符只保留媒体类型和估算原始大小，不保留内容。SSE 尽量逐 event 解析 `data`。结构化解析失败时，再使用
范围严格限定的 data URL/base64 fallback 规则；若 content type/协议表明它原本应为 JSON/SSE 且失败原因是已知
捕获截断，则按 §9.1 输出合法 truncation envelope，只有本来就是非结构化 raw text 时才直接做 UTF-8 截断。
sanitizer 失败日志不得包含被拒绝的正文。

### 9.3 Output 聚合

`aggregate.go` 把捕获到的成功 JSON/SSE 整理成适合 Langfuse 展示的紧凑结构：

- OpenAI Chat：assistant text、reasoning text、tool calls；
- Claude Messages：text/thinking/tool-use content blocks；
- OpenAI Responses：output text 和 response events 中的 tool calls；
- Gemini：candidate text 和 function calls。

无法识别且原始捕获本来就是完整 JSON 或非结构化文本时，保留脱敏后的原始内容，不丢 trace；已知因捕获上限
而不完整的 JSON/SSE 必须按 §9.1 输出合法 truncation envelope。合法错误响应保留结构化 JSON。聚合不得重新
计算 usage，结算路径仍是唯一 usage 来源。

output 使用上述四种协议的标准化结构；input 则按 §6.2 保留脱敏后的客户端原始协议形状，不执行对称的
messages 归一化。因此同一 observation 的 input/output 表示层并不对称，Gemini/Claude input 在 Langfuse UI
中可能以通用 JSON 而非 chat 列表展示；v1 接受这一可读性限制，避免引入另一套请求协议转换和字段丢失风险。

同一份标准化 output 可同时放在 root observation 和成功 generation；trace 列表预览由 root 的
`observation.input/output` fallback 提供，不能为此再写 `langfuse.trace.input/output`。虽然仍会增加 OTLP
体积，但能同时支持 trace 列表预览和 generation 详情。失败 generation 只保留归属于本 attempt 的字节。

## 10. 配置与 Runtime 生命周期

通过 `setting/config.GlobalConfig` 注册 `langfuse_setting`：

```go
type LangfuseSetting struct {
    Enabled              bool     `json:"enabled"`
    Host                 string   `json:"host"`
    PublicKey            string   `json:"public_key"`
    SecretKey            string   `json:"secret_key"`
    Environment          string   `json:"environment"`
    SampleRate           float64  `json:"sample_rate"`
    SendContent          bool     `json:"send_content"`
    MaxContentBytes      int      `json:"max_content_bytes"`
    MaxResponseBytes     int      `json:"max_response_bytes"`
    MaxInFlightCaptureBytes int   `json:"max_in_flight_capture_bytes"`
    MaxSessionBodyBytes  int      `json:"max_session_body_bytes"`
    SessionHeaderNames   []string `json:"session_header_names"`
    SessionBodyPaths     []string `json:"session_body_paths"`
    QueueSize            int      `json:"queue_size"`
    BatchSize            int      `json:"batch_size"`
    FlushIntervalSeconds int      `json:"flush_interval_seconds"`
}
```

默认值：

| 配置 | 默认值 |
|---|---:|
| enabled | false |
| environment | `default` |
| sample_rate | 0.1 |
| send_content | false |
| max_content_bytes | 65536 |
| max_response_bytes | 524288 |
| max_in_flight_capture_bytes | 536870912 |
| max_session_body_bytes | 65536 |
| 额外 session headers | 空 |
| session body paths | 空 |
| queue_size | 64 |
| batch_size | 16 |
| flush_interval_seconds | 5 |

`langfuse_setting` 使用 options key/value 表和 `config.GlobalConfig` 反射注册，不是 GORM 列；默认值由
`var langfuseSetting = LangfuseSetting{...}` 提供。默认 size/budget 对应固定 reservation 640 KiB 和约 819 个
并发正文捕获槽；平均捕获生命周期 30 秒时约承载 27.3 sampled RPS。512 MiB 默认捕获预算是对长流式网关的
有意容量选择，用来避免 64 MiB/约 102 槽在常见 100 total RPS、10% 采样下成片退化；配置文档/API schema 和
管理 UI 必须同时展示该理由、约 552 MiB 正文常驻基线及约 582 MiB 导出阶段正文规划值，避免管理员误以为
`sample_rate` 是唯一吞吐限制，或把 512 MiB 当成整个进程的内存上限。

表中的 `sample_rate=0.1`、`send_content=false` 只用于 disabled 初始配置、旧部署缺失字段回填和 UI 草稿，不能
构成静默启用语义。专用 PUT 的 request DTO 必须用 pointer/presence-aware 字段承载 `sample_rate` 与
`send_content`；当持久化状态从 `enabled=false` 切到 `enabled=true` 时，两者必须都在本次请求中显式出现，
否则返回 400 且不持久化、不发布 runtime。管理员因此必须明确选择采样率以及是否把 prompt/response 发往
Langfuse；不得由前端自动提交未确认的 0.1/false。已启用后的普通更新仍整组提交已显示的当前值；关闭后重新
启用也再次要求确认，因为隐私和容量条件可能已经变化。`sample_rate=0` 是合法的显式选择，表示保留 enabled
配置但暂停采集，Phase 0 会在读取任何 session/body 前 no-op。

后端在持久化前校验，前端镜像相同规则：

- Host 表示 Langfuse 部署的 base URL，必须是绝对 `http` 或 `https` URL；scheme 按小写规范化，host/port
  必须存在，禁止 userinfo、query、fragment、opaque URL；`RawPath` 必须为空，`Path` 必须是合法 UTF-8 且不含
  反斜杠、控制字符或空白，避免编码斜杠和转义规范化歧义。base path 用
  `path.Clean("/" + strings.Trim(u.Path, "/"))` 规范化，根路径保存为空，其余路径保存为单个前导 `/` 且不带
  尾部 `/`。最终 traces URL 固定为
  `{scheme}://{authority}{basePath}/api/public/otel/v1/traces`：例如
  `http://langfuse:3000` -> `http://langfuse:3000/api/public/otel/v1/traces`，
  `https://x.example/langfuse/` -> `https://x.example/langfuse/api/public/otel/v1/traces`。配置若已经以完整
  `/api/public/otel/v1/traces` 结尾也按 base path 处理并再次追加，因此校验必须拒绝该歧义输入，而不是猜测
  管理员意图；
- 启用时 Public Key 和 Secret Key 均非空；
- Environment 匹配 `^[a-z0-9_-]{1,40}$` 且不能以 `langfuse` 开头；
- Sample Rate 位于 `[0,1]`；
- size、queue、batch、interval 使用保守的明确单项上下限，且 batch 不得大于 queue；
  `max_content_bytes` 范围为 4 KiB 到 4 MiB（2026-08-15 由 1 MiB 上调，见 `docs/internal/patches/2026-08-15-langfuse-local-deployment.md`），默认 64 KiB，使隐私敏感部署可把单项内容收紧到 4–16 KiB；
  `max_response_bytes` 范围为 64 KiB 到 8 MiB，默认 512 KiB；`queue_size` 范围为 16–256 spans，默认 64；
  `batch_size` 范围为 1–32 spans，默认 16。
  这些是每个字段独立可达的范围，不表示所有最大值可以同时使用；整组还必须通过下面的 tuple 校验。
  `max_in_flight_capture_bytes` 必须为正并受部署内存预算约束，且按 checked arithmetic 计算的单请求
  reservation `2 * max_content_bytes + max_response_bytes` 不得超过该值；
- 同一校验过程必须按 §9.1 用 checked arithmetic 计算
  `max_queued_span_bytes = 2 * max_content_bytes + max_response_bytes`，并同时满足
  `max_queued_span_bytes <= 9,000,000`（十进制字节）以及
  `(queue_size + 3 * batch_size) * max_queued_span_bytes <= 256 MiB`。前者为 Langfuse 默认 9,500,000 字节单
  span warning 留出 envelope 余量；后者限制 New API BSP queue、当前 batch 和编码/压缩缓冲的本地正文规划，
  不代表精确 heap 上限。该 tuple 允许每一个声明的字段上限在其他字段取较小值时单独通过，例如
  `max_response_bytes=8 MiB` 可配 `max_content_bytes=4 KiB, queue_size=16, batch_size=1`，
  `max_content_bytes=4 MiB` 也可配最小 response/queue/batch；因此 API/UI 不会展示永远不可达的上限。管理员
  提高 content/response 后可能需要降低 queue/batch 才能通过整组校验；
- Langfuse 对十进制 9,500,000 字节单 span 和 16 MiB 解压后 request 的行为都只是 warning/metric，不是
  rejection。配置不提供 `max_otlp_request_bytes`，exporter 不按 wire size 拆分 batch。生产部署必须根据
  §14.2 输出的实际 gzip 前后 batch 大小及自身反向代理语义单独设置 ingress body limit；后端不能声称一个
  与部署无关的通用 20 MiB 下限；
  `max_session_body_bytes` 范围固定为 1024 到 65536 字节；硬上限为 64 KiB，以限制无 Header session 请求的
  全量 body 读取成本；
- Header 名称必须是合法 HTTP field name，且不得配置 Authorization/Cookie 等凭证 Header；
- body path 必须是非空、合法的标量路径。管理 UI 提供明确、版本化的 path 预设列表，至少包括
  `metadata.session_id`、`metadata.conversation_id`、`conversation_id` 和 `chat_id`；选择预设只把对应精确 path
  加入 `session_body_paths`，不得启用通配符、递归扫描或未展示的 fallback。管理员也可填写自定义 gjson path。
  预设默认不勾选，保存前展示它会让无有效 Header session 的请求产生最多 64 KiB 完整 JSON 身份读取。

### 10.1 密钥与专用设置接口

`langfuse_setting.secret_key` 不一定命中现有通用 options 接口的所有敏感字段 suffix 规则，因此必须把所有 `langfuse_setting.*` 显式排除在 `GET /api/option/` 之外，避免通用读取暴露密钥或形成第二套部分配置读取契约。

在现有 RootAuth 保护的 `/api/option` 下增加：

- `GET /api/option/langfuse`：返回非密钥配置、`public_key` 和 `secret_key_configured`；
- `PUT /api/option/langfuse`：接收完整配置及 secret 的 keep/replace/clear 意图；request DTO 对
  `sample_rate`/`send_content` 保留 JSON 字段存在性，首次或重新启用时执行上文的显式选择校验。

Secret Key 绝不出现在 API 响应和日志中。空 secret 输入表示保留现有值；只有在配置禁用时才允许显式 clear。Public Key 不是秘密，但只通过专用接口返回，以保持该配置契约完整。

PUT 必须整组校验并事务保存全部 `langfuse_setting.*` option，不能由前端逐字段调用通用 UpdateOption，否则 Host/Key 的中间状态可能破坏正在工作的 exporter。后端必须像 `isPaymentComplianceOptionKey` 的现有范式一样增加 `isLangfuseOptionKey(key)`（前缀严格为 `langfuse_setting.`），并在通用 `UpdateOption` 中拒绝全部匹配项；不能只靠前端不调用，也不能只拦 secret/public key。拒绝发生在任何 option 写入和运行时发布之前，返回明确的“请使用 Langfuse 专用设置接口”错误。

### 10.2 Runtime 动态切换

OTel `TracerProvider` 是进程级 runtime。manager 用一个 `atomic.Pointer[RuntimeBinding]` 同时发布
immutable `LangfuseSnapshot` 和与其对应的 active runtime；snapshot 深拷贝 slices、不可变且包含已校验
配置及单调版本。配置与 runtime 不能使用两个独立 atomic 指针，否则切换窗口内请求可能把新配置与旧
exporter 配对：

- 启用或修改 Host/Key/queue 等配置时构建新的 candidate runtime；
- candidate 构建失败则不持久化、不影响旧 runtime；
- candidate 构建成功后事务保存完整配置，再原子发布 candidate；发布过程不做可能失败的网络操作；
- 持久化失败则关闭未发布 candidate，旧 binding 保持不变；
- 每个 runtime 包含 `retiring` 状态、in-flight lease 计数和归零通知。`TryAcquire` 必须防住“请求刚 load 旧
  binding、发布线程同时退休它”的竞态：只在 `retiring=false` 时增加计数，增加后再次检查 retiring，若已退休
  就立即归还并失败；退休流程先原子设置 `retiring=true`，再等待计数归零。这样退休一旦观察到零，之后就不会
  再有成功 lease；不得只做一次无保护的 refcount increment；
- 旧 runtime 停止接受新 Recorder，并等待所有已经取得 lease 的 Recorder 完成。lease 从 `Begin` 成功后一直
  持有到同步 metadata-only materialization 的全部 `span.End` 返回，或异步 worker 正常/panic 完成；`Finish`
  仅把作业提交给 worker 不能算 Recorder 完成，也不能提前归还 lease。捕获预算和 runtime lease 在同一个
  worker 收口 defer 中幂等释放；
- retirement 等待 5 分钟仍未归零时限频告警，记录 runtime version 和 in-flight 数，但不得在 worker 仍可能
  调用 `tracer.Start`/`span.End` 时强行 `Shutdown`。继续在后台等到真正归零后，再以 15 秒预算 shutdown；该
  预算严格大于 §11 的 10 秒单次 exporter HTTP 请求超时，使一个已在途 export 有机会在 deadline 前结束，
  同时仍由 shutdown context 截断后续 drain/retry。这以可能暂时保留旧 runtime 资源换取不与 worker 竞争和
  不静默丢 telemetry。进程退出的总预算耗尽时，对仍有 lease 的 runtime 记录告警并交给进程退出回收，不能
  为了形式上的 shutdown 与活跃 worker 并发关闭 provider；
- 禁用时原子发布包含 disabled snapshot 和 nil runtime 的 binding，并按相同 lease 规则 drain 旧 runtime；
- 请求开始执行 `binding := manager.LoadBinding()`，从同一不可变 binding 取得 snapshot/runtime，完成采样后
  尝试 acquire。正常路径只 load 一次；仅当已命中采样但第一次 `TryAcquire` 因 binding 恰好退休而失败时，
  允许再 load 当前 binding 一次，用新 snapshot 重新执行全部 binding-dependent 配置/采样判定后重试，仍失败
  则 no-op。这个有界 retry 只解决 atomic load 与退休交错；可复用已校验 raw session/request ID，但不允许混用
  旧 snapshot 与新 runtime。请求侧不得读取 `GlobalConfig` 注册结构体、无限重载 atomic 指针、计算字段指纹、
  执行配置调和或触发网络/磁盘操作。版本只用于诊断；不同配置由发布点完成切换，不能把调和和惊群带入 relay
  热路径。
- 跨实例同步过程中若暂时读到不完整配置，不替换当前有效 binding；只在得到完整有效快照后切换。

relay 热路径只读取上述 atomic binding，不直接读取 `GlobalConfig` 注册的可变结构体。现有
`updateOptionMap` 会对每个 key 单独加锁，并在 `OptionMapRWMutex` 持锁期间立即调用
`handleConfigUpdate` 和部分配置的后处理。把它改成全局 batch apply + 锁外 post-hook 会影响每一种 option，
其失败模式是与 Langfuse 无关的配置漂移，因此不作为本功能的前置改造，也不在本功能 PR 中迁移
`performance_setting`、`billing_setting` 或其他 option 的行为。若后续实施这项通用重构，必须放在没有
任何 Langfuse 代码的独立提交/PR 中，并用原有 options 回归单独证明 post-hook 次数和配置结果不变。

本功能改用局部的整组 reconcile/publish 路径：

1. `langfuse_setting` 的 `GlobalConfig` 注册对象只服务于 options 持久化兼容和默认值导出；relay、专用
   GET 和 runtime manager 均不得读取该逐 key 更新的可变对象。现有 `updateOptionMap` 对
   `langfuse_setting.*` 只更新 `OptionMap`/注册对象，不触发 runtime 构建或 snapshot 发布。
2. 控制面包 `service/langfuseconfig` 增加专用配置互斥量和 `reconcileLangfuseOptions`。它在
   `InitOptionMap` 完成后先同步执行一次，再以 `common.SyncFrequency` 对齐的独立低频任务执行；该包直接通过
   `model` 查询完整的 `langfuse_setting.*` 持久化集合，也不给通用 options 增加 hook。每次在互斥量内以
   默认配置为基底构造独立 candidate，执行整组校验并与数据面当前 immutable snapshot 做等值比较。相同配置
   no-op；缺字段导致的无效启用配置或整体验证失败只限频告警并保留旧 binding。
3. 配置有效且不同后，`service/langfuseconfig` 调用 `service/langfuse` 的窄 API 构建 candidate runtime；
   成功后再通过一次 atomic store 发布包含 candidate snapshot 和 runtime 的新 binding，失败则关闭 candidate
   并保留旧版本。该 reconcile 只在控制面配置任务执行，不由请求触发；数据面包自身不查询数据库。
4. 专用 PUT 在 `service/langfuseconfig` 持有同一互斥量，先基于完整请求和 keep/replace/clear 后的 secret
   构造、校验配置并预构建 candidate runtime，再通过 `model` 事务保存全部 `langfuse_setting.*`。提交成功后
   可沿用现有逐 key 内存 apply 来保持 `OptionMap`/注册对象兼容，但只能在全部 apply 完成后发布预构建的
   candidate 一次；持久化失败则关闭 candidate，旧 binding 保持不变。`service/langfuse` 不得为实现 PUT 或
   reconcile 导入 `model`。
5. 互斥量覆盖 reconcile 的完整数据库查询到发布，以及专用 PUT 的事务到发布，避免本实例中一个较早的
   轮询结果在较新的 PUT 之后反向发布。snapshot 内含单调版本用于诊断；跨实例最终以数据库完整配置为
   准，各实例的周期 reconcile 只会读到事务提交前或提交后的完整集合，不会发布逐 key 的中间 runtime。

选择这条局部路径的原因是：本功能只需要保证 Langfuse 的 Host/Key/queue 等字段整组发布，并不需要改变
所有 options 的 apply 语义。从完整持久化集合重建 candidate，且只在整体验证及 runtime 构建成功后发布，
已经阻止请求看到“新 Host + 旧 Key”等中间 runtime；等值比较还避免一次全量同步重复重建 exporter。

进程退出时先原子禁止新 telemetry 并把 active/retired runtimes 标为 retiring，在共享 15 秒总预算内等待
materialization lease 归零并 shutdown 已归零的 provider，再继续现有 server/cache shutdown。每个 provider
不得另起一份可突破总 deadline 的 15 秒计时器，全部操作复用该绝对 deadline；shutdown context 会取消正在
等待/重试的 exporter。15 秒严格大于 §11 的 10 秒单次 HTTP 请求超时，避免无竞争的单个在途 batch 必然落入
降级分支，但不承诺清空任意长度的队列。预算耗尽时跳过仍被 worker 引用的 provider shutdown 并记录 in-flight
数；Langfuse drain/shutdown 错误只记录日志，不得延长退出预算或阻塞 relay shutdown。

## 11. 导出可靠性

使用 `sdktrace.BatchSpanProcessor` 和 `otlptracehttp`。runtime 构建时必须从 §10 已规范化的 Host 显式构造
exporter 目标，不能把完整 URL 传给只接受 authority 的 `WithEndpoint`，也不能依赖 OTLP 环境变量补全
scheme/path：

1. `authority = parsedURL.Host`，包含显式端口和 IPv6 brackets，但不含 scheme/path；
2. `urlPath = path.Join(parsedURL.Path, "/api/public/otel/v1/traces")`，强制保留单个前导 `/`；
3. 用 `url.URL{Scheme: parsedURL.Scheme, Host: authority, Path: urlPath}.String()` 得到 `finalURL`，禁止手工拼接
   URL。scheme 为 `http` 对应 insecure HTTP，`https` 对应 TLS；不能默认按 https 处理一个 http Host；
4. 把完整 `finalURL` 传给锁定版本提供的 `otlptracehttp.WithEndpointURL(finalURL)`。该 option 一次性覆盖 SDK
   从环境变量得到的 scheme/insecure、authority 和 path，避免 `https` Host 继承进程中
   `OTEL_EXPORTER_OTLP_ENDPOINT=http://...` 的 insecure 状态；
5. 同时显式设置 gzip、10 秒 timeout 和 retry options；Basic header 固定为
   `Authorization: Basic base64.StdEncoding.EncodeToString([]byte(publicKey + ":" + secretKey))`。header 只从
   不可变 secret 快照构造，不能进入日志。所有 endpoint options 都由已验证快照提供，使进程环境中的
   `OTEL_EXPORTER_OTLP*_ENDPOINT` 不能覆盖管理员配置。

如果后续锁定的 OTel 版本移除 `WithEndpointURL`，语义等价的替代才是
`WithEndpoint(authority) + WithURLPath(urlPath)`，并分别显式设置 secure/insecure；仅在 `http` 时调用
`WithInsecure()`、却不覆盖 `https` 的 secure 状态并不等价。endpoint helper 必须返回并测试上述三个组成值和
`finalURL`，集成测试还要实际观察请求 URL；不得只靠 SDK 对无效 URL 的静默 fallback。

其余导出配置：

- OTLP/HTTP protobuf；
- gzip；
- Basic `publicKey:secretKey`；
- 配置化的有界 queue 和 batch，默认分别为 64/16，并受 §10 的 9,000,000 字节单 span 包络和 256 MiB
  本地 queue/batch 正文规划联动护栏约束；不实现按 OTLP wire size 拆分。官方 exporter 的
  `ReadOnlySpan -> ResourceSpans` 转换位于 Go `internal/tracetransform`，公开 API 不提供编码后大小；为一个不拒绝
  数据的 Langfuse request warning 复刻该转换会形成第二套 OTel 编码实现，与 §3 的选型冲突；
- exporter 对瞬时错误的标准重试；
- TracerProvider 必须显式使用 `sdktrace.WithSampler(sdktrace.AlwaysSample())`。采样已在 Langfuse `Begin`
  之前完成；SDK 不能再次按默认 sampler 丢弃已选中的 trace，否则会破坏 session/request 级采样契约；
- TracerProvider 必须显式锁定 span limits，不能使用会从进程环境读取限制的默认
  `sdktrace.NewSpanLimits()` 结果。锁定的 OTel v1.44（实现期从 v1.34 上调，见下）使用能原样保留负数/零语义的
  `sdktrace.WithRawSpanLimits`（该版本已将 `WithSpanLimits` 标为 deprecated），配置固定为：

  ```go
  const langfuseSpanAttributeCountLimit = 64

  sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
      AttributeValueLengthLimit:   -1,
      AttributeCountLimit:         langfuseSpanAttributeCountLimit,
      EventCountLimit:             0,
      LinkCountLimit:              0,
      AttributePerEventCountLimit: 0,
      AttributePerLinkCountLimit:  0,
  })
  ```

  `AttributeValueLengthLimit=-1` 禁止 SDK 二次截断，确保 §9.1 结构化缩减后的 metadata/model parameters/usage/cost
  仍是完整合法 JSON；固定 64 个 span attributes 足以覆盖本文闭合 schema并给后续兼容字段留余量，生产属性
  builder 必须测试其最大属性数不超过该值。v1 不生成 span event/link，因此对应 limit 明确设为 0。所有字段
  都由字面量确定，`OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT`、`OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT`、
  `OTEL_ATTRIBUTE_COUNT_LIMIT`、`OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT` 及 event/link limit 环境变量均不得影响该
  专用 provider；
- Langfuse ingestion 的 HTTP 429（含 `rateLimitResource: "ingestion"`）按瞬时限流处理：优先遵守
  `Retry-After`，否则使用带随机抖动的指数退避并设置上限；退避发生在 exporter worker 内，不能阻塞
  relay。达到重试预算后丢弃并限频记录，不能在请求路径同步重试。
- Langfuse 在 `auth.scope.isIngestionSuspended`（用量阈值超限）时返回 HTTP 403。锁定的官方 exporter 已把
  403 归为 non-retryable；专用 `RoundTripper` 只观察目标 endpoint 的 status code 并把 403 归类为持久
  `ingestion_forbidden` 状态，当前 batch 直接失败且不重试，并按 runtime version 至多每 30 秒输出一条限频
  warning。官方 exporter 构造的 error 会包含响应 body，因此 transport 观察到 403 后必须先关闭原始 body，
  再把交给 exporter 的 `resp.Body`/`ContentLength` 替换为固定、小型、不含上游内容的 `ingestion forbidden`；
  Langfuse 代理 error handler 对所有 exporter error 也只按稳定分类记录固定摘要，不直接输出 `err.Error()`。
  warning 只包含状态码、runtime version 和失败 span 计数，不回显原始响应 body、URL query、Key、正文或 session。
  后续 batch 仍可按正常 BSP 流程尝试，以便管理员解除限额后自动恢复，但每个 403 batch 自身绝不退避重试。
- 独立 HTTP client/transport，单次请求超时 10 秒；
- Resource attributes 包含 `service.name`、`service.version` 和 deployment environment。

OTel 版本锁定从 v1.34.0 上调到 v1.44.0：上文的专用 `RoundTripper` 需要把自定义 `http.Client` 交给官方
exporter，而 `otlptracehttp.WithHTTPClient` 自 v1.36.0 才存在；v1.34 只能通过 `WithTLSClientConfig`/`WithProxy`
间接改内部 transport，无法观察响应状态码，也就无法在 exporter 读到响应前关闭并替换 403 的原始 body。
v1.44 仍提供 `WithEndpointURL`、`WithRetry(RetryConfig{Enabled,InitialInterval,MaxInterval,MaxElapsedTime})`
和 `WithRawSpanLimits`，本节其余锁定值不变。

OTel SDK 使用进程级 error handler。标准 `BatchSpanProcessor` 只有 span 条数队列，没有正文的字节维度
兜底；第一版依靠 §9.1/§10 的较小默认 queue、content 上限和联动校验控制风险，不把 512 MiB capture
budget 误认为 BSP 或整个进程的内存上限。若边界 protobuf/heap 测试证明这些护栏不足，必须引入按字节限界的
processor 作为独立设计变更。启动时只安装一次代理 handler：Langfuse 相关导出错误经过凭证清理和限频，非 Langfuse OTel 错误继续交给原 handler。

用一个只负责错误加标签和导出计数的 `sdktrace.SpanExporter` 装饰器包装官方 OTLP exporter，使全局
handler 能可靠识别 Langfuse 导出错误；装饰器不改变编码、队列、batch 边界、重试或传输行为，也不导入
`internal/tracetransform` 或自行拆分请求。每次 `ExportSpans` 入口把 `len(spans)` 累加到
`exporter_received_spans_total`；官方 exporter 在内部重试后返回 nil 时累加 `exported_spans_total`，最终返回
error 时累加 `export_failed_spans_total` 并交给限频 error handler。三者都是按 span 条数计，不能把 batch 数
冒充 span 数。

标准 `BatchSpanProcessor` 在队列满时无阻塞丢弃新结束的 span，当前采用的 Go SDK 公开 API 没有逐条 drop
callback，但不能让该缺失完全不可诊断。每个 runtime 维护以下低成本原子计数：每个 span 的 `span.End` 返回后立即累加
`materialized_spans_total`，decorator 维护上述 received/succeeded/failed 数。任意时刻
`materialized - exporter_received` 是“尚在 BSP queue/batch 中 + BSP 已丢弃”的总和；未交给 exporter 的正常
在途量至多为 `queue_size + batch_size`，因此
`max(0, materialized - exporter_received - queue_size - batch_size)` 是 BSP 累计丢弃的严格下界。runtime
以观察过的最大值维护单调 `queue_dropped_spans_lower_bound`；该值增长或差值持续超过 75% queue 容量时，按
runtime version 每 30 秒至多输出一条 warning，包含四类计数、queue/batch 配置和采样率，不包含正文、session
或凭证。runtime retirement 在拒绝新 lease、所有 materialization lease 归还并执行 BSP shutdown/drain 后，
若 shutdown/drain 在预算内成功，`materialized - exporter_received` 不再包含正常在途 span，此时它就是该
runtime 的精确 BSP queue 丢弃数，必须输出一次摘要并更新最终 metric。drain 超时或失败时只能保留 lower-bound
并把摘要标记为 incomplete，不能把仍在途 span 误报为 drop。网络/429 重试耗尽导致的
`export_failed_spans_total` 独立呈现，不能算成 queue drop。

这些计数不改变无阻塞策略，也不承诺实时精确的 active-runtime drop 数；它们至少能区分“未命中采样”与
“已 materialize 后在 BSP/导出层丢失”。若后续需要逐条实时精确 drop metric 或按字节 admission，再单独评估
自定义 SpanProcessor；第一版不为此复刻 BSP。

relay 热路径不等待导出、不做同步重试，也不因 Langfuse 改变 HTTP status 或响应正文。

`sample_rate` 的 disabled 初始化值为 0.1，但首次/重新启用时管理 UI 必须阻止提交，直到管理员显式选择
采样率和 Send Content；不得把初始化值当成已确认选择。UI 必须明确提示采样会丢弃未命中的 trace，并允许管理员
按吞吐和 Langfuse ingestion 配额调整。UI 还必须根据当前 size/budget 配置实时显示
`reservation = 2 * max_content_bytes + max_response_bytes`、`capture_slots = floor(budget/reservation)`，并提供
“可支撑 sampled RPS ≈ slots / 平均捕获生命周期秒数”的换算说明；默认配置显示约 640 KiB/请求、819 个槽位，
平均 30 秒时约 27.3 sampled RPS，并展示约 552 MiB/582 MiB 的正文常驻/导出阶段规划值。该数值是估算，不得
暗示预算会按实际响应增量计费，也不能替代部署的整进程内存规划。只有显式原始 session 成功提取且存在正数 New API 用户 ID 时，采样单位才是用户作用域
session，同一用户的同一 session 请求会一起保留或一起丢弃；不同用户的相同原始值互不影响。如果 body
storage 缺失、超过 64 KiB、大小未知、读取不完整或 JSON 非法，body path 提取会跳过；没有正数用户 ID 时也
无法建立用户作用域。两种情况都回落到 request ID 采样，同一原始 session 中这些请求不保证一起保留或一起
丢弃；无 session 时也按 request 采样。即使管理员把采样率调到 1.0，也仍受全局捕获预算、队列和 429 退避约束。

## 12. 前端

在 System Settings > Operations/Integrations 中新增 Langfuse section，复用现有 settings form 组件。

控件包括：

- 启用开关；首次或重新启用时进入确认步骤，必须显式选择 Sample Rate 和 Send Content 后才能保存；
- Host；
- Public Key；
- 带“已配置/未配置”状态的 Secret Key 密码输入框；
- Environment；
- Sample Rate；
- Send Content 开关；
- input/output 内容上限；
- response 捕获上限；
- 全局捕获预算，以及按当前内容/响应上限计算的单请求 reservation、并发 capture slots 和吞吐换算提示；
- session body path 完整 JSON 读取上限；配置 body path 时提示无 Header session 请求会产生最多 64 KiB 的全量读取成本；
- 额外 session Header 名称；
- 显式 session JSON path，以及 `metadata.session_id`、`metadata.conversation_id`、`conversation_id`、`chat_id`
  预设；预设只填充精确 path，默认均不选，不做全 body 扫描；
- 折叠的高级 queue、batch、flush 配置。

session 配置附近必须说明：客户端值会按 New API 用户作用域导出为 `{userId}:{rawSessionId}`，相同 raw 值只在
同一用户内聚合；没有正数用户 ID 时省略 session。UI 不展示或回传请求中采集到的 raw session。

关闭正文采集后，user/session、模型、耗时、usage、cost 和错误信息仍保留。Send Content 的 disabled 草稿
初值为关闭，但启用向导不得预先替管理员确认；附近必须明确提示：启用后 prompt 和模型响应会发送至配置的
外部 Langfuse 实例。采样率同样要求显式确认，允许管理员选择 0 作为“保留配置、暂停采集”。

所有可见文案通过 `useTranslation()`，覆盖 `en`、`zh`、`zh-TW`、`fr`、`ja`、`ru`、`vi`。当前仓库提供
`.agents/skills/i18n-translate/SKILL.md`，实现阶段在任何 locale 修改前必须加载并遵守它；同时把可执行步骤写死，
避免只依赖 agent 能否发现 skill：先确认 UI 的 `t('English key')` 调用点，从 `web/` 运行
`bun run i18n:sync` 并读取报告；所有七种语言的新增/修订值只通过 skill 规定的
`web/scripts/add-missing-keys.mjs` 一次性写入，禁止直接编辑 locale JSON；再运行缺 key 检查和
`bun run i18n:sync`，删除临时脚本，最后执行 typecheck/build。若其他执行环境没有 skill loader，也必须按这组
仓库内步骤完成，不能跳过逐语言翻译或直接手改 JSON。

## 13. 错误与边界场景

- Langfuse 不可用：relay 正常；exporter 重试瞬时错误，最终按有界队列策略丢弃。
- Langfuse ingestion 返回 429：读取 `Retry-After` 或执行有抖动的指数退避；重试耗尽后丢弃并限频，
  不回压 relay、不在请求 goroutine 等待。
- Langfuse ingestion 因用量阈值暂停而返回 403：视为持久、non-retryable 的
  `ingestion_forbidden`；当前 batch 立即失败且不重试，按 runtime version 限频告警，后续 batch 可继续尝试以
  自动恢复。transport 在官方 exporter 读取前关闭并替换原始响应 body，代理 error handler 也只记录固定脱敏
  摘要；告警不得包含原始响应 body、正文、session 或凭证，relay 不受影响。
- Queue 满：标准 batch processor 继续无阻塞丢弃；active runtime 通过 materialized/received 差值暴露单调
  `queue_dropped_spans_lower_bound` 和持续压力 warning；retirement drain 成功后输出该 runtime 的精确 queue
  drop 总数，drain 失败则输出标记 incomplete 的 lower-bound。export 最终失败单独计数，不能与 queue drop
  混淆。
- 凭证无效或过期：隐藏 Key 后限频记录，relay 不受影响。
- 上游错误：generation status message 使用 `MaskSensitiveErrorWithStatusCode()` 的限长结果，不直接导出原始 provider 错误或凭证。
- 客户端断开或流中断：active generation 和 root 标为失败，保留已捕获的部分 output。
- 重试后最终失败：每个经过共享 `doRequest` 的已调用渠道都有失败 generation；root output 包含客户端最终错误
  JSON。AWS SDK/Xunfei v1 旁路按下述已知限制只保留 root。
- 未选到渠道：只有失败 root。
- 缺少 session：省略 `session.id`，绝不推测。
- 显式原始 session 无效：省略并写 `session_omitted_reason`，metadata 不包含被拒绝的原值。
- 显式原始 session 合法但用户 ID 非正数：省略 `session.id` 并写
  `session_omitted_reason=user_scope_unavailable`；不能把 raw session 直接放入 Langfuse project 全局命名空间。
- 两个不同用户传相同 raw session：分别导出 `{userId}:{rawSessionId}`，不得在 Langfuse 合并或使用相同采样键。
- 缺少上游 usage：省略不可用字段，Recorder 不制造 token 数。
- Recorder 非 nil 但结算时没有 active attempt：丢弃 usage/cost，写 `usage_unattributed=true` 和
  `usage_omitted_reason=no_active_attempt`，不得把数据落到 root；该 record 不参与 settled 候选选择，root 同时
  省略 `quota`/`billing_source`。该降级用于 AWS SDK/Xunfei v1 已知旁路、其他 hook 漏接或生命周期异常。
- 同一 handler 在前一 attempt 尚 active 时再次进入共享 outbound 边界：第二次 Begin 以当前时间/offset 收口
  前一个并创建新 attempt，写 `attempt_end_reason=replaced_by_next_upstream_call` 和
  `cost_omitted_reason=attempt_superseded`；前一个 generation 不写 usage/cost，设置 OTel Error status，只有在
  该 Error 与无 usage 条件同时满足时才可写 model。只把之后的 usage/cost 归属最新 active attempt，不能静默
  覆盖或把两次调用合并。
- AWS Bedrock 非 API Key SDK 路径和 Xunfei 不经过共享 `doRequest`：v1 明确允许它们产生 root-only trace，
  不生成 generation，结算 usage/cost 以 `no_active_attempt` 丢弃，root 也省略 `quota`/`billing_source`；不能把
  数据错误归到 root 或伪造 attempt。AWS API Key 路径仍正常生成 generation。三个 AWS SDK 调用点和 Xunfei
  `c/info` 私有签名链留给后续独立 PR。
  Coze 私有轮询请求不单独建 attempt，创建调用的 attempt 覆盖轮询期。
- 非 Gemini 入站格式即使被 Gemini 渠道映射为 embedding/Imagen model，也不执行 Gemini 分类复核并按原始
  非 Gemini 格式处理；这是已知行为，不属于本 PR 的路由修复范围。
- 免费、有 billable usage 且结算成功：保留 usage，cost details 写 `total: 0`，metadata 写 `quota: 0`。
  文本没有 billable usage 时即使 billing session 成功收口也省略 cost，并写
  `cost_omitted_reason=no_billable_usage`，不写 `settlement_error=true`。
- 捕获达到上限：客户端响应不受影响，`capture_state` 保持当前生命周期状态，并通过
  `content_truncated`/`input_scan_truncated`/`output_truncated` 保留具体截断标记；结构化 input/output 必须按
  §9.1 缩减或退化成合法 JSON envelope，不能导出被腰斩的 JSON。
- 全局捕获预算耗尽：该请求按 metadata-only 处理并写 `capture_state=budget`，只保留不含正文的 metadata
  并原样透传；预算释放后也不在同一请求中重新启用采集。
- `send_content=false`：写 `capture_state=disabled`，不预留、不包装、不读取内容采集 input。
- worker admission 失败：写 `capture_state=admission` 后用已快照的业务时间结束 metadata-only spans。
- capture/sanitizer/aggregator 内部失败：写 `capture_state=panic`，省略不可信正文并继续 relay。
- relay 业务代码自身发生 panic：统一 finalizer 会在栈展开时先执行 `Finish`，按 writer 身份规则有条件恢复并
  冻结截至 panic 前已经写出的字节；随后外层 Gin Recovery 才写 500。这个 Recovery 500 发生在冻结之后，明确不进入 root
  output，因此“root output 与客户端实际收到内容一致”的保证不覆盖该异常逃逸窗口。finalizer 不得吞掉或提前
  recover 业务 panic，也不得为了捕获 Recovery 正文改变 Gin middleware 顺序；metadata 写
  `attempt_end_reason=lifecycle_panic`（存在 active attempt 时）及 root panic/error 标记，使例外可诊断。
- BSP 队列压力：所有 span 创建并设置属性后必须先 End root、再 End generation，使 root 先进入队列。
  队列在同一 trace materialization 期间耗尽时，允许丢失一个或多个后续 attempt，但优先保留承载
  `user.id`、`session.id`、input/output 的 root；不得恢复成 generation 先入队并接受 shallow trace 作为常态。
  materialized/received/exported/failed 计数和 drop lower-bound 必须让该降级可诊断；exporter 仍可能因网络
  批次最终失败丢弃整批，并由独立 failed 计数呈现。generation 使用固定 trace-update denylist，禁止以下属性：
  `langfuse.trace.name`、`langfuse.trace.input`、`langfuse.trace.output`、`langfuse.trace.metadata`、`user.id`、
  `session.id`、`langfuse.trace.public`、`langfuse.trace.tags`、`langfuse.user.id`、`langfuse.session.id`、
  `langfuse.observation.metadata.langfuse_user_id`、
  `langfuse.observation.metadata.langfuse_session_id`、`langfuse.observation.metadata.langfuse_tags`、
  `langfuse.trace.metadata.langfuse_session_id`、`langfuse.trace.metadata.langfuse_user_id`、
  `langfuse.trace.metadata.langfuse_tags`、`ai.telemetry.metadata.sessionId`、`ai.telemetry.metadata.userId`、
  `ai.telemetry.metadata.tags` 和 `tag.tags`；此外禁止任何以 `langfuse.trace.metadata` 开头的 key。这个清单逐项
  对齐固定 Langfuse revision 的 `hasTraceUpdates`，不能缩写成 `langfuse.trace.*`，因为 `user.id`/`session.id`
  也会触发 trace update。它们只在 root 按本文映射写入，generation 契约测试必须显式断言不存在
  `user.id` 和 `session.id`，避免每个 attempt 重复生成 trace update 并放大 ingestion 压力。
- runtime 切换发生在 worker 排队或执行期间：旧 runtime 保持可用直到作业完成并归还 lease；退休超时只告警，
  不得强行 shutdown 后让 worker 在 non-recording provider 上静默 materialize。
- 多层 writer：嵌入并完整委托 `gin.ResponseWriter`，保持 Flush、Hijack、CloseNotify、status 和 size 等行为。除此之外，capture writer 必须显式实现 `Unwrap() http.ResponseWriter { return w.ResponseWriter }`；不能只照搬 `middleware/audit.go` 的嵌入模式，因为 `gin.ResponseWriter` 接口本身没有声明 `Unwrap`。`relay/helper/stream_scanner.go` 会用 `http.NewResponseController(c.Writer).SetWriteDeadline(...)` 延长流式写 deadline，缺少 `Unwrap` 会让 controller 找不到底层连接并静默返回 `http.ErrNotSupported`，从而使流式超时保护失效。`Finish` 只在 `c.Writer` 仍是本次 capture writer 时恢复原始 writer；若后续 wrapper 已替换它则保留现状并限频记录 `writer_replaced_before_finish`，不得无条件回写。

## 14. 测试设计

新增或大幅修改的 Go 测试使用 `testify/require` 做 setup/fatal assertion，使用 `testify/assert` 比较值。

### 14.1 单元与契约测试

| 模块 | 保护的契约 |
|---|---|
| identity | Header 优先级、`X-Session-Id` 默认不读取但显式配置后可用、body 仅标量、非法/超长值拒绝、body size 上限内完整提取、超限跳过 body path 并保留 Header、`{userId}:{rawSessionId}` 作用域格式、不同用户相同 raw session 不碰撞且采样键不同、非正用户 ID 省略并标记 `user_scope_unavailable`、无启发式 fallback、metadata 不复制 raw session、`session_omitted_reason`/`session_body_omitted_reason` 逐项覆盖且不存在枚举外值 |
| writer | 客户端原样透传、有界共享缓冲、ResponseWriter 行为委托、`Unwrap`、writer 级 mutex 串行化所有写入、并发写下 offset 单调且字节归属正确、Finish 冻结后状态不再变化，attempt 数增加不复制响应字节；`c.Writer` 仍是本次 capture writer 时恢复原始 writer，已被另一 wrapper 替换时保留现状并限频记录 `writer_replaced_before_finish` |
| content | 结构化 base64 替换；完整 JSON 在 message/content/field 边界缩减并重新 marshal；不完整 JSON 前缀退化为合法 truncation envelope；最终 JSON 始终可解析且转义后不超过上限；raw text fallback 保证 UTF-8；正文不进入错误日志 |
| aggregate | 四种格式各自的流式/非流式真实 fixture，以及无法识别时保留 raw |
| sampling | Phase 0 的 disabled、`sample_rate<=0`、不支持/Gemini 排除均不读取 Header/body；`sample_rate=0` 即使配置 body path 也不访问 `BodyStorage`/gjson；RequestId 只断言 `GenRelayInfo` 正常产物非空，不为不可达空值分支建立功能测试；通过 Phase 0 后，Header 缺失且配置 body path 的未采样请求仍执行一次有界完整身份读取；固定 SHA-256 前 8 字节 big-endian bucket 与精确 `floor(rate*2^64)` 阈值向量，覆盖 rate 1/边界前后，rate 0 由 Phase 0 守卫覆盖；仅在 raw session 成功提取且用户 ID 为正时，同一用户作用域 session 的所有 request 判定一致；不同用户相同 raw session 使用不同采样键；body storage 缺失/超限/无效或用户 scope 不可用时回落到 request ID；无 session 时 request ID 判定稳定；未采样不申请 lease/捕获预算、不包装 writer、不读取内容采集 input、不分配 attempt；retiring retry 用新 snapshot 重跑含 rate 0 的 Phase 0、重查 Header/path/采样、复用已读 body 缓冲，且单次 `Begin` 的完整身份读取总计至多一次 |
| recorder | `ContextKeyLangfuseRecorder`/`FromContext` 对不存在、类型错误和 typed nil 都安全返回 nil；root/generation 父子关系、worker 内先创建 root/全部 generation 但 End root 后再 End generation、共享响应缓冲与固定 reservation、所有 generation 复用同一 input Go value且有引用一致性断言、`send_content=false` 不预留/不包装/不读内容 input、`GetBodyStorage` 等调用前失败只有 root；`BeginAttempt` 只在私有 `doRequest` 的 `relayClient.Do` 前调用一次，三个普通 wrapper 不调用且一次真实请求只产生一个 attempt；controller retry loop 在每次 handler 返回后、成功 return/错误处理/下一轮之前调用 `EndAttempt`；`DoTaskApiRequest` 有 nil Recorder 时 no-op，`DoWssRequest` 明确不经过 hook；AWS SDK/Xunfei v1 产生 root-only 并以 `no_active_attempt` 丢弃 usage/cost；重复 Begin 把前一 attempt 收口为 Error superseded 并只保留最新 active、`chatCompletionsViaResponses` 二次 relay 路径、无 active attempt 的 usage 被丢弃并写降级标记、channel ID/type/model 使用 nil-safe accessor、channel name 只从 `ContextKeyChannelName` 快照且三层 fallback 稳定、最终 Gemini 分类为 embedding/Imagen 时不 materialize 且预算释放、部分流失败、所有 `input_omitted_reason`/`attempt_end_reason` 均在闭合集合内、`capture_state` 五种状态、Finish 幂等、生命周期 panic 隔离、异步 worker 不引用请求对象；TraceID 使用 SHA-256 前 16 字节并覆盖全零摘要替换为末字节 `0x01` |
| usage/cost | 文本按 fold 后的 `InputExcludesCache`（含 channel declared includes/excludes、legacy Claude-derived 和 OpenRouter Claude）生成互斥桶，并按 summary 复用 image/audio 数值；正 audio prompt token 始终拆出独立桶；`Kind=text` 即使 `OutputAudioTokens>0` 也不拆 output audio、该 token 留在 `output`，只有 `Kind=audio` 导出 `output_audio_tokens`；audio 结算路径导出 text/audio 桶，cache-write 重叠 clamp 并标记，`total` 精确等于非 total 桶和、负源值/溢出/未知语义整组省略、只归属成功 attempt；root 仅在恰有一个非负 quota 的 `Settled=true` attempt 时从同一 `UsageRecord` 成对复制 `quota`/`billing_source`，零个/多个候选、结算失败、`no_active_attempt` 和非法 quota 均同时省略；`input_cache_creation`/`output_audio_tokens` 对齐内建口径，而 `input_image_tokens`/`input_audio_tokens` 明确作为自定义 usage type 原样保留；文本无 billable usage 时即使 Settle 成功也省略 cost、quota 与可变 `QuotaPerUnit` 同点快照后换 USD、worker 前修改全局汇率不改变结果、订阅 billing source、免费为 0、`Quota=1/QuotaPerUnit=500000` 与零 cost 的 JSON number 边界、合法科学计数法不被拒绝、成功 attempt 结算失败时省略所有 model fallback 属性、失败/superseded attempt 仅在无 usage 且 Error 时允许 model、`Error + usage + no cost` 必须省略 model、所有 cost/usage omission reason 在闭合集合内、禁止负成本；tiered 的四参数前置改动由独立计费回归保护，不在本组测试重算 quota |
| attribute encoding | metadata/model parameters/usage/cost 以及结构化 input/output 都是可解析的 JSON string，completion start 是 RFC3339Nano；Go 测试断言 root 发出 observation input/output/metadata、不发 `langfuse.trace.name/input/output/metadata`，显式带 `langfuse.internal.as_root="true"`；generation 不含 §13 denylist 的任何 trace-update 属性，并显式断言不存在 `user.id`/`session.id`；root span name 到 trace name 的 fallback、正文/metadata merge 由 §14.6 E2E 验证 |
| settings | Host scheme/authority/base-path 规范化和 endpoint 拼接表（含 `http://langfuse:3000`、默认/显式端口、IPv6、`https://x/langfuse/`，以及 userinfo/query/fragment/完整 traces path/任意非空 RawPath/空白或反斜杠 path 负例）；`max_content_bytes` 4 KiB 下限/64 KiB 默认、512 MiB 默认 capture budget、checked capture reservation、单 span 9,000,000 字节包络、256 MiB queue/batch 正文规划，并证明每个字段上限存在合法 tuple；disabled -> enabled/重新启用缺任一 presence-aware `sample_rate`/`send_content` 均拒绝且不发布，显式 rate 0 可启用；session path 预设只写精确路径且默认不选；secret keep/replace/clear、整组原子持久化、敏感字段不进入通用 options、所有 `langfuse_setting.*` 被通用 PUT 拒绝 |
| package boundaries | AST/import 按完整 module path 固定 §4.1 的 New API 直接依赖白名单，并固定 controller 对 `Begin`/retry-loop `EndAttempt`/finalizer `Finish` 的调用职责；显式允许 `relaykit/dto`/`relaykit/types` 并拒绝根 `dto`/`types` 的直接 import；`go list -deps ./service/langfuse` 的闭包不含根 `service`、`model`、`controller`、`service/langfuseconfig` 或 `relay/channel/**`，但允许列明的 `relay/common` 既有传递依赖；任何 `relay/**` 包的闭包不含 `service/langfuseconfig` |
| runtime | 防退休竞态的 `TryAcquire`、worker 完成前持续持有 lease、安全 swap/drain、退休超时不与活跃 worker 并发 shutdown、15 秒共享退出 deadline 大于 10 秒单请求超时且所有 provider 共用绝对 deadline、candidate 失败保留旧 runtime、完整持久化集合 reconcile、相同配置 no-op、专用 PUT 与轮询不会反向发布旧配置、materialized/received/exported/failed 与 queue drop lower-bound/退休精确值；专用 provider 的 sampler 和 raw span limits 覆盖进程 OTel 环境变量 |

`runtime` 测试还必须用并发 fixture 验证：请求通常只做一次 atomic binding load，从同一 binding 取得
snapshot/runtime 并成功 `TryAcquire`；恰逢旧 runtime 退休导致 acquire 失败时只允许再 load 当前 binding 一次，
并按新 snapshot 重新做配置/采样判定，绝不混用旧 size/SampleRate 与新 runtime，且不读取可变注册结构体；
专用 PUT 和周期 reconcile 各只发布一次；相同完整配置不重建 runtime；数百个并发
请求不会各自触发重建或调和。用 barrier 让 worker 已 admission 但尚未 materialize，再切换配置并推进退休：
断言 `Finish` 返回后旧 runtime in-flight 仍非零、不会 shutdown，worker 在旧 provider 上创建 recording span，
作业完成归还 lease 后才 shutdown；另覆盖 `TryAcquire` 与 retirement 并发，证明退休观察到零后没有晚到 lease。
全局捕获预算用并发预留/释放测试验证永不超过上限，reservation 不随 attempt 数或 `common.RetryTimes` 变化，
并覆盖 `budget`/`admission`/`panic` 降级及 capture/runtime 两种所有权的幂等释放。用极小 BSP queue 制造丢弃，
断言 active runtime lower-bound 增长并限频告警；retirement drain 成功后精确 queue drop 等于
`materialized-received`，drain 失败时只报告 incomplete lower-bound；同时 export 最终错误只增加 failed 计数。
通用 options 底座不在本功能测试范围内发生
语义变更。

writer 测试使用实现 `SetWriteDeadline(time.Time) error` 的底层 `http.ResponseWriter` fixture，经一层 capture writer 后调用 `http.NewResponseController(capture).SetWriteDeadline(deadline)`，断言返回 nil、底层收到同一个 deadline；另用不支持 deadline 的 recorder 断言仅返回 `http.ErrNotSupported` 且透传行为不变。这条测试直接保护 `Unwrap`，不能只用接口类型断言替代。另分别构造 `c.Writer` 仍是 capture writer 和已被后续 wrapper 替换的场景：前者恢复原始 writer，后者保留新 wrapper、冻结 capture 并只产生一次限频 `writer_replaced_before_finish` 告警。

writer/recorder 并发契约必须在 `go test -race` 下验证：至少让请求 goroutine、scanner ping goroutine 和 data
handler goroutine 同时写入可识别 chunk，并与 `BeginAttempt`/`EndAttempt` offset 快照竞争；测试先 join 所有
写者再调用 `Finish`，断言底层与 capture 字节流一致、每个 chunk 只出现一次、captured/logical offset 都单调，
attempt slice 互不重叠，且截断后的 logical delta 仍能标记该 attempt 已写出。另让 `Finish` 与仍持锁的最后
一个写者竞争，断言 Finish 等待该写完成后才冻结，worker 看到的长度
包含最后一次写入且冻结后不再变化；该测试不得靠 sleep 判序，使用 channel/barrier 精确控制 happens-before。

writer/recorder 还要通过可控故障注入分别让捕获追加、Begin、BeginAttempt、EndAttempt、Finish、
sanitizer 和 aggregator panic，断言客户端得到的 status/body 与未启用 Langfuse 时逐字节一致，并验证
`capture_state=panic`、告警限频且不包含正文。retry loop 中 `GetBodyStorage` 的 413/400 只保留为低优先级
防御性单测：已有测试能力能自然模拟时断言 attempt 数为 0，不为这条基本不可达路径新增生产故障注入接口。

usage recorder 使用表驱动契约至少覆盖；表格中的 `cached` 文字均表示 canonical key `input_cached_tokens`，
`cache creation` 均表示 canonical key `input_cache_creation`：

| semantic/source / input flag | 源 usage 与 input/output 语义 | 期望互斥桶 |
|---|---|---|
| OpenAI undeclared inclusive (`InputExcludesCache=false`) | summary prompt 1000、cached 800；completion 100、reasoning 50 | input 200、cached 800、output 50、reasoning 50、total 1100 |
| OpenAI declared `prompt_includes_cache` (`InputExcludesCache=true` after fold) | raw prompt 1000、cached 800；summary fold 后 prompt 200；completion 100 | input 200、cached 800、output 100、total 1100；Recorder 不重复扣 cache，不写 `usage_input_clamped` |
| OpenAI declared `prompt_excludes_cache` (`InputExcludesCache=true` without fold) | upstream/summary prompt 200、cached 800；completion 100 | input 200、cached 800、output 100、total 1100；Recorder 不重复扣 cache，不写 `usage_input_clamped` |
| legacy Claude-derived OpenAI (`usageSemanticFromUsage="openai"`, `UsageSource==""`, `UsageSemantic==""`, Claude cache creation > 0) | `InputExcludesCache=true`；prompt 已 exclusive 1000、cached 800、`input_cache_creation` 100；completion 100 | input 1000、`input_cached_tokens` 800、`input_cache_creation` 100、output 100、total 2000（不再重复相减） |
| Anthropic (`InputExcludesCache=true`) | input 200、cached 800、`input_cache_creation` 100；output 100 | input 200、`input_cached_tokens` 800、`input_cache_creation` 100、output 100、total 1200 |
| OpenRouter + Claude (`isOpenRouterClaudeBilling=true`, `InputExcludesCache=true`) | raw prompt 1100、cached 800、`input_cache_creation` 100；summary 在 OpenRouter 分支扣除 cache 后为 prompt 200；completion 100 | input 200、`input_cached_tokens` 800、`input_cache_creation` 100、output 100、total 1200；Recorder 不再次扣 cache |
| Gemini (`InputExcludesCache=false`) | prompt 1000、cached 800；completion 100、reasoning 50 | input 200、cached 800、`input_image_tokens` 0、output 50、reasoning 50、total 1100 |
| Gemini image input | prompt 1000、image 300；completion 100 | input 700、`input_image_tokens` 300、output 100、total 1100 |
| Gemini audio input | prompt 1000、audio 300；completion 100 | input 700、`input_audio_tokens` 300、`input_image_tokens` 0、output 100、total 1100；无论是否配置独立 audio 价格，都按该 usage 语义拆分 |
| OpenAI cache-write overlap | prompt 1000、cached 800、`input_cache_creation` 400；completion 100 | input clamp 为 0、`input_cached_tokens` 800、`input_cache_creation` 400、output 100、total 1300，并标记 `usage_input_clamped=true` |
| text path with completion audio token | `Kind=text`，prompt 100、completion/output 75，其中 `OutputAudioTokens=25`，例如 Chat 模型缺 audio ratio 而落入 `PostTextConsumeQuota` | input 100、output 75、total 175；不导出 `output_audio_tokens`，不从 output 再减 25 |
| Chat/Responses audio settlement | `Kind=audio`，input text 200、input audio 300、output text 50、output audio 25 | input 200、input audio 300、output 50、output audio 25、total 575，且保留权威 cost/model |
| zero | 所有已知桶为 0 | 保留 input/output/total 的 0，桶和仍为 0 |
| invalid | 任一负源值、checked arithmetic 溢出或未知 semantic | 省略整组 usage 并标记原因 |

### 14.2 OTLP 集成契约

把真实 OTel exporter 指向 `httptest.Server`，解码 protobuf 并断言：

- 确定性 trace ID；
- 一个 root 和预期数量的 generation；
- 精确的 Langfuse attribute 名称；
- Error status 映射；
- usage/cost/metadata/model parameters 在 OTLP 中都是 string attribute，且 JSON 可解析；
- 在创建 provider 前同时把 `OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT`、
  `OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT`、`OTEL_ATTRIBUTE_COUNT_LIMIT` 和
  `OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT` 设为 `1`，仍断言每个 root/generation 的全部预期属性存在、最大生产属性数
  不超过固定的 64，并且 metadata/model parameters/usage/cost 与设置 attribute 前的 `common.Marshal` 结果
  逐字节相同且均可解析；这条测试必须证明显式 `WithRawSpanLimits` 覆盖环境限制，不能只测试 helper 返回的
  `SpanLimits` 字面量；
- completion start 是有效 RFC3339Nano 字符串；
- 用可注入 clock 固定 generation 开始/结束时间，并人为阻塞 worker；解码后 generation duration 必须精确
  等于注入的 attempt 时长，结束时间早于后续 attempt 开始时间，且不包含 admission 排队或 worker 延迟；
  root duration 同样等于 `relayInfo.StartTime` 到 `Finish` 快照的注入时长；
- root 只写 observation input/output 和 observation metadata，不出现
  `langfuse.trace.name/input/output/metadata`，trace name 只由 root span name 提供，设置
  `langfuse.internal.as_root="true"`；分别构造恰好一个 settled attempt、零个 settled attempt、多个 settled
  attempt、`no_active_attempt`、结算失败和非法 quota，断言只有第一种从同一个 `UsageRecord` 成对复制
  `quota`/`billing_source`，其余情况同时省略，且 root 始终没有 usage/cost details；Go 测试不声称验证 ingestion
  后的 trace fallback/merge；
- 显式 `langfuse.observation.usage_details` 的 OTLP JSON 精确使用 `input_cache_creation`、
  `output_audio_tokens`、`input_image_tokens` 和 `input_audio_tokens`，可解析且不出现生产映射已知的拼写错误；
  其中 `output_audio_tokens` 只出现在 `Kind=audio`，`Kind=text + OutputAudioTokens>0` 仍只输出完整 base
  `output`；Go 测试不复刻 Langfuse alias 归一化或入库逻辑；
- cost details 覆盖 `Quota=1, QuotaPerUnit=500000` 精确 JSON `{"total":0.000002}` 与
  `Quota=0` 精确 JSON `{"total":0}`，反序列化后验证 number 数值；另用可产生 exponent 的合法有限正汇率证明
  科学计数法仍是 JSON number 且可解析，不允许把 cost 序列化成 string；
- 构造四类 generation：`Error + model + no usage/no cost`、`Error + usage + no cost`、成功且有 usage/no cost、
  成功且有 usage/model/cost。Go 侧只断言 New API 的属性策略：第一类允许 model；第二、三类省略全部 model
  识别属性；第四类保留 model 和 New API 权威 cost。另覆盖 superseded attempt 属于第一类并带
  `attempt_superseded`。Error 对 Langfuse tokenisation/推价的外部效果只由 §14.6 E2E 验证；
- `http://` Host 的请求实际以 HTTP 发出，`https://` Host 实际执行 TLS；根 base path 和 `/langfuse` base path
  分别精确命中 `/api/public/otel/v1/traces` 与 `/langfuse/api/public/otel/v1/traces`。测试预先设置冲突的
  `OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`，证明 snapshot 的
  `WithEndpointURL(finalURL)` 完整覆盖环境变量。Basic Authorization 存在，但测试失败输出不打印其值；
- 使用满足整组校验的边界配置分别生成最大 root span 和满 batch，解 gzip 并解码官方 exporter 的 protobuf。
  分别使用不可压缩 ASCII、全引号/反斜杠/控制字符等最坏 JSON 转义正文，断言结构化 input/output 仍为合法
  JSON、各属性按 §9.1 的转义贡献计量后不超过 `max_content_bytes`，且配置的保守
  `max_queued_span_bytes <= 9,000,000`；记录实际 protobuf/gzip 字节数作为 ingress 配置依据。Go 测试不复刻
  Langfuse TypeScript 的 `JSON.stringify(span)` event 结构，也不把 9,500,000/16 MiB warning 当接收正确性
  阈值，不声明通用 ingress limit 或要求 wire-size splitter；正文和凭证不得进入测试日志；
- 服务端返回 429 时验证 `Retry-After` 优先级、退避上限和最终丢弃；验证重试不在请求 goroutine 执行，并分别
  断言 exporter received/exported/failed span 计数；服务端返回带敏感测试 body 的 ingestion-suspended 403 时
  断言当前 batch 只请求一次、不重试、增加 failed 计数并触发 `ingestion_forbidden` 限频告警，原始 response
  body 被关闭且 exporter 只读到固定替代文本，代理 error handler 不记录原始 exporter error，日志无原始响应
  body/正文/凭证；随后的新 batch 仍可再次请求并在服务端恢复后成功；
- 使用可控的极小 BSP queue 或测试 SpanProcessor 记录 `OnEnd` 顺序，断言同一 trace 始终先收到 root、再收到
  generation；模拟容量只够 root 的压力时 root 被接收而后续 generation 被丢弃，generation 不携带
  `langfuse.trace.*` 更新属性，并验证 queue drop lower-bound warning、成功 drain 后精确计数以及 drain 失败时
  的 incomplete 摘要。

### 14.3 Controller 回归

覆盖成功、失败后重试成功、预扣失败、最终上游失败、流式中断。Realtime 错误回归必须断言 finalizer 的
`WssError` 写入发生在 `ws.Close` 之前；同时，重点断言达到计费阶段后的调用顺序严格为
`NormalizeViolationFeeError -> Refund -> ChargeViolationFeeIfNeeded -> SetMessage/写错误响应 -> Recorder.Finish`。
测试还要固定 controller 的作用域/阶段改造：`relayInfo`、Recorder、`billingPhaseReached` 在早期 var 块可见，
`GenRelayInfo` 使用赋值而非重新短声明；免费分支完成和非免费预扣成功都在分支后的唯一位置置位，预扣失败不
置位。另覆盖请求校验失败、`GenRelayInfo` 失败和预扣失败，断言 nil/未就绪的 `relayInfo` 不进入退款或违规费
逻辑。Recorder 捕获归一化后的最终错误正文，且 telemetry 故障永远不改变客户端响应。

增加受控业务 panic 回归：handler 在写出部分正文后 panic，统一 finalizer 收口 active attempt、按身份规则有条件
恢复 writer、冻结部分 output 并以同一个 panic value 重新抛出，Gin Recovery 随后写 500；断言 telemetry 标记
`lifecycle_panic`，root output 不含冻结后 Recovery 写入的 500，并把这项结果视为 §13 的显式例外而非内容不一致
失败。

Playground 回归必须覆盖一个受支持请求：断言它经过 `controller.Relay` 并写 `is_playground=true`；channel
test 回归断言不调用 `Begin`，即使共享 `doRequest` 和结算 hook 被执行也没有 root/generation。

音频结算路由必须分两组锁定当前不同条件，不能复用一个“存在 audio token”参数化断言：

- Chat Completions（包括 `chatCompletionsViaResponses` 和普通 adaptor 返回路径）：audio token 为正且 input/output
  任一 audio ratio 已配置时调用 `PostAudioConsumeQuota`；有 token 但无 ratio、或有 ratio 但 token 为 0 时都
  调用 `PostTextConsumeQuota`。其中“有 completion audio token 但无 ratio”还要断言 §14.1 的 `Kind=text`
  归一化，不丢 token、不生成 `output_audio_tokens`。
- Responses（非 Compact）：`OriginModelName` 以 `gpt-4o-audio` 开头时调用 `PostAudioConsumeQuota`，否则调用
  `PostTextConsumeQuota`；该测试不能要求 token+ratio 条件，也要覆盖“非 audio 前缀但携带 audio token/ratio”
  仍走文本路径。Responses Compact 保持现有固定文本结算路径，单独断言不受前缀规则影响。

Gemini 分类器必须先以独立 PR 提供纯函数契约测试，覆盖 `:generateContent`、`:streamGenerateContent`、
`:embedContent`、`:batchEmbedContents`、`:predict`、无 action 的 `/v1/engines/:model/embeddings` 路径和未知
action，并覆盖 `imagen*`、`text-embedding*`、`embedding*`、`gemini-embedding*` 三类上游 model 前缀。
Langfuse PR 只读调用该分类器：断言只有最终分类为 generate 且 model 非排除前缀时 materialize OTel span；
embedding、predict、unknown、imagen 以及渠道映射后的任一 embedding/imagen model 均不创建或送出 OTel span。
Begin 阶段可以保留未 materialize 的 Recorder 值对象，但不能存在 active span。另明确回归
`:generateContent + UpstreamModelName=text-embedding-004` 被分类为 embedding，Langfuse 不导出对话 trace，
但不改变现有 `geminiRelayHandler`、`GetAndValidateRequest`、Gemini adaptor `GetRequestURL`/`DoResponse`
的分支，也不把现有 URL 与响应解析不一致当作本 PR 的修复目标。独立分类器 PR 还必须验证 model 前缀优先
于 path action，防止三类前缀在最终复核中遗漏。

Gemini 分类与 Langfuse 范围回归表：

| 入站 action | 最终 `UpstreamModelName` | 分类/期望 |
|---|---|---|
| `:generateContent` | `text-embedding-004` | `embedding`，不 materialize OTel span；保护渠道映射后的 embedding 前缀复核 |
| `:generateContent` | `embedding-custom` | `embedding`，不 materialize OTel span |
| `:generateContent` | `gemini-embedding-001` | `embedding`，不 materialize OTel span |
| `:predict` | `imagen-3` | `predict`，不 materialize OTel span |
| `/v1/engines/:model/embeddings` | `gemini-2.5-flash` | `unknown`，不 materialize OTel span；覆盖无 `:action` 的 Gemini 路径 |
| `:generateContent` | `imagen-3` | `predict`，不 materialize OTel span；保护 model 前缀优先于 path action |
| `:generateContent` | `gemini-2.5-flash` | `generate`，Finish 时 materialize Langfuse spans |

### 14.4 前端验证

- 配置校验和 secret 保留行为测试；首次/重新启用未显式选择 sample rate 或 Send Content 时 UI 阻止提交，
  两者都选择后才发送 presence-aware 字段，显式 rate 0 可提交；
- session path 预设默认不选，选择后只添加对应精确路径并显示 64 KiB 完整读取提示；
- 涉及文件 lint；
- TypeScript typecheck；
- `bun run i18n:sync` 和 locale 完整性检查；
- production build。

### 14.5 构建验证

- 新包及 controller/service 接入的 focused Go tests；
- `go test -race ./service/langfuse/...`，其中必须包含 §14.1 的 writer/recorder 并发用例；
- 用 Go AST/import 测试按完整 module path 验证 `service/langfuse` 的项目内直接 imports 只来自 §4.1 白名单；
  fixture 必须证明 `github.com/QuantumNous/new-api/relaykit/dto` 和 `/relaykit/types` 通过，而根 `/dto`、`/types`
  被拒绝，不能只比较 import 的末段包名。用 `go list -deps ./service/langfuse` 验证闭包不含根 `service`、`model`、
  `controller`、`service/langfuseconfig` 或 `relay/channel/**`，不把 §4.1 已列明的 `relay/common` 传递依赖误报为违规；同时
  验证所有受影响的 `relay/**` 包不依赖 `github.com/QuantumNous/new-api/service/langfuseconfig`；
- `go test ./...` 或环境可承受的最广 root-module suite；
- `go build ./...`；
- `cd relaykit && GOWORK=off go build ./...`，即使未修改 relaykit 也验证其独立性。

### 14.6 真实 Langfuse E2E

外部行为不进入 Go 单元/契约测试。实现阶段单独启动或使用固定 revision `2aa50493a` 的真实 Langfuse web、worker
及其依赖服务，向真实 `/api/public/otel/v1/traces` endpoint 发送官方 Go exporter 的 OTLP，再通过 Langfuse API
或测试数据库等待并读取最终 ingestion/worker 结果。E2E 必须固定 project keys、模型价格和轮询 deadline，失败
时只输出 trace/observation ID 与脱敏状态，不输出正文或凭证。至少验证：

- root 不发送 `langfuse.trace.name/input/output/metadata`、只发送 span name 和 observation
  input/output/metadata 时，trace name 通过 root span name fallback 得到同名值，trace 正文/metadata 通过真实
  root fallback/merge 得到相同内容和 `capture_state`，root observation 详情也保留正文；
- `input_cache_creation`/`output_audio_tokens` 按内建口径入库，`input_image_tokens`/`input_audio_tokens` 作为
  自定义 usage type 原样保留；另发送仅用于 E2E 的故意拼错 key，确认 Langfuse 不会自动纠正。该错误 key
  绝不能由 New API 生产映射生成；
- `Error + model + no usage/no cost` 不触发缺失 usage 的 tokenisation；`Error + model + usage + no cost` 仍会按
  配置的 model prices × usage 推价；由此确认 New API 对 `Error + usage + no cost` 省略 model 的策略确实阻止
  非权威成本。再验证成功 `usage + model + authoritative cost` 保留 New API cost；
- generation 上 §13 denylist 属性缺失，且多 attempt 不覆盖 root 的 user/session/trace metadata；
- HTTP 根路径和带 base path 的部署各至少完成一次真实 ingestion。若固定 revision 的部署方式不支持子路径，
  子路径只在 §14.2 的 exporter HTTP 集成测试验证，并在 E2E 报告中明确该环境限制。

这些 E2E 是发布验收项，但不要求 `go test ./...` 启动 Node/TypeScript worker，也不允许在 Go 中复制
`OtelIngestionProcessor`/`IngestionService` 来制造一个伪 Langfuse fixture。

## 15. 验收标准

1. 有效配置启用后，一次成功的受支持共享 HTTP 对话请求生成一个 Langfuse trace：包含稳定 New API
   `user.id`、可选的用户作用域 `session.id={userId}:{rawSessionId}`，以及带实际上游模型、canonical 内建桶、
   明确声明的 image/audio-input 自定义互斥 usage types 和 New API 权威 cost 的 generation；正文采集启用且未
   降级时还包含限长、脱敏且保持合法 JSON/UTF-8 的客户端 input/output 表示。订阅场景明确标记为订阅额度消耗
   而非钱包 USD 扣款。AWS SDK/Xunfei v1 旁路按第 4 条例外处理。
2. 同一 New API 用户的多个请求携带相同显式 raw session 时，在 Langfuse 中归属于同一 session，并在采样率
   小于 1 时确定性地整组命中或整组省略；两个不同用户即使携带相同 raw session，也必须归属不同 Langfuse
   session 并使用不同采样键。该保证仅适用于 raw session 成功提取且用户 ID 为正数的请求；body storage
   缺失/无效或用户 scope 不可用时回落到 request ID 采样，同一原始 session 中这些请求不保证整组命中或省略。
3. 没有显式配置 session 标识或没有可用正数用户 ID 的请求不设置 `session.id`，不会被启发式或 project 全局
   raw session 合并。
4. 共享 `doRequest` 路径失败后重试成功产生每次失败的 generation 和最终成功 generation，usage/cost 只属于
   成功项；root 仅在恰有一个合法 settled attempt 时从同一 record 成对复制 metadata
   `quota`/`billing_source`，不复制 usage/cost details。AWS Bedrock 非 API Key SDK 路径和 Xunfei 是 v1 明确
   已知例外：只产生 root，usage/cost 以 `no_active_attempt` 丢弃，root 也省略结算摘要；AWS API Key 路径不属于
   该例外。
5. 范围内但未发生上游调用的失败只有 root；`RelayInfo` 之前的失败不产生 trace，Gemini embedding/predict/unknown
   和 `imagen*` 请求也不产生 trace。受支持的 Playground 请求产生带 `is_playground=true` 的 trace；channel
   test 永远不产生 trace。
6. 对 `send_content=true`、捕获预算/admission 正常且内容处理未 panic 的范围内请求，root observation
   input/output 都存在；其他 `capture_state` 按定义省略正文。root 设置 `langfuse.internal.as_root="true"`，只写
   observation 域 metadata/input/output，不设置 `langfuse.trace.name` 或其他 trace 域副本；trace name 由 span
   name fallback 得到，有正文时 Langfuse trace 域通过 root merge/fallback 得到同样 metadata/正文。除 §13
   明确接受的 relay 业务 panic -> Gin Recovery 逃逸窗口外，root output 与
   客户端实际收到的内容一致，包括费用 finalizer 之后写出的最终结构化错误和部分流式输出；panic 窗口中只保证
   捕获冻结前字节并写诊断标记，不声称包含 Recovery 随后写出的 500。
7. 关闭 Send Content 后不出现 input/output，`capture_state=disabled`，且不读取内容采集 input、不预留
   捕获预算、不包装 writer；user/session、耗时、模型、usage、cost 和错误仍保留，已配置 session body
   path 时仅保留其独立的有界身份读取。
8. Langfuse 宕机、凭证无效、用量超限导致的 ingestion-suspended 403、队列饱和和 shutdown 超时均不改变
   relay 行为或阻塞请求；403 batch 不重试、使用固定脱敏错误且限频告警，后续 batch 可自动恢复。队列丢弃至少
   通过 active runtime 的单调 lower-bound/限频 warning 可观察，retirement drain 成功后给出精确总数，失败则
   明确标记 incomplete；export 最终失败独立计数。runtime 切换不得在 materialization worker 完成前 shutdown
   旧 provider。
9. 配置整组原子更新；首次/重新启用必须由管理员显式选择 Sample Rate 和 Send Content，缺任一字段不保存或
   发布，session body paths 提供但不默认启用明确预设；Secret Key 永远不返回、不记录，通用 option PUT 无法
   修改任何 `langfuse_setting.*` 字段；`http://langfuse:3000` 使用明文 HTTP，带 base path 的 `https://x/langfuse` 精确发送到
   `/langfuse/api/public/otel/v1/traces`，且管理员 endpoint 不被 OTLP 环境变量覆盖；Langfuse 专用 provider 的
   sampler 与 span attribute value/count limits 同样由显式 options 锁定，不受同进程 OTel 环境变量影响。
10. 流式请求经过 capture writer 后仍能由 `http.ResponseController` 设置底层连接 write deadline；Finish
    仅在 `c.Writer` 仍是本次 capture writer 时恢复原始 writer，后续 wrapper 不会被无条件覆盖。
11. 功能关闭、`sample_rate<=0`、请求未采样或 `send_content=false` 时不包装 writer，不执行正文采集与聚合；
    其中 `sample_rate<=0` 在 Phase 0 返回，连 session Header/body 和 gjson 都不读取。Gemini
    embedding/predict/unknown 和 `imagen*` 请求不 materialize 或送出 OTel span，`Begin` 可保留的只是值对象；
    默认 512 MiB 全局预算、worker admission、panic 等降级都通过 `capture_state` 可观察。
12. capture writer 的 `Write` 不执行解析或脱敏；`Begin`、`BeginAttempt`、`EndAttempt`、`Finish`、
    sanitizer、aggregator 和异步 worker 的 panic 都被隔离并限频记录，绝不改变 relay 状态、HTTP status
    或响应正文；全局捕获预算耗尽时只产生 metadata-only telemetry。
13. `relayInfo.StartTime` 作为 root 起始基准；`EndAttempt` 和 `Finish` 在请求 goroutine 立即快照真实结束时间，
    writer 通过 join + mutex 建立最后写者到冻结快照的 happens-before，再入有界 worker；聚合、脱敏和序列化
    不阻塞响应完成，worker 不引用 `gin.Context` 等可变请求对象，并用 `trace.WithTimestamp` 创建/结束 spans，
    先 End root 再 End generation，使 duration 不受 worker 延迟影响且队列压力优先保留 root；capture budget 和
    runtime lease 都持有到 worker 完成，退休 runtime 不与 worker materialization 竞争。
14. Chat Completions 按“audio token 为正且存在 audio ratio”、Responses 按 `gpt-4o-audio` 模型前缀分别判定
    是否走 `PostAudioConsumeQuota`；两套条件都有独立回归。走音频路径时成功 generation 同时包含 text/audio
    互斥 usage 桶、New API 权威 cost 和 model 属性；文本路径的 audio prompt token 始终按 usage 语义拆出独立桶，
    即使真实计费因未配置 audio 价格而按基础倍率计算。`Kind=text` 的 completion audio token 不拆分，仍留在
    base `output`；只有 `Kind=audio` 导出 `output_audio_tokens`。
15. 文本没有 billable usage 时不导出 cost，也不误报 settlement error；有权威 cost 时，quota 与
    `QuotaPerUnit` 在 `SettleBilling` 调用点一起快照，worker 执行前的运行时汇率变更不改变该请求的 USD 换算。
    默认汇率下最小正 quota 和零 cost 分别编码为 JSON number `0.000002` 与 `0`，合法科学计数法同样可解析，
    不能序列化成字符串。
16. `langfuse.observation.model.name` 只出现在“有 New API 权威 `cost_details`”或“无 `usage_details` 且 OTel
    Error status”两种 generation；Go OTLP 契约测试证明 `Error + usage + no cost`、成功但无权威 cost 都省略
    全部模型识别属性。§14.6 的真实 Langfuse E2E 另行证明 Error 只阻止缺 usage/cost 时的 tokenisation，而
    不能阻止 model prices × usage 推价；两层都通过才可勾选本项。
17. generation 名称从 `ContextKeyChannelName` 的请求级快照构造并有稳定 fallback，不从 `ChannelMeta` 读取不存在
    的名称字段；generation 不含 §13 denylist 的 trace-update 属性，尤其不得出现 `user.id`/`session.id`。
18. 开始 Langfuse 实现前，tiered billing 的 `BuildTieredTokenParams` 签名、所有调用点和测试必须先在独立改动中
    对齐，`go test ./service` 恢复通过；Langfuse 只消费其最终契约，不把当前不可编译基线带入实现计划。

## 16. 实现顺序

0. 先完成当前在途的 channel cache semantic/tiered billing 前置改动：把
   `BuildTieredTokenParams` 的最终签名、`PostTextConsumeQuota`、`PostAudioConsumeQuota`、
   `controller/channel-test.go` 和全部 tiered 单测对齐，运行 `go test ./service`、受影响 controller tests、
   `go build ./...` 及 `cd relaykit && GOWORK=off go build ./...`。在此基线恢复绿色并固定
   `UpstreamPromptTokensIncludeCache` 的 raw-upstream 语义前，不开始 Langfuse 实现，也不使用本文 usage 表替代
   独立计费回归。
1. 将现有 indirect 的 `go.opentelemetry.io/otel`/`otel/trace` 调整为实现所需的 direct 依赖，并引入并
   锁定 `go.opentelemetry.io/otel/sdk`、`go.opentelemetry.io/otel/metric` 和
   `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`。`go.opentelemetry.io/proto/otlp` 仅可由集成
   测试直接导入，用来解码官方 exporter 发出的 protobuf；生产代码不得用它复刻 `ReadOnlySpan` 转换或实现
   splitter。`google.golang.org/protobuf v1.36.5` 已在当前 `go.mod` 中作为 indirect 依赖存在，只按实现后的
   实际 import（包括测试）结果调整 direct/indirect。当前 `go.opentelemetry.io/otel` 和
   `otel/trace` 的间接消费方包括 ClickHouse `ch-go`/`clickhouse-go`；升级 OTel 整组版本后必须确认这些
   现有数据库依赖仍能编译并通过其受影响的 model/database 测试，不能只验证 Langfuse 新包。用
   `go mod tidy`/构建确认 OTLP/HTTP exporter 与 sdk、metric、trace 的版本一致。至少运行
   `go test ./model/... ./service/...` 覆盖现有 ClickHouse 间接消费者涉及的数据库与结算路径，并在环境允许时
   运行完整 root suite；不能只验证 Langfuse 新包。依赖只进入根 module，确认
   `cd relaykit && GOWORK=off go build ./...` 仍独立通过且不被 OTel 接入牵连。
2. 增加 Langfuse 配置、校验、专用安全设置 API、默认 512 MiB 的固定全局捕获 reservation、atomic binding 和
   功能局部的整组 reconcile；专用 PUT 用 presence-aware DTO 强制首次/重新启用显式选择 Sample Rate/Send
   Content，并提供默认不选的精确 session path 预设。数据面 `service/langfuse` 禁止依赖 `model`，持久化/
   reconcile 放在 `service/langfuseconfig` 控制面包；实现跨实例同步和专用 PUT 成功后的单次发布，不修改通用
   options apply/post-hook 底座。配置校验实现 checked 的 9,000,000 字节单 span 包络与 256 MiB queue/batch
   tuple，不增加 `max_otlp_request_bytes` 或 splitter。
3. 增加带防退休 `TryAcquire` 和 worker lease 所有权的 OTel runtime manager、计数型 exporter decorator 与
   OTLP 集成契约测试；TracerProvider 显式使用 `AlwaysSample` 和 §11 的固定 `WithRawSpanLimits`，测试以冲突
   attribute value/count 环境变量证明 JSON attributes 不被截断或丢弃。契约还包含 429 `Retry-After` 退避、
   ingestion-suspended 403 的 non-retryable/响应 body 替换/限频告警、materialized/received/exported/failed/drop
   诊断和 15 秒共享 deadline 的有界 shutdown；单次 HTTP 请求超时
   保持 10 秒，生产代码只调用官方 exporter，不复刻 `internal/tracetransform`。
4. 增加 identity、正文脱敏、capture writer 和 aggregator 及其测试；先落实 writer 级 mutex、已知写
   goroutine join、Finish 冻结 happens-before、writer 实例身份校验与有条件恢复、`Write` 只透传/追加的硬约束、
   预算 CAS 和 panic 边界，并在
   `go test -race` 下覆盖请求/ping/data handler 并发写。
5. 先完成独立 Gemini action/model 分类器 PR 及其纯函数回归；Langfuse 实现只读消费分类结果。随后增加
   Recorder root/attempt 生命周期和 controller 接入。`Begin` 严格实现 §5.1 Phase 0 廉价守卫（含
   `sample_rate<=0`）-> Phase 1 Header/body 身份提取与采样 -> Phase 2 命中后 lease/预算/捕获，不得把最多
   64 KiB 的 session body 身份读取
   移到命中后；只读取 `relayInfo.RequestId`，并保留意外为空时 no-op 的防御断言，但不围绕这个由
   `GenRelayInfo` 保证不可达的分支设计功能。采样严格使用 §5.1 固定的 SHA-256/big-endian/整数阈值算法，trace
   ID 覆盖全零防御。原始 session 只从显式来源提取并按 `{userId}:{rawSessionId}` 作用域化；只在已缓存 storage
   时读取 input/body session。增加
   `ContextKeyLangfuseRecorder` 和 nil-safe `FromContext`，所有结算/hook 统一使用。

   在 `EndAttempt` 通过 nil-safe accessor 读取最终模型并复核分类，channel ID/type 同样只用 accessor；
   channel name 在 `BeginAttempt` 从 `ContextKeyChannelName` 读取、trim 后快照，并按 §5.2 提供稳定 fallback，
   worker 不访问 context。严格在 `GetBodyStorage` 成功后才可能进入 relay handler；只在共享私有 `doRequest`
   的 `relayClient.Do(req)` 紧前接入一次窄 hook，`DoApiRequest`/`DoFormRequest`/导出的 `DoRequest` 不重复调用。
   `DoTaskApiRequest` 依靠 nil Recorder no-op，`DoWssRequest` 明确范围外。AWS 非 API Key SDK 三个调用点和 Xunfei
   私有签名链在 v1 不改造，按 root-only/`no_active_attempt` 已知限制测试；AWS API Key 路径仍经共享 hook。
   Coze 只记录创建调用并让 attempt 覆盖私有轮询；重复 Begin 先收口前一 attempt 再创建最新 active，并覆盖
   `chatCompletionsViaResponses`。

   controller 早期 var 块提升 `relayInfo`/Recorder/`billingPhaseReached`，把 `GenRelayInfo` 改为普通赋值，
   免费分支完成或预扣成功后在唯一汇合点置位；原错误响应 defer 位置注册统一 finalizer，业务 panic 时完成
   telemetry cleanup 后 re-panic。retry loop 每次 handler 返回后、成功 return/渠道错误处理/下一轮之前调用
   `EndAttempt`；`EndAttempt`/`Finish` 快照结束时间并由 worker 使用 `trace.WithTimestamp` 创建 spans。root 不写
   `langfuse.trace.name`，只靠 span name fallback；覆盖闭合 reason 枚举、异步 Finish、`capture_state` 和 root 降级。该步骤
   不得修改 Gemini handler、validator 或 adaptor 的既有分发分支；worker 内必须先创建无 parent 的 root，再用
   其 SpanContext 创建全部 generation，设置完属性后先 End root、再 End generation。
6. 从 `PostTextConsumeQuota` 和 `PostAudioConsumeQuota` 接入最终 usage/cost；文本 `Settled` 同时要求
   `SettleBilling` 成功和 `summary.hasBillableUsage()`，quota 与运行时可变的 `common.QuotaPerUnit` 在结算调用点
   一起快照；文本 Recorder 只复用 fold 后的 `summary.InputExcludesCache` 和 summary image/audio tokens，
   `InputExcludesCache` 在现有 fold 之后赋值为 `!promptTokensIncludeCache`。`PromptTokensDetails.AudioTokens > 0`
   时始终拆出 audio 桶；不得用 usage semantic、channel declaration 或 fold 前的
   `UpstreamPromptTokensIncludeCache` 重新推导 Recorder 口径，也不得把唯一的 cache creation 计价档位判断改成
   `InputExcludesCache`。明确 `Kind=text` 忽略 `OutputAudioTokens` 细分且保留完整 base output，只有
   `Kind=audio` 导出 `output_audio_tokens`；`input_cache_creation`/`output_audio_tokens` 是内建口径，
   `input_image_tokens`/`input_audio_tokens` 是 New API 自定义 usage types。分别回归 Chat 的 token+ratio 与
   Responses 的模型前缀音频路由，并覆盖最小正 quota、零 cost 和科学计数法 JSON number；只有无 usage 且 OTel Error 的失败/
   superseded generation 可以在无 cost 时写 model，`Error + usage + no cost` 和成功无权威 cost 都省略所有
   model 识别属性。加入 channel declared includes/excludes、legacy Claude-derived、Gemini image/audio input、
   OpenAI cache-write clamp、audio settlement bucket 和四类 model/usage/cost 属性策略回归；Langfuse 的实际
   tokenisation/推价结果留给步骤 9 的真实 E2E，Go 测试不复刻 worker。tiered 行为由步骤 0 的独立计费测试
   保护，不在 Langfuse PR 改签名或重算 quota。
7. 增加 shutdown 与限频 OTel error handler。
8. 增加带首次/重新启用显式选择步骤和 session path 预设的设置 UI、全语言翻译和前端测试；先加载仓库
   `i18n-translate` skill，并严格执行 §12 的 `bun run i18n:sync` -> script-only 七语言写入 -> missing-key/sync
   -> typecheck/build 流程。
9. 执行 root、relaykit、frontend 验证，并按 §14.6 对固定 revision 的真实 Langfuse web/worker 执行 E2E；
   不以 Go ingestion fixture 替代。

## 17. 参考

以下 Langfuse 行为已对照本地源码 revision `2aa50493a` 核验：

- Langfuse OTLP endpoint：本地 Langfuse v4 的 `fern/apis/server/definition/opentelemetry.yml`。
- Langfuse OTel attributes：`packages/shared/src/server/otel/attributes.ts`。
- Langfuse 单 span 默认 warning 阈值：`packages/shared/src/env.ts` 的
  `LANGFUSE_OTEL_MAX_SPAN_BYTES=9_500_000`；processor 明确只记录、不拒绝。
- Langfuse OTLP request warning、ingestion suspended 和 body 读取：
  `web/src/pages/api/public/otel/v1/traces/index.ts`；路由在 `auth.scope.isIngestionSuspended` 时抛出 403，关闭
  bodyParser、直接读取/解压原始流，并对解压后 body 超过 `16 * 1024 * 1024` 只记录 warning。
- Langfuse root/observation metadata merge、显式 usage details 短路和隐式 alias 归一化：
  `packages/shared/src/server/otel/OtelIngestionProcessor.ts`。
- Langfuse usage 输入校验与 OpenAI bucket 转换：`packages/shared/src/server/ingestion/types.ts`。
- Langfuse usage total 告警、cost 推价抑制，以及 ERROR observation 跳过 tokenisation：
  `worker/src/services/IngestionService/index.ts`。
- New API 现有 ResponseWriter wrapper：`middleware/audit.go`。
- New API 流式 write deadline：`relay/helper/stream_scanner.go`。
- New API 现有配置注册：`setting/perf_metrics_setting/config.go`。
- New API 通用 option 拒绝范式：`controller/option.go`。
- New API relay retry loop：`controller/relay.go`。
- 权威文本 usage/quota 结算：`service/text_quota.go`。
