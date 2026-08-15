package langfuse

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUsageBucketsFollowTheMutuallyExclusiveTable is the design §14.1 usage
// table, row by row. The buckets are mutually exclusive: input never contains
// cache read, cache creation, image or audio prompt tokens, output never
// contains reasoning tokens, and total is the exact sum of everything exported.
func TestUsageBucketsFollowTheMutuallyExclusiveTable(t *testing.T) {
	cases := []struct {
		name    string
		record  UsageRecord
		buckets map[string]int
		flags   []string
	}{
		{
			name: "openai undeclared inclusive",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 1000, InputCachedTokens: 800,
				OutputTokens: 100, OutputReasoningTokens: 50,
			},
			buckets: map[string]int{
				"input": 200, "output": 50, "total": 1100,
				"input_cached_tokens": 800, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 50,
			},
		},
		{
			name: "openai declared prompt_includes_cache after fold",
			record: UsageRecord{
				Kind: UsageKindText, Available: true, InputExcludesCache: true,
				InputTokens: 200, InputCachedTokens: 800, OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 200, "output": 100, "total": 1100,
				"input_cached_tokens": 800, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "openai declared prompt_excludes_cache without fold",
			record: UsageRecord{
				Kind: UsageKindText, Available: true, InputExcludesCache: true,
				InputTokens: 200, InputCachedTokens: 800, OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 200, "output": 100, "total": 1100,
				"input_cached_tokens": 800, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "legacy claude derived openai",
			record: UsageRecord{
				Kind: UsageKindText, Available: true, InputExcludesCache: true,
				InputTokens: 1000, InputCachedTokens: 800, InputCacheWriteTokens: 100,
				OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 1000, "output": 100, "total": 2000,
				"input_cached_tokens": 800, "input_cache_creation": 100,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "anthropic",
			record: UsageRecord{
				Kind: UsageKindText, Available: true, InputExcludesCache: true,
				InputTokens: 200, InputCachedTokens: 800, InputCacheWriteTokens: 100,
				OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 200, "output": 100, "total": 1200,
				"input_cached_tokens": 800, "input_cache_creation": 100,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "openrouter claude after fold",
			record: UsageRecord{
				Kind: UsageKindText, Available: true, InputExcludesCache: true,
				InputTokens: 200, InputCachedTokens: 800, InputCacheWriteTokens: 100,
				OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 200, "output": 100, "total": 1200,
				"input_cached_tokens": 800, "input_cache_creation": 100,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "gemini inclusive",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 1000, InputCachedTokens: 800,
				OutputTokens: 100, OutputReasoningTokens: 50,
			},
			buckets: map[string]int{
				"input": 200, "output": 50, "total": 1100,
				"input_cached_tokens": 800, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 50,
			},
		},
		{
			name: "gemini image input",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 1000, InputImageTokens: 300, OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 700, "output": 100, "total": 1100,
				"input_cached_tokens": 0, "input_cache_creation": 0,
				"input_image_tokens": 300, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "gemini audio input",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 1000, InputAudioTokens: 300, OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 700, "output": 100, "total": 1100,
				"input_cached_tokens": 0, "input_cache_creation": 0,
				"input_image_tokens": 0, "input_audio_tokens": 300,
				"output_reasoning_tokens": 0,
			},
		},
		{
			name: "openai cache write overlap",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 1000, InputCachedTokens: 800, InputCacheWriteTokens: 400,
				OutputTokens: 100,
			},
			buckets: map[string]int{
				"input": 0, "output": 100, "total": 1300,
				"input_cached_tokens": 800, "input_cache_creation": 400,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
			flags: []string{UsageFlagInputClamped},
		},
		{
			name: "text path with completion audio token",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 100, OutputTokens: 75, OutputAudioTokens: 25,
			},
			buckets: map[string]int{
				"input": 100, "output": 75, "total": 175,
				"input_cached_tokens": 0, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name: "chat responses audio settlement",
			record: UsageRecord{
				Kind: UsageKindAudio, Available: true,
				InputTokens: 200, InputAudioTokens: 300,
				OutputTokens: 50, OutputAudioTokens: 25,
			},
			buckets: map[string]int{
				"input": 200, "input_audio_tokens": 300,
				"output": 50, "output_audio_tokens": 25, "total": 575,
			},
		},
		{
			name:   "zero text usage keeps the core buckets",
			record: UsageRecord{Kind: UsageKindText, Available: true},
			buckets: map[string]int{
				"input": 0, "output": 0, "total": 0,
				"input_cached_tokens": 0, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 0,
			},
		},
		{
			name:   "zero audio usage keeps the core buckets",
			record: UsageRecord{Kind: UsageKindAudio, Available: true},
			buckets: map[string]int{
				"input": 0, "output": 0, "total": 0,
				"input_audio_tokens": 0, "output_audio_tokens": 0,
			},
		},
		{
			name: "output reasoning larger than output clamps",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 100, OutputTokens: 10, OutputReasoningTokens: 40,
			},
			buckets: map[string]int{
				"input": 100, "output": 0, "total": 140,
				"input_cached_tokens": 0, "input_cache_creation": 0,
				"input_image_tokens": 0, "output_reasoning_tokens": 40,
			},
			flags: []string{UsageFlagOutputClamped},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			buckets, omitReason, flags := normalizeUsageBuckets(testCase.record)

			assert.Empty(t, omitReason)
			assert.Equal(t, testCase.buckets, buckets)
			assert.Equal(t, testCase.flags, flags)

			total := 0
			for key, value := range buckets {
				assert.GreaterOrEqual(t, value, 0, "%s must be a non-negative integer", key)
				if key != "total" {
					total += value
				}
			}
			assert.Equal(t, buckets["total"], total, "total must equal the sum of every other bucket")
		})
	}
}

