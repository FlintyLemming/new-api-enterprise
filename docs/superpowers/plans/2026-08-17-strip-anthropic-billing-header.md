# Strip Anthropic Billing Header Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给渠道加一个默认关闭的开关；打开后，仅在 Claude Messages → 非 Claude 上游、把 system 压成一条 string 时，丢掉 Claude Code 写入 `system[0]` 的 `x-anthropic-billing-header`，让本地 DeepSeek 的前缀缓存从「You are Claude Code」起稳定命中。

**Architecture:** 字段落在渠道 `setting` JSON，经 `RelayInfo.ConvOptions().Claude.StripAnthropicBillingHeader` 传入转换器。过滤函数与常量放在 `relaykit/relayconvert/internal/claude_messages`，只改 `ClaudeMessagesRequestToOpenAIChat` 的扁平化分支；OpenRouter + `anthropic/claude*` 分块路径、`ClaudeRequest.System` 原文、计费、golden 一律不动。

**Tech Stack:** Go 1.22+、relaykit converters（`convmeta` + `claude_messages`）、host `RelayInfo`、React 19 渠道抽屉、i18next 七语 locale。

## Global Constraints

- 开关默认 `false` / 省略。无数据库迁移。无系统级全局开关。无用户可配前缀列表或正则。
- 本期只认一条写死前缀：`x-anthropic-billing-header:`（字面大小写，与 Claude Code 实发一致）。
- 不在转换前改 `ClaudeRequest.System`（预扣估算、Langfuse 原始 body 保持原样）。`GetTokenCountMeta` 仍按原始 system 估 token。
- 不改计费表达式，不把缓存命中折进 `p`。不靠渠道 `param_override` 顶替。
- 不处理 `pass_through_body`（该模式本就不走转换）。
- 不在 OpenRouter Claude 分块路径上过滤（`isOpenRouter && strings.HasPrefix(upstream, "anthropic/claude")` 为真时，system 仍按块带 `cache_control` 发出）。
- 匹配失败或未命中：静默，原样拼接。不 400，不打错误日志。
- 不改 golden（默认关，现有样例不含此头）。禁止带 `-update` 跑 golden。
- relaykit MUST remain independently buildable: `cd relaykit && GOWORK=off go build ./...` after every relaykit edit. Do not import host packages from relaykit.
- Root-module JSON marshal/unmarshal still goes through `common.*`. relaykit tests may keep using `encoding/json`（现有 `relaykit/dto/channel_settings_test.go` 模式）。
- New Go tests use `github.com/stretchr/testify/require` for setup/fatal and `github.com/stretchr/testify/assert` for non-fatal checks.
- Frontend locale writes MUST go through `web/scripts/add-missing-keys.mjs` then `bun run i18n:sync`. Do not hand-edit `web/src/i18n/locales/*.json`.
- 前缀字面量只允许出现在 `anthropicBillingHeaderPrefix` 常量里，不要散落在拼接循环中。

---

## File structure

| File | Responsibility |
| --- | --- |
| `relaykit/dto/channel_settings.go` | 持久化 `StripAnthropicBillingHeader` |
| `relaykit/dto/channel_settings_test.go` | omitempty / 缺省 false / 往返 |
| `relaykit/relayconvert/convmeta/options.go` | `ClaudeOptions.StripAnthropicBillingHeader` |
| `relay/common/relay_info.go` | `ConvOptions()` 从渠道 setting 拷贝 |
| `relay/common/relay_info_test.go` | nil → false；true → true |
| `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go` | 常量、过滤函数、扁平化时丢头 |
| `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req_test.go` | 认头 + 转换契约（§6 用例 3–8） |
| `web/src/features/channels/types.ts` | TS 字段 |
| `web/src/features/channels/lib/channel-form.ts` | schema / 默认 / parse / serialize |
| `web/src/features/channels/lib/channel-form-errors.ts` | 高级设置字段集 |
| `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx` | Switch + 已配置指示 |
| `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | 经脚本写入，禁止手改 |
| `docs/channel/other_setting.md` | 文档第 5 项与 JSON 示例 |
| `docs/internal/patches/feat-strip-claude-system-prefix.md` | 内部 patch 账本，状态 `internal-only` |

不要新建 package。不要拆 `to_oai_chat_req.go`。过滤函数与常量放在该文件同包。

---

### Task 1: Channel setting, converter option, host snapshot

**Files:**
- Modify: `relaykit/dto/channel_settings.go`（`ChannelSettings`，`AnthropicMessagesExcludeCache` 旁）
- Modify: `relaykit/dto/channel_settings_test.go`（文件末尾追加）
- Modify: `relaykit/relayconvert/convmeta/options.go`（`ClaudeOptions`）
- Modify: `relay/common/relay_info.go`（`ConvOptions`，约 818–830 行）
- Test: `relaykit/dto/channel_settings_test.go`
- Test: `relay/common/relay_info_test.go`

**Interfaces:**
- Consumes: 现有 `dto.ChannelSettings`、`convmeta.ClaudeOptions`、`(*RelayInfo).ConvOptions()`。
- Produces:
  - `dto.ChannelSettings.StripAnthropicBillingHeader bool`，JSON `strip_anthropic_billing_header,omitempty`
  - `convmeta.ClaudeOptions.StripAnthropicBillingHeader bool`
  - `(*RelayInfo).ConvOptions()`：`info` 与 `info.ChannelMeta` 均非 nil 时拷贝 `info.ChannelSetting.StripAnthropicBillingHeader`；否则为 `false`

- [ ] **Step 1: Write the failing JSON round-trip test**

Append to `relaykit/dto/channel_settings_test.go`（同包，已导入 `encoding/json`、`assert`、`require`）：

```go
func TestChannelSettingsStripAnthropicBillingHeaderJSON(t *testing.T) {
	omitted := ChannelSettings{}
	encoded, err := json.Marshal(omitted)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "strip_anthropic_billing_header")

	var decoded ChannelSettings
	require.NoError(t, json.Unmarshal([]byte(`{"proxy":"http://127.0.0.1:8080"}`), &decoded))
	assert.False(t, decoded.StripAnthropicBillingHeader)

	enabled := ChannelSettings{StripAnthropicBillingHeader: true}
	encoded, err = json.Marshal(enabled)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"strip_anthropic_billing_header":true`)

	var enabledDecoded ChannelSettings
	require.NoError(t, json.Unmarshal(encoded, &enabledDecoded))
	assert.True(t, enabledDecoded.StripAnthropicBillingHeader)
}
```

