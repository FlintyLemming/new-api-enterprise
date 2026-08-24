package setting

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// UserIPCountLimit is the site-wide cap on how many distinct client IPs a
// single user may use within the sliding window on relay APIs. 0 disables the
// global limit; per-user overrides (users.concurrent_ip_limit) can still be
// enabled individually.
var UserIPCountLimit = 0

// UserIPWindowMinutes is the sliding window length for the per-user active IP
// set. An IP that stops appearing for a full window is no longer counted.
var UserIPWindowMinutes = 10

var (
	userIPWhitelistMutex sync.RWMutex
	userIPWhitelist      []string
	userIPWhitelistRaw   string
)

// GetUserIPWindowSeconds returns the sliding window in seconds, falling back
// to the default when misconfigured so the limiter always has a sane window.
func GetUserIPWindowSeconds() int64 {
	minutes := UserIPWindowMinutes
	if minutes <= 0 {
		minutes = 10
	}
	return int64(minutes) * 60
}

// UserIPWhitelistRaw returns the raw whitelist text as configured.
func UserIPWhitelistRaw() string {
	userIPWhitelistMutex.RLock()
	defer userIPWhitelistMutex.RUnlock()
	return userIPWhitelistRaw
}

// ParseUserIPWhitelistEntries normalizes raw whitelist text (newline
// separated, each entry a single IP or CIDR) into a clean entry list.
func ParseUserIPWhitelistEntries(raw string) []string {
	cleaned := strings.ReplaceAll(raw, " ", "")
	entries := make([]string, 0)
	for _, line := range strings.Split(cleaned, "\n") {
		entry := strings.ReplaceAll(strings.TrimSpace(line), ",", "")
		if entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// ValidateUserIPCountLimit rejects non-numeric or negative values. 0 means
// the global limit is disabled.
func ValidateUserIPCountLimit(value string) error {
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return fmt.Errorf("user IP count limit must be a non-negative integer: %q", value)
	}
	return nil
}

// ValidateUserIPWhitelist rejects any line that is neither an IP address nor
// a CIDR block.
func ValidateUserIPWhitelist(raw string) error {
	for _, entry := range ParseUserIPWhitelistEntries(raw) {
		if net.ParseIP(entry) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err != nil {
			return fmt.Errorf("invalid IP whitelist entry %q: must be an IP address or CIDR", entry)
		}
	}
	return nil
}

// UpdateUserIPWhitelist validates and applies raw whitelist text. Invalid
// input leaves the previous whitelist untouched.
func UpdateUserIPWhitelist(raw string) error {
	if err := ValidateUserIPWhitelist(raw); err != nil {
		return err
	}
	userIPWhitelistMutex.Lock()
	defer userIPWhitelistMutex.Unlock()
	userIPWhitelist = ParseUserIPWhitelistEntries(raw)
	userIPWhitelistRaw = raw
	return nil
}

// GetUserIPWhitelist returns a copy of the current whitelist so callers can
// use it without holding the internal lock.
func GetUserIPWhitelist() []string {
	userIPWhitelistMutex.RLock()
	defer userIPWhitelistMutex.RUnlock()
	if len(userIPWhitelist) == 0 {
		return nil
	}
	list := make([]string, len(userIPWhitelist))
	copy(list, userIPWhitelist)
	return list
}
