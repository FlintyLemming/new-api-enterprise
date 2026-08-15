package langfuse

import (
	"math"

	"github.com/QuantumNous/new-api/common"
)

// Canonical Langfuse usage bucket keys (design §8.4). input_cache_creation and
// output_audio_tokens are what the Langfuse alias normalizer itself produces,
// so the UI, queries and existing data already align on them. input_image_tokens
// and input_audio_tokens have no built-in equivalent: they are deliberate,
// permanent New API custom usage types and must never be renamed silently.
const (
	usageBucketInput           = "input"
	usageBucketOutput          = "output"
	usageBucketTotal           = "total"
	usageBucketInputCached     = "input_cached_tokens"
	usageBucketInputCacheWrite = "input_cache_creation"
	usageBucketInputImage      = "input_image_tokens"
	usageBucketInputAudio      = "input_audio_tokens"
	usageBucketOutputAudio     = "output_audio_tokens"
	usageBucketOutputReasoning = "output_reasoning_tokens"
)

// Closed usage_omitted_reason enum from design §8.1. Usage that still exports
// after a clamp carries no reason at all.
const (
	UsageOmittedUnavailable     = "usage_unavailable"
	UsageOmittedNoActiveAttempt = "no_active_attempt"
	UsageOmittedInvalidSource   = "invalid_source"
	UsageOmittedOverflow        = "arithmetic_overflow"
	UsageOmittedSemanticUnknown = "semantic_unknown"
)

// Closed cost_omitted_reason enum from design §8.1, in priority order.
const (
	CostOmittedAttemptFailed         = "attempt_failed"
	CostOmittedAttemptSuperseded     = "attempt_superseded"
	CostOmittedSettlementUnavailable = "settlement_unavailable"
	CostOmittedSettlementFailed      = "settlement_failed"
	CostOmittedNoBillableUsage       = "no_billable_usage"
	CostOmittedInvalidQuota          = "invalid_quota"
	CostOmittedInvalidQuotaPerUnit   = "invalid_quota_per_unit"
)

// Generation metadata markers for a usage document that survived a clamp.
const (
	UsageFlagInputClamped  = "usage_input_clamped"
	UsageFlagOutputClamped = "usage_output_clamped"
)

// costDetails is the §8.5 cost document. Total never carries omitempty:
// Langfuse treats an explicit 0 as a provided cost, and that is exactly what
// suppresses its own model-catalogue pricing for a free request.
type costDetails struct {
	Total float64 `json:"total"`
}

// normalizeUsageBuckets turns one settlement record into the mutually exclusive
// Langfuse usage buckets. The hard contract is total == sum(all other buckets),
// so a negative source value, an unmappable semantic or an arithmetic overflow
// omits the whole group with a closed reason instead of exporting a partial
// document. A record whose usage was never available exports nothing and states
// no reason of its own; the caller reports that as usage_unavailable.
func normalizeUsageBuckets(rec UsageRecord) (buckets map[string]int, omitReason string, flags []string) {
	if !rec.Available {
		return nil, "", nil
	}
	if negativeUsageSource(rec) {
		return nil, UsageOmittedInvalidSource, nil
	}
	if rec.UsageSemanticUnknown {
		return nil, UsageOmittedSemanticUnknown, nil
	}

	switch rec.Kind {
	case UsageKindText:
		input := rec.InputTokens
		if !rec.InputExcludesCache {
			// The folded input is still cache-inclusive, so both cache buckets
			// leave the base input here.
			input -= rec.InputCachedTokens
			input -= rec.InputCacheWriteTokens
		}
		input -= rec.InputImageTokens
		if rec.InputAudioTokens > 0 {
			// The audio boundary is a usage semantic, not a price lookup hit:
			// a positive audio prompt token count always splits out, even when
			// billing charged it at the base ratio.
			input -= rec.InputAudioTokens
		}
		output := rec.OutputTokens - rec.OutputReasoningTokens

		// OpenAI reports unadjusted cache-write prefixes, so cached +
		// cache_write can legitimately exceed the prompt. The overlap clamps
		// the base bucket and is reported; it never drops the whole group.
		if input < 0 {
			input = 0
			flags = append(flags, UsageFlagInputClamped)
		}
		if output < 0 {
			output = 0
			flags = append(flags, UsageFlagOutputClamped)
		}

		buckets = map[string]int{
			usageBucketInput:           input,
			usageBucketOutput:          output,
			usageBucketInputCached:     rec.InputCachedTokens,
			usageBucketInputCacheWrite: rec.InputCacheWriteTokens,
			usageBucketInputImage:      rec.InputImageTokens,
			usageBucketOutputReasoning: rec.OutputReasoningTokens,
		}
		if rec.InputAudioTokens > 0 {
			buckets[usageBucketInputAudio] = rec.InputAudioTokens
		}
		// OutputAudioTokens is ignored on this path: completion audio tokens
		// stay inside the upstream output total, and only the audio settlement
		// path splits them into their own bucket.
	case UsageKindAudio:
		buckets = map[string]int{
			usageBucketInput:       rec.InputTokens,
			usageBucketOutput:      rec.OutputTokens,
			usageBucketInputAudio:  rec.InputAudioTokens,
			usageBucketOutputAudio: rec.OutputAudioTokens,
		}
		// Cache, image and reasoning buckets are not invented here: the audio
		// settlement path never receives those sub-details.
	default:
		return nil, UsageOmittedSemanticUnknown, nil
	}

	total := 0
	for _, value := range buckets {
		if value > math.MaxInt-total {
			return nil, UsageOmittedOverflow, nil
		}
		total += value
	}
	buckets[usageBucketTotal] = total
	return buckets, "", flags
}