- [ ] **Step 2: Write the failing host snapshot test**

Append to `relay/common/relay_info_test.go`（已导入 `dto`、`assert`）：

```go
func TestRelayInfoConvOptionsCopiesStripAnthropicBillingHeader(t *testing.T) {
	enabled := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ChannelSetting: dto.ChannelSettings{StripAnthropicBillingHeader: true},
		},
	}
	assert.True(t, enabled.ConvOptions().Claude.StripAnthropicBillingHeader)

	unset := &RelayInfo{ChannelMeta: &ChannelMeta{}}
	assert.False(t, unset.ConvOptions().Claude.StripAnthropicBillingHeader)

	noMeta := &RelayInfo{}
	assert.False(t, noMeta.ConvOptions().Claude.StripAnthropicBillingHeader)

	var nilInfo *RelayInfo
	assert.False(t, nilInfo.ConvOptions().Claude.StripAnthropicBillingHeader)
}
```

This must not panic on nil `RelayInfo` or nil `ChannelMeta`（`TestRelayInfoMetaTypedNilReceiver` 已对 typed-nil `*RelayInfo` 调用 `ConvOptions()`）。

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
cd /mnt/extend/Projects/new-api/relaykit
GOWORK=off go test ./dto/ -run TestChannelSettingsStripAnthropicBillingHeaderJSON -count=1
```

Expected: FAIL compile: `decoded.StripAnthropicBillingHeader undefined (type ChannelSettings has no field or method StripAnthropicBillingHeader)`

Run:

```bash
cd /mnt/extend/Projects/new-api
go test ./relay/common/ -run TestRelayInfoConvOptionsCopiesStripAnthropicBillingHeader -count=1
```

Expected: FAIL compile: `unknown field StripAnthropicBillingHeader in struct literal of type dto.ChannelSettings`

- [ ] **Step 4: Add the setting and converter option fields**

In `relaykit/dto/channel_settings.go`，紧邻 `AnthropicMessagesExcludeCache`：

```go
type ChannelSettings struct {
	ForceFormat                   bool `json:"force_format,omitempty"`
	ThinkingToContent             bool `json:"thinking_to_content,omitempty"`
	AnthropicMessagesExcludeCache bool `json:"anthropic_messages_exclude_cache,omitempty"`
	// StripAnthropicBillingHeader drops Claude Code's
	// x-anthropic-billing-header system prefix when flattening Claude
	// Messages system blocks into one OpenAI chat system string.
	StripAnthropicBillingHeader bool `json:"strip_anthropic_billing_header,omitempty"`
	// CachePromptTokenSemantic declares whether the upstream provider counts
	// cached prompt tokens inside usage.prompt_tokens. Empty means auto-detect
	// from usage semantic; "prompt_includes_cache"/"prompt_excludes_cache"
	// are explicit channel declarations that take priority. A
	// prompt_excludes_cache declaration only affects cache accounting; image
	// and audio detail tokens still follow the inclusive-subtraction rule (it
	// is not a blanket "exclusive for everything" switch).
	CachePromptTokenSemantic string `json:"cache_prompt_token_semantic,omitempty"`
	Proxy                    string `json:"proxy"`
	PassThroughBodyEnabled   bool   `json:"pass_through_body_enabled,omitempty"`
	SystemPrompt             string `json:"system_prompt,omitempty"`
	SystemPromptOverride     bool   `json:"system_prompt_override,omitempty"`
	// HTTPProtocol controls outbound HTTP version negotiation for this channel.
	// Accepted values: "", "auto" (default), "http1".
	HTTPProtocol string `json:"http_protocol,omitempty"`
	// HTTP2ConnectionShards spreads HTTP/2 traffic across N independent transports
	// (1-8). Zero/unset means 1. Ignored when HTTPProtocol is "http1".
	HTTP2ConnectionShards int `json:"http2_connection_shards,omitempty"`
}
```

In `relaykit/relayconvert/convmeta/options.go`，`ClaudeOptions` 末尾、`AnthropicMessagesExcludeCache` 之后：

```go
	// AnthropicMessagesExcludeCache subtracts cache read and cache creation
	// from Anthropic Messages input_tokens because the upstream counted those
	// tokens inside prompt_tokens, and zeros message_start input so clients
	// do not keep the pre-usage estimate. Default false.
	AnthropicMessagesExcludeCache bool
	// StripAnthropicBillingHeader drops Claude Code's
	// x-anthropic-billing-header system prefix when flattening Claude
	// Messages system blocks into one OpenAI chat system string. Default
	// false. Does not apply to the OpenRouter Claude chunked path.
	StripAnthropicBillingHeader bool
