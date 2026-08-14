# Anthropic Messages Cache Usage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-channel opt-in so OpenAI-compatible upstreams that fold cache tokens into `prompt_tokens` expose Anthropic Messages `input_tokens` excluding cache read and cache creation, without changing billing or Chat Completions usage.

**Architecture:** Store `openai_prompt_includes_cache` on channel `setting` JSON. The host copies it into `convmeta.ClaudeOptions` when building `RelayInfo.ConvOptions()`. Only `buildClaudeUsageFromOpenAIUsage` (OpenAI Chat → Claude Messages, including the Responses → Chat → Claude chain) applies the subtract. Nested `BillingUsage` stays the original OpenAI numbers. Gemini → Claude and `service/text_quota.go` stay untouched.

**Tech Stack:** Go 1.22+, relaykit converters (`convmeta` + `oai_chat`), Gin/GORM host `RelayInfo`, React 19 channel drawer, i18next locales.

## Global Constraints

- Switch default is `false` / omitted. No global default. No auto-detection of “prompt includes cache”.
- No change to `service/text_quota.go` OpenRouter Claude subtract, pre-consume, or settle. Do not wire this switch into quota math.
- No change to usage returned to OpenAI Chat Completions / Responses clients.
- No change to Gemini → Claude usage mapping.
- No automatic enable for OpenRouter or channel type 1.
- `message_start` estimated `input_tokens` stays `info.GetEstimatePromptTokens()`. Only final `message` / `message_delta` usage is rewritten.
- relaykit MUST remain independently buildable: `cd relaykit && GOWORK=off go build ./...` after every relaykit edit. Do not import host packages from relaykit.
- Root-module JSON marshal/unmarshal still goes through `common.*`. relaykit tests may keep using `encoding/json` (existing pattern in `relaykit/dto/channel_settings_test.go`).
- New Go tests use `github.com/stretchr/testify/require` for setup/fatal and `github.com/stretchr/testify/assert` for non-fatal checks.
- Frontend locale writes MUST go through `web/scripts/add-missing-keys.mjs` then `bun run i18n:sync`. Do not hand-edit `web/src/i18n/locales/*.json`.
- Do not regenerate `relaykit/relayconvert/testdata/golden/response/openai_to_claude.golden.json` (or other goldens). Default-off contract is `prompt_tokens=10`, `cached_tokens=3`, Claude `input_tokens=10`.

---

## File structure

| File | Responsibility |
| --- | --- |
| `relaykit/dto/channel_settings.go` | Persist `OpenAIPromptIncludesCache` on channel `setting` JSON. |
| `relaykit/dto/channel_settings_test.go` | JSON omitempty / round-trip for the new field. |
| `relaykit/relayconvert/convmeta/options.go` | Converter-visible `ClaudeOptions.OpenAIPromptIncludesCache`. |
| `relay/common/relay_info.go` | Copy channel setting into the per-request `ConvOptions` snapshot. |
| `relay/common/relay_info_test.go` | Host snapshot: nil → false; setting true → option true. |
| `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go` | Subtract cache from Claude `input_tokens` when switch or existing cache-write path is on. |
| `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp_test.go` | Table tests + stream `message_delta` coverage. |
| `web/src/features/channels/types.ts` | TypeScript `ChannelSettings` field. |
| `web/src/features/channels/lib/channel-form.ts` | Schema, defaults, parse, serialize. |
| `web/src/features/channels/lib/channel-form-errors.ts` | Advanced-settings field set. |
| `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx` | Switch UI for all channel types, configured indicator. |
| `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | Label + description keys (via i18n script only). |
| `docs/channel/other_setting.md` | Document the JSON field. |

Do not create new packages. Do not split `to_claude_messages_resp.go`.

---

### Task 1: Channel setting, converter option, host snapshot

**Files:**
- Modify: `relaykit/dto/channel_settings.go`
- Modify: `relaykit/dto/channel_settings_test.go`
- Modify: `relaykit/relayconvert/convmeta/options.go`
- Modify: `relay/common/relay_info.go` (`ConvOptions`, around lines 813–839)
- Test: `relaykit/dto/channel_settings_test.go`
- Test: `relay/common/relay_info_test.go`

**Interfaces:**
- Consumes: existing `dto.ChannelSettings`, `convmeta.ClaudeOptions`, `RelayInfo.ConvOptions()`.
- Produces:
  - `dto.ChannelSettings.OpenAIPromptIncludesCache bool` with JSON `openai_prompt_includes_cache,omitempty`
  - `convmeta.ClaudeOptions.OpenAIPromptIncludesCache bool`
  - `(*RelayInfo).ConvOptions()` copies `info.ChannelSetting.OpenAIPromptIncludesCache` when `info` and `info.ChannelMeta` are non-nil; otherwise `false`

- [ ] **Step 1: Write the failing JSON round-trip test**

Append to `relaykit/dto/channel_settings_test.go` (same package, already imports `encoding/json`, `assert`, `require`):

```go
func TestChannelSettingsOpenAIPromptIncludesCacheJSON(t *testing.T) {
	omitted := ChannelSettings{}
	encoded, err := json.Marshal(omitted)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "openai_prompt_includes_cache")

	var decoded ChannelSettings
	require.NoError(t, json.Unmarshal([]byte(`{"proxy":"http://127.0.0.1:8080"}`), &decoded))
	assert.False(t, decoded.OpenAIPromptIncludesCache)

	enabled := ChannelSettings{OpenAIPromptIncludesCache: true}
	encoded, err = json.Marshal(enabled)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"openai_prompt_includes_cache":true`)

	var enabledDecoded ChannelSettings
	require.NoError(t, json.Unmarshal(encoded, &enabledDecoded))
	assert.True(t, enabledDecoded.OpenAIPromptIncludesCache)
}
```

