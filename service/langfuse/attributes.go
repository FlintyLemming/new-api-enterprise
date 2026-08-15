package langfuse

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"go.opentelemetry.io/otel/attribute"
)

// The exported attribute names are a contract with the fixed Langfuse ingestion
// revision, not an implementation detail (design §6 and §8).
const (
	attrObservationType     = "langfuse.observation.type"
	attrObservationInput    = "langfuse.observation.input"
	attrObservationOutput   = "langfuse.observation.output"
	attrObservationMetadata = "langfuse.observation.metadata"
	attrModelParameters     = "langfuse.observation.model.parameters"
	attrModelName           = "langfuse.observation.model.name"
	attrUsageDetails        = "langfuse.observation.usage_details"
	attrCostDetails         = "langfuse.observation.cost_details"
	attrCompletionStartTime = "langfuse.observation.completion_start_time"
	attrInternalAsRoot      = "langfuse.internal.as_root"
	attrEnvironment         = "langfuse.environment"
	attrRelease             = "langfuse.release"
	attrUserID              = "user.id"
	attrSessionID           = "session.id"

	observationTypeSpan       = "span"
	observationTypeGeneration = "generation"
)

// maxMetadataBytes bounds one metadata document. The non-content attributes of
// a span share a 64 KiB ceiling, and metadata is the only one whose size a
// client can influence through names and error messages.
const maxMetadataBytes = 32 * 1024

// diagnosticMetadataKeys survive a metadata reduction: without them a truncated
// trace could no longer be correlated or explained.
var diagnosticMetadataKeys = []string{
	"request_id", "capture_state", "relay_format", "origin_model", "upstream_model",
	"attempt_count", "retry_count", "use_time_ms", "attempt_index", "channel_id",
	"channel_type", "attempt_end_reason", "input_omitted_reason",
	"session_omitted_reason", "session_body_omitted_reason",
}

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

// buildRootAttributes assembles the §6.1 root observation. The root is the only
// observation carrying the trace identity; it deliberately writes no
// langfuse.trace.* attribute, because the ingestion revision derives the trace
// name from the span name and falls back to the observation input/output.
func buildRootAttributes(material *recorderMaterial, content *materializedContent) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String(attrObservationType, observationTypeSpan),
		attribute.String(attrInternalAsRoot, "true"),
		attribute.String(attrEnvironment, material.Snapshot.Environment),
		attribute.String(attrRelease, common.Version),
	}
	if material.UserId > 0 {
		attributes = append(attributes, attribute.String(attrUserID, strconv.Itoa(material.UserId)))
	}
	if material.Session.ScopedID != "" {
		attributes = append(attributes, attribute.String(attrSessionID, material.Session.ScopedID))
	}
	if content != nil {
		if len(content.Input) > 0 {
			attributes = append(attributes, attribute.String(attrObservationInput, string(content.Input)))
		}
		if len(content.RootOutput) > 0 {
			attributes = append(attributes, attribute.String(attrObservationOutput, string(content.RootOutput)))
		}
	}
	if metadata := encodeMetadata(buildRootMetadata(material, content)); metadata != "" {
		attributes = append(attributes, attribute.String(attrObservationMetadata, metadata))
	}
	return attributes
}

