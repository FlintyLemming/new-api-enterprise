package langfuse

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeJSONObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(raw, &decoded))
	return decoded
}

func TestSanitizeJSONReplacesBinaryPayloads(t *testing.T) {
	imageData := "data:image/png;base64," + strings.Repeat("A", 1024)
	audioData := "data:audio/wav;base64," + strings.Repeat("B", 512)
	inlineAudio := strings.Repeat("C", 2048)

	raw := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + imageData + `"}},` +
		`{"type":"audio","image":"` + audioData + `"},` +
		`{"type":"input_audio","input_audio":{"data":"` + inlineAudio + `","format":"wav"}}` +
		`]}]}`)

	out, ok := SanitizeJSON(raw, 64*1024)
	require.True(t, ok)

	decoded := decodeJSONObject(t, out)
	content := decoded["messages"].([]any)[0].(map[string]any)["content"].([]any)
	require.Len(t, content, 3)

	image := content[0].(map[string]any)["image_url"].(map[string]any)["url"].(map[string]any)
	assert.Equal(t, map[string]any{
		"_langfuse_redacted": true,
		"media_type":         "image/png",
		"approx_bytes":       float64(len(imageData)),
	}, image)

	audio := content[1].(map[string]any)["image"].(map[string]any)
	assert.Equal(t, map[string]any{
		"_langfuse_redacted": true,
		"media_type":         "audio/wav",
		"approx_bytes":       float64(len(audioData)),
	}, audio)

	inline := content[2].(map[string]any)["input_audio"].(map[string]any)
	assert.Equal(t, "wav", inline["format"], "the sibling format must survive redaction")
	assert.Equal(t, map[string]any{
		"_langfuse_redacted": true,
		"media_type":         "audio/wav",
		"approx_bytes":       float64(len(inlineAudio)),
	}, inline["data"])

	assert.NotContains(t, string(out), "AAAA")
	assert.NotContains(t, string(out), "CCCC")
}

func TestSanitizeJSONReducesInTheStructureLayer(t *testing.T) {
	const limit = 256
	raw := []byte(`{"role":"assistant","alpha":"` + strings.Repeat("a", 400) +
		`","beta":"` + strings.Repeat("b", 400) +
		`","gamma":"` + strings.Repeat("c", 400) + `"}`)

	out, ok := SanitizeJSON(raw, limit)
	require.True(t, ok)

	decoded := decodeJSONObject(t, out)
	assert.LessOrEqual(t, jsonEscapedLen(string(out)), limit)
	assert.Equal(t, true, decoded["_langfuse_truncated"])
	assert.Greater(t, decoded["_langfuse_omitted_bytes"], float64(0))
	assert.NotContains(t, string(out), strings.Repeat("a", 100))
}

func TestSanitizeJSONDropsTrailingElementsOfATopLevelArray(t *testing.T) {
	const limit = 512
	messages := make([]any, 0, 200)
	for i := 0; i < 200; i++ {
		messages = append(messages, map[string]any{"role": "user", "content": strings.Repeat("x", 60)})
	}
	raw, err := common.Marshal(messages)
	require.NoError(t, err)

	out, ok := SanitizeJSON(raw, limit)
	require.True(t, ok)

	var decoded []any
	require.NoError(t, common.Unmarshal(out, &decoded))
	assert.LessOrEqual(t, jsonEscapedLen(string(out)), limit)
	assert.Less(t, len(decoded), 200, "trailing elements must be dropped from the tail")

	marker := decoded[len(decoded)-1].(map[string]any)
	assert.Equal(t, true, marker["_langfuse_truncated"])
	assert.Greater(t, marker["_langfuse_omitted_bytes"], float64(0))
}

func TestSanitizeJSONHandlesWorstCaseEscaping(t *testing.T) {
	const limit = 4096
	hostile := strings.Repeat(`"\-他`+"\n", 3000)
	payload := map[string]any{
		"role":    "assistant",
		"content": []any{hostile, hostile, hostile},
		"trace":   map[string]any{"note": hostile},
	}
	raw, err := common.Marshal(payload)
	require.NoError(t, err)
	require.Greater(t, len(raw), 64*1024)

	out, ok := SanitizeJSON(raw, limit)
	require.True(t, ok)

	var decoded any
	require.NoError(t, common.Unmarshal(out, &decoded), "the reduced document must stay parsable")
	assert.LessOrEqual(t, jsonEscapedLen(string(out)), limit,
		"the escaped contribution, not the raw byte length, is the budget")
}

func TestSanitizeJSONFallsBackToTheTruncationEnvelope(t *testing.T) {
	raw, err := common.Marshal(strings.Repeat("a", 100000))
	require.NoError(t, err)

	out, ok := SanitizeJSON(raw, 4096)
	require.True(t, ok)
	assert.Equal(t, TruncationEnvelope(len(raw)), out)

	decoded := decodeJSONObject(t, out)
	assert.Equal(t, true, decoded["_langfuse_truncated"])
	assert.Equal(t, float64(len(raw)), decoded["_langfuse_original_bytes"])
}

func TestSanitizeJSONRejectsUnparsableInput(t *testing.T) {
	out, ok := SanitizeJSON([]byte(`{"broken":`), 4096)
	assert.Nil(t, out)
	assert.False(t, ok)
}

func TestSanitizeRawTextTruncatesOnUTF8Boundaries(t *testing.T) {
	const limit = 128
	text := []byte(strings.Repeat("他们好", 200))

	out := SanitizeRawText(text, limit)
	assert.True(t, utf8.Valid(out), "raw text truncation must stay on UTF-8 boundaries")
	assert.LessOrEqual(t, jsonEscapedLen(string(out)), limit)
	assert.Contains(t, string(out), "_langfuse_omitted:")
	assert.Contains(t, string(out), "bytes_")

	short := []byte("plain answer")
	assert.Equal(t, short, SanitizeRawText(short, limit))
}

func TestRedactSSEPayloadKeepsTheEventShape(t *testing.T) {
	image := "data:image/jpeg;base64," + strings.Repeat("Z", 256)
	payload := []byte(`data: {"choices":[{"index":0,"delta":{"content":"` + image + `"}}]}`)

	out, ok := RedactSSEPayload(payload)
	require.True(t, ok)

	decoded := decodeJSONObject(t, out)
	choice := decoded["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, float64(0), choice["index"])
	assert.Equal(t, map[string]any{
		"_langfuse_redacted": true,
		"media_type":         "image/jpeg",
		"approx_bytes":       float64(len(image)),
	}, choice["delta"].(map[string]any)["content"])

	unparsable := []byte("data: [DONE]")
	out, ok = RedactSSEPayload(unparsable)
	assert.False(t, ok)
	assert.Equal(t, unparsable, out)
}

func TestJSONEscapedLenCountsTheAttributeContribution(t *testing.T) {
	assert.Equal(t, 5, jsonEscapedLen("abc"))
	assert.Equal(t, 4, jsonEscapedLen(`"`), "a quote costs two bytes plus the delimiters")
	assert.Equal(t, 4, jsonEscapedLen(`\`))
	assert.Equal(t, 6, jsonEscapedLen("\n\t"), "control characters cost two bytes each")
	assert.Equal(t, 8, jsonEscapedLen("\x01"), "rare control characters cost six bytes as a unicode escape")
}
