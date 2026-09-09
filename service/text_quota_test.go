package service

import (
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateTextQuotaSummaryUnifiedForClaudeSemantic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         100,
			CachedCreationTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 10,
		ClaudeCacheCreation1hTokens: 20,
	}

	priceData := hosttypes.PriceData{
		ModelRatio:           1,
		CompletionRatio:      2,
		CacheRatio:           0.1,
		CacheCreationRatio:   1.25,
		CacheCreation5mRatio: 1.25,
		CacheCreation1hRatio: 2,
		GroupRatioInfo: hosttypes.GroupRatioInfo{
			GroupRatio: 1,
		},
	}

	chatRelayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-3-7-sonnet",
		PriceData:               priceData,
		StartTime:               time.Now(),
	}
	messageRelayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-3-7-sonnet",
		PriceData:               priceData,
		StartTime:               time.Now(),
	}

	chatSummary := calculateTextQuotaSummary(ctx, chatRelayInfo, usage)
	messageSummary := calculateTextQuotaSummary(ctx, messageRelayInfo, usage)

	require.Equal(t, messageSummary.Quota, chatSummary.Quota)
	require.Equal(t, messageSummary.CacheCreationTokens5m, chatSummary.CacheCreationTokens5m)
	require.Equal(t, messageSummary.CacheCreationTokens1h, chatSummary.CacheCreationTokens1h)
	require.True(t, chatSummary.IsClaudeUsageSemantic)
	require.Equal(t, 1488, chatSummary.Quota)
}

func TestCalculateTextQuotaSummaryUsesSplitClaudeCacheCreationRatios(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-3-7-sonnet",
		PriceData: hosttypes.PriceData{
			ModelRatio:           1,
			CompletionRatio:      1,
			CacheRatio:           0,
			CacheCreationRatio:   1,
			CacheCreation5mRatio: 2,
			CacheCreation1hRatio: 3,
			GroupRatioInfo: hosttypes.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 0,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 10,
		},
		ClaudeCacheCreation5mTokens: 2,
		ClaudeCacheCreation1hTokens: 3,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// 100 + remaining(5)*1 + 2*2 + 3*3 = 118
	require.Equal(t, 118, summary.Quota)
}

func TestCalculateTextQuotaSummaryUsesAnthropicUsageSemanticFromUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "claude-3-7-sonnet",
		PriceData: hosttypes.PriceData{
			ModelRatio:           1,
			CompletionRatio:      2,
			CacheRatio:           0.1,
			CacheCreationRatio:   1.25,
			CacheCreation5mRatio: 1.25,
			CacheCreation1hRatio: 2,
			GroupRatioInfo: hosttypes.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		UsageSemantic:    "anthropic",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         100,
			CachedCreationTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 10,
		ClaudeCacheCreation1hTokens: 20,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.True(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, "anthropic", summary.UsageSemantic)
	require.Equal(t, 1488, summary.Quota)
}

func TestCalculateTextQuotaSummaryUsesClaudeBillingUsageBeforeTopLevelUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "claude-3-7-sonnet",
		PriceData: hosttypes.PriceData{
			ModelRatio:           1,
			CompletionRatio:      2,
			CacheRatio:           0.1,
			CacheCreationRatio:   1.25,
			CacheCreation5mRatio: 1.25,
			CacheCreation1hRatio: 2,
			GroupRatioInfo:       hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     999,
		CompletionTokens: 999,
		TotalTokens:      1998,
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
			InputTokens:              70,
			CacheReadInputTokens:     30,
			CacheCreationInputTokens: 20,
			OutputTokens:             7,
			CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 12,
				Ephemeral1hInputTokens: 8,
			},
		}),
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, effectiveBillingUsage(usage))

	require.True(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, dto.BillingUsageSemanticAnthropic, summary.UsageSemantic)
	require.Equal(t, 70, summary.PromptTokens)
	require.Equal(t, 7, summary.CompletionTokens)
	require.Equal(t, 30, summary.CacheTokens)
	require.Equal(t, 20, summary.CacheCreationTokens)
	require.Equal(t, 12, summary.CacheCreationTokens5m)
	require.Equal(t, 8, summary.CacheCreationTokens1h)
	require.Equal(t, 118, summary.Quota)
}

func TestCalculateTextQuotaSummaryUsesGeminiBillingUsageBeforeTopLevelUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gemini-2.5-flash",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 2,
			CacheRatio:      0.1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     999,
		CompletionTokens: 999,
		TotalTokens:      1998,
		BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
			PromptTokenCount:        100,
			ToolUsePromptTokenCount: 5,
			CandidatesTokenCount:    20,
			ThoughtsTokenCount:      3,
			TotalTokenCount:         128,
			CachedContentTokenCount: 7,
		}),
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, effectiveBillingUsage(usage))

	require.False(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, dto.BillingUsageSemanticGemini, summary.UsageSemantic)
	require.Equal(t, 105, summary.PromptTokens)
	require.Equal(t, 23, summary.CompletionTokens)
	require.Equal(t, 7, summary.CacheTokens)
	require.Equal(t, 128, summary.TotalTokens)
	require.Equal(t, 145, summary.Quota)
}

