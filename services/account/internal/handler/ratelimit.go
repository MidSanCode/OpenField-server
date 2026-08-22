package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/ratelimit"
)

// Brute-force protection budgets.
//
//   - Login: per-account failures are capped tightly (password spraying one
//     account is the primary online threat); a looser per-IP budget slows
//     wide spraying across many usernames from one host.
//   - Payment PIN: the 6-digit keyspace is small, so only 5 wrong entries are
//     allowed before a lockout. The budget is shared by PIN verification and
//     outgoing transfers because both check the same credential.
const (
	maxLoginFailuresPerAccount = 10
	maxLoginFailuresPerIP      = 100
	maxPinFailuresPerUser      = 5

	loginWindow      = 15 * time.Minute
	loginLockout     = 15 * time.Minute
	pinWindow        = 15 * time.Minute
	pinLockout       = 15 * time.Minute
	pinRetryAfterMsg = "尝试次数过多，请稍后再试"
)

var (
	loginAccountLimiter = ratelimit.New(maxLoginFailuresPerAccount, loginWindow, loginLockout)
	loginIPLimiter      = ratelimit.New(maxLoginFailuresPerIP, loginWindow, loginLockout)
	pinLimiter          = ratelimit.New(maxPinFailuresPerUser, pinWindow, pinLockout)
)

// clientAddress returns the best client address for rate limiting. The gateway
// is the only direct caller and appends the real peer to X-Forwarded-For, so
// the last entry is trusted; earlier entries may be forged by clients.
func clientAddress(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	return c.ClientIP()
}

// pinAttemptKey is the shared limiter key for every payment-PIN check of a
// user, so lockouts from /pin/verify also protect outgoing transfers.
func pinAttemptKey(userID int64) string {
	return "pin:" + itoa64(userID)
}

func itoa64(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// lockedResponse writes the standard 429 body used by every limiter here.
func lockedResponse(c *gin.Context, retryAfter time.Duration) {
	seconds := int(retryAfter/time.Second) + 1
	c.Header("Retry-After", itoa(seconds))
	c.JSON(http.StatusTooManyRequests, gin.H{"error": pinRetryAfterMsg})
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
