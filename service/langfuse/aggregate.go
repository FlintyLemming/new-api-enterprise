package langfuse

import (
	"bufio"
	"bytes"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Output kinds design §9.3 defines a normalized aggregation for. The Recorder
// maps its relay format onto one of these; every other format keeps its raw
// captured body.
const (
	OutputKindOpenAI    = "openai"
	OutputKindClaude    = "claude"
	OutputKindResponses = "responses"
	OutputKindGemini    = "gemini"
)

// sseLineLimit bounds one SSE line. Upstream events stay far below it, and a
// longer line simply ends the scan instead of growing the buffer without limit.
const sseLineLimit = 1 << 20

// AggregateOutput turns a captured response into the compact structure Langfuse
// displays. ok is false when the body cannot be recognized; the aggregated
// value is then the raw text, which the caller sanitizes or replaces with a
// truncation envelope (design §9.3). Usage is never read here: settlement stays
// the only source of token counts.
func AggregateOutput(kind string, isStream bool, body []byte) (aggregated any, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture("aggregate_panic")
			aggregated, ok = nil, false
		}
	}()

	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	if value, recognized := aggregateByKind(kind, isStream, body); recognized {
		return value, true
	}
	return string(body), false
}

func aggregateByKind(kind string, isStream bool, body []byte) (any, bool) {
	switch kind {
	case OutputKindOpenAI:
		if isStream {
			return aggregateOpenAISSE(body)
		}
		return aggregateOpenAI(body)
	case OutputKindClaude:
		if isStream {
			return aggregateClaudeSSE(body)
		}
		return aggregateClaude(body)
	case OutputKindResponses:
		if isStream {
			return aggregateResponsesSSE(body)
		}
		return aggregateResponses(body)
	case OutputKindGemini:
		if isStream {
			return aggregateGeminiSSE(body)
		}
		return aggregateGemini(body)
	}
	return nil, false
}

// aggregateOpenAI keeps the assistant message of the first choice: text,
// reasoning text and the tool calls, without the surrounding envelope.
func aggregateOpenAI(body []byte) (any, bool) {
	root, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	choices := sliceField(root, "choices")
	if len(choices) == 0 {
		return nil, false
	}
	message, ok := choices[0].(map[string]any)
	if !ok {
		return nil, false
	}
	if nested, isObject := message["message"].(map[string]any); isObject {
		message = nested
	}

	out := map[string]any{"content": stringField(message, "content")}
	if role := stringField(message, "role"); role != "" {
		out["role"] = role
	}
	if reasoning := openAIReasoning(message); reasoning != "" {
		out["reasoning_content"] = reasoning
	}
	if calls := normalizedToolCalls(sliceField(message, "tool_calls")); len(calls) > 0 {
		out["tool_calls"] = calls
	}
	return out, true
}

// aggregateOpenAISSE merges the chunk deltas of the first choice back into the
// non streaming message shape. Tool call arguments arrive in fragments and are
// concatenated per tool call index.
func aggregateOpenAISSE(body []byte) (any, bool) {
	var content, reasoning strings.Builder
	role := ""
	calls := newToolCallAccumulator()

	events := scanSSE(body, func(event map[string]any) {
		choices := sliceField(event, "choices")
		if len(choices) == 0 {
			return
		}
		choice, isObject := choices[0].(map[string]any)
		if !isObject {
			return
		}
		delta, isObject := choice["delta"].(map[string]any)
		if !isObject {
			return
		}
		if role == "" {
			role = stringField(delta, "role")
		}
		content.WriteString(stringField(delta, "content"))
		reasoning.WriteString(openAIReasoning(delta))
		calls.merge(sliceField(delta, "tool_calls"))
	})
	if events == 0 {
		return nil, false
	}

	out := map[string]any{"content": content.String()}
	if role != "" {
		out["role"] = role
	}
	if reasoning.Len() > 0 {
		out["reasoning_content"] = reasoning.String()
	}
	if merged := calls.result(); len(merged) > 0 {
		out["tool_calls"] = merged
	}
	return out, true
}