// TestUsageOmitsTheWholeGroupOnInvalidSources covers the closed omission set:
// a partial usage document would silently under-report the request, so a bad
// source omits everything and names the reason.
func TestUsageOmitsTheWholeGroupOnInvalidSources(t *testing.T) {
	cases := []struct {
		name   string
		record UsageRecord
		reason string
	}{
		{
			name:   "unavailable usage exports nothing and no reason of its own",
			record: UsageRecord{Kind: UsageKindText, InputTokens: 10},
			reason: "",
		},
		{
			name: "negative cached tokens",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 100, InputCachedTokens: -5,
			},
			reason: UsageOmittedInvalidSource,
		},
		{
			name: "negative completion tokens",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: 100, OutputTokens: -1,
			},
			reason: UsageOmittedInvalidSource,
		},
		{
			name: "negative audio output tokens",
			record: UsageRecord{
				Kind: UsageKindAudio, Available: true,
				InputTokens: 10, OutputAudioTokens: -2,
			},
			reason: UsageOmittedInvalidSource,
		},
		{
			name: "total overflows",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				InputTokens: math.MaxInt, OutputTokens: 1,
			},
			reason: UsageOmittedOverflow,
		},
		{
			name: "unknown semantic",
			record: UsageRecord{
				Kind: UsageKindText, Available: true,
				UsageSemanticUnknown: true, InputTokens: 100,
			},
			reason: UsageOmittedSemanticUnknown,
		},
		{
			name:   "unmappable kind",
			record: UsageRecord{Kind: UsageKind("video"), Available: true, InputTokens: 100},
			reason: UsageOmittedSemanticUnknown,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			buckets, omitReason, flags := normalizeUsageBuckets(testCase.record)

			assert.Nil(t, buckets, "no partial usage may be exported")
			assert.Nil(t, flags, "an omitted group carries no clamp marker")
			assert.Equal(t, testCase.reason, omitReason)
		})
	}
}

