package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setUserIpLimitSettings(t *testing.T, limit, windowMinutes int, whitelist string) {
	t.Helper()
	previousLimit := setting.UserIPCountLimit
	previousWindow := setting.UserIPWindowMinutes
	previousWhitelist := setting.UserIPWhitelistRaw()
	require.NoError(t, setting.UpdateUserIPWhitelist(whitelist))
	setting.UserIPCountLimit = limit
	setting.UserIPWindowMinutes = windowMinutes
	t.Cleanup(func() {
		setting.UserIPCountLimit = previousLimit
		setting.UserIPWindowMinutes = previousWindow
		require.NoError(t, setting.UpdateUserIPWhitelist(previousWhitelist))
	})
}

func resetMemoryUserIpLimiter() {
	memoryUserIpLimiter.mutex.Lock()
	memoryUserIpLimiter.active = nil
	memoryUserIpLimiter.cleanup = 0
	memoryUserIpLimiter.mutex.Unlock()
}

func newUserIpLimitRouter(t *testing.T) *gin.Engine {
	t.Helper()
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	return router
}

func addUserIpLimitRoute(router *gin.Engine, path string, userID int, override int) {
	router.GET(path, func(c *gin.Context) {
		c.Set("id", userID)
		if override != 0 {
			common.SetContextKey(c, constant.ContextKeyUserConcurrentIpLimit, override)
		}
		c.Next()
	}, UserIpLimit(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
}

func performUserIpLimitRequest(router http.Handler, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestRedisUserIpLimitTakeSlidingWindow(t *testing.T) {
	useRateLimitMiniRedis(t)
	const (
		userID = 7
		window = int64(600)
		limit  = 2
	)
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Unix()

	testCases := []struct {
		name     string
		now      int64
		clientIP string
		allowed  bool
		count    int64
	}{
		{name: "first ip admitted", now: base, clientIP: "192.0.2.10", allowed: true, count: 1},
		{name: "second ip admitted", now: base + 60, clientIP: "192.0.2.11", allowed: true, count: 2},
		{name: "known ip stays admitted and refreshes its window", now: base + 120, clientIP: "192.0.2.10", allowed: true, count: 2},
		{name: "third distinct ip rejected at capacity", now: base + 180, clientIP: "192.0.2.12", allowed: false, count: 2},
		// ip 192.0.2.10 was last seen at base+120 and 192.0.2.11 at base+60,
		// so a full window after the later refresh both have slid out.
		{name: "ips expire after full window so new ip fits", now: base + window + 121, clientIP: "192.0.2.12", allowed: true, count: 1},
		{name: "another new ip fits in the emptied window", now: base + window + 122, clientIP: "192.0.2.13", allowed: true, count: 2},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			allowed, count, err := redisUserIpLimitTake(context.Background(), userID, testCase.clientIP, limit, window, testCase.now)
			require.NoError(t, err)
			assert.Equal(t, testCase.allowed, allowed)
			assert.Equal(t, testCase.count, count)
		})
	}
}

func TestInMemoryUserIpLimitSlidingWindow(t *testing.T) {
	resetMemoryUserIpLimiter()
	const (
		userID = 7
		window = int64(600)
		limit  = 2
	)
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Unix()

	assert.True(t, memoryUserIpLimiter.checkAt(base, userID, "192.0.2.10", limit, window))
	assert.True(t, memoryUserIpLimiter.checkAt(base+60, userID, "192.0.2.11", limit, window))
	assert.True(t, memoryUserIpLimiter.checkAt(base+120, userID, "192.0.2.10", limit, window), "a known IP must not consume an extra slot")
	assert.False(t, memoryUserIpLimiter.checkAt(base+180, userID, "192.0.2.12", limit, window))
	// A full window after the last refresh of 192.0.2.10 slides both IPs out.
	assert.True(t, memoryUserIpLimiter.checkAt(base+window+121, userID, "192.0.2.12", limit, window))
	assert.True(t, memoryUserIpLimiter.checkAt(base+window+122, userID, "192.0.2.13", limit, window))
}

func TestUserIpLimitMiddlewareRedisEnforcement(t *testing.T) {
	useRateLimitMiniRedis(t)
	setUserIpLimitSettings(t, 2, 10, "")
	resetMemoryUserIpLimiter()
	router := newUserIpLimitRouter(t)
	addUserIpLimitRoute(router, "/relay", 42, 0)

	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.10:12345").Code)
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.11:12345").Code)
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.10:12345").Code)

	rejected := performUserIpLimitRequest(router, "/relay", "192.0.2.12:12345")
	assert.Equal(t, http.StatusForbidden, rejected.Code)
	assert.Contains(t, rejected.Body.String(), "access_denied")
}

func TestUserIpLimitMiddlewareWhitelistAndExemption(t *testing.T) {
	useRateLimitMiniRedis(t)
	setUserIpLimitSettings(t, 1, 10, "10.0.0.0/8\n198.51.100.7")
	resetMemoryUserIpLimiter()
	router := newUserIpLimitRouter(t)
	addUserIpLimitRoute(router, "/relay", 43, 0)

	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.10:12345").Code)
	// Whitelisted IPs never count against the limit.
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "10.1.2.3:12345").Code)
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "198.51.100.7:12345").Code)
	assert.Equal(t, http.StatusForbidden, performUserIpLimitRequest(router, "/relay", "192.0.2.11:12345").Code)
	// The exact whitelist IP must not be rejected even at capacity.
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "198.51.100.7:12345").Code)

	count, err := common.RDB.ZCard(context.Background(), redisUserIpLimitKey(43)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "whitelisted IPs must not be recorded")
}