func TestCalculateTextQuotaSummaryUsesOpenAIBillingUsageBeforeTopLevelUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatClaude,
		OriginModelName: "gpt-4o",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 2,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     999,
		CompletionTokens: 999,
		TotalTokens:      1998,
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
			PromptTokens:     80,
			CompletionTokens: 9,
			TotalTokens:      89,
		}),
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, effectiveBillingUsage(usage))

	require.False(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, dto.BillingUsageSemanticOpenAI, summary.UsageSemantic)
	require.Equal(t, 80, summary.PromptTokens)
	require.Equal(t, 9, summary.CompletionTokens)
	require.Equal(t, 89, summary.TotalTokens)
	require.Equal(t, 98, summary.Quota)
}

func TestCalculateTextQuotaSummaryUsesOpenAIResponsesInputTokenDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gpt-4o",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 2,
			CacheRatio:      0.25,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	responsesUsage := &dto.Usage{
		InputTokens:  100,
		OutputTokens: 10,
		TotalTokens:  110,
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens: 40,
		},
	}
	convertedUsage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 10,
		TotalTokens:      110,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 40,
		},
		BillingUsage: dto.NewOpenAIResponsesBillingUsage(responsesUsage),
	}

	effectiveUsage := effectiveBillingUsage(convertedUsage)
	require.Equal(t, 40, effectiveUsage.PromptTokensDetails.CachedTokens)
	require.Zero(t, convertedUsage.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)

	summary := calculateTextQuotaSummary(ctx, relayInfo, effectiveUsage)
	require.Equal(t, 40, summary.CacheTokens)
	// 60 uncached input + 40*0.25 cached input + 10*2 output = 90.
	require.Equal(t, 90, summary.Quota)
}

func TestUsageFromOpenAIBillingUsageNormalizesCacheDetailsWithoutOverwritingCanonicalValues(t *testing.T) {
	responsesUsage := &dto.Usage{
		InputTokens:          100,
		OutputTokens:         10,
		PromptCacheHitTokens: 55,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 8,
			TextTokens:   12,
		},
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens:         40,
			CachedCreationTokens: 5,
			CacheWriteTokens:     6,
			TextTokens:           60,
			ImageTokens:          7,
			AudioTokens:          9,
		},
	}

	billingUsage := dto.NewOpenAIResponsesBillingUsage(responsesUsage)
	usage := effectiveBillingUsage(&dto.Usage{BillingUsage: billingUsage})

	require.Equal(t, 8, usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 5, usage.PromptTokensDetails.CachedCreationTokens)
	require.Equal(t, 6, usage.PromptTokensDetails.CacheWriteTokens)
	require.Equal(t, 12, usage.PromptTokensDetails.TextTokens)
	require.Equal(t, 7, usage.PromptTokensDetails.ImageTokens)
	require.Equal(t, 9, usage.PromptTokensDetails.AudioTokens)
	require.Zero(t, billingUsage.OpenAIUsage.PromptTokensDetails.CachedCreationTokens)
}

func TestUsageFromOpenAIBillingUsageFallsBackToPromptCacheHitTokens(t *testing.T) {
	usage := effectiveBillingUsage(&dto.Usage{
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
			PromptTokens:         100,
			CompletionTokens:     10,
			PromptCacheHitTokens: 35,
		}),
	})

	require.Equal(t, 35, usage.PromptTokensDetails.CachedTokens)
}

func TestCalculateTextQuotaSummaryNormalizesOpenAIResponsesBillingUsageDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatClaude,
		OriginModelName: "gpt-5.6-sol",
		PriceData: hosttypes.PriceData{
			ModelRatio:         1,
			CompletionRatio:    2,
			CacheRatio:         0.5,
			CacheCreationRatio: 2,
			GroupRatioInfo:     hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	responsesDetails := dto.InputTokenDetails{
		CachedTokens:     80,
		CacheWriteTokens: 10,
		TextTokens:       100,
	}
	usage := &dto.Usage{
		PromptTokens:     999,
		CompletionTokens: 999,
		BillingUsage: dto.NewOpenAIResponsesBillingUsage(&dto.Usage{
			InputTokens:        100,
			OutputTokens:       10,
			TotalTokens:        110,
			InputTokensDetails: &responsesDetails,
		}),
	}

	effectiveUsage := effectiveBillingUsage(usage)
	summary := calculateTextQuotaSummary(ctx, relayInfo, effectiveUsage)

	require.Equal(t, dto.BillingUsageSourceOAIResponses, effectiveUsage.UsageSource)
	require.Equal(t, responsesDetails, effectiveUsage.PromptTokensDetails)
	require.Equal(t, 100, summary.PromptTokens)
	require.Equal(t, 10, summary.CompletionTokens)
	require.Equal(t, 80, summary.CacheTokens)
	require.Equal(t, 10, summary.CacheCreationTokens)
	// (100-80-10) + 80*0.5 + 10*2 + 10*2 = 90
	require.Equal(t, 90, summary.Quota)
}

