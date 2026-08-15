package langfuse

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

// settledTextUsage is the shape a successful PostTextConsumeQuota produces.
func settledTextUsage(quota int) UsageRecord {
	return UsageRecord{
		Kind:          UsageKindText,
		Available:     true,
		ModelName:     "gpt-4o-1",
		InputTokens:   1000,
		OutputTokens:  100,
		Quota:         quota,
		QuotaPerUnit:  500000,
		BillingSource: "wallet",
		Settled:       true,
	}
}

// runSettledAttempt drives one upstream call whose settlement recorded usage
// between the attempt hooks, which is exactly where PostTextConsumeQuota and
// PostAudioConsumeQuota run.
func (f *finishFixture) runSettledAttempt(t *testing.T, upstreamModel string, usage *UsageRecord, apiErr *types.NewAPIError) {
	t.Helper()
	common.SetContextKey(f.context, constant.ContextKeyChannelName, "azure-eu")
	f.info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 7, ChannelType: 1, UpstreamModelName: upstreamModel}

	BeginAttempt(f.context, f.info)
	f.clock.advance(100 * time.Millisecond)
	if usage != nil {
		FromContext(f.context).RecordUsage(*usage)
	}
	f.clock.advance(100 * time.Millisecond)
	EndAttempt(f.context, f.info, apiErr)
	f.clock.advance(10 * time.Millisecond)
}

func upstreamError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("boom"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
}

func rootMetadataOf(t *testing.T, fixture *finishFixture) map[string]any {
	t.Helper()
	ended := fixture.spans.Ended()
	require.NotEmpty(t, ended)
	return decodeMetadata(t, attributesOf(ended[0])[attrObservationMetadata])
}

// TestRecordUsageWithoutActiveAttemptIsDroppedAndMarked covers the §8.4
// degradation the AWS SDK and Xunfei v1 bypasses hit: the settlement arrives
// without an attempt to own it, so the usage is dropped instead of being moved
// onto the root, and the omission is stated.
func TestRecordUsageWithoutActiveAttemptIsDroppedAndMarked(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runSettledAttempt(t, "gpt-4o-1", nil, nil)

	// The attempt is already closed when this settlement runs.
	fixture.recorder.RecordUsage(settledTextUsage(615))

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	root := rootMetadataOf(t, fixture)
	assert.Equal(t, true, root["usage_unattributed"])
	assert.Equal(t, UsageOmittedNoActiveAttempt, root["usage_omitted_reason"])
	assert.NotContains(t, root, "quota", "a dropped record is not a settlement candidate")
	assert.NotContains(t, root, "billing_source")

	generation := attributesOf(ended[1])
	assert.NotContains(t, generation, attribute.Key(attrUsageDetails),
		"the closed attempt must not inherit a later settlement")
	assert.NotContains(t, generation, attribute.Key(attrCostDetails))
}