// TestCostDetailsJSONEncodesTheAuthoritativeQuota pins the §8.5 wire format.
// Langfuse reads cost as a JSON number and treats an explicit 0 as a provided
// cost, which is what suppresses its own model-catalogue pricing.
func TestCostDetailsJSONEncodesTheAuthoritativeQuota(t *testing.T) {
	cases := []struct {
		name     string
		record   UsageRecord
		expected string
	}{
		{
			name:     "smallest positive quota at the default rate",
			record:   UsageRecord{Settled: true, Quota: 1, QuotaPerUnit: 500000},
			expected: `{"total":0.000002}`,
		},
		{
			name:     "free request with billable usage",
			record:   UsageRecord{Settled: true, Quota: 0, QuotaPerUnit: 500000},
			expected: `{"total":0}`,
		},
		{
			name:     "subscription quota uses the same conversion",
			record:   UsageRecord{Settled: true, Quota: 615, QuotaPerUnit: 500000},
			expected: `{"total":0.00123}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, ok := costDetailsJSON(testCase.record)

			require.True(t, ok)
			assert.Equal(t, testCase.expected, encoded)

			var decoded struct {
				Total float64 `json:"total"`
			}
			require.NoError(t, common.UnmarshalJsonStr(encoded, &decoded))
			assert.Equal(t, float64(testCase.record.Quota)/testCase.record.QuotaPerUnit, decoded.Total)
		})
	}
}

// TestCostDetailsJSONKeepsScientificNotationANumber protects a runtime changed
// rate: an exponent form is still a JSON number, and cost must never be
// serialized as a string.
func TestCostDetailsJSONKeepsScientificNotationANumber(t *testing.T) {
	encoded, ok := costDetailsJSON(UsageRecord{Settled: true, Quota: 1, QuotaPerUnit: 1e9})

	require.True(t, ok)
	assert.NotContains(t, encoded, `"1e-09"`, "cost must not be encoded as a string")

	var decoded map[string]any
	require.NoError(t, common.UnmarshalJsonStr(encoded, &decoded))
	total, isNumber := decoded["total"].(float64)
	require.True(t, isNumber, "total must decode as a JSON number")
	assert.Equal(t, 1e-9, total)
}

// TestCostDetailsJSONRefusesNonAuthoritativeInput keeps Langfuse from showing a
// cost New API never settled, and never emits a negative one.
func TestCostDetailsJSONRefusesNonAuthoritativeInput(t *testing.T) {
	cases := []struct {
		name   string
		record UsageRecord
	}{
		{name: "not settled", record: UsageRecord{Quota: 100, QuotaPerUnit: 500000}},
		{name: "negative quota", record: UsageRecord{Settled: true, Quota: -1, QuotaPerUnit: 500000}},
		{name: "zero rate", record: UsageRecord{Settled: true, Quota: 100, QuotaPerUnit: 0}},
		{name: "negative rate", record: UsageRecord{Settled: true, Quota: 100, QuotaPerUnit: -500000}},
		{name: "nan rate", record: UsageRecord{Settled: true, Quota: 100, QuotaPerUnit: math.NaN()}},
		{name: "infinite rate", record: UsageRecord{Settled: true, Quota: 100, QuotaPerUnit: math.Inf(1)}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, ok := costDetailsJSON(testCase.record)

			assert.False(t, ok)
			assert.Empty(t, encoded)
		})
	}
}

// TestCostOmittedReasonFollowsTheDocumentedPriority pins the closed §8.1 set. A
// superseded attempt must never claim the provider failed, and a settlement
// that simply had nothing to bill must never look like a settlement error.
func TestCostOmittedReasonFollowsTheDocumentedPriority(t *testing.T) {
	cases := []struct {
		name    string
		attempt attemptValue
		reason  string
	}{
		{
			name:    "provider failure",
			attempt: attemptValue{Failed: true, ErrCode: "upstream_error"},
			reason:  CostOmittedAttemptFailed,
		},
		{
			// A provider that reports an empty error code is still a failure:
			// the flag, not the code, decides.
			name:    "provider failure without an error code",
			attempt: attemptValue{Failed: true},
			reason:  CostOmittedAttemptFailed,
		},
		{
			name:    "superseded outranks the failure it implies",
			attempt: attemptValue{Superseded: true, Failed: true, ErrCode: "upstream_error"},
			reason:  CostOmittedAttemptSuperseded,
		},
		{
			name:    "successful attempt never settled",
			attempt: attemptValue{},
			reason:  CostOmittedSettlementUnavailable,
		},
		{
			name:    "settlement failed",
			attempt: attemptValue{Usage: &UsageRecord{Available: true, SettlementFailed: true}},
			reason:  CostOmittedSettlementFailed,
		},
		{
			name:    "no billable usage",
			attempt: attemptValue{Usage: &UsageRecord{Available: true}},
			reason:  CostOmittedNoBillableUsage,
		},
		{
			name:    "invalid quota",
			attempt: attemptValue{Usage: &UsageRecord{Available: true, Settled: true, Quota: -1, QuotaPerUnit: 500000}},
			reason:  CostOmittedInvalidQuota,
		},
		{
			name:    "invalid rate",
			attempt: attemptValue{Usage: &UsageRecord{Available: true, Settled: true, Quota: 10}},
			reason:  CostOmittedInvalidQuotaPerUnit,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.reason, costOmittedReason(&testCase.attempt))
		})
	}
}