- [ ] **Step 2: Write the failing host snapshot test**

Append to `relay/common/relay_info_test.go` (already imports `dto`, `assert`, `require`):

```go
func TestRelayInfoConvOptionsCopiesOpenAIPromptIncludesCache(t *testing.T) {
	enabled := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ChannelSetting: dto.ChannelSettings{OpenAIPromptIncludesCache: true},
		},
	}
	assert.True(t, enabled.ConvOptions().Claude.OpenAIPromptIncludesCache)

	unset := &RelayInfo{ChannelMeta: &ChannelMeta{}}
	assert.False(t, unset.ConvOptions().Claude.OpenAIPromptIncludesCache)

	noMeta := &RelayInfo{}
	assert.False(t, noMeta.ConvOptions().Claude.OpenAIPromptIncludesCache)

	var nilInfo *RelayInfo
	assert.False(t, nilInfo.ConvOptions().Claude.OpenAIPromptIncludesCache)
}
```

This must not panic on nil `RelayInfo` or nil `ChannelMeta` (`TestRelayInfoMetaTypedNilReceiver` already calls `ConvOptions()` on a typed-nil `*RelayInfo`).

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
cd /Users/flintylemming/Projects/new-api
cd relaykit && GOWORK=off go test ./dto/ -run TestChannelSettingsOpenAIPromptIncludesCacheJSON -count=1
```

Expected: FAIL compile: `decoded.OpenAIPromptIncludesCache undefined (type ChannelSettings has no field or method OpenAIPromptIncludesCache)`

Run:

```bash
cd /Users/flintylemming/Projects/new-api
go test ./relay/common/ -run TestRelayInfoConvOptionsCopiesOpenAIPromptIncludesCache -count=1
```

Expected: FAIL compile: `unknown field OpenAIPromptIncludesCache in struct literal of type dto.ChannelSettings`

- [ ] **Step 4: Add the setting and converter option fields**

In `relaykit/dto/channel_settings.go`, add the field next to `ThinkingToContent`:

```go
type ChannelSettings struct {
	ForceFormat               bool   `json:"force_format,omitempty"`
	ThinkingToContent         bool   `json:"thinking_to_content,omitempty"`
	OpenAIPromptIncludesCache bool   `json:"openai_prompt_includes_cache,omitempty"`
	Proxy                     string `json:"proxy"`
	PassThroughBodyEnabled    bool   `json:"pass_through_body_enabled,omitempty"`
	SystemPrompt              string `json:"system_prompt,omitempty"`
	SystemPromptOverride      bool   `json:"system_prompt_override,omitempty"`
	// HTTPProtocol controls outbound HTTP version negotiation for this channel.
	// Accepted values: "", "auto" (default), "http1".
	HTTPProtocol string `json:"http_protocol,omitempty"`
	// HTTP2ConnectionShards spreads HTTP/2 traffic across N independent transports
	// (1-8). Zero/unset means 1. Ignored when HTTPProtocol is "http1".
	HTTP2ConnectionShards int `json:"http2_connection_shards,omitempty"`
}
```

`OpenAIPromptIncludesCache` means the upstream's OpenAI-style `prompt_tokens` already includes cache read and cache creation. Default false. Existing channels need no migration.

In `relaykit/relayconvert/convmeta/options.go`, add to `ClaudeOptions` after `DefaultMaxTokens`:

```go
type ClaudeOptions struct {
	// ThinkingAdapterEnabled turns "-thinking"-suffixed OpenAI model names
	// into Claude extended-thinking requests.
	ThinkingAdapterEnabled bool
	// ThinkingAdapterBudgetTokensPercentage sizes thinking budget_tokens as a
	// fraction of max_tokens when the adapter fires.
	ThinkingAdapterBudgetTokensPercentage float64
	// DefaultMaxTokens returns the max_tokens to inject when the source
	// request carries none. The Claude Messages API requires max_tokens
	// (omitting it is a 400), so when this hook is nil and no other path
	// supplies a value, OpenAI→Claude request conversion fails with an
	// explicit error instead of emitting a request the upstream is
	// guaranteed to reject. The new-api host always provides this hook;
	// standalone relaykit users must supply one or guarantee max_tokens on
	// every request.
	DefaultMaxTokens func(modelName string) int
	// OpenAIPromptIncludesCache subtracts cache read and cache creation from
	// Anthropic Messages input_tokens because the upstream counted those
	// tokens inside prompt_tokens. Default false.
	OpenAIPromptIncludesCache bool
}
```

- [ ] **Step 5: Copy the flag in `RelayInfo.ConvOptions`**

In `relay/common/relay_info.go`, `ConvOptions` currently builds:

```go
Claude: convmeta.ClaudeOptions{
    ThinkingAdapterEnabled:                claudeSettings.ThinkingAdapterEnabled,
    ThinkingAdapterBudgetTokensPercentage: claudeSettings.ThinkingAdapterBudgetTokensPercentage,
    DefaultMaxTokens:                      claudeSettings.GetDefaultMaxTokens,
},
```

Change it to:

```go
openaiPromptIncludesCache := false
if info != nil && info.ChannelMeta != nil {
    openaiPromptIncludesCache = info.ChannelSetting.OpenAIPromptIncludesCache
}
options := &convmeta.Options{
    Claude: convmeta.ClaudeOptions{
        ThinkingAdapterEnabled:                claudeSettings.ThinkingAdapterEnabled,
        ThinkingAdapterBudgetTokensPercentage: claudeSettings.ThinkingAdapterBudgetTokensPercentage,
        DefaultMaxTokens:                      claudeSettings.GetDefaultMaxTokens,
        OpenAIPromptIncludesCache:             openaiPromptIncludesCache,
    },
    Gemini: convmeta.GeminiOptions{
        ThinkingAdapterEnabled:                geminiSettings.ThinkingAdapterEnabled,
        ThinkingAdapterBudgetTokensPercentage: geminiSettings.ThinkingAdapterBudgetTokensPercentage,
        FunctionCallThoughtSignatureEnabled:   geminiSettings.FunctionCallThoughtSignatureEnabled,
        SupportsImagine:                       model_setting.IsGeminiModelSupportImagine,
        SafetySetting:                         model_setting.GetGeminiSafetySetting,
    },
    OpenRouterDialect:      info != nil && info.GetChannelType() == constant.ChannelTypeOpenRouter,
    PreserveThinkingSuffix: model_setting.ShouldPreserveThinkingSuffix,
}
```

Do not read `info.ChannelSetting` unless `info.ChannelMeta != nil` (promoted field would panic). The snapshot is already cached on `info.convOptions`; do not add invalidation. Channel settings are on `RelayInfo` before the first `ConvOptions()` call.

- [ ] **Step 6: Run tests to verify they pass**

Run:

```bash
cd /Users/flintylemming/Projects/new-api
cd relaykit && GOWORK=off go test ./dto/ -run TestChannelSettingsOpenAIPromptIncludesCacheJSON -count=1
cd relaykit && GOWORK=off go build ./...
```

Expected: `PASS`, then build success with no output.

Run:

```bash
cd /Users/flintylemming/Projects/new-api
go test ./relay/common/ -run 'TestRelayInfoConvOptionsCopiesOpenAIPromptIncludesCache|TestRelayInfoMetaTypedNilReceiver' -count=1
```

Expected: `PASS` (both tests).

- [ ] **Step 7: Commit**

```bash
git add relaykit/dto/channel_settings.go relaykit/dto/channel_settings_test.go relaykit/relayconvert/convmeta/options.go relay/common/relay_info.go relay/common/relay_info_test.go
git commit -m "$(cat <<'EOF'
feat: snapshot openai_prompt_includes_cache into Claude converter options

