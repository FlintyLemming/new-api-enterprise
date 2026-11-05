# 渠道额外设置说明

该配置用于设置一些额外的渠道参数，可以通过 JSON 对象进行配置。主要包含以下四个设置项：

1. force_format
    - 用于标识是否对数据进行强制格式化为 OpenAI 格式
    - 类型为布尔值，设置为 true 时启用强制格式化

2. proxy
    - 用于配置网络代理
    - 类型为字符串，支持 `http`、`https`、`socks5` 和 `socks5h` 协议
    - 保存时必须包含协议和主机；仅允许空路径或根路径 `/`，不允许 query 或 fragment
    - SOCKS 代理未填写端口时，运行时使用默认端口 `1080`

3. thinking_to_content
   - 用于标识是否将思考内容`reasoning_content`转换为`<think>`标签拼接到内容中返回
   - 类型为布尔值，设置为 true 时启用思考内容转换

4. strip_anthropic_billing_header
   - 将 Claude Messages 的 system 压成一条 OpenAI chat string 时，丢掉以 `x-anthropic-billing-header:` 开头的 system 块（或字符串 system 的第一行）
   - 类型为布尔值，默认 false / 省略
   - 只影响发给非 Claude 上游的 system 正文；OpenRouter 上 `anthropic/claude-*` 的分块路径不过滤；不改 New API 计费，也不改客户端请求
   - 在 Claude Code 对接本地 DeepSeek 等非 Claude 上游时建议开启，避免变化的计费头打断前缀缓存

5. inline_claude_system_messages
   - 把 Claude Messages 对话中途的 `role: "system"` 消息改写为 `<system-reminder>` 文本块，追加到前一条 user 消息末尾；前一条不是 user 时单独成为一条 user 消息；没有文本的 system 消息（只带 `output_config.effort`）直接丢弃
   - 类型为布尔值，默认 false / 省略
   - 对所有接收 Claude Messages 请求的渠道生效（原生透传和转 OpenAI chat 都在转换前改写）；`pass_through_body` 不处理；不改 New API 计费，也不改客户端请求原文
   - Claude Code 会在对话中插入 `<total_tokens>…tokens left</total_tokens>`、`# Environment update` 等 system 消息；DeepSeek 等 chat template 会把所有 system 消息提到 prompt 开头，导致每轮整段历史的前缀缓存失效。对接这类 vLLM / SGLang 上游时建议开启

--------------------------------------------------------------

## JSON 格式示例

以下是一个示例配置，启用强制格式化并设置了代理地址：

```json
{
    "force_format": true,
    "thinking_to_content": true,
    "strip_anthropic_billing_header": true,
    "inline_claude_system_messages": true,
    "proxy": "socks5://proxy.example:1080"
}
```

--------------------------------------------------------------

通过调整上述 JSON 配置中的值，可以灵活控制渠道的额外行为，比如是否进行格式化以及使用特定的网络代理。

## 升级兼容性

旧版本会忽略代理地址中的 path、query 和 fragment。为避免升级后中断已有渠道流量，运行时会继续剥离这些遗留后缀，并对同一代理地址每个进程记录一次不含凭证和后缀的警告。该兼容逻辑不会改写数据库；再次保存渠道时必须按上述严格规则修正代理地址。

代理连接使用 30 秒 TCP 拨号超时和 30 秒 KeepAlive；TLS 握手超时为 10 秒。这些超时同样适用于未配置渠道代理的中转请求。

自 v1.0.0-rc.36 起，转换后的 Anthropic Messages 用量由上游统一排除缓存，终态 usage 修正首帧估算；原 `anthropic_messages_exclude_cache` 开关不再使用。此调整只影响客户端用量表示，New API 计费仍使用原始 usage sidecar。
