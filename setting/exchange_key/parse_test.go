package exchange_key

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-16ch"

func TestParseExchangeKeyAcceptsUsernameAndHexSuffix(t *testing.T) {
	mac, err := hex.DecodeString("4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.NoError(t, err)

	username, got, ok := ParseExchangeKey("sk-alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.True(t, ok)
	assert.Equal(t, "alice", username)
	assert.Equal(t, mac, got)

	username, got, ok = ParseExchangeKey("alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.True(t, ok)
	assert.Equal(t, "alice", username)
	assert.Equal(t, mac, got)
}

func TestParseExchangeKeyKeepsHyphenatedUsername(t *testing.T) {
	username, mac, ok := ParseExchangeKey("sk-foo-bar-998068798a8831c9f16243767b026e2ec3b64245b4adca698c9622622905106f")
	require.True(t, ok)
	assert.Equal(t, "foo-bar", username)
	assert.Len(t, mac, 32)
}

func TestParseExchangeKeyRejectsNonExchangeShapes(t *testing.T) {
	cases := []string{
		"sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sk-alice-deadbeef",
		"sk-alice-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"alice",
		"sk-alice",
		"",
		"sk-" + strings.Repeat("a", 21) + "-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655",
		"sk--4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655",
		"sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-12",
	}
	for _, raw := range cases {
		_, _, ok := ParseExchangeKey(raw)
		assert.False(t, ok, raw)
	}
}

func TestParseExchangeKeyRejectsInvalidUTF8Username(t *testing.T) {
	raw := "sk-" + string([]byte{0xff}) + "-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655"

	_, _, ok := ParseExchangeKey(raw)

	assert.False(t, ok)
}

func TestParseExchangeKeyDoesNotTrimUsername(t *testing.T) {
	_, _, ok := ParseExchangeKey("sk- alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	assert.True(t, ok)
	username, _, ok := ParseExchangeKey("sk- alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.True(t, ok)
	assert.Equal(t, " alice", username)
}

func TestMACEqualAcceptsMixedCaseHexAndRejectsWrongInputs(t *testing.T) {
	lower := "4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655"
	mac, err := hex.DecodeString(strings.ToUpper(lower))
	require.NoError(t, err)
	assert.True(t, MACEqual(testSecret, "alice", mac))
	assert.False(t, MACEqual(testSecret, "bob", mac))
	assert.False(t, MACEqual("other-secret-16ch", "alice", mac))
}