```

- [ ] **Step 5: Copy the field in `ConvOptions()`**

In `relay/common/relay_info.go` 的 `ConvOptions()`（约 818–830 行），与 `AnthropicMessagesExcludeCache` 同一套 nil 守卫：

```go
	claudeSettings := model_setting.GetClaudeSettings()
	geminiSettings := model_setting.GetGeminiSettings()
	anthropicMessagesExcludeCache := false
	stripAnthropicBillingHeader := false
	if info != nil && info.ChannelMeta != nil {
		anthropicMessagesExcludeCache = info.ChannelSetting.AnthropicMessagesExcludeCache
		stripAnthropicBillingHeader = info.ChannelSetting.StripAnthropicBillingHeader
	}
	options := &convmeta.Options{
		Claude: convmeta.ClaudeOptions{
			ThinkingAdapterEnabled:                claudeSettings.ThinkingAdapterEnabled,
			ThinkingAdapterBudgetTokensPercentage: claudeSettings.ThinkingAdapterBudgetTokensPercentage,
			DefaultMaxTokens:                      claudeSettings.GetDefaultMaxTokens,
			AnthropicMessagesExcludeCache:         anthropicMessagesExcludeCache,
			StripAnthropicBillingHeader:           stripAnthropicBillingHeader,
		},
```

不要改 `Gemini` / `OpenRouterDialect` / `PreserveThinkingSuffix` 分支。不要在 `info` 或 `ChannelMeta` 为 nil 时读 `ChannelSetting`。

- [ ] **Step 6: Run tests to verify they pass**

```bash
cd /mnt/extend/Projects/new-api/relaykit
GOWORK=off go test ./dto/ -run TestChannelSettingsStripAnthropicBillingHeaderJSON -count=1
GOWORK=off go build ./...
```

Expected: `PASS`；`go build` 无输出、exit 0。

```bash
cd /mnt/extend/Projects/new-api
go test ./relay/common/ -run TestRelayInfoConvOptionsCopiesStripAnthropicBillingHeader -count=1
```

Expected: `PASS`

- [ ] **Step 7: Commit**

```bash
git add relaykit/dto/channel_settings.go relaykit/dto/channel_settings_test.go relaykit/relayconvert/convmeta/options.go relay/common/relay_info.go relay/common/relay_info_test.go
git commit -m "$(cat <<'EOF'
feat: add channel setting to strip Claude billing headers

Persist strip_anthropic_billing_header on channel setting JSON and
copy it into ConvOptions so converters can opt in per channel.
EOF
)"
```

---

### Task 2: Flatten-path filter in Claude Messages → OpenAI Chat

**Files:**
- Create: `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req_test.go`
- Modify: `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go`
- Test: `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req_test.go`
- 只读核对：`relaykit/relayconvert/testdata/golden/request/claude_to_openai.golden.json`（禁止改、禁止 `-update`）

**Interfaces:**
- Consumes: Task 1 的 `convmeta.ClaudeOptions.StripAnthropicBillingHeader`；现有 `ClaudeMessagesRequestToOpenAIChat(claudeRequest dto.ClaudeRequest, info convmeta.Meta) (*dto.GeneralOpenAIRequest, error)`；`convmeta.OptionsOf`、`convmeta.UpstreamModelName`、`convmeta.Values`。
- Produces（同包、未导出）：
  - `const anthropicBillingHeaderPrefix = "x-anthropic-billing-header:"`
  - `func shouldStripClaudeSystemText(text string) bool` — `strings.HasPrefix(strings.TrimSpace(text), anthropicBillingHeaderPrefix)`
  - `func stripLeadingClaudeBillingHeaderLine(s string) string` — 未命中原样返回；命中则丢掉第一行（到第一个 `\n`，含换行；无换行则整段丢掉），再 `TrimLeftFunc` 空白
  - `ClaudeMessagesRequestToOpenAIChat`：开关开且走扁平化 string 时过滤；OpenRouter Claude 分块路径不过滤；不改 `claudeRequest.System`

- [ ] **Step 1: Write the failing helper and convert tests**

Create `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req_test.go`：

```go
package claudemessages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const billingHeaderLine = "x-anthropic-billing-header: cc_version=2.0.76; cch=abc123;"