func TestUsageBillingPathForLog(t *testing.T) {
	require.Equal(t, usageBillingPathAnthropic, usageBillingPathForLog(true, &dto.Usage{
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 1}),
	}))
	invalidBillingUsage := &dto.Usage{
		PromptTokens: 1,
		BillingUsage: &dto.BillingUsage{
			Source:   dto.BillingUsageSourceClaudeMessages,
			Semantic: dto.BillingUsageSemanticAnthropic,
		},
	}
	require.Equal(t, usageBillingPathLocal, usageBillingPathForLog(true, invalidBillingUsage))
	require.Equal(t, usageBillingPathUpstream, usageBillingPathForLog(false, invalidBillingUsage))
	require.Equal(t, usageBillingPathUpstream, usageBillingPathForLog(false, &dto.Usage{}))
	require.Equal(t, usageBillingPathOpenAI, usageBillingPathForLog(false, &dto.Usage{
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{PromptTokens: 1}),
	}))
	require.Equal(t, usageBillingPathAnthropic, usageBillingPathForLog(false, &dto.Usage{
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 1}),
	}))
	require.Equal(t, usageBillingPathGemini, usageBillingPathForLog(false, &dto.Usage{
		BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{PromptTokenCount: 1}),
	}))
	require.Equal(t, usageBillingPathGeminiEstimated, usageBillingPathForLog(true, &dto.Usage{
		BillingUsage: dto.NewEstimatedGeminiChatBillingUsage(&dto.Usage{PromptTokens: 1}),
	}))
}

func TestAppendUsageBillingPathForLogWritesAdminInfo(t *testing.T) {
	other := model.NewLogOther()
	appendUsageBillingPathForLog(other, true, &dto.Usage{
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 1}),
	})

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, usageBillingPathAnthropic, adminInfo["usage_billing_path"])

	other = model.NewLogOther()
	appendUsageBillingPathForLog(other, true, nil)
	adminInfo, ok = other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, usageBillingPathLocal, adminInfo["usage_billing_path"])
}

func TestCacheWriteTokensTotal(t *testing.T) {
	t.Run("split cache creation", func(t *testing.T) {
		summary := textQuotaSummary{
			CacheCreationTokens:   50,
			CacheCreationTokens5m: 10,
			CacheCreationTokens1h: 20,
		}
		require.Equal(t, 50, cacheWriteTokensTotal(summary))
	})

	t.Run("legacy cache creation", func(t *testing.T) {
		summary := textQuotaSummary{CacheCreationTokens: 50}
		require.Equal(t, 50, cacheWriteTokensTotal(summary))
	})

	t.Run("split cache creation without aggregate remainder", func(t *testing.T) {
		summary := textQuotaSummary{
			CacheCreationTokens5m: 10,
			CacheCreationTokens1h: 20,
		}
		require.Equal(t, 30, cacheWriteTokensTotal(summary))
	})
}

func TestCalculateTextQuotaSummaryHandlesLegacyClaudeDerivedOpenAIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "claude-3-7-sonnet",
		PriceData: hosttypes.PriceData{
			ModelRatio:           1,
			CompletionRatio:      5,
			CacheRatio:           0.1,
			CacheCreationRatio:   1.25,
			CacheCreation5mRatio: 1.25,
			CacheCreation1hRatio: 2,
			GroupRatioInfo:       hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     62,
		CompletionTokens: 95,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 3544,
		},
		ClaudeCacheCreation5mTokens: 586,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// 62 + 3544*0.1 + 586*1.25 + 95*5 = 1624.9 => 1624
	require.Equal(t, 1624, summary.Quota)
}

func TestCalculateTextQuotaSummaryBillsOpenAICacheWriteTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gpt-5.1",
		PriceData: hosttypes.PriceData{
			ModelRatio:         1,
			CompletionRatio:    2,
			CacheRatio:         0.1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	t.Run("uncached remainder stays positive", func(t *testing.T) {
		usage := &dto.Usage{
			PromptTokens:     1473,
			CompletionTokens: 19,
			PromptTokensDetails: dto.InputTokenDetails{
				CacheWriteTokens: 1470,
			},
		}

		summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

		require.Equal(t, 1470, summary.CacheCreationTokens)
		// (1473-0-1470) + 1470*1.25 + 19*2 = 3 + 1837.5 + 38 = 1878.5 => 1879
		require.Equal(t, 1879, summary.Quota)
	})

	t.Run("uncached remainder clamps to zero", func(t *testing.T) {
		// Real OpenAI payload shape: cached_tokens + cache_write_tokens exceeds
		// prompt_tokens because both are unadjusted prefix counts. The negative
		// remainder must clamp to zero, never turn into a negative base charge.
		usage := &dto.Usage{
			PromptTokens:     3619,
			CompletionTokens: 36,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:     2921,
				CacheWriteTokens: 3616,
			},
		}

		summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

		require.Equal(t, 3619, summary.PromptTokens)
		require.Equal(t, 3616, summary.CacheCreationTokens)
		// max(3619-2921-3616, 0) + 2921*0.1 + 3616*1.25 + 36*2 = 4884.1 => 4884
		require.Equal(t, 4884, summary.Quota)
	})
}

func TestCalculateTextQuotaSummarySeparatesOpenRouterCacheReadFromPromptBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "openai/gpt-4.1",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenRouter,
		},
		PriceData: hosttypes.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheRatio:         0.1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     2604,
		CompletionTokens: 383,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 2432,
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// OpenRouter OpenAI-format display keeps prompt_tokens as total input,
	// but billing still separates normal input from cache read tokens.
	// quota = (2604 - 2432) + 2432*0.1 + 383 = 798.2 => 798
	require.Equal(t, 2604, summary.PromptTokens)
	require.Equal(t, 798, summary.Quota)
}