Channel setting JSON can opt a channel into Anthropic cache semantics
without making relaykit import host setting packages.
EOF
)"
```

---

### Task 2: Subtract cache from Anthropic Messages `input_tokens`

**Files:**
- Modify: `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go`
- Modify: `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp_test.go`
- Do not modify: `service/text_quota.go`, Gemini converters, golden fixtures

**Interfaces:**
- Consumes: `convmeta.OptionsOf(info).Claude.OpenAIPromptIncludesCache` from Task 1; `dto.Usage`; `InputTokenDetails.CacheCreationTokensTotal()`; existing early return when `BillingUsage` is Claude Messages / Anthropic semantic
- Produces: `buildClaudeUsageFromOpenAIUsage(oaiUsage *dto.Usage, info convmeta.Meta) *dto.ClaudeUsage`
  - Nil `oaiUsage` → nil
  - Nil `info` → switch off (`OptionsOf` returns empty options)
  - Claude `input_tokens` = `prompt_tokens` unless `CacheWriteTokens > 0` OR switch on, then `max(0, prompt_tokens - cache_read - cache_creation)`
  - `CacheReadInputTokens` / `CacheCreationInputTokens` still filled from OpenAI details
  - `BillingUsage` remains original OpenAI usage

Call sites that already have `info convmeta.Meta` (pass it through):

- `StreamResponseOpenAI2Claude`: three `buildClaudeUsageFromOpenAIUsage(oaiUsage)` calls (approx. lines 260, 287, 425)
- `FinalizeStreamResponseOpenAI2Claude`: `buildClaudeUsageFromOpenAIUsage(state.Usage)` (approx. line 459)
- `ResponseOpenAI2Claude`: `buildClaudeUsageFromOpenAIUsage(&openAIResponse.Usage)` (approx. line 507)

- [ ] **Step 1: Write the failing table test and stream test**

In `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp_test.go`, keep `TestBuildClaudeUsageFromOpenAICacheWriteUsage` but change its call to the new two-arg signature (nil meta = switch off). Then add:

```go
func switchOnMeta() convmeta.Meta {
	return &convmeta.Values{
		Options: &convmeta.Options{
			Claude: convmeta.ClaudeOptions{OpenAIPromptIncludesCache: true},
		},
	}
}