func TestUserIpLimitMiddlewarePerUserOverride(t *testing.T) {
	useRateLimitMiniRedis(t)
	setUserIpLimitSettings(t, 0, 10, "")
	resetMemoryUserIpLimiter()
	router := newUserIpLimitRouter(t)
	addUserIpLimitRoute(router, "/relay", 44, 0)
	addUserIpLimitRoute(router, "/override", 44, 1)

	// Global limit disabled, so /relay must not enforce anything.
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.10:12345").Code)
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.11:12345").Code)
	// The per-user override enables enforcement for the same user.
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/override", "192.0.2.10:12345").Code)
	assert.Equal(t, http.StatusForbidden, performUserIpLimitRequest(router, "/override", "192.0.2.11:12345").Code)
}

func TestUserIpLimitMiddlewareExemptUser(t *testing.T) {
	useRateLimitMiniRedis(t)
	setUserIpLimitSettings(t, 1, 10, "")
	resetMemoryUserIpLimiter()
	router := newUserIpLimitRouter(t)
	addUserIpLimitRoute(router, "/relay", 45, 0)
	addUserIpLimitRoute(router, "/exempt", 45, -1)

	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/relay", "192.0.2.10:12345").Code)
	assert.Equal(t, http.StatusForbidden, performUserIpLimitRequest(router, "/relay", "192.0.2.11:12345").Code)
	countBefore, err := common.RDB.ZCard(context.Background(), redisUserIpLimitKey(45)).Result()
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/exempt", "192.0.2.11:12345").Code)
	assert.Equal(t, http.StatusNoContent, performUserIpLimitRequest(router, "/exempt", "192.0.2.12:12345").Code)

	countAfter, err := common.RDB.ZCard(context.Background(), redisUserIpLimitKey(45)).Result()
	require.NoError(t, err)
	assert.Equal(t, countBefore, countAfter, "exempt requests must not record or remove IPs")
}

func TestUserIpLimitMiddlewareRedisFailure(t *testing.T) {
	_, redisClient := useRateLimitMiniRedis(t)
	setUserIpLimitSettings(t, 1, 10, "")
	resetMemoryUserIpLimiter()
	router := newUserIpLimitRouter(t)
	addUserIpLimitRoute(router, "/relay", 46, 0)
	require.NoError(t, redisClient.Close())

	response := performUserIpLimitRequest(router, "/relay", "192.0.2.10:12345")
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Empty(t, response.Body.String())
}

func TestClearUserIpLimitRecords(t *testing.T) {
	useRateLimitMiniRedis(t)
	resetMemoryUserIpLimiter()
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Unix()

	// Populate both backends with one active IP for the same user.
	allowed, _, err := redisUserIpLimitTake(context.Background(), 47, "192.0.2.10", 2, 600, base)
	require.NoError(t, err)
	require.True(t, allowed)
	require.True(t, memoryUserIpLimiter.checkAt(base, 47, "192.0.2.10", 2, 600))

	require.NoError(t, ClearUserIpLimitRecords(context.Background(), 47))

	// Redis set gone: the previously-rejected second IP is admitted at limit 1.
	allowed, _, err = redisUserIpLimitTake(context.Background(), 47, "192.0.2.11", 1, 600, base+1)
	require.NoError(t, err)
	assert.True(t, allowed, "clearing must reset the Redis window")
	// Memory state gone: a new IP is admitted even though the old one filled the window.
	assert.True(t, memoryUserIpLimiter.checkAt(base+1, 47, "192.0.2.11", 1, 600), "clearing must reset the in-memory window")

	require.Error(t, ClearUserIpLimitRecords(context.Background(), 0))
}