func TestCalculateTextQuotaSummarySeparatesOpenRouterCacheCreationFromPromptBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "openai/gpt-4.1",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenRouter,
		},
		PriceData: hosttypes.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     2604,
		CompletionTokens: 383,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 100,
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// prompt_tokens is still logged as total input, but cache creation is billed separately.
	// quota = (2604 - 100) + 100*1.25 + 383 = 3012
	require.Equal(t, 2604, summary.PromptTokens)
	require.Equal(t, 3012, summary.Quota)
}

func TestCalculateTextQuotaSummaryKeepsPrePRClaudeOpenRouterBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "anthropic/claude-3.7-sonnet",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenRouter,
		},
		PriceData: hosttypes.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheRatio:         0.1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     2604,
		CompletionTokens: 383,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 2432,
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// Pre-PR PostClaudeConsumeQuota behavior for OpenRouter:
	// prompt = 2604 - 2432 = 172
	// quota = 172 + 2432*0.1 + 383 = 798.2 => 798
	require.True(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, 172, summary.PromptTokens)
	require.Equal(t, 798, summary.Quota)
}

func TestCalculateTextQuotaSummary_CacheSemantic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	priceData := hosttypes.PriceData{
		ModelRatio:           1,
		CompletionRatio:      1,
		CacheRatio:           0.1,
		CacheCreationRatio:   1.25,
		CacheCreation5mRatio: 1.25,
		CacheCreation1hRatio: 2,
		GroupRatioInfo:       hosttypes.GroupRatioInfo{GroupRatio: 1},
	}

	cases := []struct {
		name           string
		semantic       string // usage.UsageSemantic
		declared       string // ChannelSetting.CachePromptTokenSemantic
		legacyClaude   bool   // 触发 isLegacyClaudeDerivedOpenAIUsage 的 5m/1h 字段
		openRouter     bool
		rawPrompt      int
		cacheRead      int
		cacheCreation  int
		wantSummaryPT  int // fold 后 summary.PromptTokens
		wantUpstreamIn bool
		wantExcludes   bool
		wantQuota      int // nonzero: also pin summary.Quota
	}{
		{name: "undeclared openai inclusive", semantic: "openai", rawPrompt: 1000, cacheRead: 800,
			wantSummaryPT: 1000, wantUpstreamIn: true, wantExcludes: false},
		// quota = 100 (folded prompt) + 800*0.1 (cache read) + 100*1.25 (flat
		// cache creation, no base subtraction after fold) = 305.
		{name: "declared includes non-claude", semantic: "openai", declared: cacheSemanticIncludes,
			rawPrompt: 1000, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 100, wantUpstreamIn: true, wantExcludes: true, wantQuota: 305},
		// quota = 200 (exclusive prompt) + 800*0.1 + 100*1.25 (default flat
		// arm for declared-excludes non-Claude) = 405.
		{name: "declared excludes non-claude", semantic: "openai", declared: cacheSemanticExcludes,
			rawPrompt: 200, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 200, wantUpstreamIn: false, wantExcludes: true, wantQuota: 405},
		{name: "anthropic semantic", semantic: "anthropic", rawPrompt: 200, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 200, wantUpstreamIn: false, wantExcludes: true},
		// legacy claude derived 要求 UsageSemantic 为空,仅凭 5m/1h 字段触发 legacy 判定。
		{name: "legacy claude derived", legacyClaude: true, rawPrompt: 1000, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 1000, wantUpstreamIn: false, wantExcludes: true},
		// OpenRouter Claude 行沿用现有构造方式(FinalRequestRelayFormat=Claude +
		// ChannelTypeOpenRouter),期望与现状一致:summary fold 后 prompt 为
		// raw-cacheRead-cacheCreation,InputExcludesCache=true。
		{name: "undeclared openrouter claude", openRouter: true, rawPrompt: 1000, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 100, wantUpstreamIn: false, wantExcludes: true},
		// declared includes 且 raw-cache < 0 时 fold 结果 clamp 为 0。
		{name: "declared includes negative fold clamps to zero", semantic: "openai", declared: cacheSemanticIncludes,
			rawPrompt: 500, cacheRead: 800, cacheCreation: 100,
			wantSummaryPT: 0, wantUpstreamIn: true, wantExcludes: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relayInfo := &relaycommon.RelayInfo{
				RelayFormat:     types.RelayFormatOpenAI,
				OriginModelName: "claude-3-7-sonnet",
				PriceData:       priceData,
				StartTime:       time.Now(),
			}
			if tc.openRouter {
				relayInfo.FinalRequestRelayFormat = types.RelayFormatClaude
				relayInfo.ChannelMeta = &relaycommon.ChannelMeta{
					ChannelType: constant.ChannelTypeOpenRouter,
				}
			}
			if tc.declared != "" {
				if relayInfo.ChannelMeta == nil {
					relayInfo.ChannelMeta = &relaycommon.ChannelMeta{}
				}
				relayInfo.ChannelMeta.ChannelSetting = dto.ChannelSettings{
					CachePromptTokenSemantic: tc.declared,
				}
			}

			usage := &dto.Usage{
				PromptTokens:     tc.rawPrompt,
				CompletionTokens: 0,
				UsageSemantic:    tc.semantic,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens:         tc.cacheRead,
					CachedCreationTokens: tc.cacheCreation,
				},
			}
			if tc.legacyClaude {
				usage.ClaudeCacheCreation5mTokens = 60
				usage.ClaudeCacheCreation1hTokens = 40
			}

			summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

			assert.Equal(t, tc.wantSummaryPT, summary.PromptTokens, "summary.PromptTokens")
			assert.Equal(t, tc.wantUpstreamIn, summary.UpstreamPromptTokensIncludeCache, "summary.UpstreamPromptTokensIncludeCache")
			assert.Equal(t, tc.wantExcludes, summary.InputExcludesCache, "summary.InputExcludesCache")
			if tc.wantQuota != 0 {
				assert.Equal(t, tc.wantQuota, summary.Quota, "summary.Quota")
			}
		})
	}
}