func textPtr(s string) *string {
	return &s
}

func stripOnMeta() convmeta.Meta {
	return &convmeta.Values{
		Options: &convmeta.Options{
			Claude: convmeta.ClaudeOptions{StripAnthropicBillingHeader: true},
		},
	}
}

func threeBlockClaudeRequest() dto.ClaudeRequest {
	return dto.ClaudeRequest{
		Model: "deepseek-chat",
		System: []dto.ClaudeMediaMessage{
			{Type: "text", Text: textPtr(billingHeaderLine)},
			{Type: "text", Text: textPtr("You are Claude Code")},
			{Type: "text", Text: textPtr("Follow the user's instructions.")},
		},
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
	}
}

func systemMessages(req *dto.GeneralOpenAIRequest) []dto.Message {
	var out []dto.Message
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			out = append(out, msg)
		}
	}
	return out
}

func TestShouldStripClaudeSystemText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "exact prefix", text: billingHeaderLine, want: true},
		{name: "leading whitespace", text: "  \n" + billingHeaderLine, want: true},
		{name: "wrong case", text: "X-Anthropic-Billing-Header: cc_version=1;", want: false},
		{name: "normal prompt", text: "You are Claude Code", want: false},
		{name: "prefix mid-line", text: "note " + billingHeaderLine, want: false},
		{name: "empty", text: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldStripClaudeSystemText(tt.text))
		})
	}
}

func TestStripLeadingClaudeBillingHeaderLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "header then body",
			in:   billingHeaderLine + "\nYou are Claude Code\nFollow the user's instructions.",
			want: "You are Claude Code\nFollow the user's instructions.",
		},
		{
			name: "header only no newline",
			in:   billingHeaderLine,
			want: "",
		},
		{
			name: "leading whitespace then header then body",
			in:   "  \n" + billingHeaderLine + "\nYou are Claude Code",
			want: "You are Claude Code",
		},
		{
			name: "non-matching unchanged",
			in:   "You are Claude Code",
			want: "You are Claude Code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripLeadingClaudeBillingHeaderLine(tt.in))
		})
	}
}

func TestClaudeMessagesRequestToOpenAIChatStripsBillingHeaderWhenSwitchOn(t *testing.T) {
	req := threeBlockClaudeRequest()
	originalSystem := req.System

	got, err := ClaudeMessagesRequestToOpenAIChat(req, stripOnMeta())
	require.NoError(t, err)
	require.Equal(t, originalSystem, req.System)

	systems := systemMessages(got)
	require.Len(t, systems, 1)
	content := systems[0].StringContent()
	assert.True(t, strings.HasPrefix(content, "You are Claude Code"))
	assert.NotContains(t, content, "x-anthropic-billing-header")
	assert.Contains(t, content, "Follow the user's instructions.")
}

func TestClaudeMessagesRequestToOpenAIChatKeepsBillingHeaderWhenSwitchOff(t *testing.T) {
	req := threeBlockClaudeRequest()

	got, err := ClaudeMessagesRequestToOpenAIChat(req, &convmeta.Values{})
	require.NoError(t, err)

	systems := systemMessages(got)
	require.Len(t, systems, 1)
	content := systems[0].StringContent()
	assert.True(t, strings.HasPrefix(content, billingHeaderLine))
	assert.Contains(t, content, "You are Claude Code")
	assert.Contains(t, content, "Follow the user's instructions.")
}

func TestClaudeMessagesRequestToOpenAIChatOpenRouterClaudeKeepsChunks(t *testing.T) {
	req := threeBlockClaudeRequest()
	info := &convmeta.Values{
		ChannelMetaAttached: true,
		UpstreamModelName:   "anthropic/claude-sonnet-4",
		Options: &convmeta.Options{
			OpenRouterDialect: true,
			Claude:            convmeta.ClaudeOptions{StripAnthropicBillingHeader: true},
		},
	}

	got, err := ClaudeMessagesRequestToOpenAIChat(req, info)
	require.NoError(t, err)

	systems := systemMessages(got)
	require.Len(t, systems, 1)
	assert.False(t, systems[0].IsStringContent())
	parts := systems[0].ParseContent()
	require.Len(t, parts, 3)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(parts[0].Text), "x-anthropic-billing-header:"))
	assert.Equal(t, "You are Claude Code", parts[1].Text)
	assert.Equal(t, "Follow the user's instructions.", parts[2].Text)
}

func TestClaudeMessagesRequestToOpenAIChatStripsFirstLineOfStringSystem(t *testing.T) {
	req := dto.ClaudeRequest{
		Model:    "deepseek-chat",
		System:   billingHeaderLine + "\nYou are Claude Code\nFollow the user's instructions.",
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
	}

	got, err := ClaudeMessagesRequestToOpenAIChat(req, stripOnMeta())
	require.NoError(t, err)

	systems := systemMessages(got)
	require.Len(t, systems, 1)
	assert.Equal(t, "You are Claude Code\nFollow the user's instructions.", systems[0].StringContent())
}

