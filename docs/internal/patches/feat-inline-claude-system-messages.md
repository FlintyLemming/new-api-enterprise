# feat/inline-claude-system-messages

| 项 | 值 |
| -- | -- |
| 分支 | 直接提交到 `internal-custom` |
| 基线 | `internal-custom` @ `2420a9efa` |
| 合入 commit | 见 README 生效中的 patch 表 |
| 合入日期 | 2026-11-05 |
| 状态 | `internal-only` |
| 上游 PR | 未提交 |

## 为什么要这个改动

2026-11-05 排查 zhangran 在 channel 44 `[H200] DeepSeek V4.1 Flash Native Dual`（vLLM，`/v1/messages` 原生透传）上的缓存率：同渠道其他用户 96–98%，zhangran 约 30–40%，缓存写入占 prompt 58%。Langfuse `events_full` 里对比同一会话相邻两轮请求：system、tools、历史消息完全相同、只追加，但 Claude Code（2.1.285）每轮在 `messages` 里插入一条 `role: "system"` 消息（`<total_tokens>N tokens left</total_tokens>`、`# Environment update ...`）。

DeepSeek 的 chat template 把所有 system 消息提到 prompt 开头，于是每轮新增的 system 消息改变了前缀：主对话请求读缓存一直停在约 5.5k（≈ 原 system 提示词 + 之前累积的 system 消息），其余 10 万级历史每轮重写。直连 vLLM 复现：只改末尾 system 消息，cache_read 3200 / creation 11264；不带 system 消息只改末尾 user，cache_read 14400 / creation 0。

## 改动内容

新增渠道设置 `inline_claude_system_messages`，默认关闭。打开后，`relay/claude_handler.go` 在交给 adaptor 转换前调用 `ClaudeRequest.InlineSystemMessages()`：

- system 消息的文本包成 `<system-reminder>\n…\n</system-reminder>` 文本块，追加到前一条 user 消息（string content 转成 text 块）；前一条不是 user（或位于开头）时成为单独一条 user 消息；非 text 块原样跟在后面
- 没有文本的 system 消息（per-message effort）丢弃
- 每条消息的改写只依赖它自身和前一条消息，历史轮次在之后每次请求中渲染一致；Claude Code 下一轮把同一条 system 消息从块数组改成 string 也不影响结果

文件：

- `relaykit/dto/claude_system_messages.go` / `_test.go` — 改写逻辑与用例（含前缀稳定性用例）
- `relaykit/dto/channel_settings.go`、`relay/claude_handler.go` — 开关与接线
- `web/src/features/channels/**`、`web/src/i18n/locales/*.json` — 渠道抽屉「Request processing」块开关及七语文案
- `docs/channel/other_setting.md` — 第 5 项说明

不改计费、不改客户端原始请求（`info.Request` 已在 handler 中 DeepCopy）、不处理 `pass_through_body`。真实 Anthropic 上游原生支持对话内 system 消息，不要在这类渠道打开。

## 部署影响

- 无数据库迁移。默认关，现网行为不变。
- 镜像上线后给 channel 44 打开此开关。
