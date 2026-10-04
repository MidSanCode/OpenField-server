package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/ratelimit"
	"github.com/openfield/server/pkg/security"
)

// securityVerifyPin is a thin alias so the guard below reads as one unit.
func securityVerifyPin(pin, hash string) bool { return security.VerifyPin(pin, hash) }

// Payment-PIN brute-force protection for the tip path.
//
// The payment PIN is 6 digits, so only a handful of wrong entries may be
// tolerated before a lockout. The account service keeps its own limiter for
// the same credential; this one exists because the posts service verifies the
// PIN locally and cannot share that process's state. The budgets and window
// deliberately match services/account/internal/handler/ratelimit.go so one
// credential does not have a weaker guard on one endpoint than another.
const (
	maxPinFailuresPerUser = 5
	pinWindow             = 15 * time.Minute
	pinLockout            = 15 * time.Minute
	pinRetryAfterMsg      = "尝试次数过多，请稍后再试"
)

var pinLimiter = ratelimit.New(maxPinFailuresPerUser, pinWindow, pinLockout)

// checkPaymentPin applies the per-user PIN budget and reports whether the
// caller may proceed. Every payment-PIN verification in this service must go
// through it: calling security.VerifyPin directly allows unlimited guesses
// against a 10^6 keyspace. When it returns false the response is already sent.
func checkPaymentPin(c *gin.Context, userID int64, pin, pinHash string) bool {
	key := "pin:" + itoa64(userID)
	if retry := pinLimiter.RetryAfter(key); retry > 0 {
		c.Header("Retry-After", itoa(int(retry/time.Second)+1))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": pinRetryAfterMsg})
		return false
	}
	if pinHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "payment pin not set"})
		return false
	}
	if !securityVerifyPin(pin, pinHash) {
		pinLimiter.Fail(key)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid payment pin"})
		return false
	}
	pinLimiter.Reset(key)
	return true
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