func TestBuildClaudeUsageFromOpenAIPromptIncludesCache(t *testing.T) {
	anthropicUsage := &dto.ClaudeUsage{
		InputTokens:          99,
		OutputTokens:         912,
		CacheReadInputTokens: 28416,
	}

	tests := []struct {
		name               string
		info               convmeta.Meta
		oai                dto.Usage
		wantInput          int
		wantCacheRead      int
		wantCacheCreate    int
		wantBillingPrompt  int
		wantSameClaudePtr  bool
		wantClaudeInput    int
	}{
		{
			name: "switch off cached tokens keeps prompt_tokens",
			info: nil,
			oai: dto.Usage{
				PromptTokens:     30032,
				CompletionTokens: 912,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 28416,
				},
			},
			wantInput:         30032,
			wantCacheRead:     28416,
			wantBillingPrompt: 30032,
		},
		{
			name: "switch on subtracts cache read",
			info: switchOnMeta(),
			oai: dto.Usage{
				PromptTokens:     30032,
				CompletionTokens: 912,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 28416,
				},
			},
			wantInput:         1616,
			wantCacheRead:     28416,
			wantBillingPrompt: 30032,
		},
		{
			name: "switch on subtracts cache read and cache creation",
			info: switchOnMeta(),
			oai: dto.Usage{
				PromptTokens:     10000,
				CompletionTokens: 10,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens:         3000,
					CachedCreationTokens: 2000,
				},
			},
			wantInput:         5000,
			wantCacheRead:     3000,
			wantCacheCreate:   2000,
			wantBillingPrompt: 10000,
		},
		{
			name: "switch off cache_write still clamps overlapping prefixes",
			info: nil,
			oai: dto.Usage{
				PromptTokens:     3619,
				CompletionTokens: 36,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens:     2921,
					CacheWriteTokens: 3616,
				},
			},
			wantInput:         0,
			wantCacheRead:     2921,
			wantCacheCreate:   3616,
			wantBillingPrompt: 3619,
		},
		{
			name: "switch on and cache_write subtracts once",
			info: switchOnMeta(),
			oai: dto.Usage{
				PromptTokens:     10000,
				CompletionTokens: 10,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens:     1000,
					CacheWriteTokens: 2000,
				},
			},
			wantInput:         7000,
			wantCacheRead:     1000,
			wantCacheCreate:   2000,
			wantBillingPrompt: 10000,
		},
		{
			name: "switch on prompt smaller than cache clamps to zero",
			info: switchOnMeta(),
			oai: dto.Usage{
				PromptTokens:     100,
				CompletionTokens: 10,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 150,
				},
			},
			wantInput:         0,
			wantCacheRead:     150,
			wantBillingPrompt: 100,
		},
		{
			name: "switch on does not rewrite anthropic billing usage",
			info: switchOnMeta(),
			oai: dto.Usage{
				PromptTokens:     30032,
				CompletionTokens: 912,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 28416,
				},
				BillingUsage: &dto.BillingUsage{
					Source:      dto.BillingUsageSourceClaudeMessages,
					Semantic:    dto.BillingUsageSemanticAnthropic,
					ClaudeUsage: anthropicUsage,
				},
			},
			wantInput:         99,
			wantCacheRead:     28416,
			wantSameClaudePtr: false,
			wantClaudeInput:   99,
		},
		{
			name: "switch on preserves original openai billing prompt tokens",
			info: switchOnMeta(),
			oai: dto.Usage{
				PromptTokens:     30032,
				CompletionTokens: 912,
				TotalTokens:      30944,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 28416,
				},
			},
			wantInput:         1616,
			wantCacheRead:     28416,
			wantBillingPrompt: 30032,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := buildClaudeUsageFromOpenAIUsage(&tt.oai, tt.info)
			require.NotNil(t, usage)
			assert.Equal(t, tt.wantInput, usage.InputTokens)
			assert.Equal(t, tt.wantCacheRead, usage.CacheReadInputTokens)
			if tt.wantCacheCreate != 0 {
				assert.Equal(t, tt.wantCacheCreate, usage.CacheCreationInputTokens)
			}
			if tt.oai.BillingUsage != nil && tt.oai.BillingUsage.ClaudeUsage != nil {
				assert.Equal(t, tt.wantClaudeInput, usage.InputTokens)
				assert.Equal(t, anthropicUsage.InputTokens, usage.InputTokens)
				return
			}
			require.NotNil(t, usage.BillingUsage)
			require.NotNil(t, usage.BillingUsage.OpenAIUsage)
			assert.Equal(t, tt.wantBillingPrompt, usage.BillingUsage.OpenAIUsage.PromptTokens)
			assert.Equal(t, dto.BillingUsageSemanticOpenAI, usage.BillingUsage.Semantic)
		})
	}
}

