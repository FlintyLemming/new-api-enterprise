package dto

import (
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

const (
	claudeSystemReminderOpen  = "<system-reminder>\n"
	claudeSystemReminderClose = "\n</system-reminder>"
)

// InlineSystemMessages rewrites mid-conversation `role: "system"` messages
// into user-visible <system-reminder> text blocks kept at the same position.
//
// Claude Code inserts system messages into the conversation (token budget,
// environment updates). Chat templates such as DeepSeek's hoist every system
// message to the start of the rendered prompt, so each new one changes the
// prompt prefix and invalidates the upstream prefix cache for the whole
// history. Keeping the text in place makes the history append-only again.
//
// A system message is appended to the preceding user message; when there is
// none it becomes a standalone user message. System messages without text
// (effort-only ones) are dropped. The rewrite of a message depends only on
// that message and its predecessor, so earlier turns render identically on
// every later request. It reports whether anything changed.
func (c *ClaudeRequest) InlineSystemMessages() bool {
	if c == nil || len(c.Messages) == 0 {
		return false
	}
	hasSystem := false
	for i := range c.Messages {
		if c.Messages[i].Role == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		return false
	}

	out := make([]ClaudeMessage, 0, len(c.Messages))
	for _, message := range c.Messages {
		if message.Role != "system" {
			out = append(out, message)
			continue
		}
		blocks := systemMessageAsReminderBlocks(message)
		if len(blocks) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == "user" {
			if merged, ok := claudeContentAsBlocks(out[n-1].Content); ok {
				out[n-1].Content = append(merged, blocks...)
				continue
			}
		}
		out = append(out, ClaudeMessage{Role: "user", Content: blocks})
	}
	c.Messages = out
	return true
}

// systemMessageAsReminderBlocks wraps the text of a system message in a
// single <system-reminder> block. Non-text blocks are kept after it.
func systemMessageAsReminderBlocks(message ClaudeMessage) []any {
	var texts []string
	var extra []any
	switch content := message.Content.(type) {
	case nil:
	case string:
		texts = append(texts, content)
	default:
		blocks, ok := claudeContentAsBlocks(content)
		if !ok {
			return nil
		}
		for _, block := range blocks {
			blockMap, isMap := block.(map[string]any)
			if isMap && blockMap["type"] == ContentTypeText {
				if text, ok := blockMap["text"].(string); ok {
					texts = append(texts, text)
				}
				continue
			}
			extra = append(extra, block)
		}
	}

	text := strings.TrimSpace(strings.Join(texts, "\n"))
	result := make([]any, 0, 1+len(extra))
	if text != "" {
		result = append(result, map[string]any{
			"type": ContentTypeText,
			"text": claudeSystemReminderOpen + text + claudeSystemReminderClose,
		})
	}
	return append(result, extra...)
}

// claudeContentAsBlocks returns message content as a fresh slice of generic
// content blocks, converting string content to one text block.
func claudeContentAsBlocks(content any) ([]any, bool) {
	switch value := content.(type) {
	case nil:
		return []any{}, true
	case string:
		if value == "" {
			return []any{}, true
		}
		return []any{map[string]any{"type": ContentTypeText, "text": value}}, true
	case []any:
		return append(make([]any, 0, len(value)+1), value...), true
	default:
		blocks, err := kitutil.Any2Type[[]any](value)
		if err != nil {
			return nil, false
		}
		return blocks, true
	}
}