func openAIReasoning(message map[string]any) string {
	if reasoning := stringField(message, "reasoning_content"); reasoning != "" {
		return reasoning
	}
	return stringField(message, "reasoning")
}

// streamedToolCall is one tool call rebuilt from the chunk deltas that carry
// its index.
type streamedToolCall struct {
	id        string
	callType  string
	name      string
	arguments strings.Builder
}

// toolCallAccumulator merges streamed tool call fragments by their index.
type toolCallAccumulator struct {
	order   []float64
	byIndex map[float64]*streamedToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: map[float64]*streamedToolCall{}}
}

func (a *toolCallAccumulator) merge(fragments []any) {
	for position, fragment := range fragments {
		call, isObject := fragment.(map[string]any)
		if !isObject {
			continue
		}
		// Only chunk objects carry an index; a provider that omits it keeps the
		// position it arrived in.
		index, hasIndex := call["index"].(float64)
		if !hasIndex {
			index = float64(position)
		}
		merged, seen := a.byIndex[index]
		if !seen {
			merged = &streamedToolCall{}
			a.byIndex[index] = merged
			a.order = append(a.order, index)
		}
		if id := stringField(call, "id"); id != "" {
			merged.id = id
		}
		if callType := stringField(call, "type"); callType != "" {
			merged.callType = callType
		}
		function, isObject := call["function"].(map[string]any)
		if !isObject {
			continue
		}
		if name := stringField(function, "name"); name != "" {
			merged.name = name
		}
		merged.arguments.WriteString(stringField(function, "arguments"))
	}
}

func (a *toolCallAccumulator) result() []any {
	sort.Float64s(a.order)
	calls := make([]any, 0, len(a.order))
	for _, index := range a.order {
		merged := a.byIndex[index]
		calls = append(calls, toolCall(merged.id, merged.callType, merged.name, merged.arguments.String()))
	}
	return calls
}

// normalizedToolCalls keeps only the identity and the function payload of a non
// streaming tool call.
func normalizedToolCalls(raw []any) []any {
	calls := make([]any, 0, len(raw))
	for _, entry := range raw {
		call, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		function, _ := call["function"].(map[string]any)
		calls = append(calls, toolCall(stringField(call, "id"), stringField(call, "type"),
			stringField(function, "name"), stringField(function, "arguments")))
	}
	return calls
}

func toolCall(id, callType, name, arguments string) map[string]any {
	call := map[string]any{"function": map[string]any{"name": name, "arguments": arguments}}
	if id != "" {
		call["id"] = id
	}
	if callType != "" {
		call["type"] = callType
	}
	return call
}

// aggregateClaude folds the content blocks by type: text is concatenated,
// thinking becomes its own field and tool_use blocks are kept whole.
func aggregateClaude(body []byte) (any, bool) {
	root, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	blocks := sliceField(root, "content")
	if blocks == nil {
		return nil, false
	}

	var text, thinking strings.Builder
	toolUse := []any{}
	for _, entry := range blocks {
		block, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		switch stringField(block, "type") {
		case "text":
			text.WriteString(stringField(block, "text"))
		case "thinking":
			thinking.WriteString(stringField(block, "thinking"))
		case "tool_use":
			toolUse = append(toolUse, block)
		}
	}

	out := map[string]any{"content": text.String()}
	if role := stringField(root, "role"); role != "" {
		out["role"] = role
	}
	if thinking.Len() > 0 {
		out["thinking"] = thinking.String()
	}
	if len(toolUse) > 0 {
		out["tool_use"] = toolUse
	}
	if stopReason := stringField(root, "stop_reason"); stopReason != "" {
		out["stop_reason"] = stopReason
	}
	return out, true
}