// TestRootCopiesTheSettlementPairOnlyFromOneSettledAttempt is the §8.1 rule:
// the trace level quota summary exists only when exactly one attempt settled,
// and quota and billing_source are always written or omitted together.
func TestRootCopiesTheSettlementPairOnlyFromOneSettledAttempt(t *testing.T) {
	t.Run("exactly one settled attempt after a failed retry", func(t *testing.T) {
		fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
		fixture.runSettledAttempt(t, "gpt-4o-1", nil, upstreamError())
		fixture.runSettledAttempt(t, "gpt-4o-2", ptr(settledTextUsage(615)), nil)

		Finish(fixture.context, fixture.info, nil)
		root := rootMetadataOf(t, fixture)
		assert.EqualValues(t, 615, root["quota"])
		assert.Equal(t, "wallet", root["billing_source"])
	})

	t.Run("no settled attempt", func(t *testing.T) {
		usage := settledTextUsage(615)
		usage.Settled = false
		fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
		fixture.runSettledAttempt(t, "gpt-4o-1", &usage, nil)

		Finish(fixture.context, fixture.info, nil)
		root := rootMetadataOf(t, fixture)
		assert.NotContains(t, root, "quota")
		assert.NotContains(t, root, "billing_source")
	})

	t.Run("several settled attempts", func(t *testing.T) {
		fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
		fixture.runSettledAttempt(t, "gpt-4o-1", ptr(settledTextUsage(100)), nil)
		fixture.runSettledAttempt(t, "gpt-4o-2", ptr(settledTextUsage(615)), nil)

		Finish(fixture.context, fixture.info, nil)
		root := rootMetadataOf(t, fixture)
		assert.NotContains(t, root, "quota", "no attempt may be picked and nothing is summed")
		assert.NotContains(t, root, "billing_source")
	})

	t.Run("settled usage on a failed attempt", func(t *testing.T) {
		fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
		fixture.runSettledAttempt(t, "gpt-4o-1", ptr(settledTextUsage(615)), upstreamError())

		Finish(fixture.context, fixture.info, nil)
		root := rootMetadataOf(t, fixture)
		assert.NotContains(t, root, "quota", "a failed attempt is not a settlement candidate")
		assert.NotContains(t, root, "billing_source")
	})

	t.Run("illegal quota", func(t *testing.T) {
		usage := settledTextUsage(-1)
		fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
		fixture.runSettledAttempt(t, "gpt-4o-1", &usage, nil)

		Finish(fixture.context, fixture.info, nil)
		root := rootMetadataOf(t, fixture)
		assert.NotContains(t, root, "quota")
		assert.NotContains(t, root, "billing_source")
	})
}

// TestRootNeverCarriesUsageOrCostDetails keeps the observation level attribution
// intact: the trace summary is a metadata field for filtering, not a second
// place where the request's usage or cost lives.
func TestRootNeverCarriesUsageOrCostDetails(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runSettledAttempt(t, "gpt-4o-1", ptr(settledTextUsage(615)), nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	root := attributesOf(ended[0])
	assert.NotContains(t, root, attribute.Key(attrUsageDetails))
	assert.NotContains(t, root, attribute.Key(attrCostDetails))
	assert.NotContains(t, root, attribute.Key(attrModelName))

	generation := attributesOf(ended[1])
	assert.Contains(t, generation, attribute.Key(attrUsageDetails), "usage stays on the successful attempt")
	assert.Contains(t, generation, attribute.Key(attrCostDetails))
}

// TestGenerationModelNameFollowsTheHardContract covers the four §14.2 classes.
// Naming the model without an authoritative cost lets Langfuse price the
// generation from its own model catalogue, so a generation that still carries
// usage must stay anonymous even when its status is already Error.
func TestGenerationModelNameFollowsTheHardContract(t *testing.T) {
	unsettled := settledTextUsage(615)
	unsettled.Settled = false

	cases := []struct {
		name        string
		usage       *UsageRecord
		apiErr      *types.NewAPIError
		wantModel   bool
		wantUsage   bool
		wantCost    bool
		wantReason  string
		wantSources string
	}{
		{
			name: "error with model and neither usage nor cost", apiErr: upstreamError(),
			wantModel: true, wantReason: CostOmittedAttemptFailed, wantSources: "unavailable",
		},
		{
			name: "error with usage and no cost", usage: &unsettled, apiErr: upstreamError(),
			wantUsage: true, wantReason: CostOmittedAttemptFailed, wantSources: "unavailable",
		},
		{
			name: "success with usage and no cost", usage: &unsettled,
			wantUsage: true, wantReason: CostOmittedNoBillableUsage, wantSources: "unavailable",
		},
		{
			name: "success with usage, model and authoritative cost", usage: ptr(settledTextUsage(615)),
			wantModel: true, wantUsage: true, wantCost: true, wantSources: "new_api_settlement",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
			fixture.runSettledAttempt(t, "gpt-4o-1", testCase.usage, testCase.apiErr)

			Finish(fixture.context, fixture.info, nil)
			ended := fixture.spans.Ended()
			require.Len(t, ended, 2)

			generation := attributesOf(ended[1])
			if testCase.wantModel {
				assert.Equal(t, "gpt-4o-1", generation[attrModelName])
			} else {
				assert.NotContains(t, generation, attribute.Key(attrModelName))
			}
			assert.Equal(t, testCase.wantUsage, generation[attrUsageDetails] != "")
			assert.Equal(t, testCase.wantCost, generation[attrCostDetails] != "")

			// The model fallback attributes Langfuse also recognizes are never
			// written, whatever the class.
			for _, fallback := range []string{
				"gen_ai.response.model", "gen_ai.request.model", "ai.model.id",
				"llm.response.model", "llm.model_name", "model",
			} {
				assert.NotContains(t, generation, attribute.Key(fallback))
			}

			metadata := decodeMetadata(t, generation[attrObservationMetadata])
			assert.Equal(t, testCase.wantSources, metadata["cost_source"])
			if testCase.wantReason == "" {
				assert.NotContains(t, metadata, "cost_omitted_reason")
			} else {
				assert.Equal(t, testCase.wantReason, metadata["cost_omitted_reason"])
			}
			// The real model always stays available for diagnostics.
			assert.Equal(t, "gpt-4o-1", metadata["upstream_model"])
		})
	}
}

