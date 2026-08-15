package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/langfuse"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tracedSettlement is the state a settlement function really runs in: a request
// with a Langfuse Recorder whose attempt is still open, because the shared HTTP
// boundary has not been closed by the retry loop yet.
type tracedSettlement struct {
	context   *gin.Context
	relayInfo *relaycommon.RelayInfo
	recorder  *langfuse.Recorder
}

func newTracedSettlement(t *testing.T, mutate func(*relaycommon.RelayInfo)) *tracedSettlement {
	t.Helper()
	truncate(t)
	seedUser(t, 1, 1_000_000)
	seedToken(t, 1, 1, "settlement-key", 1_000_000)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("token_name", "test_token")

	relayInfo := &relaycommon.RelayInfo{
		UserId:                  1,
		TokenId:                 1,
		TokenKey:                "settlement-key",
		UserQuota:               1_000_000,
		UsingGroup:              "default",
		OriginModelName:         "gpt-4o",
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		StartTime:               time.Now(),
		BillingSource:           BillingSourceWallet,
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			CacheRatio:      1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         5,
			ChannelType:       1,
			UpstreamModelName: "gpt-4o-2024-11-20",
		},
	}
	if mutate != nil {
		mutate(relayInfo)
	}

	recorder := &langfuse.Recorder{}
	common.SetContextKey(ctx, constant.ContextKeyLangfuseRecorder, recorder)
	// The generation this settlement belongs to.
	langfuse.BeginAttempt(ctx, relayInfo)

	return &tracedSettlement{context: ctx, relayInfo: relayInfo, recorder: recorder}
}

func (s *tracedSettlement) usage(t *testing.T) langfuse.UsageRecord {
	t.Helper()
	record, ok := s.recorder.AttemptUsage(0)
	require.True(t, ok, "the settlement must reach the open attempt")
	return record
}

func openAIUsage() *dto.Usage {
	return &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 100,
		TotalTokens:      1100,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 800,
			ImageTokens:  0,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 50},
	}
}

// TestPostTextConsumeQuotaSnapshotsTheSettledQuota pins the §8.4 snapshot
// discipline: the record carries the quota SettleBilling was actually called
// with — after the tiered override — together with the conversion rate that was
// in effect at that moment, so a later configuration change cannot re-price it.
func TestPostTextConsumeQuotaSnapshotsTheSettledQuota(t *testing.T) {
	const flatTieredExpr = `tier("default", p * 2 + c * 10)`
	settlement := newTracedSettlement(t, func(relayInfo *relaycommon.RelayInfo) {
		relayInfo.TieredBillingSnapshot = &billingexpr.BillingSnapshot{
			BillingMode:  "tiered_expr",
			ExprString:   flatTieredExpr,
			ExprHash:     billingexpr.ExprHashString(flatTieredExpr),
			GroupRatio:   1,
			QuotaPerUnit: 500_000,
		}
	})

	previousRate := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousRate })

	usage := openAIUsage()
	PostTextConsumeQuota(settlement.context, settlement.relayInfo, usage, nil)

	// The rate is a runtime option; the worker must not read the new value.
	common.QuotaPerUnit = 1_000_000

	record := settlement.usage(t)
	assert.Equal(t, langfuse.UsageKindText, record.Kind)
	assert.True(t, record.Available)
	assert.True(t, record.Settled)
	assert.False(t, record.SettlementFailed)
	assert.Equal(t, "gpt-4o-2024-11-20", record.ModelName)
	assert.Equal(t, BillingSourceWallet, record.BillingSource)
	assert.Equal(t, 500_000.0, record.QuotaPerUnit)

	assert.Equal(t, 1000, record.InputTokens)
	assert.Equal(t, 100, record.OutputTokens)
	assert.Equal(t, 800, record.InputCachedTokens)
	assert.Equal(t, 50, record.OutputReasoningTokens)
	assert.False(t, record.InputExcludesCache, "an undeclared OpenAI channel stays cache-inclusive")

	// p*2 + c*10 = 3000 per million tokens at rate 500000 -> 1500 quota; the
	// plain ratio product would have been 1100.
	assert.Equal(t, 1500, record.Quota, "the tiered result, not the intermediate ratio quota")
}