func TestClaudeMessagesRequestToOpenAIChatDropsSystemWhenOnlyBillingHeader(t *testing.T) {
	t.Run("all array blocks", func(t *testing.T) {
		req := dto.ClaudeRequest{
			Model: "deepseek-chat",
			System: []dto.ClaudeMediaMessage{
				{Type: "text", Text: textPtr(billingHeaderLine)},
				{Text: textPtr("  " + billingHeaderLine)},
			},
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
		}

		got, err := ClaudeMessagesRequestToOpenAIChat(req, stripOnMeta())
		require.NoError(t, err)
		assert.Empty(t, systemMessages(got))
		require.Len(t, got.Messages, 1)
		assert.Equal(t, "user", got.Messages[0].Role)
	})

	t.Run("string system no newline", func(t *testing.T) {
		req := dto.ClaudeRequest{
			Model:    "deepseek-chat",
			System:   billingHeaderLine,
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
		}

		got, err := ClaudeMessagesRequestToOpenAIChat(req, stripOnMeta())
		require.NoError(t, err)
		assert.Empty(t, systemMessages(got))
	})
}

func TestClaudeMessagesRequestToOpenAIChatStripsBillingHeaderInSecondBlock(t *testing.T) {
	req := dto.ClaudeRequest{
		Model: "deepseek-chat",
		System: []dto.ClaudeMediaMessage{
			{Type: "text", Text: textPtr("You are Claude Code")},
			{Type: "text", Text: textPtr(billingHeaderLine)},
			{Type: "text", Text: textPtr("Follow the user's instructions.")},
		},
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
	}

	got, err := ClaudeMessagesRequestToOpenAIChat(req, stripOnMeta())
	require.NoError(t, err)

	systems := systemMessages(got)
	require.Len(t, systems, 1)
	content := systems[0].StringContent()
	assert.True(t, strings.HasPrefix(content, "You are Claude Code"))
	assert.NotContains(t, content, "x-anthropic-billing-header")
	assert.Contains(t, content, "Follow the user's instructions.")
}

func TestClaudeMessagesRequestToOpenAIChatOpenRouterClaudeCacheControlPreserved(t *testing.T) {
	req := threeBlockClaudeRequest()
	systems := req.System.([]dto.ClaudeMediaMessage)
	systems[0].CacheControl = json.RawMessage(`{"type":"ephemeral"}`)
	req.System = systems
	info := &convmeta.Values{
		ChannelMetaAttached: true,
		UpstreamModelName:   "anthropic/claude-3.5-sonnet",
		Options: &convmeta.Options{
			OpenRouterDialect: true,
			Claude:            convmeta.ClaudeOptions{StripAnthropicBillingHeader: true},
		},
	}

	got, err := ClaudeMessagesRequestToOpenAIChat(req, info)
	require.NoError(t, err)
	parts := systemMessages(got)[0].ParseContent()
	require.Len(t, parts, 3)
	assert.JSONEq(t, `{"type":"ephemeral"}`, string(parts[0].CacheControl))
}
```

第二块不要求 `Type == "text"`：`all array blocks` 子用例里第二块只设 `Text`。OpenRouter 用例必须设 `ChannelMetaAttached: true`，否则 `convmeta.UpstreamModelName` 返回空串，会误走扁平化。

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /mnt/extend/Projects/new-api/relaykit
GOWORK=off go test ./relayconvert/internal/claude_messages/ -count=1
```

Expected: FAIL compile: `undefined: shouldStripClaudeSystemText`（以及 `stripLeadingClaudeBillingHeaderLine`）。

- [ ] **Step 3: Add the helpers and wire flattening**

Append to `relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go`（`requestToJSONString` 之后）：

```go
const anthropicBillingHeaderPrefix = "x-anthropic-billing-header:"

func shouldStripClaudeSystemText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), anthropicBillingHeaderPrefix)
}

func stripLeadingClaudeBillingHeaderLine(s string) string {
	if !shouldStripClaudeSystemText(s) {
		return s
	}
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimLeftFunc(s[i+1:], func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || r == '\r'
		})
	}
	return ""
}
```

`strings` 已导入。不要把前缀字面量写进循环。`TrimLeftFunc` 覆盖空格 / tab / `\n` / `\r`，与 `TrimSpace` 对 ASCII 空白一致。

Replace `ClaudeMessagesRequestToOpenAIChat` 里处理 `claudeRequest.System` 的整段（约 97–134 行）为：

