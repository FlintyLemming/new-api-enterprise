package exchange_key

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

const (
	TokenName        = "exchange-key"
	hmacHexLength    = 64
	hmacByteLength   = 32
	maxUsernameRunes = 20
)

func ParseExchangeKey(raw string) (username string, mac []byte, ok bool) {
	trimmed := strings.TrimPrefix(raw, "sk-")
	idx := strings.LastIndex(trimmed, "-")
	if idx <= 0 {
		return "", nil, false
	}
	username = trimmed[:idx]
	suffix := trimmed[idx+1:]
	if !utf8.ValidString(username) {
		return "", nil, false
	}
	if utf8.RuneCountInString(username) < 1 || utf8.RuneCountInString(username) > maxUsernameRunes {
		return "", nil, false
	}
	if len(suffix) != hmacHexLength {
		return "", nil, false
	}
	mac, err := hex.DecodeString(suffix)
	if err != nil || len(mac) != hmacByteLength {
		return "", nil, false
	}
	return username, mac, true
}

func MACEqual(secret, username string, mac []byte) bool {
	if secret == "" || username == "" || len(mac) != hmacByteLength {
		return false
	}
	expected := hmac.New(sha256.New, []byte(secret))
	_, _ = expected.Write([]byte(username))
	return hmac.Equal(expected.Sum(nil), mac)
}
