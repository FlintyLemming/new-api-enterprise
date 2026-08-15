package langfuse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return body
}

func TestAggregateOutputPerProtocol(t *testing.T) {
	openAIExpected := map[string]any{
		"role":              "assistant",
		"content":           "The weather in Paris is 18°C.",
		"reasoning_content": "The user asked about Paris, so the weather tool is needed.",
		"tool_calls": []any{map[string]any{
			"id":       "call_ax1",
			"type":     "function",
			"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`},
		}},
	}
	openAIStreamExpected := map[string]any{
		"role":              "assistant",
		"content":           "The weather in Paris is 18°C.",
		"reasoning_content": "The user asked about Paris, so the weather tool is needed.",
		"tool_calls": []any{map[string]any{
			"id":       "call_ax2",
			"type":     "function",
			"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`},
		}},
	}
	claudeExpected := map[string]any{
		"role":     "assistant",
		"content":  "Let me check the weather in Paris for you.",
		"thinking": "The user wants live weather, so the tool is required.",
		"tool_use": []any{map[string]any{
			"type":  "tool_use",
			"id":    "toolu_01A",
			"name":  "get_weather",
			"input": map[string]any{"city": "Paris"},
		}},
		"stop_reason": "tool_use",
	}
	claudeStreamExpected := map[string]any{
		"role":     "assistant",
		"content":  "Let me check the weather in Paris for you.",
		"thinking": "The user wants live weather, so the tool is required.",
		"tool_use": []any{map[string]any{
			"type":  "tool_use",
			"id":    "toolu_01B",
			"name":  "get_weather",
			"input": map[string]any{"city": "Paris"},
		}},
		"stop_reason": "tool_use",
	}
	responsesExpected := map[string]any{
		"content": "Paris is sunny and 18°C.",
		"function_calls": []any{map[string]any{
			"name":      "get_weather",
			"arguments": `{"city":"Paris"}`,
			"call_id":   "call_r1",
		}},
	}
	responsesStreamExpected := map[string]any{
		"content": "Paris is sunny and 18°C.",
		"function_calls": []any{map[string]any{
			"name":      "get_weather",
			"arguments": `{"city":"Paris"}`,
			"call_id":   "call_r2",
		}},
	}
	geminiExpected := map[string]any{
		"content": "Paris is sunny and 18°C.",
		"function_calls": []any{map[string]any{
			"name": "get_weather",
			"args": map[string]any{"city": "Paris"},
		}},
		"finishReason": "STOP",
	}

	cases := []struct {
		name     string
		kind     string
		isStream bool
		file     string
		want     any
	}{
		{name: "openai chat completion", kind: OutputKindOpenAI, file: "openai_chat.json", want: openAIExpected},
		{name: "openai chat stream", kind: OutputKindOpenAI, isStream: true, file: "openai_chat_sse.txt", want: openAIStreamExpected},
		{name: "claude messages", kind: OutputKindClaude, file: "claude.json", want: claudeExpected},
		{name: "claude messages stream", kind: OutputKindClaude, isStream: true, file: "claude_sse.txt", want: claudeStreamExpected},
		{name: "openai responses", kind: OutputKindResponses, file: "responses.json", want: responsesExpected},
		{name: "openai responses stream", kind: OutputKindResponses, isStream: true, file: "responses_sse.txt", want: responsesStreamExpected},
		{name: "gemini generate content", kind: OutputKindGemini, file: "gemini.json", want: geminiExpected},
		{name: "gemini generate content stream", kind: OutputKindGemini, isStream: true, file: "gemini_sse.txt", want: geminiExpected},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			aggregated, ok := AggregateOutput(testCase.kind, testCase.isStream, fixture(t, testCase.file))
			require.True(t, ok)
			assert.Equal(t, testCase.want, aggregated)
		})
	}
}

func TestAggregateOutputNeverReportsUsage(t *testing.T) {
	for _, testCase := range []struct {
		kind     string
		isStream bool
		file     string
	}{
		{OutputKindOpenAI, false, "openai_chat.json"},
		{OutputKindOpenAI, true, "openai_chat_sse.txt"},
		{OutputKindClaude, false, "claude.json"},
		{OutputKindClaude, true, "claude_sse.txt"},
		{OutputKindResponses, false, "responses.json"},
		{OutputKindResponses, true, "responses_sse.txt"},
		{OutputKindGemini, false, "gemini.json"},
		{OutputKindGemini, true, "gemini_sse.txt"},
	} {
		aggregated, ok := AggregateOutput(testCase.kind, testCase.isStream, fixture(t, testCase.file))
		require.True(t, ok)
		object, isObject := aggregated.(map[string]any)
		require.True(t, isObject)
		for _, forbidden := range []string{"usage", "usageMetadata", "usage_details", "total_tokens"} {
			assert.NotContains(t, object, forbidden, "settlement is the only usage source")
		}
	}
}

func TestAggregateOutputFallsBackToTheRawBody(t *testing.T) {
	blob := fixture(t, "unknown_blob.txt")

	aggregated, ok := AggregateOutput(OutputKindOpenAI, false, blob)
	assert.False(t, ok)
	assert.Equal(t, string(blob), aggregated)

	aggregated, ok = AggregateOutput("openai_image", false, []byte(`{"data":[]}`))
	assert.False(t, ok)
	assert.Equal(t, `{"data":[]}`, aggregated)

	aggregated, ok = AggregateOutput(OutputKindOpenAI, false, []byte("   "))
	assert.False(t, ok)
	assert.Nil(t, aggregated)
}

func TestAggregateOutputSkipsUnparsableStreamLines(t *testing.T) {
	partial := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"kept\"}}]}\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":\n" +
		"data: [DONE]\n")

	aggregated, ok := AggregateOutput(OutputKindOpenAI, true, partial)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"content": "kept"}, aggregated)

	broken := []byte("data: {\"choices\":\ndata: {\"broken\":\n")
	aggregated, ok = AggregateOutput(OutputKindOpenAI, true, broken)
	assert.False(t, ok)
	assert.Equal(t, string(broken), aggregated)
}

func TestAggregateResponsesStreamFallsBackToTheCompletedEvent(t *testing.T) {
	completed := []byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"final only"}]}]}}
`)

	aggregated, ok := AggregateOutput(OutputKindResponses, true, completed)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"content": "final only"}, aggregated)
}