func TestStreamResponseOpenAI2ClaudeSubtractsCacheWhenSwitchOn(t *testing.T) {
	info := &convmeta.Values{
		SendResponseCount: 2,
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeText,
			Index:            0,
		},
		Options: &convmeta.Options{
			Claude: convmeta.ClaudeOptions{OpenAIPromptIncludesCache: true},
		},
	}

	responses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{FinishReason: ptr("stop")},
		},
		Usage: &dto.Usage{
			PromptTokens:     30032,
			CompletionTokens: 912,
			TotalTokens:      30944,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 28416,
			},
		},
	}, info)
	require.NotEmpty(t, responses)

	var delta *dto.ClaudeResponse
	for _, resp := range responses {
		if resp != nil && resp.Type == "message_delta" {
			delta = resp
			break
		}
	}
	require.NotNil(t, delta)
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 1616, delta.Usage.InputTokens)
	assert.Equal(t, 28416, delta.Usage.CacheReadInputTokens)
	assert.Equal(t, 912, delta.Usage.OutputTokens)
	require.NotNil(t, delta.Usage.BillingUsage)
	require.NotNil(t, delta.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 30032, delta.Usage.BillingUsage.OpenAIUsage.PromptTokens)
}
```

Also update the existing cache-write test call:

```go
usage := buildClaudeUsageFromOpenAIUsage(&dto.Usage{
    PromptTokens:     3619,
    CompletionTokens: 36,
    TotalTokens:      3655,
    PromptTokensDetails: dto.InputTokenDetails{
        CachedTokens:     2921,
        CacheWriteTokens: 3616,
    },
}, nil)
```

Row notes for the implementer:

- Row “switch on subtracts cache creation” uses `CachedCreationTokens` (not `CacheWriteTokens`) so the new switch, not the old `CacheWriteTokens > 0` branch, is what enables subtract.
- Row “switch on and cache_write subtracts once” would be `4000` if cache were subtracted twice (`10000-1000-2000-1000-2000`). Expected `7000` proves a single subtract.
- Anthropic `BillingUsage` uses `InputTokens: 99` so a mistaken rewrite (`30032-28416=1616`) cannot pass.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/flintylemming/Projects/new-api/relaykit
GOWORK=off go test ./relayconvert/internal/oai_chat/ -run 'TestBuildClaudeUsageFromOpenAIPromptIncludesCache|TestStreamResponseOpenAI2ClaudeSubtractsCacheWhenSwitchOn|TestBuildClaudeUsageFromOpenAICacheWriteUsage' -count=1
```

Expected: FAIL compile: `too many arguments in call to buildClaudeUsageFromOpenAIUsage` (and the old one-arg call sites still exist until Step 3).

- [ ] **Step 3: Change `buildClaudeUsageFromOpenAIUsage` and every call site**

Replace the function in `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go` (starts at line 38). Keep the existing early returns. Replace only the subtract condition:

```go
func buildClaudeUsageFromOpenAIUsage(oaiUsage *dto.Usage, info convmeta.Meta) *dto.ClaudeUsage {
	if oaiUsage == nil {
		return nil
	}
	if billingUsage := dto.CloneBillingUsage(oaiUsage.BillingUsage); billingUsage != nil && billingUsage.ClaudeUsage != nil {
		if billingUsage.Source == dto.BillingUsageSourceClaudeMessages || billingUsage.Semantic == dto.BillingUsageSemanticAnthropic {
			return billingUsage.ClaudeUsage
		}
	}
	billingUsage := dto.NewOpenAIChatBillingUsage(oaiUsage)
	if existingBillingUsage := dto.CloneBillingUsage(oaiUsage.BillingUsage); existingBillingUsage != nil && existingBillingUsage.OpenAIUsage != nil {
		if existingBillingUsage.Source == dto.BillingUsageSourceOAIChat ||
			existingBillingUsage.Source == dto.BillingUsageSourceOAIResponses ||
			existingBillingUsage.Semantic == dto.BillingUsageSemanticOpenAI {
			billingUsage = existingBillingUsage
		}
	}
	cacheCreation5m, cacheCreation1h := NormalizeCacheCreationSplit(
		oaiUsage.PromptTokensDetails.CachedCreationTokens,
		oaiUsage.ClaudeCacheCreation5mTokens,
		oaiUsage.ClaudeCacheCreation1hTokens,
	)
	cacheRead := oaiUsage.PromptTokensDetails.CachedTokens
	cacheCreate := oaiUsage.PromptTokensDetails.CacheCreationTokensTotal()
	inputTokens := oaiUsage.PromptTokens
	excludeCache := oaiUsage.PromptTokensDetails.CacheWriteTokens > 0 ||
		convmeta.OptionsOf(info).Claude.OpenAIPromptIncludesCache
	if excludeCache {
		inputTokens = oaiUsage.PromptTokens - cacheRead - cacheCreate
		if inputTokens < 0 {
			inputTokens = 0
		}
	}
	usage := &dto.ClaudeUsage{
		InputTokens:              inputTokens,
		OutputTokens:             oaiUsage.CompletionTokens,
		CacheCreationInputTokens: cacheCreate,
		CacheReadInputTokens:     cacheRead,
		BillingUsage:             billingUsage,
	}
	if cacheCreation5m > 0 || cacheCreation1h > 0 {
		usage.CacheCreation = &dto.ClaudeCacheCreationUsage{
			Ephemeral5mInputTokens: cacheCreation5m,
			Ephemeral1hInputTokens: cacheCreation1h,
		}
	}
	return usage
}
```

