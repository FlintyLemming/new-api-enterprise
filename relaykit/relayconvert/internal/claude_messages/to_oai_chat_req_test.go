package claudemessages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/samber/lo"
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

	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, stripOnMeta())
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

	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, &convmeta.Values{})
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

	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, info)
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

	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, stripOnMeta())
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

		got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, stripOnMeta())
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

		got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, stripOnMeta())
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

	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, stripOnMeta())
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

	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), req, info)
	require.NoError(t, err)
	parts := systemMessages(got)[0].ParseContent()
	require.Len(t, parts, 3)
	assert.JSONEq(t, `{"type":"ephemeral"}`, string(parts[0].CacheControl))
}

func TestClaudeMessagesRequestToOpenAIChatToolResultContent(t *testing.T) {
	const imageData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	const dataURL = "data:image/png;base64," + imageData
	imageBlock := dto.ClaudeMediaMessage{
		Type:   "image",
		Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "image/png", Data: imageData},
	}
	textBlock := func(text string) dto.ClaudeMediaMessage {
		return dto.ClaudeMediaMessage{Type: "text", Text: lo.ToPtr(text)}
	}

	tests := []struct {
		name        string
		content     any
		wantContent string
		wantImages  []string
	}{
		{
			name:        "string passes through",
			content:     "done",
			wantContent: "done",
		},
		{
			name:        "text and image keep text on tool message",
			content:     []dto.ClaudeMediaMessage{textBlock("screenshot taken"), imageBlock},
			wantContent: "screenshot taken",
			wantImages:  []string{dataURL},
		},
		{
			name:        "image only uses placeholder",
			content:     []dto.ClaudeMediaMessage{imageBlock},
			wantContent: "[image]",
			wantImages:  []string{dataURL},
		},
		{
			name: "url image source is forwarded as is",
			content: []dto.ClaudeMediaMessage{{
				Type:   "image",
				Source: &dto.ClaudeMessageSource{Type: "url", Url: "https://example.com/shot.png"},
			}},
			wantContent: "[image]",
			wantImages:  []string{"https://example.com/shot.png"},
		},
		{
			name:        "text blocks join with newline",
			content:     []dto.ClaudeMediaMessage{textBlock("first"), textBlock(""), textBlock("second")},
			wantContent: "first\nsecond",
		},
		{
			name:        "unknown block keeps whole array",
			content:     []dto.ClaudeMediaMessage{textBlock("see doc"), {Type: "document", Data: "opaque"}},
			wantContent: `[{"type":"text","text":"see doc"},{"type":"document","data":"opaque"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), dto.ClaudeRequest{
				Model:     "claude-test",
				MaxTokens: lo.ToPtr(uint(64)),
				Messages: []dto.ClaudeMessage{
					{Role: "user", Content: "take a screenshot"},
					{Role: "assistant", Content: []dto.ClaudeMediaMessage{
						{Type: "tool_use", Id: "toolu_1", Name: "screenshot", Input: map[string]any{}},
					}},
					{Role: "user", Content: []dto.ClaudeMediaMessage{
						{Type: "tool_result", ToolUseId: "toolu_1", Content: tt.content},
						textBlock("what do you see?"),
					}},
				},
			}, &convmeta.Values{})
			require.NoError(t, err)

			require.Len(t, got.Messages, 4)
			tool := got.Messages[2]
			assert.Equal(t, "tool", tool.Role)
			assert.Equal(t, "toolu_1", tool.ToolCallId)
			if tt.wantImages == nil && tt.wantContent[0] == '[' {
				assert.JSONEq(t, tt.wantContent, tool.StringContent())
			} else {
				assert.Equal(t, tt.wantContent, tool.StringContent())
			}

			user := got.Messages[3]
			assert.Equal(t, "user", user.Role)
			parts := user.ParseContent()
			require.Len(t, parts, len(tt.wantImages)+1)
			for i, wantURL := range tt.wantImages {
				assert.Equal(t, dto.ContentTypeImageURL, parts[i].Type)
				require.NotNil(t, parts[i].GetImageMedia())
				assert.Equal(t, wantURL, parts[i].GetImageMedia().Url)
			}
			assert.Equal(t, dto.ContentTypeText, parts[len(parts)-1].Type)
			assert.Equal(t, "what do you see?", parts[len(parts)-1].Text)
		})
	}
}

func TestClaudeMessagesRequestToOpenAIChatKeepsParallelToolResultsContiguous(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgo="
	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: lo.ToPtr(uint(64)),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "screenshot and read the note"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "text", Text: lo.ToPtr("Let me look.")},
				{Type: "tool_use", Id: "toolu_shot", Name: "screenshot", Input: map[string]any{}},
				{Type: "tool_use", Id: "toolu_read", Name: "read_file", Input: map[string]any{"path": "note.txt"}},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "toolu_shot", Content: []dto.ClaudeMediaMessage{{
					Type:   "image",
					Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
				}}},
				{Type: "tool_result", ToolUseId: "toolu_read", Content: "note contents"},
			}},
		},
	}, &convmeta.Values{})
	require.NoError(t, err)

	roles := make([]string, 0, len(got.Messages))
	for _, message := range got.Messages {
		roles = append(roles, message.Role)
	}
	assert.Equal(t, []string{"user", "assistant", "tool", "tool", "user"}, roles)
	assert.Len(t, got.Messages[1].ParseToolCalls(), 2)
	assert.Equal(t, "[image]", got.Messages[2].StringContent())
	assert.Equal(t, "toolu_shot", got.Messages[2].ToolCallId)
	assert.Equal(t, "note contents", got.Messages[3].StringContent())
	assert.NotContains(t, got.Messages[2].StringContent()+got.Messages[3].StringContent(), "base64")

	parts := got.Messages[4].ParseContent()
	require.Len(t, parts, 1)
	assert.Equal(t, dto.ContentTypeImageURL, parts[0].Type)
	require.NotNil(t, parts[0].GetImageMedia())
	assert.Equal(t, dataURL, parts[0].GetImageMedia().Url)
}