```go
	openAIMessages := make([]dto.Message, 0)
	if claudeRequest.System != nil {
		stripBilling := convmeta.OptionsOf(info).Claude.StripAnthropicBillingHeader
		if claudeRequest.IsStringSystem() && claudeRequest.GetStringSystem() != "" {
			systemText := claudeRequest.GetStringSystem()
			if stripBilling {
				systemText = stripLeadingClaudeBillingHeaderLine(systemText)
			}
			if systemText != "" {
				openAIMessage := dto.Message{
					Role: "system",
				}
				openAIMessage.SetStringContent(systemText)
				openAIMessages = append(openAIMessages, openAIMessage)
			}
		} else {
			systems := claudeRequest.ParseSystem()
			if len(systems) > 0 {
				isOpenRouterClaude := isOpenRouter && strings.HasPrefix(convmeta.UpstreamModelName(info), "anthropic/claude")
				if isOpenRouterClaude {
					openAIMessage := dto.Message{
						Role: "system",
					}
					systemMediaMessages := make([]dto.MediaContent, 0, len(systems))
					for _, system := range systems {
						message := dto.MediaContent{
							Type:         "text",
							Text:         system.GetText(),
							CacheControl: system.CacheControl,
						}
						systemMediaMessages = append(systemMediaMessages, message)
					}
					openAIMessage.SetMediaContent(systemMediaMessages)
					openAIMessages = append(openAIMessages, openAIMessage)
				} else {
					systemStr := ""
					for _, system := range systems {
						if system.Text == nil {
							continue
						}
						if stripBilling && shouldStripClaudeSystemText(*system.Text) {
							continue
						}
						systemStr += *system.Text
					}
					if systemStr != "" || !stripBilling {
						openAIMessage := dto.Message{
							Role: "system",
						}
						openAIMessage.SetStringContent(systemStr)
						openAIMessages = append(openAIMessages, openAIMessage)
					}
				}
			}
		}
	}
```

约束：

- 不要调用 `claudeRequest.SetStringSystem`，不要改 `claudeRequest.System` 的任何元素。
- OpenRouter Claude 分支不要读 `shouldStripClaudeSystemText`。
- 开关关时数组拼接与现在完全一致（含空 `Text` 跳过、非空直接 `+=`、`len(systems) > 0` 仍插入 system）。
- 开关开且过滤后 `systemStr == ""`：不插入 `role=system`。
- 字符串 system 未命中前缀时原样发出（含原有前导空白）。

- [ ] **Step 4: Run convert tests, goldens, and independent build**

```bash
cd /mnt/extend/Projects/new-api/relaykit
GOWORK=off go test ./relayconvert/internal/claude_messages/ -count=1
GOWORK=off go test ./relayconvert/ -run 'TestGoldenRequestConversionMatrix|TestGoldenResponseConversionMatrix|TestGoldenStreamConversionMatrix' -count=1
GOWORK=off go build ./...
```

Expected: 全部 `PASS`；`go build` exit 0。golden 不得出现 `x-anthropic-billing-header` 相关 diff。若 golden 失败，先停下来查是不是默认路径被改了，不要 `-update`。

- [ ] **Step 5: Commit**

```bash
git add relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go relaykit/relayconvert/internal/claude_messages/to_oai_chat_req_test.go
git commit -m "$(cat <<'EOF'
feat: strip Claude billing header when flattening system text

When the channel switch is on, drop x-anthropic-billing-header from
the flattened OpenAI system string so DeepSeek prefix cache can stick.
EOF
)"
```

---

### Task 3: Channel UI, i18n, docs, and internal patch ledger

**Files:**
- Modify: `web/src/features/channels/types.ts`
- Modify: `web/src/features/channels/lib/channel-form.ts`
- Modify: `web/src/features/channels/lib/channel-form-errors.ts`
- Modify: `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx`
- Modify: `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json`（只许经脚本）
- Modify: `docs/channel/other_setting.md`
- Create: `docs/internal/patches/feat-strip-claude-system-prefix.md`

**Interfaces:**
- Consumes: Task 1 的渠道 `setting` JSON 字段 `strip_anthropic_billing_header`。
- Produces: 表单字段 `strip_anthropic_billing_header: boolean`（默认 `false`），与 `anthropic_messages_exclude_cache` 同一组、对所有渠道类型展示；进入 parse / serialize / 高级设置「已配置」指示；七语文案；文档第 5 项；内部 patch 账本状态 `internal-only`。

无新的前端行为测试。契约是接线 + 全 locale 有 key。

- [ ] **Step 1: Add the TypeScript and form field**

`web/src/features/channels/types.ts` — `ChannelSettings`，紧邻 `anthropic_messages_exclude_cache`：

```ts
export interface ChannelSettings {
  force_format?: boolean
  thinking_to_content?: boolean
  anthropic_messages_exclude_cache?: boolean
  strip_anthropic_billing_header?: boolean
  proxy?: string
  pass_through_body_enabled?: boolean
  system_prompt?: string
  system_prompt_override?: boolean
  http_protocol?: 'auto' | 'http1' | string
  http2_connection_shards?: number
  cache_prompt_token_semantic?:
    | 'prompt_includes_cache'
    | 'prompt_excludes_cache'
    | string
}
```

`web/src/features/channels/lib/channel-form.ts` 四处，都紧挨 `anthropic_messages_exclude_cache`：

1. Schema（约 276 行）：

```ts
    anthropic_messages_exclude_cache: z.boolean().optional(),
    strip_anthropic_billing_header: z.boolean().optional(),
```

2. `defaultFormValues` extra settings（约 455 行）：