// TestResolvePromptCacheInclusionUndeclaredAutoDetect covers the auto-detect
// fallbacks when no channel declaration exists, including nil relayInfo /
// nil ChannelMeta / nil usage inputs.
func TestResolvePromptCacheInclusionUndeclaredAutoDetect(t *testing.T) {
	t.Run("nil relayInfo falls back to usage semantic", func(t *testing.T) {
		included, declared := resolvePromptCacheInclusion(nil, &dto.Usage{UsageSemantic: "anthropic"})
		assert.False(t, included)
		assert.False(t, declared)

		included, declared = resolvePromptCacheInclusion(nil, &dto.Usage{UsageSemantic: "openai"})
		assert.True(t, included)
		assert.False(t, declared)
	})

	t.Run("nil ChannelMeta declares nothing", func(t *testing.T) {
		relayInfo := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI}
		included, declared := resolvePromptCacheInclusion(relayInfo, &dto.Usage{UsageSemantic: "openai"})
		assert.True(t, included)
		assert.False(t, declared)
	})

	t.Run("nil usage is treated as undeclared", func(t *testing.T) {
		relayInfo := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude}
		included, declared := resolvePromptCacheInclusion(relayInfo, nil)
		assert.True(t, included)
		assert.False(t, declared)
	})

	t.Run("declared semantic wins over usage semantic", func(t *testing.T) {
		relayInfo := &relaycommon.RelayInfo{
			RelayFormat: types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting: dto.ChannelSettings{CachePromptTokenSemantic: cacheSemanticExcludes},
			},
		}
		included, declared := resolvePromptCacheInclusion(relayInfo, &dto.Usage{UsageSemantic: "openai"})
		assert.False(t, included)
		assert.True(t, declared)
	})
}

func TestComposeTieredTextQuotaKeepsToolCallSurcharges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	// 11 $/1K => 0.011 per completed image output, matching the prior fixed low-tier charge.
	operation_setting.SetToolPriceForTest(dto.BuildInToolImageGeneration, 11.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest(dto.BuildInToolImageGeneration)
	})

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "o1",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {
					CallCount: 1,
				},
				dto.BuildInToolFileSearch: {
					CallCount: 2,
				},
				dto.BuildInToolImageGeneration: {
					CallCount: 1,
				},
			},
		},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			GroupRatio:                1,
			EstimatedQuotaBeforeGroup: 1000,
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)
	quota := composeTieredTextQuota(relayInfo, summary, 1000, &billingexpr.TieredResult{
		ActualQuotaBeforeGroup: 1000,
		ActualQuotaAfterGroup:  1000,
	})

	require.Equal(t, int64(13000), summary.ToolCallSurchargeQuota.Round(0).IntPart())
	require.Equal(t, 14000, quota)
}

func TestComposeTieredTextQuotaFallbackKeepsToolCallSurcharges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Set("claude_web_search_requests", 2)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1.25},
		},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			GroupRatio:                1.25,
			EstimatedQuotaBeforeGroup: 1000,
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)
	quota := composeTieredTextQuota(relayInfo, summary, 1250, nil)

	require.Equal(t, int64(12500), summary.ToolCallSurchargeQuota.Round(0).IntPart())
	require.Equal(t, 13750, quota)
}

func TestComposeTieredTextQuotaErrorFallbackUsesPreConsumedQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Set("claude_web_search_requests", 2)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1.25},
		},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			GroupRatio:                1.25,
			EstimatedQuotaBeforeGroup: 1000,
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// tieredResult=nil simulates a settlement error where TryTieredSettle
	// falls back to FinalPreConsumedQuota (2000), which differs from
	// EstimatedQuotaBeforeGroup * GroupRatio (1250).
	preConsumedFallback := 2000
	quota := composeTieredTextQuota(relayInfo, summary, preConsumedFallback, nil)

	require.Equal(t, int64(12500), summary.ToolCallSurchargeQuota.Round(0).IntPart())
	require.Equal(t, 14500, quota)
}

// TestTryTieredSettleRecordsClampOnOverflow guards that an oversized tiered
// settlement both saturates the quota and records the clamp on RelayInfo, so
// every consume path (text, audio, WSS) can surface it under admin_info.
func TestTryTieredSettleRecordsClampOnOverflow(t *testing.T) {
	// exprOutput = p * 1e12; quotaBeforeGroup = p*1e12 / 1e6 * 5e5 far exceeds
	// the supported single-request range and must saturate.
	exprStr := `tier("base", p * 1000000000000)`
	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "overflow-model",
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:  "tiered_expr",
			ExprString:   exprStr,
			ExprHash:     billingexpr.ExprHashString(exprStr),
			GroupRatio:   1,
			QuotaPerUnit: 500_000,
		},
	}

	ok, quota, result := TryTieredSettle(relayInfo, billingexpr.TokenParams{P: 1_000_000_000})

	require.True(t, ok)
	require.NotNil(t, result)
	require.Equal(t, common.MaxQuota, quota, "oversized settlement must clamp, never wrap negative")
	require.NotNil(t, relayInfo.QuotaClamp, "clamp must be recorded on RelayInfo for admin auditing")
	require.Equal(t, common.QuotaClampOverflow, relayInfo.QuotaClamp.Kind)
}