// TestSupersededAttemptKeepsItsOwnCostReason pins that a replaced attempt never
// claims the provider failed, and that it may still name its model because it
// carries no usage.
func TestSupersededAttemptKeepsItsOwnCostReason(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	common.SetContextKey(fixture.context, constant.ContextKeyChannelName, "azure-eu")
	fixture.info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 7, ChannelType: 1, UpstreamModelName: "gpt-4o-1"}

	BeginAttempt(fixture.context, fixture.info)
	fixture.clock.advance(100 * time.Millisecond)
	// The handler crossed the shared boundary again without the outer
	// EndAttempt, which supersedes the first attempt.
	fixture.runSettledAttempt(t, "gpt-4o-1", ptr(settledTextUsage(615)), nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 3)

	superseded := attributesOf(ended[1])
	assert.NotContains(t, superseded, attribute.Key(attrUsageDetails))
	assert.NotContains(t, superseded, attribute.Key(attrCostDetails))
	metadata := decodeMetadata(t, superseded[attrObservationMetadata])
	assert.Equal(t, CostOmittedAttemptSuperseded, metadata["cost_omitted_reason"])
	assert.Equal(t, AttemptEndSuperseded, metadata["attempt_end_reason"])
}

// TestGenerationMetadataExplainsEveryUsageOmission keeps the closed reason sets
// observable: telemetry that silently drops usage looks the same as a request
// that never produced any.
func TestGenerationMetadataExplainsEveryUsageOmission(t *testing.T) {
	cases := []struct {
		name     string
		usage    UsageRecord
		reason   string
		invalid  bool
		semantic bool
	}{
		{
			name:   "upstream returned no usage",
			usage:  UsageRecord{Kind: UsageKindText, ModelName: "gpt-4o-1", Quota: 615, QuotaPerUnit: 500000, Settled: true, BillingSource: "wallet"},
			reason: UsageOmittedUnavailable,
		},
		{
			name:    "negative source value",
			usage:   UsageRecord{Kind: UsageKindText, Available: true, InputTokens: 100, InputCachedTokens: -5},
			reason:  UsageOmittedInvalidSource,
			invalid: true,
		},
		{
			name:     "unmappable semantic",
			usage:    UsageRecord{Kind: UsageKindText, Available: true, UsageSemanticUnknown: true},
			reason:   UsageOmittedSemanticUnknown,
			semantic: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
			fixture.runSettledAttempt(t, "gpt-4o-1", &testCase.usage, nil)

			Finish(fixture.context, fixture.info, nil)
			ended := fixture.spans.Ended()
			require.Len(t, ended, 2)

			generation := attributesOf(ended[1])
			assert.NotContains(t, generation, attribute.Key(attrUsageDetails))
			metadata := decodeMetadata(t, generation[attrObservationMetadata])
			assert.Equal(t, testCase.reason, metadata["usage_omitted_reason"])
			assert.Equal(t, testCase.invalid, metadata["usage_invalid"] == true)
			assert.Equal(t, testCase.semantic, metadata["usage_semantic_unknown"] == true)
		})
	}
}

