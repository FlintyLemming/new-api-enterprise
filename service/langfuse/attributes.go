package langfuse

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// extractModelParams reads the §8.3 parameter set off the validated request
// DTO. Only fields the client actually provided are recorded, and pointer
// fields keep an explicit 0 or false instead of dropping it; nothing is copied
// from unknown passthrough members or from channel parameter overrides.
func extractModelParams(request dto.Request) map[string]any {
	params := map[string]any{}
	switch typed := request.(type) {
	case *dto.GeneralOpenAIRequest:
		putPtr(params, "temperature", typed.Temperature)
		putPtr(params, "top_p", typed.TopP)
		putPtr(params, "max_tokens", typed.MaxTokens)
		putPtr(params, "max_completion_tokens", typed.MaxCompletionTokens)
		putPtr(params, "stream", typed.Stream)
		putToolCount(params, len(typed.Tools), typed.Tools != nil)
		if typed.ReasoningEffort != "" {
			params["reasoning_effort"] = typed.ReasoningEffort
		}
	case *dto.ClaudeRequest:
		putPtr(params, "temperature", typed.Temperature)
		putPtr(params, "top_p", typed.TopP)
		putPtr(params, "max_tokens", typed.MaxTokens)
		putPtr(params, "stream", typed.Stream)
		if tools, ok := typed.Tools.([]any); ok {
			putToolCount(params, len(tools), true)
		}
		if typed.Thinking != nil {
			thinking := map[string]any{}
			if typed.Thinking.Type != "" {
				thinking["type"] = typed.Thinking.Type
			}
			putPtr(thinking, "budget_tokens", typed.Thinking.BudgetTokens)
			if len(thinking) > 0 {
				params["thinking"] = thinking
			}
		}
	case *dto.OpenAIResponsesRequest:
		putPtr(params, "temperature", typed.Temperature)
		putPtr(params, "top_p", typed.TopP)
		putPtr(params, "max_output_tokens", typed.MaxOutputTokens)
		putPtr(params, "stream", typed.Stream)
		putRawToolCount(params, typed.Tools)
		if typed.Reasoning != nil {
			reasoning := map[string]any{}
			if typed.Reasoning.Effort != "" {
				reasoning["effort"] = typed.Reasoning.Effort
			}
			if typed.Reasoning.Summary != "" {
				reasoning["summary"] = typed.Reasoning.Summary
			}
			if len(reasoning) > 0 {
				params["reasoning"] = reasoning
			}
		}
	case *dto.GeminiChatRequest:
		config := typed.GenerationConfig
		putPtr(params, "temperature", config.Temperature)
		putPtr(params, "top_p", config.TopP)
		putPtr(params, "max_output_tokens", config.MaxOutputTokens)
		putRawToolCount(params, typed.Tools)
		if config.ThinkingConfig != nil {
			thinking := map[string]any{"include_thoughts": config.ThinkingConfig.IncludeThoughts}
			putPtr(thinking, "thinking_budget", config.ThinkingConfig.ThinkingBudget)
			if config.ThinkingConfig.ThinkingLevel != "" {
				thinking["thinking_level"] = config.ThinkingConfig.ThinkingLevel
			}
			params["thinking"] = thinking
		}
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

// putPtr records a pointer parameter under its own type, so an explicit 0,
// 0.0 or false survives while an absent field stays absent.
func putPtr[T any](params map[string]any, key string, value *T) {
	if value == nil {
		return
	}
	params[key] = *value
}

func putToolCount(params map[string]any, count int, provided bool) {
	if !provided {
		return
	}
	params["tool_count"] = count
}

// putRawToolCount counts the elements of a tools member the DTO keeps as raw
// JSON. A non-array value carries no comparable count and is skipped rather
// than guessed at.
func putRawToolCount(params map[string]any, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var tools []json.RawMessage
	if err := common.Unmarshal(raw, &tools); err != nil {
		return
	}
	params["tool_count"] = len(tools)
}