// TestPostTextConsumeQuotaSeparatesBillingSourceFromTheWallet keeps the §8.5
// wording honest: a subscription charge is not a wallet deduction, a free model
// says so, and an unknown compatibility path never impersonates the wallet.
func TestPostTextConsumeQuotaSeparatesBillingSourceFromTheWallet(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*relaycommon.RelayInfo)
		expected string
	}{
		{
			name:     "free model",
			mutate:   func(relayInfo *relaycommon.RelayInfo) { relayInfo.PriceData.FreeModel = true },
			expected: "free",
		},
		{
			name:     "unknown source is not the wallet",
			mutate:   func(relayInfo *relaycommon.RelayInfo) { relayInfo.BillingSource = "" },
			expected: "unknown",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			settlement := newTracedSettlement(t, testCase.mutate)
			PostTextConsumeQuota(settlement.context, settlement.relayInfo, openAIUsage(), nil)

			assert.Equal(t, testCase.expected, settlement.usage(t).BillingSource)
		})
	}
}

// TestPostTextConsumeQuotaWithoutBillableUsageIsNotSettled separates closing the
// billing session from actually charging: a request with nothing to bill must
// not produce an authoritative cost even though SettleBilling succeeded.
func TestPostTextConsumeQuotaWithoutBillableUsageIsNotSettled(t *testing.T) {
	settlement := newTracedSettlement(t, nil)
	empty := &dto.Usage{}

	PostTextConsumeQuota(settlement.context, settlement.relayInfo, empty, nil)

	record := settlement.usage(t)
	assert.True(t, record.Available, "an all zero usage is still a real usage")
	assert.False(t, record.Settled)
	assert.False(t, record.SettlementFailed, "nothing to bill is not a settlement error")
	assert.Equal(t, 0, record.Quota)
}

// TestPostTextConsumeQuotaKeepsUsageWhenSettlementFails documents the two
// independent dimensions: the usage was confirmed by the upstream, the cost was
// not, and only the settlement failure is flagged.
func TestPostTextConsumeQuotaKeepsUsageWhenSettlementFails(t *testing.T) {
	settlement := newTracedSettlement(t, func(relayInfo *relaycommon.RelayInfo) {
		// A subscription charge without a subscription id fails inside
		// SettleBilling, after the quota was computed.
		relayInfo.BillingSource = BillingSourceSubscription
		relayInfo.SubscriptionId = 0
	})

	PostTextConsumeQuota(settlement.context, settlement.relayInfo, openAIUsage(), nil)

	record := settlement.usage(t)
	assert.True(t, record.Available, "confirmed usage survives a settlement failure")
	assert.False(t, record.Settled)
	assert.True(t, record.SettlementFailed)
	assert.Equal(t, BillingSourceSubscription, record.BillingSource)
	assert.Positive(t, record.Quota, "the final settlement parameter is kept for diagnostics")
}

// TestPostTextConsumeQuotaReportsMissingUpstreamUsage keeps an internally
// estimated prompt count from impersonating provider usage.
func TestPostTextConsumeQuotaReportsMissingUpstreamUsage(t *testing.T) {
	settlement := newTracedSettlement(t, nil)

	PostTextConsumeQuota(settlement.context, settlement.relayInfo, nil, nil)

	record := settlement.usage(t)
	assert.False(t, record.Available)
	assert.Equal(t, langfuse.UsageKindText, record.Kind)
}

// TestPostTextConsumeQuotaOmitsUsageOnNegativeSourceValues checks the source
// validation runs on the raw fields, before CacheCreationTokensTotal clamps a
// negative one to zero — and that billing itself is untouched by it.
func TestPostTextConsumeQuotaOmitsUsageOnNegativeSourceValues(t *testing.T) {
	settlement := newTracedSettlement(t, nil)
	usage := openAIUsage()
	usage.PromptTokensDetails.CachedCreationTokens = -5

	PostTextConsumeQuota(settlement.context, settlement.relayInfo, usage, nil)

	assert.False(t, settlement.usage(t).Available, "a negative source value omits the usage")

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", 1).Find(&logs).Error)
	require.Len(t, logs, 1, "billing and logging continue regardless of telemetry")
	assert.Equal(t, "gpt-4o", logs[0].ModelName)
}

// TestPostTextConsumeQuotaWithoutRecorderStaysSilent keeps every untraced
// request on exactly the path it had before Langfuse existed.
func TestPostTextConsumeQuotaWithoutRecorderStaysSilent(t *testing.T) {
	settlement := newTracedSettlement(t, nil)
	common.SetContextKey(settlement.context, constant.ContextKeyLangfuseRecorder, (*langfuse.Recorder)(nil))

	assert.NotPanics(t, func() {
		PostTextConsumeQuota(settlement.context, settlement.relayInfo, openAIUsage(), nil)
	})

	_, ok := settlement.recorder.AttemptUsage(0)
	assert.False(t, ok)
}