// buildGenerationAttributes assembles the §6.2 generation. The §13 denylist is
// enforced by construction: no trace domain attribute, no user.id and no
// session.id is ever written here, so an attempt cannot produce a trace update.
func buildGenerationAttributes(attempt *attemptValue, material *recorderMaterial, content *materializedContent) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String(attrObservationType, observationTypeGeneration),
	}
	if content != nil {
		if len(content.Input) > 0 {
			// Every generation shares the one sanitized client request; the
			// input is never re-derived per attempt.
			attributes = append(attributes, attribute.String(attrObservationInput, string(content.Input)))
		}
		if output := content.AttemptOutputs[attempt.Index]; len(output) > 0 {
			attributes = append(attributes, attribute.String(attrObservationOutput, string(output)))
		}
	}
	if params := buildModelParams(material); params != "" {
		attributes = append(attributes, attribute.String(attrModelParameters, params))
	}
	usage := buildUsageExport(attempt)
	exportedUsage := false
	if len(usage.Buckets) > 0 {
		if encoded, err := common.Marshal(usage.Buckets); err == nil {
			attributes = append(attributes, attribute.String(attrUsageDetails, string(encoded)))
			exportedUsage = true
		}
	}
	if usage.HasCost {
		attributes = append(attributes, attribute.String(attrCostDetails, usage.Cost))
	}
	// Design §6.2: naming the model without an authoritative cost lets Langfuse
	// price the generation from its model catalogue, so the name is only
	// allowed when a cost exists, or when there is no usage and the span
	// already failed.
	if allowModelName(usage.HasCost, exportedUsage, attemptFailed(attempt)) && attempt.UpstreamModel != "" {
		attributes = append(attributes, attribute.String(attrModelName, attempt.UpstreamModel))
	}
	if !attempt.FirstResponseTime.IsZero() {
		attributes = append(attributes, attribute.String(attrCompletionStartTime,
			attempt.FirstResponseTime.UTC().Format(time.RFC3339Nano)))
	}
	if metadata := encodeMetadata(buildGenerationMetadata(attempt, material, usage)); metadata != "" {
		attributes = append(attributes, attribute.String(attrObservationMetadata, metadata))
	}
	return attributes
}

// allowModelName is the hard §6.2 contract that keeps Langfuse from inferring a
// non-authoritative cost from the model catalogue.
func allowModelName(hasCost, hasUsage, isError bool) bool {
	if hasCost {
		return true
	}
	return !hasUsage && isError
}

// buildRootMetadata is the §8.1 trace level summary. Only the root carries it;
// the trace domain copy is redundant because ingestion merges the root
// observation metadata into the trace.
func buildRootMetadata(material *recorderMaterial, content *materializedContent) map[string]any {
	metadata := map[string]any{
		"request_id":     material.RequestId,
		"relay_format":   material.RelayFormat,
		"origin_model":   material.OriginModel,
		"request_path":   material.RequestPath,
		"is_stream":      material.IsStream,
		"attempt_count":  len(material.Attempts),
		"retry_count":    max(0, len(material.Attempts)-1),
		"use_time_ms":    material.RootEnd.Sub(material.RootStart).Milliseconds(),
		"capture_state":  material.CaptureState,
		"selected_group": material.SelectedGroup,
	}
	putNonEmpty(metadata, "username", material.Username)
	putNonEmpty(metadata, "user_group", material.UserGroup)
	putNonEmpty(metadata, "token_name", material.TokenName)
	if material.TokenId > 0 {
		metadata["token_id"] = material.TokenId
	}
	if material.IsPlayground {
		metadata["is_playground"] = true
	}
	if material.LifecyclePanic {
		metadata["lifecycle_panic"] = true
	}
	if first := firstResponseMillis(material); first >= 0 {
		metadata["first_response_ms"] = first
	}
	if material.Settlement != nil {
		// The pair is copied from the single settled attempt or omitted whole.
		metadata["quota"] = material.Settlement.Quota
		metadata["billing_source"] = material.Settlement.BillingSource
	}
	if material.UsageUnattributed {
		// A settlement without an attempt to own it is reported here; the usage
		// itself is dropped rather than attributed to the root.
		metadata["usage_unattributed"] = true
		metadata["usage_omitted_reason"] = UsageOmittedNoActiveAttempt
	}

	metadata["content_truncated"] = material.Capture.Truncated
	metadata["content_redacted"] = content != nil && content.Redacted
	if material.InputScanTruncated {
		metadata["input_scan_truncated"] = true
	}
	putNonEmpty(metadata, "input_omitted_reason", material.InputOmittedReason)

	if material.Session.ScopedID != "" {
		metadata["session_scope"] = "user"
	}
	// The source and the omission reasons are diagnostics; the raw client value
	// is never copied into metadata.
	putNonEmpty(metadata, "session_source", material.Session.Source)
	putNonEmpty(metadata, "session_omitted_reason", material.Session.OmittedReason)
	putNonEmpty(metadata, "session_body_omitted_reason", material.Session.BodyOmittedReason)
	return metadata
}