```ts
  anthropic_messages_exclude_cache: false,
  strip_anthropic_billing_header: false,
```

3. `transformChannelToFormDefaults` 的 `extraSettings` 默认对象与 parse 对象（约 497、520 行）：

```ts
    anthropic_messages_exclude_cache: false,
    strip_anthropic_billing_header: false,
```

```ts
        anthropic_messages_exclude_cache:
          parsed.anthropic_messages_exclude_cache || false,
        strip_anthropic_billing_header:
          parsed.strip_anthropic_billing_header || false,
```

4. `buildSettingJSON`（约 644 行）：

```ts
    anthropic_messages_exclude_cache:
      formData.anthropic_messages_exclude_cache || false,
    strip_anthropic_billing_header:
      formData.strip_anthropic_billing_header || false,
```

`web/src/features/channels/lib/channel-form-errors.ts` — `ADVANCED_SETTINGS_FIELDS` 在 `'anthropic_messages_exclude_cache'` 后加 `'strip_anthropic_billing_header'`。

不要删现有的 `cache_prompt_token_semantic` / HTTP 字段。

- [ ] **Step 2: Wire the drawer switch and configured indicators**

In `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx`：

1. 字段名列表（约 286 行），`'anthropic_messages_exclude_cache'` 之后：

```ts
  'anthropic_messages_exclude_cache',
  'strip_anthropic_billing_header',
```

2. `hasAdvancedSettingsValues`（约 344 行）：

```ts
    values.anthropic_messages_exclude_cache ||
    values.strip_anthropic_billing_header ||
    values.pass_through_body_enabled ||
```

3. `currentAnthropicMessagesExcludeCache` 旁（约 753 行）：

```ts
  const currentAnthropicMessagesExcludeCache = form.watch(
    'anthropic_messages_exclude_cache'
  )
  const currentStripAnthropicBillingHeader = form.watch(
    'strip_anthropic_billing_header'
  )
```

4. `extraSettingsConfigured`（约 1028 行）：

```ts
  const extraSettingsConfigured = Boolean(
    currentForceFormat ||
    currentThinkingToContent ||
    currentAnthropicMessagesExcludeCache ||
    currentStripAnthropicBillingHeader ||
    currentPassThroughBodyEnabled ||
    currentDisableTaskPollingSleep ||
    currentProxy?.trim() ||
    currentSystemPrompt?.trim() ||
    currentSystemPromptOverride ||
    (currentHttpProtocol && currentHttpProtocol !== 'auto') ||
    (currentHttp2ConnectionShards != null &&
      currentHttp2ConnectionShards > 1) ||
    (currentCachePromptTokenSemantic &&
      currentCachePromptTokenSemantic !== 'auto')
  )
```

5. Switch 紧挨 `anthropic_messages_exclude_cache` 的 `FormField` 之后（约 4162 行后），对**所有**渠道类型展示，不要按 `currentType`  gating：

```tsx
                              <FormField
                                control={form.control}
                                name='strip_anthropic_billing_header'
                                render={({ field }) => (
                                  <FormItem className='flex items-center justify-between px-4 py-3'>
                                    <div className='space-y-0.5'>
                                      <FormLabel>
                                        {t(
                                          'Strip Claude client billing headers'
                                        )}
                                      </FormLabel>
                                      <FormDescription>
                                        {t(
                                          'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.'
                                        )}
                                      </FormDescription>
                                    </div>
                                    <FormControl>
                                      <Switch
                                        checked={field.value}
                                        onCheckedChange={field.onChange}
                                      />
                                    </FormControl>
                                  </FormItem>
                                )}
                              />
```

Default unchecked 靠表单默认值。`t()` 的英文 key 必须与 Step 3 的 `newKeys.en` 完全一致。

- [ ] **Step 3: Add i18n keys via the sanctioned script**

Create `web/scripts/add-missing-keys.mjs`（不要手改 locale JSON）：

