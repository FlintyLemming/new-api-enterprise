package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/langfuse"

	"github.com/stretchr/testify/assert"
)

func audioUsage(promptText, promptAudio, completionText, completionAudio int) *dto.Usage {
	return &dto.Usage{
		PromptTokens:     promptText + promptAudio,
		CompletionTokens: completionText + completionAudio,
		TotalTokens:      promptText + promptAudio + completionText + completionAudio,
		PromptTokensDetails: dto.InputTokenDetails{
			TextTokens:  promptText,
			AudioTokens: promptAudio,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			TextTokens:  completionText,
			AudioTokens: completionAudio,
		},
	}
}

// TestPostAudioConsumeQuotaRecordsTheFourTokenBuckets pins the §8.4 audio
// contract: the exported record uses exactly the token fields the audio billing
// computed from, so the generation carries text and audio buckets rather than a
// guessed split of the prompt total.
func TestPostAudioConsumeQuotaRecordsTheFourTokenBuckets(t *testing.T) {
	settlement := newTracedSettlement(t, nil)

	previousRate := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousRate })

	PostAudioConsumeQuota(settlement.context, settlement.relayInfo, audioUsage(200, 300, 50, 25), "")

	record := settlement.usage(t)
	assert.Equal(t, langfuse.UsageKindAudio, record.Kind)
	assert.True(t, record.Available)
	assert.True(t, record.Settled)
	assert.False(t, record.SettlementFailed)
	assert.Equal(t, "gpt-4o-2024-11-20", record.ModelName)
	assert.Equal(t, BillingSourceWallet, record.BillingSource)
	assert.Equal(t, 500_000.0, record.QuotaPerUnit)

	assert.Equal(t, 200, record.InputTokens)
	assert.Equal(t, 300, record.InputAudioTokens)
	assert.Equal(t, 50, record.OutputTokens)
	assert.Equal(t, 25, record.OutputAudioTokens)
	assert.Positive(t, record.Quota)
}

// TestPostAudioConsumeQuotaKeepsTheZeroTokenRuleSettled documents the existing
// self-consistent audio rule: no tokens means no charge, and the settlement
// still succeeded, so the generation gets an authoritative zero cost instead of
// letting Langfuse price the model itself.
func TestPostAudioConsumeQuotaKeepsTheZeroTokenRuleSettled(t *testing.T) {
	settlement := newTracedSettlement(t, nil)

	PostAudioConsumeQuota(settlement.context, settlement.relayInfo, audioUsage(0, 0, 0, 0), "")

	record := settlement.usage(t)
	assert.True(t, record.Available)
	assert.True(t, record.Settled)
	assert.Equal(t, 0, record.Quota)
}

// TestPostAudioConsumeQuotaKeepsUsageWhenSettlementFails keeps the usage and
// cost dimensions independent on the audio path too.
func TestPostAudioConsumeQuotaKeepsUsageWhenSettlementFails(t *testing.T) {
	settlement := newTracedSettlement(t, func(relayInfo *relaycommon.RelayInfo) {
		relayInfo.BillingSource = BillingSourceSubscription
		relayInfo.SubscriptionId = 0
	})

	PostAudioConsumeQuota(settlement.context, settlement.relayInfo, audioUsage(200, 300, 50, 25), "")

	record := settlement.usage(t)
	assert.True(t, record.Available)
	assert.False(t, record.Settled)
	assert.True(t, record.SettlementFailed)
	assert.Equal(t, BillingSourceSubscription, record.BillingSource)
}
