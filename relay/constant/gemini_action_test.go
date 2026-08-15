package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyGeminiAction(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		modelName string
		expected  GeminiAction
	}{
		{
			name:      "generate content",
			path:      "/v1beta/models/gemini-2.5-flash:generateContent",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionGenerate,
		},
		{
			name:      "stream generate content",
			path:      "/v1beta/models/gemini-2.5-flash:streamGenerateContent",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionGenerate,
		},
		{
			name:      "embed content with text-embedding model",
			path:      "/v1beta/models/text-embedding-004:embedContent",
			modelName: "text-embedding-004",
			expected:  GeminiActionEmbedding,
		},
		{
			name:      "batch embed contents with embedding model prefix",
			path:      "/v1beta/models/x:batchEmbedContents",
			modelName: "embedding-custom",
			expected:  GeminiActionEmbedding,
		},
		{
			name:      "imagen predict",
			path:      "/v1beta/models/imagen-3:predict",
			modelName: "imagen-3",
			expected:  GeminiActionPredict,
		},
		{
			name:      "mapped embedding model wins over generate path",
			path:      "/v1beta/models/gemini-2.5-flash:generateContent",
			modelName: "text-embedding-004",
			expected:  GeminiActionEmbedding,
		},
		{
			name:      "gemini-embedding prefix wins over generate path",
			path:      "/v1beta/models/gemini-2.5-flash:generateContent",
			modelName: "gemini-embedding-001",
			expected:  GeminiActionEmbedding,
		},
		{
			name:      "imagen prefix wins over generate path",
			path:      "/v1beta/models/gemini-2.5-flash:generateContent",
			modelName: "imagen-3",
			expected:  GeminiActionPredict,
		},
		{
			name:      "engines embeddings path without action is unknown",
			path:      "/v1/engines/gemini-2.5-flash/embeddings",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "engines embeddings path with embedding model",
			path:      "/v1/engines/text-embedding-004/embeddings",
			modelName: "text-embedding-004",
			expected:  GeminiActionEmbedding,
		},
		{
			name:      "unrecognized action",
			path:      "/v1beta/models/gemini-2.5-flash:countTokens",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "query string is stripped from action",
			path:      "/v1beta/models/gemini-2.5-flash:generateContent?alt=sse",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionGenerate,
		},
		{
			name:      "query string is stripped from unknown action",
			path:      "/v1beta/models/gemini-2.5-flash:countTokens?alt=sse",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "colon inside query string is not an action",
			path:      "/v1beta/models/gemini-2.5-flash:generateContent?ts=10:30",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionGenerate,
		},
		{
			name:      "empty path and model",
			path:      "",
			modelName: "",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "empty path with generate model",
			path:      "",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "root path with generate model",
			path:      "/",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "root path with empty model",
			path:      "/",
			modelName: "",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "colon in earlier segment is not an action",
			path:      "/v1beta/models:weird/gemini-2.5-flash",
			modelName: "gemini-2.5-flash",
			expected:  GeminiActionUnknown,
		},
		{
			name:      "predict action without imagen model",
			path:      "/v1beta/models/some-model:predict",
			modelName: "some-model",
			expected:  GeminiActionPredict,
		},
		{
			name:      "embed content action without embedding model prefix",
			path:      "/v1beta/models/some-model:embedContent",
			modelName: "some-model",
			expected:  GeminiActionEmbedding,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, ClassifyGeminiAction(tc.path, tc.modelName))
		})
	}
}
