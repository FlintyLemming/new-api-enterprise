# internal-custom 台账

内部长期分支 `internal-custom` 的改动总台账。每一条内部 patch 都在 `patches/` 下有一份详情文档，本文件是索引和流程约定。

## 分支模型

| 分支 | 远端 | 定位 |
| -- | -- | -- |
| `main` | `origin`（github.com/QuantumNous/new-api） | 上游镜像，**不放任何内部改动**，只做快进同步 |
| `internal-custom` | `git.mitsea.com` | 内部部署实际使用的分支，= `main` + 若干 patch 分支的 merge |
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
| `feat/anthropic-messages-cache-usage` | 渠道开关：Anthropic Messages 的 `input_tokens` 可扣除缓存 | `upstream-pending` | 未提交 | `4abaa43a9` | 2026-08-13 | [详情](patches/feat-anthropic-messages-cache-usage.md) |
| `feature/langfuse-tracing` | 可选的 Langfuse OTLP 对话追踪导出（默认关闭） | `upstream-pending` | 未提交 | 线性合入，tip `8eb884f4` | 2026-08-16 | [详情](patches/feature-langfuse-tracing.md) |
| `feat/strip-claude-system-prefix` | 渠道开关：Claude→非 Claude 转换时剥离 Claude Code 计费头，保住前缀缓存 | `internal-only` | 未提交 | 线性合入，tip `a5dfe631` | 2026-08-17 | [详情](patches/feat-strip-claude-system-prefix.md) |
| `feat/exchange-key` | 内部应用用共享 SECRET 派生 per-user HMAC key 调 relay | `internal-only` | 未提交 | 线性合入，tip `e4d5ea76` | 2026-08-23 | [详情](patches/feat-exchange-key.md) |
| `feature/user-concurrent-ip-limit` | 每用户滑动窗口内并发客户端 IP 数限制（含白名单与每用户覆盖） | `upstream-pending` | 未提交 | 线性合入，tip `7ddc7866` | 2026-08-24 | [详情](patches/feat-user-concurrent-ip-limit.md) |
| `fix/oai-to-claude-tool-call-stream` | OAI→Claude 流式：finish chunk 保留 tool 参数、tool block start 去重 | `upstream-pending` | 未提交 | `527d6fca` | 2026-09-01 | [详情](patches/fix-oai-to-claude-tool-call-stream.md) |
| `fix/log-duration-ms` | 日志总耗时毫秒精度：`other.duration_ms` + 前端回退 `use_time` | `upstream-pending` | 未提交 | `0da33748` | 2026-09-01 | [详情](patches/fix-log-duration-ms.md) |

## 已归档

| 分支 | 摘要 | 归档原因 | 归档日期 | 详情 |
| -- | -- | -- | -- | -- |
| —（backport） | 异步任务退款时同步减少 `used_quota` | 上游已收编：同步 rc.30 时确认 `58d4e9bd3` 即合并 merge-base，语义一致无残留 | 2026-09-01 | [详情](patches/backport-6795-async-task-refund-used-quota.md) |

## 工作流

### 新增一个内部改动

```bash
git fetch origin
git switch -c feat/xxx origin/main     # 一定从 main 切，不要从 internal-custom 切
# 开发、自测
git switch internal-custom
git merge --no-ff feat/xxx -m "Merge feat/xxx into internal-custom"
```

合并后立刻做两件事：从 `patches/_template.md` 复制一份 `patches/feat-xxx.md` 填好，并在上面的表里加一行。台账更新单独一个 `docs(internal): ...` 提交即可，不要混进功能提交。

如果这个改动要提给上游，PR 正文草稿写在根目录未跟踪的 `PR_NOTICE.md` 里（`.gitignore` 之外但不提交），PR 开出来之后把链接补回台账条目。

### 同步上游

```bash
git fetch origin
git switch main && git merge --ff-only origin/main
git switch internal-custom && git merge main
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
