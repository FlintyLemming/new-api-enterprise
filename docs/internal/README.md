# internal-custom 台账

内部长期分支 `internal-custom` 的改动总台账。每一条内部 patch 都在 `patches/` 下有一份详情文档，本文件是索引和流程约定。

## 当前基线

2026-11-09 同步至 `v1.0.0-rc.49`（`48a6c87f9`），合并提交 `c334b1f5e`，详见下方 [rc.49 同步记录](#2026-11-09-同步上游-v100-rc49)。合并前本地与 `enterprise/internal-custom` 一致（`f475dbbb8`），备份为 `backup/internal-custom-before-rc49-20261109`；本地 `main` 快进至该 tag。随后 `7bbbaf593` 让版本 tag 构建同时推 `:internal-custom` 镜像，不再使用同名 git tag 与 Release（见 rc.49 记录「浮动镜像 tag」）。同日打 tag `v1.0.0-rc.49-internal.20261109` 并部署到 8850，见[部署台账 §21](patches/2026-08-15-langfuse-local-deployment.md#21-2026-11-09-rc49-镜像构建与上线)；`deepseek-flash` / `llm-prime` 表达式随后加 `cc * 0` 维持缓存写入免费，见[部署台账 §22](patches/2026-08-15-langfuse-local-deployment.md#22-2026-11-09-deepseek-flash--llm-prime-表达式加-cc--0)。

上一基线：2026-10-13 同步至 `v1.0.0-rc.44`（`633b4c5b1`，GitHub Latest release），合并提交 `04f2b55c5`，详见下方 [rc.44 同步记录](#2026-10-13-同步上游-v100-rc44)。合并前先把本地 `internal-custom` 快进到 `enterprise/internal-custom`（`f8775ede8`，含 2026-09-14 远端会话提交的重置卡持卡人用户名 `286e1bd2c` 与 GHCR 镜像发布 `556468079`），备份为 `backup/internal-custom-before-rc44-20261013`；本地 `main` 快进至该 tag。`internal-custom` 与 `main` 已推送到 GitHub `enterprise`；GitLab 未推送（本机没有可用的 GitLab SSH 凭据）。随后手动触发 GHCR 镜像构建，结果记在[部署台账 §13](patches/2026-08-15-langfuse-local-deployment.md#13-2026-10-13-rc44-镜像构建)。2026-10-15 已部署到 8850（`:internal-custom-20261013-bd8600f`），彩排、切换与验证见[部署台账 §14](patches/2026-08-15-langfuse-local-deployment.md#14-2026-10-15-rc44-镜像上线)。

2026-11-05 在基线上追加两项直接提交并上线 8850：对话内 system 消息内联开关 `62c9e13c8`（[部署台账 §15](patches/2026-08-15-langfuse-local-deployment.md#15-2026-11-05-对话内-system-消息内联开关上线)）与流式"完成后客户端断开"修复 `ea09002bc`（[部署台账 §16](patches/2026-08-15-langfuse-local-deployment.md#16-2026-11-05-流式完成后客户端断开误记-client_gone-修复上线)）。2026-11-06 在 `internal-custom` 上打 tag `v1.0.0-rc.44-internal.20261106` 触发 tag 构建，见[部署台账 §17](patches/2026-08-15-langfuse-local-deployment.md#17-2026-11-06-打-tag-v100-rc44-internal20261106)。2026-11-09 追加中国大陆节假日计价 `d4e7f7c18`，打 tag `v1.0.0-rc.44-internal.20261109` 并移动浮动 tag `internal-custom`，8850 当前运行 `:internal-custom`（revision `446695161`），见[部署台账 §19](patches/2026-08-15-langfuse-local-deployment.md#19-2026-11-09-中国大陆节假日计价上线)。

更早基线：2026-09-09 同步 `v1.0.0-rc.36`（合并提交 `a72e8c116`，见 [rc.36 同步与数据库验证记录](sync-rc36-2026-09-09.md)），8850 环境此前运行 `new-api:rc36-e6b8fc10d`（[部署台账 §11](patches/2026-08-15-langfuse-local-deployment.md#11-2026-09-12-重置卡--缓存统计口径镜像上线)）。

## 分支模型

| 分支 | 远端 | 定位 |
| -- | -- | -- |
| `main` | `upstream`（github.com/QuantumNous/new-api） | 上游镜像，**不放任何内部改动**，只做快进同步 |
| `internal-custom` | `enterprise`（github.com/FlintyLemming/new-api-enterprise） | 内部部署实际使用的分支，= `main` + 若干 patch 分支的 merge |
| `<type>/<topic>` | 视情况 | 单个改动的开发分支，从 `main` 切出 |

`docs/internal/` 这个目录只存在于 `internal-custom`。要提给上游的分支一律从 `main` 切，台账目录自然不会出现在 PR diff 里。

## 状态取值

| 状态 | 含义 | 后续动作 |
| -- | -- | -- |
| `internal-only` | 内部专用，不打算提上游 | 长期保留，每次同步上游后确认仍能干净合并 |
| `upstream-pending` | 已提 PR 或准备提 PR，等待上游处理 | 关注 PR 进展 |
| `upstream-merged` | 上游已收编 | 下次同步 `main` 后核对差异并移除内部副本，条目移入「已归档」 |
| `dropped` | 已废弃或回退 | 条目移入「已归档」，写明原因 |

## 生效中的 patch

| 分支 | 摘要 | 状态 | 上游 PR | 合入 commit | 合入日期 | 详情 |
| -- | -- | -- | -- | -- | -- | -- |
| `feature/langfuse-tracing` | 可选的 Langfuse OTLP 对话追踪导出（默认关闭） | `upstream-pending` | 未提交 | 线性合入，tip `8eb884f4` | 2026-08-16 | [详情](patches/feature-langfuse-tracing.md) |
| `feat/strip-claude-system-prefix` | 渠道开关：Claude→非 Claude 转换时剥离 Claude Code 计费头，保住前缀缓存 | `internal-only` | 未提交 | 线性合入，tip `a5dfe631` | 2026-08-17 | [详情](patches/feat-strip-claude-system-prefix.md) |
| `feat/exchange-key` | 内部应用用共享 SECRET 派生 per-user HMAC key 调 relay | `internal-only` | 未提交 | 线性合入，tip `e4d5ea76` | 2026-08-23 | [详情](patches/feat-exchange-key.md) |
| `feature/user-concurrent-ip-limit` | 每用户滑动窗口内并发客户端 IP 数限制（含白名单与每用户覆盖） | `upstream-pending` | 未提交 | 线性合入，tip `7ddc7866` | 2026-08-24 | [详情](patches/feat-user-concurrent-ip-limit.md) |
| `fix/log-duration-ms` | 日志总耗时毫秒精度：`other.duration_ms` + 前端回退 `use_time` | `upstream-pending` | 未提交 | `0da33748` | 2026-09-01 | [详情](patches/fix-log-duration-ms.md) |
| `feat/subscription-reset-card` | 订阅重置卡：管理员发卡、用户自助清零当前周期用量（含 2026-09-14 持卡人用户名列） | `internal-only` | 未提交 | 线性合入，tip `496f1674e`；`fcf8e49a1` | 2026-09-12 | [设计](../superpowers/specs/2026-09-11-subscription-reset-card-design.md) |
| `feature/usage-stats-cache-caliber` | 用量统计 `prompt_tokens` 缓存口径全局归一化开关（不影响计费） | `internal-only` | 未提交 | `e6b8fc10d` | 2026-09-12 | [设计](../superpowers/specs/2026-09-09-usage-stats-cache-normalization-design.md) |
| —（直接提交） | 镜像发布 workflow 改推本仓库 GHCR；版本 tag 构建同时推浮动镜像 `:internal-custom` | `internal-only` | 不适用 | `556468079`；`7bbbaf593` | 2026-09-14 | [部署台账 §12](patches/2026-08-15-langfuse-local-deployment.md#12-2026-09-14-镜像发布改走-ghcr重置卡持卡人用户名) |
| —（直接提交） | 渠道开关：Claude Messages 对话内 system 消息内联为 `<system-reminder>`，保住 DeepSeek 等上游前缀缓存 | `internal-only` | 未提交 | `62c9e13c8` | 2026-11-05 | [详情](patches/feat-inline-claude-system-messages.md) |
| —（直接提交） | 流式：客户端在协议终止事件（如 `message_stop`）之后、上游 EOF 之前断开时按 `eof` 结束，不再误记 `client_gone` | `upstream-pending` | 未提交 | `ea09002bc`；rc.49 改写于 `c334b1f5e` | 2026-11-05 | [详情](patches/fix-stream-client-close-after-complete.md) |
| —（直接提交） | 计费表达式 `is_workday(tz)`：按小米节假日 API 同步的中国大陆节假日/调休日历判断工作日，用于峰谷计价 | `internal-only` | 未提交 | `d4e7f7c18` | 2026-11-09 | [详情](patches/feat-cn-holiday-workday.md) |

## 已归档

| 分支 | 摘要 | 归档原因 | 归档日期 | 详情 |
| -- | -- | -- | -- | -- |
| `feat/anthropic-messages-cache-usage` | Anthropic 客户端缓存用量口径 | `upstream-merged`：#7137 / #7170 默认扣除缓存并修正流式 usage；移除内部开关和转换实现 | 2026-09-09 | [详情](patches/feat-anthropic-messages-cache-usage.md) |
| `fix/oai-to-claude-tool-call-stream` | 末帧工具参数与重复 start 修复 | `upstream-merged`：#7137 / #7170 的工具状态机覆盖原修复，保留 3 个行为回归测试 | 2026-09-09 | [详情](patches/fix-oai-to-claude-tool-call-stream.md) |
| —（backport） | 异步任务退款时同步减少 `used_quota` | 上游已收编：同步 rc.30 时确认 `58d4e9bd3` 即合并 merge-base，语义一致无残留 | 2026-09-01 | [详情](patches/backport-6795-async-task-refund-used-quota.md) |

## 2026-11-09 同步上游 v1.0.0-rc.49

### 范围

- 上游 rc.44 → rc.49：137 个提交、558 个文件；双方都改过的文件 72 个，其中 20 个冲突（Go 与仓库根文件 11、前端 9，含 7 个语言文件），其余自动合并。自动合并文件逐个比对了内部改动行数（合并前相对 rc.44、合并后相对 rc.49），除下面主动调整的文件外完全一致；`text_quota.go` / `tiered_settle.go` 变少是因为上游已有同样的 12h 缓存与 Claude 缓存写入处理，以及结构体重新对齐。
- 自动合并带来一处编译问题：`relaykit/.../to_claude_messages_resp_test.go` 双方都加了 `kitutil` import，删掉重复的一行。

### 逐项核对

| 内部改动 | 上游是否已有等价实现 | 冲突与适配 |
| --- | --- | --- |
| Langfuse 追踪 | 无 | `doRequest` 里 `langfuse.BeginAttempt` 放在上游新加的 `ContextKeyUpstreamRequestOrigin` 之后；`go.mod` 保留 OTel 1.44.0 直接依赖 |
| 渠道 `cache_prompt_token_semantic` | 无 | 见下方「计费」 |
| 剥离 Claude 计费头 / 对话内 system 消息内联 | 无 | 渠道抽屉保留两个开关；上游去掉了 passthrough 字段的「高级自定义渠道除外」条件，照上游 |
| Exchange Key | 无 | `GetOptions` 过滤条件并入上游新增的 `ResendKeyConfigured` |
| 每用户并发 IP 限制 | 无 | 上游 rc.45+ 给 Redis 用户缓存加了 `CacheExpiresAt` / `CacheMAC` 完整性校验；`ConcurrentIpLimit` 写入哈希（Lua `ARGV[15]`），带 JSON tag，所以被 MAC 覆盖。内部缓存 schema 仍为 v3，旧缓存因无 MAC 被拒后从数据库重建。任务插件协议路由改用上游 `RouteTaskPluginEndpoint`，`openai_video.retrieve` 的 `UserIpLimit()` 放在上游新加的原生任务查询转交之前；上游新增的插件原生路由在自动合并后的 `plugin-router.go` 里已带 `UserIpLimit()` |
| 日志毫秒耗时 | 无 | 错误日志保留毫秒，并接上上游新增的上游 URL 脱敏参数；CSV 导出测试两边用例都保留 |
| 流式完成后客户端断开 | 部分：上游 #7956 给 Responses / Dify 加了「完成事件写出后 `sr.Done()`、全部 goroutine 退出后再纠正取消」的机制，Claude `message_stop` 没有接入 | 见下方「流式」 |
| 订阅重置卡、用量统计缓存口径、节假日计价、GHCR 发布 | 无 | 自动合并，测试通过 |

### 计费（表达式计费与倍率计费都有变化）

1. 上游（#7965、94cc3ff09）：OpenAI 12 小时缓存写入有自己的价格（表达式变量 `cc12h`，倍率 `CacheCreationRatio12h`），没单独定价时按 `cc` 的 2 倍计；Claude 语义下缓存写入总量缺 5m/1h 拆分时，未标注部分按 5m 计；**表达式没有给 `cc` 单价时，Claude 语义的缓存写入现在按输入价并入 `p`**（此前不收费）。这一条直接影响 8850 上 Anthropic 语义、表达式里没写 `cc` 的模型，上线后这些请求的费用会上升。
2. `BuildTieredTokenParams` 保留内部 `promptTokensIncludeCache` 参数，分三支：Claude 语义（口径不含缓存）照上游；含缓存口径照上游 GPT 分支（含 12h）；**声明 `prompt_excludes_cache` 的非 Claude 渠道**与 Claude 语义保持一致：未定价的缓存读、缓存写都并入 `p`（此前只并缓存读），12h 写入未定价时按 2 倍并入 `cc`。`len` 不变。
3. 倍率计费（`text_quota.go`）：含缓存口径用上游的 12h 拆分；声明不含缓存的非 Claude 渠道同样按 12h 倍率单算。`hasBillableUsage` 除 Claude 语义外，也把「口径不含缓存且只有缓存用量」的请求算作可计费。
4. 测试：`TestBuildTieredTokenParams_PromptCacheInclusionFlag` 的「declared exclusive」用例改为缓存读写都并入（P=1300），新增「`cr` 有单价时只并入写入」（P=1100）；上游新加的 3 处测试调用补上口径参数（`!isClaude`）。

### 流式

内部 `ea09002bc` 原来在主循环的客户端断开分支里直接判断「响应已完成 → 记 eof」。这会抢在上游 #7956 的纠正逻辑之前，并把 Responses「完成事件还没写出客户端就断开」也记成 eof，上游 `TestOaiResponsesStreamHandlerCompletionLifecycle` 有 4 个用例失败。现在改为：断开分支照上游记 `client_gone`；所有 goroutine 退出后，响应已完成、结束原因是 `context.Canceled`、且没有记录错误时，调用过 `sr.Done()` 的协议记 `done`（上游原逻辑），没调用的（Claude 等）记 `eof`（内部原意）。上游用例与内部 `TestStreamScannerHandler_ClientCloseAfterCompletedIsNormalEnd` 都通过；把 eof 分支改回 `client_gone` 后内部用例失败，确认它仍守着这条行为。

### 浮动镜像 tag

`:internal-custom` 镜像表示"修改版最新版"，此前靠推同名 git tag `internal-custom` 触发构建，`release.yml` 因此也会发一个同名 Release，而且 tag 与分支同名，本地 git 命令会报 ambiguous。`7bbbaf593` 改为每次版本 tag 构建时，用该 tag 自己的两个架构镜像同时生成 `:latest` 与 `:internal-custom` 清单并签名。以后不再打或移动 `internal-custom` git tag，[部署台账 §17](patches/2026-08-15-langfuse-local-deployment.md#17-2026-11-06-打-tag-v100-rc44-internal20261106) 里的移动方法作废。本地 tag 已删除；`enterprise` 上的同名 tag 与 Release 待确认后删除。手动分支构建（`docker-image-branch.yml`）跑 `internal-custom` 分支时仍会写 `:internal-custom`，需要时注意先后顺序。另外 `/api/status` 版本号会显示具体版本 tag，不再是 `internal-custom`。

### 部署注意

1. **计费**：见上方「计费」第 1、2 条。上线前应检查 8850 上走 Anthropic 语义或声明 `prompt_excludes_cache`、且表达式没写 `cc` 的模型，决定是补 `cc` 单价还是接受按输入价收缓存写入。
2. **Redis 鉴权缓存完整性**：MAC 用 `CRYPTO_SECRET` 派生，多个节点共用 Redis 时 `CRYPTO_SECRET` 必须一致，否则缓存反复校验失败、回落数据库。升级后旧缓存全部失效一次。
3. **数据库**：GORM 组版本不变。`audit_logs`（日志库）新增联合索引 `idx_audit_time_event`，`quota_data` 新增 `cache_read_tokens`、`cache_creation_tokens` 两列，大表上建索引会有锁表时间，部署前照例先做逻辑备份。
4. **构建工具**：上游 `go.mod` 要求 Go 1.26.10；前端改用 Bun 1.4.4 的 `bun check`。本机 Bun 是 1.3.14，`bun run typecheck` 跑不起来，本次用同一套 `tsgo -b` 代替；Dockerfile 用的是上游自己的 Bun 版本。
5. **其他上游变化**：Resend 邮件服务商、通知按类型开关、渠道优先级拖拽、DeepSeek V4.1 Flash / 搜索与抓取、任务插件按厂商原生路径提供服务、普通渠道参与插件共享端点竞争、API Key 可读取所属用户额度、HTTP/2 健康检查（可选）等。

### 验证

| 命令（仓库根目录，另有注明除外） | 结果 |
| --- | --- |
| `go build ./...`、`go vet ./...` | 通过 |
| `go test ./...` | 52 个包通过；不把未配置 DSN 时跳过的集成测试计为数据库验证 |
| `cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...` | 通过（9 个包） |
| `cd tokenkit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...` | 通过 |
| `go test -race ./service/langfuse/...` | 通过（324.480s） |
| `cd web && bun install --frozen-lockfile` | 通过，lockfile 不变 |
| `cd web && ./node_modules/.bin/tsgo -b`（`bun check -b` 的替代，见部署注意第 4 条） | 通过 |
| `cd web && bun run test` | 218 个文件、2615 个用例通过 |
| `cd web && bun run lint` | 未通过：151 个 error；同一 oxlint 配置下纯 rc.49 为 152 个，忽略行号比对后内部没有新增 error（少的一个仍是 `pricing-section.tsx` 嵌套三元） |
| `cd web && bun run build` | 通过，产物约 67.7 MB / gzip 20.8 MB |
| `cd web && bun run i18n:sync` | 七种语言 missingCount=0；生成的 `_reports/*.untranslated.json` 改动已还原 |

### 数据库矩阵

只使用本机新建的 `newapi-rc49-review-*` 容器和会话临时目录里的 SQLite 文件，没有连接生产库。同一份检查程序分别编译为合并前内部版 `f475dbbb8`、上游 `v1.0.0-rc.49`、合并版 `c334b1f5e` 三个二进制。每个用例先用一个版本建库并写入代表性数据，再用合并版连续启动两次（`InitDB` → `authz.Init` → `MigrateRetiredFrontendOptions` → `InitOptionMap` → 独立日志库 `InitLogDB`）。fresh 用合并版建库，internal 用合并前内部版，upstream 用 rc.49，legacy 用内部版建库后把 `idx_users_aff_code` 唯一索引换成同名 UNIQUE 约束（模拟 pgloader 遗留）。

| 引擎 | 实测版本 | fresh | internal 升级 | upstream 升级 | legacy |
| --- | --- | --- | --- | --- | --- |
| SQLite | 3.50.4（Go driver 实测） | 通过 | 通过 | 通过 | — |
| MySQL | 8.4.5 | 通过 | 通过 | 通过 | — |
| MySQL | 5.7.44 | 通过 | 通过 | 通过 | — |
| PostgreSQL | 15.17 | 通过 | 通过 | 通过 | 通过 |
| PostgreSQL | 9.6.24 | 通过 | 通过 | 通过 | 通过 |

每格检查：用户额度和中文、token 额度、渠道设置 JSON（含内部的剥离计费头、system 内联开关）、prefill JSON、decimal 价格、`quota_data` 与审计日志行、`HolidayCalendar` 等四个内部 option、分离日志库里带 `duration_ms` 的日志都保留；用户名、`aff_code`、token key、prefill name、PAT `token_hash`、审计 `event_id` 的重复插入仍被拒；合并后 `quota_data` 两个新列和 `audit_logs.idx_audit_time_event` 存在；内部建库的 `users.concurrent_ip_limit` 值与 `subscription_reset_cards` 行保留，从上游库升级时这两项被补建；两次启动后完整 schema 元数据一致，种子阶段已有的索引和约束全部还在；legacy 用例的遗留约束被删除并重建为唯一索引。MySQL 5.7.8 这一最小补丁版本仍未单独验证（同前两次记录）。验证程序和驱动脚本只放在会话临时目录，未写入仓库；四个测试容器和三个临时 worktree 已删除。

### 遗留

- rc.44 记录里的遗留项都还在：Langfuse 不追踪 Responses WebSocket；`controller.Relay` panic 时不退预扣；任务产物下载的 Bearer 回退未接 IP 限制；`controller/option.go` 两条中文硬编码提示。
- Claude 流没有接入上游的 `sr.Done()` 机制，所以「`message_stop` 已读到但还没写给客户端时断开」仍会记成 eof，与 rc.44 时的行为一样。要彻底区分，需要让 Claude 流处理在写出 `message_stop` 后调用 `sr.Done()`，可以和该修复一起提上游。

## 2026-10-13 同步上游 v1.0.0-rc.44

### 范围

- 上游 rc.36 → rc.44：219 个提交、1189 个文件；双方都改过的文件 80 个，其中 40 个冲突（后端与仓库根文件 27、前端 13），其余自动合并，靠编译、测试和重点抽查（`model/user.go`、`model/option.go`、`controller/option.go`、共享 `doRequest` 等）确认。
- 内部：合并前相对 rc.44 的 108 个非 merge 提交。`enterprise` 远端的 `internal-custom` 领先本地 4 个提交（重置卡持卡人用户名、GHCR 发布及其台账），已先快进再合并；`gitlab` 远端本机无访问权限，未抓取。

### 逐项核对

| 内部改动 | 上游是否已有等价实现 | 冲突与适配 |
| --- | --- | --- |
| Langfuse 追踪 | 无；上游只有 otel 1.41.0 间接依赖，内部 1.44.0 直接依赖覆盖 | `controller/relay.go` 并入上游新的预扣/退款 defer，顺序契约改由新测试锁定；rc.38 的 Responses WebSocket 不产生 trace（已知缺口） |
| 渠道 `cache_prompt_token_semantic` | 无 | `BuildTieredTokenParams` 同时保留内部口径参数和上游 `img_cr`、缓存读并入规则，见下方「行为变化」第 1 条 |
| 剥离 Claude 计费头 | 无；上游 rc.42 反向为 DashScope 原生 Claude 请求注入同名头，路径不相交 | 转换函数新增 `ctx` 参数；add/add 测试文件合并；开关移植到重写后的渠道抽屉 |
| Exchange Key | 无；rc.41「访问令牌」是面板 PAT，不是 relay 凭据 | `TokenAuthReadOnly` 改用上游响应形状；PAT 路由规则，`PUT /api/option/exchange-key` 仅限登录会话 |
| 每用户并发 IP 限制 | 无；rc.38「请求策略」只管亲和、重试、渠道健康 | 补挂到 Responses WebSocket（握手与每个事件）、`/v1/tasks`、插件动态任务路由、`openai_image` 任务插件链 |
| 日志毫秒耗时 | 无；`use_time` 仍为整秒 | 日志正文结构化后的签名合并；错误日志随上游挪到 `service/relay_error.go`；rc.44 CSV 导出改用毫秒 |
| 订阅重置卡 | 无；上游只有管理员按套餐重置订阅 | 后端 i18n 键改英文源文本并补七语前端翻译；使用记录改为 `common.NewMessage`；PAT 规则 |
| 用量统计缓存口径 | 无 | 接线点自动合并，测试并集 |
| GHCR 镜像发布 | 不适用 | 上游未改两个 docker workflow，自动合并后仍推 `ghcr.io/<owner>/<repo>` |
| 已归档三项 | — | 结论不变 |

各条目的冲突细节写在对应详情文档的「2026-10-13 rc.44 核对」一节。

### 行为变化与部署注意

1. **计费（只涉及表达式计费，倍率计费不变）**：`BuildTieredTokenParams` 现在完全按渠道声明的原始口径归一化。
   - 声明 `prompt_excludes_cache` 的渠道（以及 legacy Claude 派生用量）：表达式没给 `cr` 单价时，缓存读按输入价并入 `p`（此前不收费，跟随上游 `f064bffa2` 对 Anthropic 语义的同一修正）；`len` 加回缓存读写，按完整上下文选档（此前只算未缓存部分，长上下文档会被低估）；图片、音频输入仍在 prompt 里，表达式单独定价时从 `p` 扣除（此前重复计费）。
   - 声明 `prompt_includes_cache` 但上游回 Anthropic 语义用量的渠道：按含缓存口径归一化。此前表达式路径忽略这一声明，`cr` 单独定价时缓存读被重复计费；合并上游的并入规则后，`cr` 未定价时也会重复（P 从 1000 变 1800）。这一条由合并后的独立复核发现，已在 `e886f45ac` 修复，`a9fddc117` 构建的镜像含此问题，不应部署。
   - 后两类是 rc.36 起就存在的问题，按 `pkg/billingexpr/expr.md` 的自动排除与 `len` 规则一并修正；未声明口径的渠道行为不变。
2. **访问令牌（rc.41）**：面板 PAT 对未声明的路由默认拒绝，已为 12 条内部路由声明规则（Langfuse/Exchange Key 设置读 `option:read`；两者的写入仅限登录会话，因为 Exchange Key 能为任意用户派生 relay key、Langfuse 配置决定全部对话内容导出到哪里；清除活动 IP `user:write`、重置卡管理 `billing:*`、用户自助 `wallet:*`）。旧版单一面板令牌升级后 30 天内仍可用，之后被拒绝，依赖它的内部脚本需提前换新令牌。
3. **i18n（rc.43）**：面板接口消息改为英文源文本、由前端翻译。新增环境变量 `DEFAULT_LANGUAGE`（默认 `en`），决定服务端日志和未声明语言时后端消息的语言，内部部署可按需设为 `zh-CN`。重置卡使用记录从中文硬编码改为英文 key + `content_parts`，前端按查看者语言渲染；历史日志文本不变。
4. **IP 限制覆盖面扩大**：上面列出的几条任务/插件/WebSocket 链路开始计入同一滑动窗口，现网若开启了全局限制，这些调用也会受约束。
5. **数据库**：GORM 组版本未变（core 1.25.12、mysql 1.5.7、postgres 1.5.9、glebarez/sqlite 1.11.0）。上游 rc.38 的 PostgreSQL 迁移器会按 catalog 名删除与 `uniqueIndex` 字段冲突的单列 UNIQUE 约束（`ACCESS EXCLUSIVE` 锁表），随后 GORM 重建唯一索引；`users.username`、`top_ups.trade_no` 等裸 `unique` 字段不受影响。生产库来自 pgloader，已用模拟的遗留约束验证（见下方 legacy 用例），部署前仍应先做逻辑备份，并用 `SELECT conrelid::regclass, conname FROM pg_constraint WHERE contype = 'u'` 看清会被处理的约束。
6. **其他上游变化**：构建需要 Go 1.26（上游 `go.mod`、Dockerfile 已改）；rc.44 起普通用户的日志查询、导出和看板限制在最长 365 天内（日志维护可调）；游乐场数据按账号存储，升级后清空一次；任务插件运行时由 Sobek 换为 moejs。

### 验证

| 命令（仓库根目录，另有注明除外） | 结果 |
| --- | --- |
| `go build ./...`、`go vet ./...` | 通过 |
| `go test ./...` | 通过（48 个包）；不把未配置 DSN 时跳过的集成测试计为数据库验证 |
| `cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...` | 通过 |
| `cd tokenkit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...` | 通过（rc.39 起的新独立模块） |
| `go test -race ./service/langfuse/...` | 通过（318.191s） |
| `cd web && bun install --frozen-lockfile` | 通过，lockfile 不变 |
| `cd web && bun run typecheck` | 通过 |
| `cd web && bun run test` | 201 个文件、2359 个用例通过 |
| `cd web && bun run lint` | 未通过：163 个 error，同一 oxlint 配置下纯 rc.44 为 164 个；忽略行号比对后内部没有新增 error（少的一个是内部改过的 `pricing-section.tsx` 嵌套三元） |
| `cd web && bun run build` | 通过，产物约 67.5 MB / gzip 20.8 MB |
| `cd web && bun run i18n:sync` | 七种语言 missingCount=0 |

新增回归用例均做过反向确认：把 `PUT /api/option/exchange-key`、`PUT /api/option/langfuse` 临时放宽为 `option:write` 后 `TestSecretAndExportSettingWritesRequireDashboardSession` 失败；在 `a9fddc117` 的 `tiered_settle.go` 上，`TestBuildTieredTokenParams_PromptCacheInclusionFlag` 新增的三个用例与 `len` 断言失败（P=1200、1000、1000，len=1000）；在上游原 `export.ts` 上运行新增的 CSV 毫秒用例失败。

### 数据库矩阵

只使用本机新建的 `newapi-rc44-review-*` 容器和会话临时目录里的 SQLite 文件，没有连接生产库。每个用例先用一个版本的程序建库并写入代表性数据，再用合并后的程序连续启动两次（`InitDB` → `authz.Init` → `MigrateRetiredFrontendOptions` → `InitOptionMap` → 独立日志库 `InitLogDB`）。种子程序：fresh 为合并版，internal 为合并前的内部版 `f8775ede8`，upstream 为 `v1.0.0-rc.44`，legacy 为内部版建库后把 `idx_users_aff_code` 唯一索引换成同名的 UNIQUE 约束（模拟 pgloader 遗留）。

| 引擎 | 实测版本 | fresh | internal 升级 | upstream 升级 | legacy |
| --- | --- | --- | --- | --- | --- |
| SQLite | 3.50.4（Go driver 实测） | 通过 | 通过 | 通过 | — |
| MySQL | 8.4.5 | 通过 | 通过 | 通过 | — |
| MySQL | 5.7.44 | 通过 | 通过 | 通过 | — |
| PostgreSQL | 15.17 | 通过 | 通过 | 通过 | 通过 |
| PostgreSQL | 9.6.24 | 通过 | 通过 | 通过 | 通过 |

每格检查：用户额度和中文、token 额度、渠道设置 JSON、prefill JSON、decimal 价格、四个内部 option、分离日志库里带 `duration_ms` 的日志保留；用户名、`aff_code`、token key、prefill name、PAT `token_hash` 的重复插入仍被拒；内部的 `users.concurrent_ip_limit`、`subscription_reset_cards` 与上游的 `user_access_tokens` 在合并后同时存在，各自的种子数据保留；两次启动后的完整 schema 元数据一致，种子阶段已有的索引和约束全部还在。legacy 用例确认合并版删除了遗留约束、重建了唯一索引 `idx_users_aff_code`。

另外 `TestMigrationSchemaStability` 在 SQLite 与 MySQL 8.4.5 / PostgreSQL 15.17、MySQL 5.7.44 / PostgreSQL 9.6.24 上实际执行通过，没有被 DSN 跳过。MySQL 5.7.8 这一最小补丁版本仍未单独验证（同 rc.36 记录，只覆盖 5.7 系列的 5.7.44）。验证程序和驱动脚本只放在会话临时目录，未写入仓库，会话结束后不保留；四个测试容器已删除。

### 遗留

- Langfuse 不追踪 Responses WebSocket 会话。
- `controller.Relay` 在业务代码 panic 时不退还预扣额度：退款 defer 只看 `newAPIError`，panic 时它为 nil。上游 rc.44 与合并前的内部版本同样如此，本次未改。
- 任务产物下载 `TokenOrTaskArtifactAccessAuth` 的 Bearer 回退未接 IP 限制。
- `controller/option.go` 里两条拒绝通用接口写入 Langfuse / Exchange Key 配置的提示仍是中文硬编码，未按 rc.43 规范改成英文 key。
- `web/src/i18n/locales/_reports/*.untranslated.json` 是 `i18n:sync` 的生成物，已在 `web/.gitignore` 中，但早先被内部提交跟踪；本次没有动。

## 工作流

### 新增一个内部改动

```bash
git fetch upstream
git switch -c feat/xxx main     # 一定从 main 切，不要从 internal-custom 切
# 开发、自测
git switch internal-custom
git merge --no-ff feat/xxx -m "Merge feat/xxx into internal-custom"
```

合并后立刻做两件事：从 `patches/_template.md` 复制一份 `patches/feat-xxx.md` 填好，并在上面的表里加一行。台账更新单独一个 `docs(internal): ...` 提交即可，不要混进功能提交。

如果这个改动要提给上游，PR 正文草稿写在根目录未跟踪的 `PR_NOTICE.md` 里（`.gitignore` 之外但不提交），PR 开出来之后把链接补回台账条目。

### 同步上游

```bash
git fetch upstream tag <target-tag>
git switch main && git merge --ff-only <target-tag>
git switch internal-custom && git merge --no-ff main
```

合完之后**逐条走一遍台账**：

1. 上游是否已经收编了某条 patch（通常表现为合并时该文件冲突，且上游侧已有等价实现）。是的话把内部副本改回上游实现，条目状态改 `upstream-merged` 并移入已归档。
2. 剩下的 `internal-only` 条目，确认「与上游的冲突风险」一节里记的文件有没有被上游大改，有的话更新该节。

### 移除一条已被上游收编的 patch

先确认内部实现和上游实现语义一致：

```bash
git diff main:<file> internal-custom:<file>
```

差异清干净后，条目保留文件、改状态、填归档日期和原因，从「生效中」表移到「已归档」表。不要删除详情文档，历史部署可能还需要回查。