// TestTryTieredSettleNoClampInRange confirms an in-range settlement leaves
// RelayInfo.QuotaClamp nil.
func TestTryTieredSettleNoClampInRange(t *testing.T) {
	exprStr := `tier("base", p * 2 + c * 10)`
	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "in-range-model",
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:  "tiered_expr",
			ExprString:   exprStr,
			ExprHash:     billingexpr.ExprHashString(exprStr),
			GroupRatio:   1,
			QuotaPerUnit: 500_000,
		},
	}

	ok, _, result := TryTieredSettle(relayInfo, billingexpr.TokenParams{P: 1000, C: 500})

	require.True(t, ok)
	require.NotNil(t, result)
	require.Nil(t, relayInfo.QuotaClamp, "in-range settlement must not record a clamp")
}

func TestCalculateTextQuotaSummaryFixedPriceAppliesImageCountOnceAndAllowsOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	priceData := hosttypes.PriceData{
		ModelPrice: 0.12,
		UsePrice:   true,
		GroupRatioInfo: hosttypes.GroupRatioInfo{
			GroupRatio: 1,
		},
	}
	priceData.AddOtherRatio("n", 3)
	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "dall-e-3",
		PriceData:       priceData,
		StartTime:       time.Now(),
	}
	usage := &dto.Usage{PromptTokens: 1, TotalTokens: 1}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)
	require.Equal(t, 180000, summary.Quota)

	// An adaptor-reported actual count replaces the requested count rather
	// than multiplying it a second time.
	relayInfo.PriceData.AddOtherRatio("n", 2)
	summary = calculateTextQuotaSummary(ctx, relayInfo, usage)
	require.Equal(t, 120000, summary.Quota)
}

func TestCalculateTextToolCallSurchargeGeneralizedBuiltInTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	operation_setting.SetToolPriceForTest("my_fn", 5.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("my_fn")
	})

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "o1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {CallCount: 2},
				"my_fn":                         {CallCount: 3},
				"unpriced":                      {CallCount: 5},
			},
		},
	}
	summary := &textQuotaSummary{
		ModelName:  "o1",
		GroupRatio: 1,
	}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)
	expected := decimal.NewFromFloat((10.0*2 + 5.0*3) / 1000).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(surcharge), "got %s want %s", surcharge, expected)
	require.Len(t, summary.ToolSurchargeItems, 2)
	assert.Equal(t, "my_fn", summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, 3, summary.ToolSurchargeItems[0].Count)
	assert.Equal(t, 5.0, summary.ToolSurchargeItems[0].Price)
	assert.Equal(t, dto.BuildInToolWebSearchPreview, summary.ToolSurchargeItems[1].Name)
	assert.Equal(t, 2, summary.ToolSurchargeItems[1].Count)
	assert.Equal(t, 10.0, summary.ToolSurchargeItems[1].Price)
}

func TestCalculateTextToolCallSurchargeKeepsSearchPreviewFallbackWithCustomFunctions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	operation_setting.SetToolPriceForTest("my_fn", 5)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("my_fn")
	})

	relayInfo := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-4o-search-preview",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				"my_fn": {CallCount: 1},
			},
		},
	}
	summary := &textQuotaSummary{
		ModelName:  relayInfo.OriginModelName,
		GroupRatio: 1,
	}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)

	require.Len(t, summary.ToolSurchargeItems, 2)
	assert.Equal(t, "my_fn", summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, dto.BuildInToolWebSearchPreview, summary.ToolSurchargeItems[1].Name)
	expected := decimal.NewFromFloat((5.0 + 25.0) / 1000).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(surcharge), "got %s want %s", surcharge, expected)
}

func TestCalculateTextToolCallSurchargeDoesNotInferSearchForResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponses,
		OriginModelName: "gpt-4o-search-preview",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{},
		},
	}
	summary := &textQuotaSummary{
		ModelName:  relayInfo.OriginModelName,
		GroupRatio: 1,
	}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)

	assert.True(t, surcharge.IsZero())
	assert.Empty(t, summary.ToolSurchargeItems)
}

func TestCalculateTextToolCallSurchargeMergesSameNameAndPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("claude_web_search_requests", 3)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearch: {CallCount: 2},
			},
		},
	}
	summary := &textQuotaSummary{ModelName: relayInfo.OriginModelName, GroupRatio: 1}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)

	require.Len(t, summary.ToolSurchargeItems, 1)
	assert.Equal(t, dto.BuildInToolWebSearch, summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, 5, summary.ToolSurchargeItems[0].Count)
	assert.Equal(t, 10.0, summary.ToolSurchargeItems[0].Price)
	expected := decimal.NewFromFloat(10.0 * 5 / 1000).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(surcharge), "got %s want %s", surcharge, expected)
}

