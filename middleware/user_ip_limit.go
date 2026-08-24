package middleware

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

const userIpLimitNamespace = "rateLimit:v2:userIp"

// userIpLimitScript maintains the per-user sliding-window distinct-IP set
// atomically: prune expired members, then decide whether the client IP is
// already known, whether the window is full, or whether it can be admitted.
// A known IP refreshes its score instead of being counted again, so a stable
// IP never consumes extra slots.
const userIpLimitScript = `
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
if redis.call('ZSCORE', KEYS[1], ARGV[4]) ~= false then
  redis.call('ZADD', KEYS[1], now, ARGV[4])
  redis.call('EXPIRE', KEYS[1], window + 1)
  return {1, redis.call('ZCARD', KEYS[1])}
end
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then
  redis.call('EXPIRE', KEYS[1], window + 1)
  return {0, redis.call('ZCARD', KEYS[1])}
end
redis.call('ZADD', KEYS[1], now, ARGV[4])
redis.call('EXPIRE', KEYS[1], window + 1)
return {1, redis.call('ZCARD', KEYS[1])}
`

func redisUserIpLimitKey(userID int) string {
	return fmt.Sprintf("%s:%d", userIpLimitNamespace, userID)
}

// redisUserIpLimitTake records clientIP in the user's sliding-window set and
// reports whether the request is allowed. nowSeconds is injectable so tests
// can drive window expiration deterministically.
func redisUserIpLimitTake(ctx context.Context, userID int, clientIP string, limit int, windowSeconds, nowSeconds int64) (bool, int64, error) {
	values, err := common.RDB.Eval(
		ctx,
		userIpLimitScript,
		[]string{redisUserIpLimitKey(userID)},
		nowSeconds,
		windowSeconds,
		limit,
		clientIP,
	).Slice()
	if err != nil {
		return false, 0, err
	}
	if len(values) != 2 {
		return false, 0, fmt.Errorf("unexpected user IP limit reply length %d", len(values))
	}
	allowedValue, err := redisReplyInteger(values[0])
	if err != nil {
		return false, 0, err
	}
	count, err := redisReplyInteger(values[1])
	if err != nil {
		return false, 0, err
	}
	return allowedValue == 1, count, nil
}

// In-memory fallback (single-instance deployments, no Redis). It mirrors the
// Redis semantics: a sliding window of distinct client IPs per user, pruned
// lazily on every check.
type inMemoryUserIpLimiter struct {
	mutex   sync.Mutex
	active  map[int]map[string]int64 // userID -> ip -> last seen unix seconds
	cleanup int64
}

var memoryUserIpLimiter inMemoryUserIpLimiter

// checkAt admits clientIP if it is already known or the user's window holds
// fewer than maxCount distinct IPs. nowSeconds is injectable for tests.
func (l *inMemoryUserIpLimiter) checkAt(nowSeconds int64, userID int, clientIP string, maxCount int, windowSeconds int64) bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.active == nil {
		l.active = make(map[int]map[string]int64)
	}
	// Lazily prune expired entries so the maps stay bounded by real traffic.
	if nowSeconds-l.cleanup >= windowSeconds {
		l.cleanup = nowSeconds
		for uid, ips := range l.active {
			for ip, seen := range ips {
				if nowSeconds-seen >= windowSeconds {
					delete(ips, ip)
				}
			}
			if len(ips) == 0 {
				delete(l.active, uid)
			}
		}
	}
	ips := l.active[userID]
	if ips == nil {
		ips = make(map[string]int64)
		l.active[userID] = ips
	}
	if _, known := ips[clientIP]; !known {
		if len(ips) >= maxCount {
			return false
		}
	}
	ips[clientIP] = nowSeconds
	return true
}

// ClearUserIpLimitRecords drops the recorded active-IP set for one user on
// both storage backends, used by the admin reset endpoint.
func ClearUserIpLimitRecords(ctx context.Context, userID int) error {
	if userID <= 0 {
		return errors.New("invalid user id")
	}
	memoryUserIpLimiter.mutex.Lock()
	delete(memoryUserIpLimiter.active, userID)
	memoryUserIpLimiter.mutex.Unlock()
	if common.RedisEnabled {
		if err := common.RDB.Del(ctx, redisUserIpLimitKey(userID)).Err(); err != nil {
			return err
		}
	}
	return nil
}

// effectiveUserIpLimit resolves the limit that applies to the authenticated
// user: a non-zero per-user override wins over the site-wide setting, and -1
// exempts the user entirely.
func effectiveUserIpLimit(c *gin.Context) int {
	if override := common.GetContextKeyInt(c, constant.ContextKeyUserConcurrentIpLimit); override != 0 {
		return override
	}
	return setting.UserIPCountLimit
}

// UserIpLimit restricts each authenticated user to at most N distinct client
// IPs within a sliding window on relay API routes. Whitelisted IPs/CIDRs are
// never counted. Must run AFTER TokenAuth (needs the user id in context).
func UserIpLimit() func(c *gin.Context) {
	return func(c *gin.Context) {
		userID := c.GetInt("id")
		limit := effectiveUserIpLimit(c)
		if userID == 0 || limit < 0 {
			c.Next()
			return
		}
		clientIP := c.ClientIP()
		if common.IsIpInCIDRList(net.ParseIP(clientIP), setting.GetUserIPWhitelist()) {
			c.Next()
			return
		}
		if limit == 0 {
			c.Next()
			return
		}
		windowSeconds := setting.GetUserIPWindowSeconds()
		nowSeconds := time.Now().Unix()

		allowed := false
		if common.RedisEnabled {
			var err error
			allowed, _, err = redisUserIpLimitTake(c.Request.Context(), userID, clientIP, limit, windowSeconds, nowSeconds)
			if err != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("user IP limit check failed (user=%d): %v", userID, err))
				c.Status(http.StatusInternalServerError)
				c.Abort()
				return
			}
		} else {
			allowed = memoryUserIpLimiter.checkAt(nowSeconds, userID, clientIP, limit, windowSeconds)
		}

		if !allowed {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("user %d rejected by concurrent IP limit (max=%d, ip=%s)", userID, limit, clientIP))
			abortWithOpenAiMessage(c, http.StatusForbidden,
				common.TranslateMessage(c, i18n.MsgUserIPLimitReached, map[string]any{"Max": limit}),
				types.ErrorCodeAccessDenied)
			return
		}
		c.Next()
	}
}