// buildGenerationMetadata is the §8.2 attempt level summary.
func buildGenerationMetadata(attempt *attemptValue, material *recorderMaterial, usage usageExport) map[string]any {
	metadata := map[string]any{
		"attempt_index": attempt.Index,
		"channel_id":    attempt.ChannelID,
		"channel_type":  attempt.ChannelType,
		"relay_format":  material.RelayFormat,
		"is_stream":     material.IsStream,
		// The exported input is the client's own request, not the converted
		// upstream payload, so readers cannot mistake one for the other.
		"input_source":       "client_request",
		"partial_output":     attempt.PartialOutput,
		"output_truncated":   attempt.OutputTruncated,
		"attempt_end_reason": attempt.EndReason,
	}
	// The channel name is only written when the request context really carried
	// one; the span name falls back on its own.
	putNonEmpty(metadata, "channel_name", attempt.ChannelName)
	putNonEmpty(metadata, "selected_group", attempt.SelectedGroup)
	putNonEmpty(metadata, "upstream_relay_format", attempt.UpstreamRelayFormat)
	putNonEmpty(metadata, "upstream_request_id", attempt.UpstreamRequestID)
	// The real model stays in metadata even when the model identity attributes
	// must be omitted, so a reader can still tell what ran.
	putNonEmpty(metadata, "upstream_model", attempt.UpstreamModel)
	putNonEmpty(metadata, "origin_model", attempt.OriginModel)

	if usage.HasCost {
		metadata["cost_source"] = "new_api_settlement"
	} else {
		metadata["cost_source"] = "unavailable"
		reason := costOmittedReason(attempt)
		metadata["cost_omitted_reason"] = reason
		switch reason {
		case CostOmittedSettlementFailed:
			metadata["settlement_error"] = true
		case CostOmittedNoBillableUsage:
			// A billing session that closed with nothing to charge is a normal
			// omission, not a settlement error.
			metadata["settlement_error"] = false
		}
	}
	if usage.Record != nil {
		putNonEmpty(metadata, "billing_source", usage.Record.BillingSource)
	}

	putNonEmpty(metadata, "usage_omitted_reason", usage.OmitReason)
	switch usage.OmitReason {
	case UsageOmittedInvalidSource, UsageOmittedOverflow:
		metadata["usage_invalid"] = true
	case UsageOmittedSemanticUnknown:
		metadata["usage_semantic_unknown"] = true
	}
	for _, flag := range usage.Flags {
		metadata[flag] = true
	}
	return metadata
}

// buildModelParams serializes the §8.3 parameter set. Langfuse parses this
// attribute as JSON, so an empty set is omitted rather than sent as "null".
func buildModelParams(material *recorderMaterial) string {
	if len(material.ModelParams) == 0 {
		return ""
	}
	encoded, err := common.Marshal(material.ModelParams)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// encodeMetadata serializes one metadata document, reducing it to the
// diagnostic keys when it would blow the per-span attribute budget.
func encodeMetadata(metadata map[string]any) string {
	encoded, err := common.Marshal(metadata)
	if err != nil {
		return ""
	}
	if len(encoded) <= maxMetadataBytes {
		return string(encoded)
	}

	reduced := map[string]any{"metadata_truncated": true}
	for _, key := range diagnosticMetadataKeys {
		if value, ok := metadata[key]; ok {
			reduced[key] = value
		}
	}
	if encoded, err = common.Marshal(reduced); err != nil {
		return ""
	}
	return string(encoded)
}

// firstResponseMillis reports the request level time to first byte, or -1 when
// no attempt ever owned a first response time.
func firstResponseMillis(material *recorderMaterial) int64 {
	for i := range material.Attempts {
		first := material.Attempts[i].FirstResponseTime
		if first.IsZero() {
			continue
		}
		if elapsed := first.Sub(material.RootStart).Milliseconds(); elapsed >= 0 {
			return elapsed
		}
	}
	return -1
}

// contentRedacted reports whether any exported payload had an inline binary
// replaced, which is what the root metadata flag documents.
func contentRedacted(content *materializedContent) bool {
	marker := []byte(markerRedacted)
	if bytes.Contains(content.Input, marker) || bytes.Contains(content.RootOutput, marker) {
		return true
	}
	for _, output := range content.AttemptOutputs {
		if bytes.Contains(output, marker) {
			return true
		}
	}
	return false
}

func putNonEmpty(metadata map[string]any, key, value string) {
	if value == "" {
		return
	}
	metadata[key] = value
}
