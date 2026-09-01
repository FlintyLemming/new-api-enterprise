# feat/strip-claude-system-prefix

| 项 | 值 |
| -- | -- |
| 分支 | `feat/strip-claude-system-prefix` |
| 基线 | `internal-custom` @ `8eb884f4` |
| 合入 commit | 线性合入（无 merge commit），tip `a5dfe631` |
| 合入日期 | 2026-08-17 |
| 状态 | `internal-only` |
| 上游 PR | 未提交 |

## 为什么要这个改动

Claude Code 每个 `/v1/messages` 请求都把变化的 `x-anthropic-billing-header: cc_version=...; cch=<每次不同>;` 放在 `system[0]`。new-api 转成 OpenAI chat 时把所有 system 段拼成一条 string，这段头变成发给本地 DeepSeek 的第一个 token，前缀缓存从第二块起全部失效，命中钉死在约 17920 token。

## 改动内容

新增渠道设置 `strip_anthropic_billing_header`，默认关闭。打开后只在 Claude Messages → 非 Claude 上游、把 system 压成一条 string 时丢掉这份已知计费头：

- `relaykit/dto/channel_settings.go`、`relaykit/relayconvert/convmeta/options.go`、`relay/common/relay_info.go` — 开关持久化并拷进转换选项
- `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go` — 扁平化前按写死前缀丢块 / 丢字符串第一行；OpenRouter `anthropic/claude*` 分块路径不过滤
- `web/src/features/channels/**`、`web/src/i18n/locales/*.json` — 渠道编辑抽屉开关及七语文案
- `docs/channel/other_setting.md` — 第 5 项说明

不改 `ClaudeRequest.System` 原文、不改计费、不改 Reasonix / 原生 `/v1/chat/completions`、不处理 `pass_through_body`。

## 部署影响

- 无数据库迁移。默认关，现网行为不变。
- 镜像上线后，给 channel 5 `[H200] Deepseek V4 Flash` 勾上此开关。同一渠道上的 Reasonix 走 `/v1/chat/completions`，没有这段头，不受影响。
- 回滚：关掉渠道开关即可，不必回滚镜像。

## 与上游的冲突风险

`relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go` 的 system 拼接是上游会改的路径；`relaykit/dto/channel_settings.go` 的字段列表也常被上游追加。同步上游时保留本开关与过滤，并确认扁平化循环没有被重写成另一条路径而绕过 `shouldStripClaudeSystemText`。

前端 `channel-mutate-drawer.tsx` 和七个 locale 文件属于机械冲突，取并集即可。

## 验证方式

```bash
cd relaykit && GOWORK=off go test ./dto ./relayconvert/internal/claude_messages ./relayconvert
go test ./relay/common -run TestRelayInfoConvOptionsCopiesStripAnthropicBillingHeader
cd web && bun run typecheck
```

手工验证：同一条 H200 DeepSeek 渠道打开开关后，Claude Code 连续多轮的上游 system 前缀以 `You are Claude Code` 或主 system 开头，且不含 `x-anthropic-billing-header`。

## 退出条件

若上游 new-api 自己提供等价过滤，或 Claude Code 不再发送该头，删掉本开关及相关文案，状态改为 `dropped` 或 `upstream-merged`。
