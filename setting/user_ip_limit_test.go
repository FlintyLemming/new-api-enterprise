package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetUserIPWindowSeconds(t *testing.T) {
	previous := UserIPWindowMinutes
	t.Cleanup(func() { UserIPWindowMinutes = previous })

	UserIPWindowMinutes = 10
	assert.Equal(t, int64(600), GetUserIPWindowSeconds())

	// Misconfigured values fall back to the documented default instead of
	// producing a zero/negative window in the limiter scripts.
	UserIPWindowMinutes = 0
	assert.Equal(t, int64(600), GetUserIPWindowSeconds())
	UserIPWindowMinutes = -5
	assert.Equal(t, int64(600), GetUserIPWindowSeconds())
}

func TestParseUserIPWhitelistEntries(t *testing.T) {
	testCases := []struct {
		name     string
		raw      string
		expected []string
	}{
		{name: "empty", raw: "", expected: []string{}},
		{name: "blank lines and spaces", raw: " 10.0.0.0/8 \n\n 1.2.3.4 ,", expected: []string{"10.0.0.0/8", "1.2.3.4"}},
		{name: "single entry", raw: "203.0.113.5", expected: []string{"203.0.113.5"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, ParseUserIPWhitelistEntries(testCase.raw))
		})
	}
}

func TestValidateUserIPWhitelist(t *testing.T) {
	assert.NoError(t, ValidateUserIPWhitelist(""))
	assert.NoError(t, ValidateUserIPWhitelist("10.0.0.0/8\n192.168.1.1\n2001:db8::/32"))
	assert.Error(t, ValidateUserIPWhitelist("10.0.0.0/8\nnot-an-ip"))
	assert.Error(t, ValidateUserIPWhitelist("10.0.0.0/33"))
}

func TestUpdateUserIPWhitelistRejectsInvalidAndKeepsPrevious(t *testing.T) {
	previousRaw := UserIPWhitelistRaw()
	t.Cleanup(func() { require.NoError(t, UpdateUserIPWhitelist(previousRaw)) })

	require.NoError(t, UpdateUserIPWhitelist("10.0.0.0/8\n198.51.100.7"))
	assert.Equal(t, []string{"10.0.0.0/8", "198.51.100.7"}, GetUserIPWhitelist())
	assert.Equal(t, "10.0.0.0/8\n198.51.100.7", UserIPWhitelistRaw())

	require.Error(t, UpdateUserIPWhitelist("10.0.0.0/8\nbogus"))
	assert.Equal(t, []string{"10.0.0.0/8", "198.51.100.7"}, GetUserIPWhitelist(), "invalid input must not replace the active whitelist")
}

func TestValidateUserIPCountLimit(t *testing.T) {
	assert.NoError(t, ValidateUserIPCountLimit("0"))
	assert.NoError(t, ValidateUserIPCountLimit("5"))
	assert.Error(t, ValidateUserIPCountLimit("-1"))
	assert.Error(t, ValidateUserIPCountLimit("abc"))
}