// TestGenerationMetadataReportsClampedUsage documents the OpenAI cache-write
// overlap: the usage is still exported, and the clamp is stated instead of
// pretending the base input was really zero.
func TestGenerationMetadataReportsClampedUsage(t *testing.T) {
	usage := settledTextUsage(615)
	usage.InputCachedTokens = 800
	usage.InputCacheWriteTokens = 400

	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runSettledAttempt(t, "gpt-4o-1", &usage, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	generation := attributesOf(ended[1])
	var buckets map[string]int
	require.NoError(t, common.UnmarshalJsonStr(generation[attrUsageDetails], &buckets))
	assert.Equal(t, 0, buckets["input"])
	assert.Equal(t, 1300, buckets["total"])

	metadata := decodeMetadata(t, generation[attrObservationMetadata])
	assert.Equal(t, true, metadata[UsageFlagInputClamped])
	assert.NotContains(t, metadata, UsageFlagOutputClamped)
	assert.Equal(t, "wallet", metadata["billing_source"])
}

// TestSettlementFailureKeepsUsageAndFlagsTheError separates the two ways a
// settlement can end without a cost: a real SettleBilling error is reported as
// such, while a request with nothing to bill is a normal omission.
func TestSettlementFailureKeepsUsageAndFlagsTheError(t *testing.T) {
	failed := settledTextUsage(615)
	failed.Settled = false
	failed.SettlementFailed = true

	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runSettledAttempt(t, "gpt-4o-1", &failed, nil)

	Finish(fixture.context, fixture.info, nil)
	ended := fixture.spans.Ended()
	require.Len(t, ended, 2)

	generation := attributesOf(ended[1])
	assert.Contains(t, generation, attribute.Key(attrUsageDetails), "confirmed usage survives a settlement failure")
	assert.NotContains(t, generation, attribute.Key(attrCostDetails))
	assert.NotContains(t, generation, attribute.Key(attrModelName))

	metadata := decodeMetadata(t, generation[attrObservationMetadata])
	assert.Equal(t, true, metadata["settlement_error"])
	assert.Equal(t, CostOmittedSettlementFailed, metadata["cost_omitted_reason"])

	root := rootMetadataOf(t, fixture)
	assert.NotContains(t, root, "quota", "a failed settlement leaves no trace level summary")
}

// TestRecordUsageIgnoresAFrozenRecorder keeps a late settlement out of an
// already submitted worker payload.
func TestRecordUsageIgnoresAFrozenRecorder(t *testing.T) {
	fixture := newFinishFixture(t, nil, `{"model":"gpt-4o"}`)
	fixture.runSettledAttempt(t, "gpt-4o-1", nil, nil)
	Finish(fixture.context, fixture.info, nil)

	assert.NotPanics(t, func() {
		fixture.recorder.RecordUsage(settledTextUsage(615))
		var absent *Recorder
		absent.RecordUsage(settledTextUsage(615))
	})

	fixture.recorder.mu.Lock()
	defer fixture.recorder.mu.Unlock()
	assert.False(t, fixture.recorder.usageUnattributed, "a frozen recorder records nothing at all")
	assert.Nil(t, fixture.recorder.attempts[0].Usage)
}

func ptr[T any](value T) *T { return &value }