func TestMergeToolSurchargeItemsSaturatesCountOverflow(t *testing.T) {
	items := []ToolSurchargeItem{
		{Name: "custom_fn", Count: math.MaxInt, Price: 5},
		{Name: "custom_fn", Count: 1, Price: 5},
	}

	merged := mergeToolSurchargeItems(items)

	require.Len(t, merged, 1)
	assert.Equal(t, math.MaxInt, merged[0].Count)
}

// A zero-token request (e.g. /v1/alpha/search returns no usage) must still
// bill a tool-call surcharge. Regression for the TotalTokens==0 gate zeroing
// out the surcharge quota.
func TestCalculateTextQuotaSummaryZeroTokensStillBillsToolSurcharge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "o1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {CallCount: 1},
			},
		},
	}
	relayInfo.PriceData.GroupRatioInfo.GroupRatio = 1

	usage := &dto.Usage{} // zero tokens, mirrors alpha search
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 0, summary.TotalTokens)
	assert.False(t, summary.ToolCallSurchargeQuota.IsZero(), "surcharge should be computed")
	assert.Greater(t, summary.Quota, 0, "quota must not be zeroed out for a zero-token web search request")
	expected := common.QuotaFromDecimal(summary.ToolCallSurchargeQuota)
	assert.Equal(t, expected, summary.Quota)
}

func TestCalculateTextQuotaSummaryDoesNotApplyRequestMultipliersToToolSurcharge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "o1",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {CallCount: 1},
			},
		},
	}
	relayInfo.PriceData.AddOtherRatio("n", 3)

	summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{})

	expected := decimal.NewFromFloat(10.0 / 1000).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(summary.ToolCallSurchargeQuota))
	assert.Equal(t, common.QuotaFromDecimal(expected), summary.Quota)
}

func TestCalculateTextToolCallSurchargeGeminiGoogleSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("gemini_google_search_call", true)

	relayInfo := &relaycommon.RelayInfo{OriginModelName: "gemini-2.5-flash"}
	summary := &textQuotaSummary{ModelName: "gemini-2.5-flash", GroupRatio: 1}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)
	expected := decimal.NewFromFloat(14.0 / 1000).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(surcharge), "got %s want %s", surcharge, expected)
	require.Len(t, summary.ToolSurchargeItems, 1)
	assert.Equal(t, dto.BuildInToolGoogleSearch, summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, 1, summary.ToolSurchargeItems[0].Count)
	assert.Equal(t, 14.0, summary.ToolSurchargeItems[0].Price)
}

func TestCalculateTextToolCallSurchargeGeminiFunctionCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	operation_setting.SetToolPriceForTest("gemini_surcharge_fn", 5.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("gemini_surcharge_fn")
	})

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				"gemini_surcharge_fn": {CallCount: 2},
			},
		},
	}
	summary := &textQuotaSummary{ModelName: "gemini-2.5-flash", GroupRatio: 1}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)
	expected := decimal.NewFromFloat(5.0 * 2 / 1000).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(surcharge), "got %s want %s", surcharge, expected)
	require.Len(t, summary.ToolSurchargeItems, 1)
	assert.Equal(t, "gemini_surcharge_fn", summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, 2, summary.ToolSurchargeItems[0].Count)
	assert.Equal(t, 5.0, summary.ToolSurchargeItems[0].Price)

	other := model.NewLogOther()
	appendToolSurchargeLogInfo(other, summary.ToolSurchargeItems)
	assert.Equal(t, summary.ToolSurchargeItems, other.Snapshot()["tool_surcharges"])
}

func TestCalculateTextToolCallSurchargeImageGenerationDefaultPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest(dto.BuildInToolImageGeneration)
	})

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolImageGeneration: {CallCount: 2},
			},
		},
	}
	summary := &textQuotaSummary{ModelName: "gpt-5.1", GroupRatio: 1.5}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)
	expected := decimal.NewFromFloat(150.0).
		Mul(decimal.NewFromInt(2)).
		Div(decimal.NewFromInt(1000)).
		Mul(decimal.NewFromFloat(1.5)).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expected.Equal(surcharge), "got %s want %s", surcharge, expected)
	require.Len(t, summary.ToolSurchargeItems, 1)
	assert.Equal(t, dto.BuildInToolImageGeneration, summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, 2, summary.ToolSurchargeItems[0].Count)
	assert.Equal(t, 150.0, summary.ToolSurchargeItems[0].Price)
}

func TestCalculateTextToolCallSurchargeImageGenerationExplicitZeroDisables(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	operation_setting.SetToolPriceForTest(dto.BuildInToolImageGeneration, 0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest(dto.BuildInToolImageGeneration)
	})

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolImageGeneration: {CallCount: 3},
			},
		},
	}
	summary := &textQuotaSummary{ModelName: "gpt-5.1", GroupRatio: 1}

	surcharge := calculateTextToolCallSurcharge(ctx, relayInfo, summary)
	assert.True(t, surcharge.IsZero())
	assert.Empty(t, summary.ToolSurchargeItems)
}

