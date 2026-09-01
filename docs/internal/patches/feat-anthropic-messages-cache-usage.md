# feat/anthropic-messages-cache-usage

| 项 | 值 |
| -- | -- |
| 分支 | `feat/anthropic-messages-cache-usage` |
| 基线 | `origin/main` @ `ccd535ef8` |
| 合入 commit | `4abaa43a9` |
| 合入日期 | 2026-08-13 |
| 状态 | `upstream-pending` |
| 上游 PR | 未提交（正文草稿见根目录 `PR_NOTICE.md`） |

## 为什么要这个改动

OpenAI 兼容上游经常把缓存命中算进 `prompt_tokens`。经 New API 转成 Anthropic Messages 之后，Claude Code 侧看到的 `input_tokens` 里也含缓存，读 token 显示偏高。流式还多一层：`message_start` 先报一版预估 prompt，客户端会拿它和终态 usage 取 max，偏差进一步放大。

## 改动内容

新增渠道设置 `anthropic_messages_exclude_cache`，默认关闭。打开后只影响 OpenAI Chat → Anthropic Messages 这条链路上返回给客户端的 usage：

- `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go` — 终态 `message` / `message_delta` 的 `input_tokens = prompt_tokens - cache_read - cache_creation`，下限 0；流式 `message_start` 的 `input_tokens` / `output_tokens` 置 0，不再发预估值
- `relaykit/dto/channel_settings.go` — 新增开关字段，**不兼容**旧键 `openai_prompt_includes_cache`
- `relaykit/relayconvert/convmeta/options.go`、`relay/common/relay_info.go` — 把开关透传进转换选项
- `web/src/features/channels/**`、`web/src/i18n/locales/*.json` — 渠道编辑抽屉里的开关及七语文案

计费、Chat Completions 原生响应、New API 自身日志仍用原始 OpenAI usage，不受影响。

## 部署影响

- 无数据库迁移。
- 开关键名从 `openai_prompt_includes_cache` 改成了 `anthropic_messages_exclude_cache`，**旧键不再读取**。如果有渠道在改名前配过旧开关，需要重新勾选并保存。
- 建议给 Claude Code 实际在用的 OpenAI 兼容渠道打开。

## 与上游的冲突风险

`relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go` 是上游活跃文件，usage 组装逻辑随时可能重构；`relaykit/dto/channel_settings.go` 的字段列表也常被上游追加。同步上游时优先保留内部实现，但要重新确认上游的 usage 组装顺序没有绕过这里的扣减。

前端 `channel-mutate-drawer.tsx` 和七个 locale 文件属于机械冲突，取并集即可（rc.30 起 `channel_settings.go`/`channel-form.ts` 多了上游的 `task_plugin_key` 字段）。

## 验证方式

```bash
cd relaykit && GOWORK=off go test ./dto ./relayconvert/internal/oai_chat ./relayconvert
go test ./relay/common -run TestRelayInfoConvOptionsCopiesAnthropicMessagesExcludeCache
cd web && bun run typecheck
```

手工验证：同一条带 `cache_control` 的 `/v1/messages` 请求，热请求场景下开关关闭时 `usage.input_tokens` 等于 `prompt_tokens`，开启后应等于 `prompt_tokens - cache_read`，且 `cache_read_input_tokens` 两种情况都不变。实测数据见 `PR_NOTICE.md` 的运行证明一节。

## 退出条件

上游合并同名或等价能力后，确认上游的开关键名与扣减语义一致，改用上游实现并归档本条。若上游明确拒绝，状态改为 `internal-only` 长期保留。
