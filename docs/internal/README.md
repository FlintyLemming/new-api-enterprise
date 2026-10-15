# internal-custom 台账

内部长期分支 `internal-custom` 的改动总台账。每一条内部 patch 都在 `patches/` 下有一份详情文档，本文件是索引和流程约定。

## 当前基线

2026-10-13 同步至 `v1.0.0-rc.44`（`633b4c5b1`，GitHub Latest release），合并提交 `04f2b55c5`，详见下方 [rc.44 同步记录](#2026-10-13-同步上游-v100-rc44)。合并前先把本地 `internal-custom` 快进到 `enterprise/internal-custom`（`f8775ede8`，含 2026-09-14 远端会话提交的重置卡持卡人用户名 `286e1bd2c` 与 GHCR 镜像发布 `556468079`），备份为 `backup/internal-custom-before-rc44-20261013`；本地 `main` 快进至该 tag。`internal-custom` 与 `main` 已推送到 GitHub `enterprise`；GitLab 未推送（本机没有可用的 GitLab SSH 凭据）。随后手动触发 GHCR 镜像构建，结果记在[部署台账 §13](patches/2026-08-15-langfuse-local-deployment.md#13-2026-10-13-rc44-镜像构建)。2026-10-15 已部署到 8850（`:internal-custom-20261013-bd8600f`），彩排、切换与验证见[部署台账 §14](patches/2026-08-15-langfuse-local-deployment.md#14-2026-10-15-rc44-镜像上线)。

上一基线：2026-09-09 同步 `v1.0.0-rc.36`（合并提交 `a72e8c116`，见 [rc.36 同步与数据库验证记录](sync-rc36-2026-09-09.md)），8850 环境此前运行 `new-api:rc36-e6b8fc10d`（[部署台账 §11](patches/2026-08-15-langfuse-local-deployment.md#11-2026-09-12-重置卡--缓存统计口径镜像上线)）。

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
| —（直接提交） | 镜像发布 workflow 改推本仓库 GHCR | `internal-only` | 不适用 | `556468079` | 2026-09-14 | [部署台账 §12](patches/2026-08-15-langfuse-local-deployment.md#12-2026-09-14-镜像发布改走-ghcr重置卡持卡人用户名) |

## 已归档

| 分支 | 摘要 | 归档原因 | 归档日期 | 详情 |
| -- | -- | -- | -- | -- |
| `feat/anthropic-messages-cache-usage` | Anthropic 客户端缓存用量口径 | `upstream-merged`：#7137 / #7170 默认扣除缓存并修正流式 usage；移除内部开关和转换实现 | 2026-09-09 | [详情](patches/feat-anthropic-messages-cache-usage.md) |
| `fix/oai-to-claude-tool-call-stream` | 末帧工具参数与重复 start 修复 | `upstream-merged`：#7137 / #7170 的工具状态机覆盖原修复，保留 3 个行为回归测试 | 2026-09-09 | [详情](patches/fix-oai-to-claude-tool-call-stream.md) |
| —（backport） | 异步任务退款时同步减少 `used_quota` | 上游已收编：同步 rc.30 时确认 `58d4e9bd3` 即合并 merge-base，语义一致无残留 | 2026-09-01 | [详情](patches/backport-6795-async-task-refund-used-quota.md) |

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