```javascript
import fs from 'node:fs/promises'
import path from 'node:path'

const LOCALES_DIR = path.resolve('src/i18n/locales')

function stableStringify(obj) {
  return JSON.stringify(obj, null, 2) + '\n'
}

const newKeys = {
  en: {
    'Strip Claude client billing headers':
      'Strip Claude client billing headers',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.',
  },
  zh: {
    'Strip Claude client billing headers': '剥离 Claude 客户端计费头',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      '从发给非 Claude 上游的 system 正文中删除 x-anthropic-billing-header。不改 New API 计费，也不改客户端请求。',
  },
  'zh-TW': {
    'Strip Claude client billing headers': '剝離 Claude 用戶端計費頭',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      '從發給非 Claude 上游的 system 正文中刪除 x-anthropic-billing-header。不改 New API 計費，也不改用戶端請求。',
  },
  fr: {
    'Strip Claude client billing headers':
      'Retirer les en-têtes de facturation du client Claude',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      'Supprime x-anthropic-billing-header du texte system envoyé aux upstreams non-Claude. Ne modifie ni la facturation New API, ni la requête du client.',
  },
  ja: {
    'Strip Claude client billing headers':
      'Claude クライアントの課金ヘッダーを除去',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      '非 Claude 上流に送る system 本文から x-anthropic-billing-header を削除します。New API の課金とクライアントリクエストは変更しません。',
  },
  ru: {
    'Strip Claude client billing headers':
      'Удалять заголовки биллинга клиента Claude',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      'Удаляет x-anthropic-billing-header из текста system, отправляемого не-Claude апстримам. Не меняет биллинг New API и исходный запрос клиента.',
  },
  vi: {
    'Strip Claude client billing headers':
      'Gỡ header thanh toán của client Claude',
    'Removes x-anthropic-billing-header from the system text sent to non-Claude upstreams. Does not change New API billing or the original client request.':
      'Xóa x-anthropic-billing-header khỏi nội dung system gửi tới upstream không phải Claude. Không đổi billing của New API và không đổi request của client.',
  },
}

async function main() {
  let totalAdded = 0

  for (const [locale, trans] of Object.entries(newKeys)) {
    const filePath = path.join(LOCALES_DIR, `${locale}.json`)
    const json = JSON.parse(await fs.readFile(filePath, 'utf8'))

    let count = 0
    for (const [key, value] of Object.entries(trans)) {
      if (!Object.prototype.hasOwnProperty.call(json.translation, key)) {
        json.translation[key] = value
        count++
      } else if (json.translation[key] !== value) {
        json.translation[key] = value
        count++
      }
    }

    if (count > 0) {
      json.translation = Object.fromEntries(
        Object.entries(json.translation).sort(([a], [b]) => a.localeCompare(b))
      )
      await fs.writeFile(filePath, stableStringify(json), 'utf8')
    }

    console.log(`${locale}: ${count} translations applied`)
    totalAdded += count
  }

  console.log(`\nTotal: ${totalAdded} translations applied`)
}

main().catch((err) => {
  console.error(err)
  process.exitCode = 1
})
```

Run from `web/`：

```bash
cd /mnt/extend/Projects/new-api/web
node scripts/add-missing-keys.mjs
bun run i18n:sync
```

Expected: 每个 locale 报告 2 translations applied，然后 sync 成功。

If `find-missing-keys.mjs` is not already in the repo, create it from `.agents/skills/i18n-translate/SKILL.md` Step 2, run `node scripts/find-missing-keys.mjs`, then delete any scripts you created for this task（`add-missing-keys.mjs` 和 `find-missing-keys.mjs`，若原先不在仓库里）。

Expected from find-missing-keys: `All t() keys found in en.json!`

- [ ] **Step 4: Update channel extra-settings docs**

In `docs/channel/other_setting.md`：

1. 把开头「主要包含以下四个设置项」改成「主要包含以下五个设置项」。
2. 在第 4 项 `anthropic_messages_exclude_cache` 之后追加：

```markdown
5. strip_anthropic_billing_header
   - 将 Claude Messages 的 system 压成一条 OpenAI chat string 时，丢掉以 `x-anthropic-billing-header:` 开头的 system 块（或字符串 system 的第一行）
   - 类型为布尔值，默认 false / 省略
   - 只影响发给非 Claude 上游的 system 正文；OpenRouter 上 `anthropic/claude-*` 的分块路径不过滤；不改 New API 计费，也不改客户端请求
   - 在 Claude Code 对接本地 DeepSeek 等非 Claude 上游时建议开启，避免变化的计费头打断前缀缓存
```

3. JSON 示例加上该字段：

```json
{
    "force_format": true,
    "thinking_to_content": true,
    "anthropic_messages_exclude_cache": true,
    "strip_anthropic_billing_header": true,
    "proxy": "socks5://proxy.example:1080"
}
```

- [ ] **Step 5: Write the internal patch ledger**

Create `docs/internal/patches/feat-strip-claude-system-prefix.md`（按 `docs/internal/patches/_template.md`）：

```markdown
# feat/strip-claude-system-prefix

| 项 | 值 |
| -- | -- |
| 分支 | `feat/strip-claude-system-prefix` |
| 基线 | `internal-custom` @ `8eb884f4` |
| 合入 commit | 未合入（本分支实施） |
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
```

- [ ] **Step 6: Typecheck the frontend**

```bash
cd /mnt/extend/Projects/new-api/web
bun run typecheck
```

Expected: exit 0，无与 `strip_anthropic_billing_header` 相关的错误。

- [ ] **Step 7: Commit**

```bash
git add web/src/features/channels/types.ts web/src/features/channels/lib/channel-form.ts web/src/features/channels/lib/channel-form-errors.ts web/src/features/channels/components/drawers/channel-mutate-drawer.tsx web/src/i18n/locales/*.json docs/channel/other_setting.md docs/internal/patches/feat-strip-claude-system-prefix.md
git commit -m "$(cat <<'EOF'
feat: add channel UI to strip Claude billing headers

Operators can opt a channel into dropping x-anthropic-billing-header
from flattened system text without changing billing or client requests.
EOF
)"
```

Do not commit `web/scripts/add-missing-keys.mjs` or `web/scripts/find-missing-keys.mjs` unless those files already existed in the repo.