// aggregateClaudeSSE rebuilds the same shape from the event stream: block
// deltas accumulate per index and message_delta carries the stop reason.
func aggregateClaudeSSE(body []byte) (any, bool) {
	var text, thinking strings.Builder
	role, stopReason := "", ""
	toolUse := map[float64]map[string]any{}
	toolArgs := map[float64]*strings.Builder{}
	var toolOrder []float64

	events := scanSSE(body, func(event map[string]any) {
		index, _ := event["index"].(float64)
		switch stringField(event, "type") {
		case "message_start":
			if message, isObject := event["message"].(map[string]any); isObject {
				role = stringField(message, "role")
			}
		case "content_block_start":
			block, isObject := event["content_block"].(map[string]any)
			if !isObject || stringField(block, "type") != "tool_use" {
				return
			}
			toolUse[index] = block
			toolArgs[index] = &strings.Builder{}
			toolOrder = append(toolOrder, index)
		case "content_block_delta":
			delta, isObject := event["delta"].(map[string]any)
			if !isObject {
				return
			}
			switch stringField(delta, "type") {
			case "text_delta":
				text.WriteString(stringField(delta, "text"))
			case "thinking_delta":
				thinking.WriteString(stringField(delta, "thinking"))
			case "input_json_delta":
				if builder, started := toolArgs[index]; started {
					builder.WriteString(stringField(delta, "partial_json"))
				}
			}
		case "message_delta":
			if delta, isObject := event["delta"].(map[string]any); isObject {
				if reason := stringField(delta, "stop_reason"); reason != "" {
					stopReason = reason
				}
			}
		}
	})
	if events == 0 {
		return nil, false
	}

	out := map[string]any{"content": text.String()}
	if role != "" {
		out["role"] = role
	}
	if thinking.Len() > 0 {
		out["thinking"] = thinking.String()
	}
	if len(toolOrder) > 0 {
		sort.Float64s(toolOrder)
		blocks := make([]any, 0, len(toolOrder))
		for _, index := range toolOrder {
			block := toolUse[index]
			if arguments := toolArgs[index].String(); arguments != "" {
				block["input"] = decodeInput(arguments)
			}
			blocks = append(blocks, block)
		}
		out["tool_use"] = blocks
	}
	if stopReason != "" {
		out["stop_reason"] = stopReason
	}
	return out, true
}

// decodeInput turns accumulated tool input JSON back into a value, keeping the
// raw fragment when the stream ended before it was complete.
func decodeInput(arguments string) any {
	var decoded any
	if err := common.UnmarshalJsonStr(arguments, &decoded); err != nil {
		return arguments
	}
	return decoded
}

// aggregateResponses concatenates the output text items and keeps the function
// calls of an OpenAI Responses payload.
func aggregateResponses(body []byte) (any, bool) {
	root, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	return responsesFromOutput(root)
}

func responsesFromOutput(root map[string]any) (any, bool) {
	items := sliceField(root, "output")
	if items == nil {
		return nil, false
	}

	var text strings.Builder
	functionCalls := []any{}
	for _, entry := range items {
		item, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		switch stringField(item, "type") {
		case "message":
			for _, part := range sliceField(item, "content") {
				if block, isObject := part.(map[string]any); isObject {
					text.WriteString(stringField(block, "text"))
				}
			}
		case "function_call":
			functionCalls = append(functionCalls, responsesFunctionCall(item, stringField(item, "arguments")))
		}
	}

	out := map[string]any{"content": text.String()}
	if len(functionCalls) > 0 {
		out["function_calls"] = functionCalls
	}
	return out, true
}

func responsesFunctionCall(item map[string]any, arguments string) map[string]any {
	return map[string]any{
		"name":      stringField(item, "name"),
		"arguments": arguments,
		"call_id":   stringField(item, "call_id"),
	}
}