Single condition, no double subtract. `CacheCreationTokensTotal()` already prefers the larger of `cached_creation_tokens` and `cache_write_tokens`.

Update the five call sites in the same file to pass `info`:

```go
Usage: buildClaudeUsageFromOpenAIUsage(oaiUsage, info),
```

```go
Usage: buildClaudeUsageFromOpenAIUsage(state.Usage, info),
```

```go
claudeResponse.Usage = buildClaudeUsageFromOpenAIUsage(&openAIResponse.Usage, info)
```

Do not change the `message_start` block (approx. lines 133–148). It must keep:

```go
Usage: &dto.ClaudeUsage{
    InputTokens:  info.GetEstimatePromptTokens(),
    OutputTokens: 0,
},
```

Do not add a helper function. Do not touch Gemini converters. Do not touch `service/text_quota.go`.

- [ ] **Step 4: Run converter tests and goldens**

Run:

```bash
cd /Users/flintylemming/Projects/new-api/relaykit
GOWORK=off go test ./relayconvert/internal/oai_chat/ -count=1
```

Expected: `PASS`

Run golden matrix (default-off must still report `input_tokens=10` with `cached_tokens=3`):

```bash
cd /Users/flintylemming/Projects/new-api/relaykit
GOWORK=off go test ./relayconvert/ -run 'TestGoldenResponseConversionMatrix|TestGoldenStreamConversionMatrix' -count=1
```

Expected: `PASS`. If `openai_to_claude` golden fails because `input_tokens` became `7`, the switch is incorrectly defaulting on — fix `OptionsOf(nil)` / zero `ClaudeOptions`, do not rewrite the fixture.

Run:

```bash
cd /Users/flintylemming/Projects/new-api/relaykit
GOWORK=off go build ./...
```

Expected: success, no output.

- [ ] **Step 5: Commit**

```bash
git add relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp_test.go
git commit -m "$(cat <<'EOF'
fix: subtract included cache from Anthropic Messages input_tokens

OpenAI-compatible upstreams that fold cache into prompt_tokens were
inflating Claude Code input counts; the channel switch now matches
Anthropic cache semantics on the client-facing usage only.
EOF
)"
```

---

### Task 3: Channel UI, i18n, and setting docs

**Files:**
- Modify: `web/src/features/channels/types.ts`
- Modify: `web/src/features/channels/lib/channel-form.ts`
- Modify: `web/src/features/channels/lib/channel-form-errors.ts`
- Modify: `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx`
- Modify: `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` (script only)
- Modify: `docs/channel/other_setting.md`

**Interfaces:**
- Consumes: channel `setting` JSON field `openai_prompt_includes_cache` from Task 1
- Produces: form field `openai_prompt_includes_cache: boolean` (default `false`), shown for **all** channel types next to `thinking_to_content`, included in parse/serialize and the advanced “configured” indicator

No new frontend behavioral test. Spec: wiring + i18n keys in all locales.

- [ ] **Step 1: Add the TypeScript and form field**

`web/src/features/channels/types.ts` — `ChannelSettings`:

```ts
export interface ChannelSettings {
  force_format?: boolean
  thinking_to_content?: boolean
  openai_prompt_includes_cache?: boolean
  proxy?: string
  pass_through_body_enabled?: boolean
  system_prompt?: string
  system_prompt_override?: boolean
  http_protocol?: 'auto' | 'http1' | string
  http2_connection_shards?: number
}
```

`web/src/features/channels/lib/channel-form.ts`:

1. Schema, immediately after `thinking_to_content`:

```ts
force_format: z.boolean().optional(),
thinking_to_content: z.boolean().optional(),
openai_prompt_includes_cache: z.boolean().optional(),
```

2. `defaultFormValues` extra settings:

```ts
force_format: false,
thinking_to_content: false,
openai_prompt_includes_cache: false,
```

3. `transformChannelToFormDefaults` `extraSettings` default object and parsed object:

```ts
let extraSettings = {
  force_format: false,
  thinking_to_content: false,
  openai_prompt_includes_cache: false,
  proxy: '',
  http_protocol: HTTP_PROTOCOL_AUTO as 'auto' | 'http1',
  http2_connection_shards: 1,
  pass_through_body_enabled: false,
  system_prompt: '',
  system_prompt_override: false,
}
```

