package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeClaudeRequest(t *testing.T, body string) *ClaudeRequest {
	t.Helper()
	var request ClaudeRequest
	require.NoError(t, json.Unmarshal([]byte(body), &request))
	return &request
}

func encodeMessages(t *testing.T, request *ClaudeRequest) string {
	t.Helper()
	encoded, err := json.Marshal(request.Messages)
	require.NoError(t, err)
	return string(encoded)
}

func TestInlineSystemMessagesNoSystemIsUntouched(t *testing.T) {
	request := decodeClaudeRequest(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`)
	before := encodeMessages(t, request)
	assert.False(t, request.InlineSystemMessages())
	assert.Equal(t, before, encodeMessages(t, request))
}

func TestInlineSystemMessagesAppendsToPreviousToolResult(t *testing.T) {
	request := decodeClaudeRequest(t, `{"messages":[
		{"role":"user","content":"start"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},
		{"role":"system","content":[{"type":"text","text":"<total_tokens>100 tokens left</total_tokens>","cache_control":{"type":"ephemeral"}}]}
	]}`)
	assert.True(t, request.InlineSystemMessages())
	assert.JSONEq(t, `[
		{"role":"user","content":"start"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"t1","content":"ok"},
			{"type":"text","text":"<system-reminder>\n<total_tokens>100 tokens left</total_tokens>\n</system-reminder>"}
		]}
	]`, encodeMessages(t, request))
}

func TestInlineSystemMessagesStringPreviousAndStandalone(t *testing.T) {
	request := decodeClaudeRequest(t, `{"messages":[
		{"role":"system","content":"leading"},
		{"role":"user","content":"hi"},
		{"role":"system","content":"after user"},
		{"role":"assistant","content":"yo"},
		{"role":"system","content":"after assistant"}
	]}`)
	assert.True(t, request.InlineSystemMessages())
	assert.JSONEq(t, `[
		{"role":"user","content":[{"type":"text","text":"<system-reminder>\nleading\n</system-reminder>"}]},
		{"role":"user","content":[
			{"type":"text","text":"hi"},
			{"type":"text","text":"<system-reminder>\nafter user\n</system-reminder>"}
		]},
		{"role":"assistant","content":"yo"},
		{"role":"user","content":[{"type":"text","text":"<system-reminder>\nafter assistant\n</system-reminder>"}]}
	]`, encodeMessages(t, request))
}

func TestInlineSystemMessagesDropsEffortOnly(t *testing.T) {
	request := decodeClaudeRequest(t, `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"system","content":[],"output_config":{"effort":"high"}}
	]}`)
	assert.True(t, request.InlineSystemMessages())
	assert.JSONEq(t, `[{"role":"user","content":"hi"}]`, encodeMessages(t, request))
}

// The rendering of earlier turns must not depend on what follows, otherwise
// the upstream prefix cache breaks again. Claude Code also flips a system
// message between block and string content on the next turn.
func TestInlineSystemMessagesKeepsHistoryPrefixStable(t *testing.T) {
	turn1 := decodeClaudeRequest(t, `{"messages":[
		{"role":"user","content":"start"},
		{"role":"system","content":[{"type":"text","text":"budget 100","cache_control":{"type":"ephemeral"}}]}
	]}`)
	turn2 := decodeClaudeRequest(t, `{"messages":[
		{"role":"user","content":"start"},
		{"role":"system","content":"budget 100"},
		{"role":"assistant","content":"working"},
		{"role":"user","content":"next"},
		{"role":"system","content":"budget 90"}
	]}`)
	turn1.InlineSystemMessages()
	turn2.InlineSystemMessages()
	require.Len(t, turn1.Messages, 1)
	require.Len(t, turn2.Messages, 3)

	first, err := json.Marshal(turn1.Messages[0])
	require.NoError(t, err)
	second, err := json.Marshal(turn2.Messages[0])
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))
}

func TestChannelSettingsInlineClaudeSystemMessagesJSON(t *testing.T) {
	encoded, err := json.Marshal(ChannelSettings{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "inline_claude_system_messages")

	var decoded ChannelSettings
	require.NoError(t, json.Unmarshal([]byte(`{"inline_claude_system_messages":true}`), &decoded))
	assert.True(t, decoded.InlineClaudeSystemMessages)
}