// aggregateResponsesSSE follows the Responses event stream: output text deltas
// are concatenated, added function call items collect their argument deltas,
// and a completed event is the fallback when no delta was observed.
func aggregateResponsesSSE(body []byte) (any, bool) {
	var text strings.Builder
	items := map[string]map[string]any{}
	arguments := map[string]*strings.Builder{}
	var order []string
	var completed map[string]any

	events := scanSSE(body, func(event map[string]any) {
		switch stringField(event, "type") {
		case "response.output_text.delta":
			text.WriteString(stringField(event, "delta"))
		case "response.output_item.added":
			item, isObject := event["item"].(map[string]any)
			if !isObject || stringField(item, "type") != "function_call" {
				return
			}
			id := stringField(item, "id")
			items[id] = item
			arguments[id] = &strings.Builder{}
			arguments[id].WriteString(stringField(item, "arguments"))
			order = append(order, id)
		case "response.function_call_arguments.delta":
			if builder, started := arguments[stringField(event, "item_id")]; started {
				builder.WriteString(stringField(event, "delta"))
			}
		case "response.completed":
			if response, isObject := event["response"].(map[string]any); isObject {
				completed = response
			}
		}
	})
	if events == 0 {
		return nil, false
	}

	if text.Len() == 0 && len(order) == 0 {
		if completed == nil {
			return nil, false
		}
		return responsesFromOutput(completed)
	}

	out := map[string]any{"content": text.String()}
	if len(order) > 0 {
		calls := make([]any, 0, len(order))
		for _, id := range order {
			calls = append(calls, responsesFunctionCall(items[id], arguments[id].String()))
		}
		out["function_calls"] = calls
	}
	return out, true
}

// aggregateGemini keeps the text parts and function calls of the first
// candidate together with its finish reason.
func aggregateGemini(body []byte) (any, bool) {
	root, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	var text strings.Builder
	functionCalls := []any{}
	finishReason := ""
	if !collectGeminiCandidate(root, &text, &functionCalls, &finishReason) {
		return nil, false
	}
	return geminiOutput(&text, functionCalls, finishReason), true
}

// aggregateGeminiSSE concatenates the parts of every streamed chunk, which all
// carry the same candidate shape.
func aggregateGeminiSSE(body []byte) (any, bool) {
	var text strings.Builder
	functionCalls := []any{}
	finishReason := ""
	recognized := false

	events := scanSSE(body, func(event map[string]any) {
		if collectGeminiCandidate(event, &text, &functionCalls, &finishReason) {
			recognized = true
		}
	})
	if events == 0 || !recognized {
		return nil, false
	}
	return geminiOutput(&text, functionCalls, finishReason), true
}

func collectGeminiCandidate(root map[string]any, text *strings.Builder, functionCalls *[]any, finishReason *string) bool {
	candidates := sliceField(root, "candidates")
	if len(candidates) == 0 {
		return false
	}
	candidate, isObject := candidates[0].(map[string]any)
	if !isObject {
		return false
	}
	if reason := stringField(candidate, "finishReason"); reason != "" {
		*finishReason = reason
	}
	content, isObject := candidate["content"].(map[string]any)
	if !isObject {
		return true
	}
	for _, entry := range sliceField(content, "parts") {
		part, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		text.WriteString(stringField(part, "text"))
		if call, isObject := part["functionCall"].(map[string]any); isObject {
			*functionCalls = append(*functionCalls, call)
		}
	}
	return true
}

func geminiOutput(text *strings.Builder, functionCalls []any, finishReason string) map[string]any {
	out := map[string]any{"content": text.String()}
	if len(functionCalls) > 0 {
		out["function_calls"] = functionCalls
	}
	if finishReason != "" {
		out["finishReason"] = finishReason
	}
	return out
}

// scanSSE hands every parsable data payload to visit and reports how many were
// parsed. Unparsable lines are skipped: a stream that was cut off mid event
// still yields everything before the break.
func scanSSE(body []byte, visit func(map[string]any)) int {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 4096), sseLineLimit)

	parsed := 0
	for scanner.Scan() {
		payload, isData := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "data:")
		if !isData {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := common.UnmarshalJsonStr(payload, &event); err != nil {
			continue
		}
		parsed++
		visit(event)
	}
	return parsed
}

func decodeObject(body []byte) (map[string]any, bool) {
	var root map[string]any
	if err := common.Unmarshal(body, &root); err != nil {
		return nil, false
	}
	return root, true
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func sliceField(object map[string]any, key string) []any {
	value, _ := object[key].([]any)
	return value
}
