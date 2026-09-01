# fix/oai-to-claude-tool-call-stream

| 项 | 值 |
| -- | -- |
| 分支 | `fix/oai-to-claude-tool-call-stream` |
| 基线 | `origin/main` @ `ccd535ef8` |
| 合入 commit | `527d6fca`（merge） |
| 合入日期 | 2026-09-01 |
| 状态 | `upstream-pending` |
| 上游 PR | 未提交 |

## 为什么要这个改动

OpenAI chat → Claude Messages 的流式转换在 vLLM / GLM 上游下有两个截断 bug，客户端（Claude Code）表现为 "Invalid tool parameters" 或工具参数被清空：

1. vLLM 把最后一段 tool_call 参数碎片和 `finish_reason` 放在同一个 chunk，转换器遇到 finish 提前收尾，这段参数被丢掉，下游 JSON 不完整。
2. GLM/vLLM 的续传 chunk 会重发 `function.name`（vllm#44098），转换器因此再发一个 `content_block_start`，客户端的输入累加器被重置成 `{}`。

## 改动内容

- `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go` — finish chunk 不再立即关流，推迟到随后的 usage-only chunk 再收尾，保证最后一段参数先发出；用 `ClaudeConvertInfo.ToolCallStartSent` 记录已开始的 tool block 偏移，重发名字的续传 chunk 不再产生重复 `content_block_start`
- `relaykit/relayconvert/convmeta/meta.go` — `ToolCallStartSent` 状态字段
- `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp_test.go` — 两个回归测试钉住上述语义

只影响 OpenAI chat → Claude Messages 的流式响应转换路径，非流式与其它协议组合不变。

## 部署影响

无。

## 与上游的冲突风险

`relaykit/relayconvert` 是上游活跃重构区（conversion 层刚从 `relay/` 拆成独立 module）。同步上游时若该文件被大改，以两个回归测试为准重新适配，不要直接取上游覆盖。验证时必须跑 `relaykit` 的独立构建（`GOWORK=off`），根模块构建通过不算数。

## 验证方式

```bash
cd relaykit && GOWORK=off go test ./relayconvert/internal/oai_chat ./relayconvert/convmeta
cd relaykit && GOWORK=off go build ./...
```

手工验证：Claude Code 经 vLLM 渠道触发工具调用，流式响应中工具参数 JSON 完整（不再报 Invalid tool parameters），GLM 渠道续传 chunk 不再清空已累积参数。

## 退出条件

上游收编等价修复（finish chunk 保留参数 + tool block start 去重）后移除内部实现，条目移入已归档；或上游 vLLM/GLM 侧修复了各自的发送行为后重新评估。