```ts
extraSettings = {
  force_format: parsed.force_format || false,
  thinking_to_content: parsed.thinking_to_content || false,
  openai_prompt_includes_cache:
    parsed.openai_prompt_includes_cache || false,
  proxy: parsed.proxy || '',
  http_protocol: protocol,
  http2_connection_shards:
    protocol === HTTP_PROTOCOL_HTTP1 ? 1 : shards,
  pass_through_body_enabled: parsed.pass_through_body_enabled || false,
  system_prompt: parsed.system_prompt || '',
  system_prompt_override: parsed.system_prompt_override || false,
}
```

4. `buildSettingJSON`:

```ts
const settingObj: Record<string, unknown> = {
  force_format: formData.force_format || false,
  thinking_to_content: formData.thinking_to_content || false,
  openai_prompt_includes_cache:
    formData.openai_prompt_includes_cache || false,
  proxy: formData.proxy?.trim() || '',
  pass_through_body_enabled: formData.pass_through_body_enabled || false,
  system_prompt: formData.system_prompt || '',
  system_prompt_override: formData.system_prompt_override || false,
}
```

`web/src/features/channels/lib/channel-form-errors.ts` — add `'openai_prompt_includes_cache'` to `ADVANCED_SETTINGS_FIELDS` immediately after `'thinking_to_content'`.

- [ ] **Step 2: Wire the drawer switch and configured indicators**

In `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx`:

1. Field-name list (~line 284), after `'thinking_to_content'`:

```ts
'force_format',
'thinking_to_content',
'openai_prompt_includes_cache',
```

2. `hasAdvancedSettingsValues` (~line 341):

```ts
values.force_format ||
values.thinking_to_content ||
values.openai_prompt_includes_cache ||
values.pass_through_body_enabled ||
```

3. Watch the field next to `currentThinkingToContent` (~line 747):

```ts
const currentForceFormat = form.watch('force_format')
const currentThinkingToContent = form.watch('thinking_to_content')
const currentOpenAIPromptIncludesCache = form.watch(
  'openai_prompt_includes_cache'
)
```

4. `extraSettingsConfigured` (~line 1017):

```ts
const extraSettingsConfigured = Boolean(
  currentForceFormat ||
  currentThinkingToContent ||
  currentOpenAIPromptIncludesCache ||
  currentPassThroughBodyEnabled ||
  currentDisableTaskPollingSleep ||
  currentProxy?.trim() ||
  currentSystemPrompt?.trim() ||
  currentSystemPromptOverride ||
  (currentHttpProtocol && currentHttpProtocol !== 'auto') ||
  (currentHttp2ConnectionShards != null && currentHttp2ConnectionShards > 1)
)
```

5. Switch UI immediately after the `thinking_to_content` `FormField` (shown for **all** channel types, unlike `force_format` which stays `currentType === 1`):

```tsx
<FormField
  control={form.control}
  name='openai_prompt_includes_cache'
  render={({ field }) => (
    <FormItem className='flex items-center justify-between px-4 py-3'>
      <div className='space-y-0.5'>
        <FormLabel>
          {t('OpenAI prompt tokens include cache')}
        </FormLabel>
        <FormDescription>
          {t(
            'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.'
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

Default unchecked via form defaults. Do not gate on channel type.

- [ ] **Step 3: Add i18n keys via the sanctioned script**

Create `web/scripts/add-missing-keys.mjs` with this `newKeys` object (do not hand-edit locale JSON):

```javascript
import fs from 'node:fs/promises'
import path from 'node:path'

const LOCALES_DIR = path.resolve('src/i18n/locales')

function stableStringify(obj) {
  return JSON.stringify(obj, null, 2) + '\n'
}

