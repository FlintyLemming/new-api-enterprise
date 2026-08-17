# 渠道开关：剥离 Claude 客户端计费头

日期：2026-08-17
状态：已确认，待实施
分支：`feat/strip-claude-system-prefix`
基线：`internal-custom` @ `8eb884f4`

## 1. 目标

Claude Code 每个 `/v1/messages` 请求都把一段变化的

`x-anthropic-billing-header: cc_version=...; cch=<每次不同>;`

放在 `system[0]`。new-api 转成 OpenAI chat 时把所有 system 段拼成一条 string，这段头变成发给本地 DeepSeek 的第一个 token，前缀缓存从第二块起全部失效。

加一个**默认关闭**的渠道开关。打开后，仅在 Claude Messages → 非 Claude 上游、把 system 压成一条 string 时，丢掉这份已知计费头。渠道画面说明里写明会删哪些头。

成功标准：同一条 H200 DeepSeek 渠道上，Claude Code 连续多轮的上游 system 前缀稳定（以 `You are Claude Code` 或主 system 开头），缓存不再被钉死在约 17920 token。Reasonix / 原生 `/v1/chat/completions` 不受影响。new-api 计费公式不变。

## 2. 不做

- 不提供用户可配的前缀列表或正则。
- 不在转换前改 `ClaudeRequest.System`（预扣估算、Langfuse 原始 body 保持原样）。
- 不做成系统级全局开关。
- 不改计费表达式，不把缓存命中折进 `p`。
- 不靠渠道 `param_override` 顶替。
- 不处理 `pass_through_body`（该模式本就不走转换）。
- 不在 OpenRouter Claude 分块路径上过滤（那条路上 system 仍按块带 `cache_control` 发给 Claude）。

## 3. 范围与匹配

**放置：** 渠道 `setting` JSON，字段

```go
StripAnthropicBillingHeader bool `json:"strip_anthropic_billing_header,omitempty"`
```

默认 `false` / 省略。无数据库迁移。

**何时过滤：** 只在 `ClaudeMessagesRequestToOpenAIChat` 走「拼成一条 system string」的分支。也就是：

- `isOpenRouter && strings.HasPrefix(upstream, "anthropic/claude")` 为假
- 且开关为真

**认头规则（写死，本期只有一条）：**

- 常量前缀：`x-anthropic-billing-header:`（字面大小写，与 Claude Code 实发一致）。
- 数组 system：按块看文本（`GetText()` / 非 nil 的 `Text`），不要求 `Type` 必须是 `"text"`。`TrimSpace(text)` 以该前缀开头 → **整块丢掉**。
- 字符串 system：若 `TrimSpace(s)` 以该前缀开头，去掉第一行（到第一个 `\n`，含换行；没有换行则整段视为这一行），再 `TrimLeft` 空白；剩下为空则视为没有 system。
- 过滤后没有任何剩余文本：不插入 role=system 的消息。
- 头出现在第二块、第一块是正常提示：只删匹配块，第一块仍在最前（前缀缓存仍可能断，但行为明确）。

常量与过滤函数放在 `relaykit/relayconvert/internal/claude_messages`（与 `to_oai_chat_req.go` 同包），例如 `anthropicBillingHeaderPrefix` + `shouldStripClaudeSystemText`。不要把前缀字面量散落在拼接循环里。以后若再加固定头，只改该常量和渠道说明文案。

**UI：** 渠道编辑抽屉、与 `anthropic_messages_exclude_cache` 同一组 Switch。

- 标题：剥离 Claude 客户端计费头（英：Strip Claude client billing headers）
- 说明必须点名会删除的头：`x-anthropic-billing-header`。并写清：只影响发给非 Claude 上游的 system 正文，不改 New API 计费，也不改客户端请求。

## 4. 数据流

```
渠道抽屉 Switch
  → ChannelSettings.strip_anthropic_billing_header
  → RelayInfo.ConvOptions().Claude.StripAnthropicBillingHeader
  → ClaudeMessagesRequestToOpenAIChat 扁平化 system 前过滤
  → 发给 OpenAI 兼容上游的 messages[0]
```

`ConvOptions` 的拷贝规则与 `AnthropicMessagesExcludeCache` 相同：`info` 或 `ChannelMeta` 为 nil 时为 false。

Langfuse 仍抓转换前的原始 Claude 请求，能看到这段头。计费仍读上游 usage。`GetTokenCountMeta` 仍按原始 system 估 token（多估约 20–30，结算以上游为准）。

## 5. 组件与文件

| 文件 | 职责 |
| --- | --- |
| `relaykit/dto/channel_settings.go` | 持久化字段 |
| `relaykit/dto/channel_settings_test.go` | omitempty / 往返 |
| `relaykit/relayconvert/convmeta/options.go` | `ClaudeOptions.StripAnthropicBillingHeader` |
| `relay/common/relay_info.go` | `ConvOptions()` 拷贝 |
| `relay/common/relay_info_test.go` | nil → false；true → true |
| `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go` | 扁平化时过滤 |
| 同包测试（新建或并入现有 convert 测试） | 见 §6 |
| `web/src/features/channels/types.ts` | TS 字段 |
| `web/src/features/channels/lib/channel-form.ts` | schema / 默认 / parse / serialize |
| `web/src/features/channels/lib/channel-form-errors.ts` | 高级设置字段集 |
| `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx` | Switch + 已配置指示 |
| `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | 经 `add-missing-keys.mjs` + `i18n:sync`，禁止手改七份 locale |
| `docs/channel/other_setting.md` | 文档第 5 项与 JSON 示例 |
| `docs/internal/patches/` | 内部 patch 账本，状态 `internal-only` |

relaykit 必须保持独立可构建：`cd relaykit && GOWORK=off go build ./...`。

## 6. 错误处理与测试

匹配失败或未命中：静默，原样拼接。不 400，不打错误日志。

必测：

1. JSON 省略字段不出现 `strip_anthropic_billing_header`；缺省反序列化为 false。
2. `ConvOptions`：设置 true 则 option true；未设 / 无 meta / nil info 为 false。
3. 三块 system（计费头 +「You are Claude Code」+ 主提示）+ 开关开 + 非 OpenRouter Claude → 发出的 system string 以「You are Claude Code」开头，且不含 `x-anthropic-billing-header`。
4. 开关关：三块全在，头在最前。
5. OpenRouter + `anthropic/claude-*` 上游：分块仍含计费头。
6. 字符串 system 以该头开头、后面换行再接正文：只去掉第一行。
7. 全部块都是计费头：结果里没有 system 消息。
8. 计费头在第二块：第一块仍在最前，第二块消失。

不改 golden（默认关，现有样例不含此头）。

## 7. 部署

- 无迁移。默认关，现网行为不变。
- 镜像上线后，给 channel 5 `[H200] Deepseek V4 Flash` 勾上此开关。同一渠道上的 Reasonix 走 `/v1/chat/completions`，没有这段头，不受影响。
- 回滚：关掉渠道开关即可，不必回滚镜像。

## 8. 退出条件

若上游 new-api 自己提供等价过滤，或 Claude Code 不再发送该头，删掉本开关及相关文案，状态改为 `dropped` 或 `upstream-merged`。