// negativeUsageSource reports whether any source token count is negative. A
// negative count can only come from a broken upstream payload, and normalizing
// it would produce buckets that no longer sum to a truthful total.
func negativeUsageSource(rec UsageRecord) bool {
	return rec.InputTokens < 0 || rec.OutputTokens < 0 ||
		rec.InputCachedTokens < 0 || rec.InputCacheWriteTokens < 0 ||
		rec.InputImageTokens < 0 || rec.InputAudioTokens < 0 ||
		rec.OutputAudioTokens < 0 || rec.OutputReasoningTokens < 0
}

// costDetailsJSON encodes the authoritative New API settlement cost in USD. Both
// the quota and the rate come from the same snapshot the settlement call site
// took, so a configuration switch between the request and the worker can never
// re-price an old quota.
func costDetailsJSON(rec UsageRecord) (string, bool) {
	if !rec.Settled || rec.Quota < 0 {
		return "", false
	}
	if rec.QuotaPerUnit <= 0 || math.IsNaN(rec.QuotaPerUnit) || math.IsInf(rec.QuotaPerUnit, 0) {
		return "", false
	}
	total := float64(rec.Quota) / rec.QuotaPerUnit
	if total < 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return "", false
	}
	encoded, err := common.Marshal(costDetails{Total: total})
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// usageExport is everything one attempt contributes to the usage and cost
// attributes: the buckets that may be exported, the authoritative cost document
// and the markers explaining whatever was left out.
type usageExport struct {
	Record     *UsageRecord
	Buckets    map[string]int
	Cost       string
	HasCost    bool
	OmitReason string
	Flags      []string
}

// buildUsageExport applies §8.4 and §8.5 to one attempt. A failed or superseded
// attempt never produces a cost: its settlement, if any, belongs to whichever
// attempt actually served the request.
func buildUsageExport(attempt *attemptValue) usageExport {
	export := usageExport{Record: attempt.Usage}
	if attempt.Usage == nil {
		return export
	}
	export.Buckets, export.OmitReason, export.Flags = normalizeUsageBuckets(*attempt.Usage)
	if len(export.Buckets) == 0 && export.OmitReason == "" {
		export.OmitReason = UsageOmittedUnavailable
	}
	if !attemptFailed(attempt) {
		export.Cost, export.HasCost = costDetailsJSON(*attempt.Usage)
	}
	return export
}

// settledSummary is the §8.1 trace level quota pair. It exists only when exactly
// one attempt really settled: zero candidates, several candidates or an illegal
// quota omit the pair whole rather than pick an attempt or add them up.
func settledSummary(attempts []attemptValue) *settlementSummary {
	var summary *settlementSummary
	for i := range attempts {
		attempt := &attempts[i]
		// Failed and superseded attempts are not candidates, whatever they were
		// handed.
		if attemptFailed(attempt) {
			continue
		}
		record := attempt.Usage
		if record == nil || !record.Settled || record.Quota < 0 {
			continue
		}
		if summary != nil {
			return nil
		}
		summary = &settlementSummary{Quota: record.Quota, BillingSource: record.BillingSource}
	}
	return summary
}

// costOmittedReason names why a generation carries no authoritative cost, in the
// documented §8.1 priority. A superseded attempt keeps its own reason so it
// never pretends the provider failed, and a settlement that had nothing to bill
// never looks like a settlement error.
func costOmittedReason(attempt *attemptValue) string {
	switch {
	case attempt.Superseded:
		return CostOmittedAttemptSuperseded
	case attemptFailed(attempt):
		return CostOmittedAttemptFailed
	case attempt.Usage == nil:
		return CostOmittedSettlementUnavailable
	}

	switch rec := attempt.Usage; {
	case rec.SettlementFailed:
		return CostOmittedSettlementFailed
	case !rec.Settled:
		return CostOmittedNoBillableUsage
	case rec.Quota < 0:
		return CostOmittedInvalidQuota
	default:
		return CostOmittedInvalidQuotaPerUnit
	}
}
