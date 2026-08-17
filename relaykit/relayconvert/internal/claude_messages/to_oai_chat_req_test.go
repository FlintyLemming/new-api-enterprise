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