const newKeys = {
  en: {
    'OpenAI prompt tokens include cache':
      'OpenAI prompt tokens include cache',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.',
  },
  zh: {
    'OpenAI prompt tokens include cache':
      'OpenAI prompt tokens 包含缓存',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      '将此渠道转换为 Anthropic Messages 时，从 input_tokens 减去缓存读取和缓存创建。若上游把缓存 token 计入 prompt_tokens，请开启此项。',
  },
  'zh-TW': {
    'OpenAI prompt tokens include cache':
      'OpenAI prompt tokens 包含快取',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      '將此渠道轉換為 Anthropic Messages 時，從 input_tokens 減去快取讀取與快取建立。若上游把快取 token 計入 prompt_tokens，請開啟此項。',
  },
  fr: {
    'OpenAI prompt tokens include cache':
      'Les prompt tokens OpenAI incluent le cache',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      'Lors de la conversion de ce canal vers Anthropic Messages, soustraire la lecture et la création de cache de input_tokens. Activez si l’amont compte les jetons en cache dans prompt_tokens.',
  },
  ja: {
    'OpenAI prompt tokens include cache':
      'OpenAI の prompt tokens にキャッシュを含む',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      'このチャネルを Anthropic Messages に変換するとき、input_tokens からキャッシュ読み取りとキャッシュ作成を減算します。上流がキャッシュトークンを prompt_tokens に含めている場合に有効にしてください。',
  },
  ru: {
    'OpenAI prompt tokens include cache':
      'OpenAI prompt tokens включают кэш',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      'При преобразовании канала в Anthropic Messages вычитать cache read и cache creation из input_tokens. Включайте, если апстрим считает кэшированные токены внутри prompt_tokens.',
  },
  vi: {
    'OpenAI prompt tokens include cache':
      'OpenAI prompt tokens bao gồm cache',
    'When converting this channel to Anthropic Messages, subtract cache read and cache creation from input_tokens. Enable if the upstream counts cached tokens inside prompt_tokens.':
      'Khi chuyển kênh này sang Anthropic Messages, trừ cache read và cache creation khỏi input_tokens. Bật nếu upstream tính token cache trong prompt_tokens.',
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

Run from `web/`:

```bash
cd /Users/flintylemming/Projects/new-api/web
node scripts/add-missing-keys.mjs
bun run i18n:sync
```

Expected: each locale reports 2 translations applied (or updates), then sync succeeds.

If `find-missing-keys.mjs` is not already in the repo, create it from `.agents/skills/i18n-translate/SKILL.md` Step 2, run `node scripts/find-missing-keys.mjs`, then delete any scripts you created for this task (`add-missing-keys.mjs` and `find-missing-keys.mjs` if they were not previously committed).

Expected from find-missing-keys: `All t() keys found in en.json!`

- [ ] **Step 4: Update channel extra-settings docs**

In `docs/channel/other_setting.md`, add item 4 after `thinking_to_content`:

```markdown
4. openai_prompt_includes_cache
   - 用于标识上游 OpenAI 兼容接口的 `prompt_tokens` 是否已包含缓存读取和缓存创建
   - 类型为布尔值，默认 false / 省略。设置为 true 时，转换为 Anthropic Messages 会从 `input_tokens` 中减去 `cache_read_input_tokens` 与 `cache_creation_input_tokens`
   - 仅影响返回给 Anthropic Messages 客户端的 usage，不影响计费，也不影响 Chat Completions 客户端
   - 若上游的 `prompt_tokens` 已经排除缓存，不要开启此项，否则 `input_tokens` 会少计
```

Add the field to the JSON example:

```json
{
    "force_format": true,
    "thinking_to_content": true,
    "openai_prompt_includes_cache": true,
    "proxy": "socks5://proxy.example:1080"
}
```

- [ ] **Step 5: Typecheck the frontend**

Run:

```bash
cd /Users/flintylemming/Projects/new-api/web
bun run typecheck
```

Expected: exit 0, no errors related to `openai_prompt_includes_cache`.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/channels/types.ts web/src/features/channels/lib/channel-form.ts web/src/features/channels/lib/channel-form-errors.ts web/src/features/channels/components/drawers/channel-mutate-drawer.tsx web/src/i18n/locales/*.json docs/channel/other_setting.md
git commit -m "$(cat <<'EOF'
feat: add channel switch for OpenAI prompt tokens that include cache

Operators can opt Anthropic Messages clients into cache-excluded
input_tokens without changing billing or Chat Completions usage.
EOF
)"
```

Do not commit `web/scripts/add-missing-keys.mjs` or `web/scripts/find-missing-keys.mjs` unless those files already existed in the repo.

---

## Self-review

**Spec coverage**

| Spec requirement | Task |
| --- | --- |
| Channel `setting` JSON `openai_prompt_includes_cache`, default false / omitempty | Task 1 |
| `convmeta.ClaudeOptions.OpenAIPromptIncludesCache` | Task 1 |
| Host copies setting into `RelayInfo.ConvOptions`; nil → false | Task 1 |
| `buildClaudeUsageFromOpenAIUsage` takes `convmeta.Meta` | Task 2 |
| Early return when BillingUsage is already Anthropic | Task 2 |
| Subtract iff `cache_write_tokens > 0` OR switch; clamp at 0; subtract once | Task 2 |
| Cache read/create fields still populated; BillingUsage original OpenAI | Task 2 |
| Stream `message_delta` with switch on | Task 2 |
| `message_start` estimate unchanged | Task 2 (explicit non-edit) |
| Golden `openai_to_claude` default-off unchanged | Task 2 |
| Table rows 1–8 | Task 2 |
| UI all channel types, next to `thinking_to_content` | Task 3 |
| Form schema / parse / serialize / configured indicator / `ADVANCED_SETTINGS_FIELDS` | Task 3 |
| i18n all seven locales | Task 3 |
| `docs/channel/other_setting.md` | Task 3 |
| No `text_quota.go` change; no Chat Completions / Gemini rewrite; no auto-enable | Global Constraints + Task 2 non-edits |
| relaykit independent build | Tasks 1–2 |

**Placeholder scan:** none. Every step has exact paths, full code, and commands with expected output.

**Type consistency:** field name is `OpenAIPromptIncludesCache` / JSON `openai_prompt_includes_cache` / form `openai_prompt_includes_cache` throughout. Function signature is `buildClaudeUsageFromOpenAIUsage(oaiUsage *dto.Usage, info convmeta.Meta) *dto.ClaudeUsage` in Task 2 tests and implementation. Tests set the flag on `convmeta.Values.Options.Claude.OpenAIPromptIncludesCache`.