func TestCalculateTextQuotaSummaryImageGenerationUsesStructuredSurcharge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest(dto.BuildInToolImageGeneration)
	})

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolImageGeneration: {CallCount: 1},
			},
		},
	}
	relayInfo.PriceData.GroupRatioInfo.GroupRatio = 1
	relayInfo.PriceData.ModelRatio = 1
	relayInfo.PriceData.CompletionRatio = 1

	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Len(t, summary.ToolSurchargeItems, 1)
	assert.Equal(t, dto.BuildInToolImageGeneration, summary.ToolSurchargeItems[0].Name)
	assert.Equal(t, 1, summary.ToolSurchargeItems[0].Count)
	assert.Equal(t, 150.0, summary.ToolSurchargeItems[0].Price)

	expectedSurcharge := decimal.NewFromFloat(150.0 / 1000).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	assert.True(t, expectedSurcharge.Equal(summary.ToolCallSurchargeQuota),
		"got %s want %s", summary.ToolCallSurchargeQuota, expectedSurcharge)
	assert.Greater(t, summary.Quota, 0)
}

func TestAppendToolSurchargeLogInfoWritesOnlyStructuredFields(t *testing.T) {
	items := []ToolSurchargeItem{
		{Name: dto.BuildInToolWebSearch, Count: 2, Price: 10},
		{Name: dto.BuildInToolImageGeneration, Count: 1, Price: 150},
	}
	other := model.NewLogOther()

	appendToolSurchargeLogInfo(other, items)

	fields := other.Snapshot()
	assert.Equal(t, items, fields["tool_surcharges"])
	assert.NotContains(t, fields, "web_search")
	assert.NotContains(t, fields, "web_search_call_count")
	assert.NotContains(t, fields, "web_search_price")
	assert.NotContains(t, fields, "file_search")
	assert.NotContains(t, fields, "image_generation_call")
	assert.NotContains(t, fields, "image_generation_call_price")
}

func TestGetUsageStatsCacheCaliberFallsBackToUpstream(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	original := setting.UsageStatsCacheCaliber
	t.Cleanup(func() { setting.UsageStatsCacheCaliber = original })

	setting.UsageStatsCacheCaliber = operation_setting.StatsCacheCaliberExcludeCache
	assert.Equal(t, operation_setting.StatsCacheCaliberExcludeCache, operation_setting.GetUsageStatsCacheCaliber())

	setting.UsageStatsCacheCaliber = operation_setting.StatsCacheCaliberIncludeCache
	assert.Equal(t, operation_setting.StatsCacheCaliberIncludeCache, operation_setting.GetUsageStatsCacheCaliber())

	// 非法存储值不信任，读取时按 upstream 处理
	setting.UsageStatsCacheCaliber = "bogus"
	assert.Equal(t, operation_setting.StatsCacheCaliberUpstream, operation_setting.GetUsageStatsCacheCaliber())

	setting.UsageStatsCacheCaliber = ""
	assert.Equal(t, operation_setting.StatsCacheCaliberUpstream, operation_setting.GetUsageStatsCacheCaliber())
}

func TestNormalizeLogPromptTokens(t *testing.T) {
	// cacheRead=100；cacheWrite 取 checkedCacheWriteTokensTotal：5m+1h=60 > 50 → 60。
	// 缓存合计 = 160。
	inclusive := textQuotaSummary{
		PromptTokens:          1000,
		CacheTokens:           100,
		CacheCreationTokens:   50,
		CacheCreationTokens5m: 20,
		CacheCreationTokens1h: 40,
		InputExcludesCache:    false,
	}
	exclusive := inclusive
	exclusive.InputExcludesCache = true

	zeroCache := textQuotaSummary{PromptTokens: 1000, InputExcludesCache: false}

	underflow := inclusive
	underflow.PromptTokens = 100 // 100 < 160，减法越界

	// 拆分合计小于总量时回退扁平 cache_creation_tokens：cacheWrite=80，缓存合计=180
	flatLarger := inclusive
	flatLarger.CacheCreationTokens = 80

	tests := []struct {
		name             string
		summary          textQuotaSummary
		target           string
		wantPromptTokens int
		wantApplied      bool
		wantSkipReason   string
	}{
		{"exclude from inclusive subtracts cache", inclusive, operation_setting.StatsCacheCaliberExcludeCache, 840, true, ""},
		{"exclude from exclusive is no-op", exclusive, operation_setting.StatsCacheCaliberExcludeCache, 1000, false, "already_matches_target"},
		{"include from exclusive adds cache", exclusive, operation_setting.StatsCacheCaliberIncludeCache, 1160, true, ""},
		{"include from inclusive is no-op", inclusive, operation_setting.StatsCacheCaliberIncludeCache, 1000, false, "already_matches_target"},
		{"upstream target is no-op", inclusive, operation_setting.StatsCacheCaliberUpstream, 1000, false, "unsupported_target"},
		{"unknown target is no-op", inclusive, "bogus", 1000, false, "unsupported_target"},
		{"zero cache buckets are no-op both directions", zeroCache, operation_setting.StatsCacheCaliberExcludeCache, 1000, false, "no_cache_tokens"},
		{"subtraction underflow keeps original", underflow, operation_setting.StatsCacheCaliberExcludeCache, 100, false, "prompt_less_than_cache"},
		{"flat creation total larger than split sum", flatLarger, operation_setting.StatsCacheCaliberExcludeCache, 820, true, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTokens, gotApplied, gotSkip := normalizeLogPromptTokens(tc.summary, tc.target)
			assert.Equal(t, tc.wantPromptTokens, gotTokens)
			assert.Equal(t, tc.wantApplied, gotApplied)
			assert.Equal(t, tc.wantSkipReason, gotSkip)
			assert.GreaterOrEqual(t, gotTokens, 0, "normalized output must never be negative")
		})
	}
}
