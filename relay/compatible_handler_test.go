package relay

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two audio settlement routes are locked separately on purpose: Chat
// Completions decides from audio tokens plus configured audio ratios, Responses
// decides from the origin model prefix alone. A single shared "has audio token"
// assertion would hide a change to either rule.

func withAudioRatio(t *testing.T, modelName string) {
	t.Helper()
	previous, err := common.Marshal(ratio_setting.GetAudioRatioCopy())
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(fmt.Sprintf(`{%q:16}`, modelName)))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(string(previous)))
	})
}

func usageWithAudio(promptAudio, completionAudio int) *dto.Usage {
	return &dto.Usage{
		PromptTokens:           1000,
		CompletionTokens:       100,
		PromptTokensDetails:    dto.InputTokenDetails{AudioTokens: promptAudio},
		CompletionTokenDetails: dto.OutputTokenDetails{AudioTokens: completionAudio},
	}
}

func TestChatCompletionsSettlesAsAudioOnlyWithTokensAndRatios(t *testing.T) {
	const audioModel = "gpt-4o-audio-preview"
	const plainModel = "gpt-4o"
	withAudioRatio(t, audioModel)

	cases := []struct {
		name      string
		modelName string
		usage     *dto.Usage
		asAudio   bool
	}{
		{
			name:      "prompt audio tokens with a configured ratio",
			modelName: audioModel, usage: usageWithAudio(300, 0), asAudio: true,
		},
		{
			name:      "completion audio tokens with a configured ratio",
			modelName: audioModel, usage: usageWithAudio(0, 25), asAudio: true,
		},
		{
			// The text path then keeps those tokens inside the base output; the
			// normalization itself is pinned by the Recorder usage table.
			name:      "completion audio tokens without any configured ratio",
			modelName: plainModel, usage: usageWithAudio(0, 25), asAudio: false,
		},
		{
			name:      "configured ratio but no audio tokens",
			modelName: audioModel, usage: usageWithAudio(0, 0), asAudio: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{OriginModelName: testCase.modelName}

			assert.Equal(t, testCase.asAudio, settlesAsAudioUsage(info, testCase.usage))
		})
	}
}

// TestChatCompletionsAudioRoutingIsSharedByBothReturnPaths keeps the responses
// backed path and the plain adaptor path on one rule; a divergent copy would
// settle the same reply two different ways.
func TestChatCompletionsAudioRoutingIsSharedByBothReturnPaths(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "compatible_handler.go", nil, 0)
	require.NoError(t, err)

	routed := map[string]int{}
	for _, decl := range parsed.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(function, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "settlesAsAudioUsage" {
				routed[function.Name.Name]++
			}
			return true
		})
	}

	// Both return paths live in TextHelper: the responses backed branch and the
	// plain adaptor tail.
	assert.Equal(t, map[string]int{"TextHelper": 2}, routed)
}

func TestResponsesSettlesAsAudioByModelPrefixAlone(t *testing.T) {
	const plainModel = "gpt-4o"
	withAudioRatio(t, plainModel)

	cases := []struct {
		name      string
		modelName string
		asAudio   bool
	}{
		{name: "audio prefix", modelName: "gpt-4o-audio-preview", asAudio: true},
		{
			// Audio tokens and a configured audio ratio do not move a non prefix
			// model onto the audio path.
			name: "configured audio ratio without the prefix", modelName: plainModel, asAudio: false,
		},
		{name: "unrelated model", modelName: "o3-mini", asAudio: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{OriginModelName: testCase.modelName}

			assert.Equal(t, testCase.asAudio, responsesSettlesAsAudio(info))
		})
	}
}

// TestResponsesCompactAlwaysSettlesAsText pins that the compact mode returns
// through the text settlement before the prefix rule is reached, so a compact
// gpt-4o-audio request keeps its current fixed path.
func TestResponsesCompactAlwaysSettlesAsText(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "responses_handler.go", nil, 0)
	require.NoError(t, err)

	var compact *ast.IfStmt
	ast.Inspect(parsed, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		if binary, ok := branch.Cond.(*ast.BinaryExpr); ok {
			if selector, ok := binary.Y.(*ast.SelectorExpr); ok &&
				selector.Sel.Name == "RelayModeResponsesCompact" {
				compact = branch
			}
		}
		return true
	})
	require.NotNil(t, compact, "the compact settlement branch is gone")

	settlements := map[string]int{}
	returned := false
	ast.Inspect(compact.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				settlements[selector.Sel.Name]++
			}
		}
		if _, ok := node.(*ast.ReturnStmt); ok {
			returned = true
		}
		return true
	})

	assert.Equal(t, 1, settlements["PostTextConsumeQuota"], "compact settles as text")
	assert.Zero(t, settlements["PostAudioConsumeQuota"])
	assert.True(t, returned, "compact must return before the model prefix rule")
}
