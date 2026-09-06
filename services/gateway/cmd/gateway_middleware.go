package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// clientLimiter wraps a token-bucket limiter with a last-seen timestamp so
// idle buckets can be garbage-collected.
type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// rateLimiter is a per-IP token bucket over the whole gateway. The bucket is
// deliberately coarse: it exists to blunt abusive clients and accidental
// loops, not to shape normal traffic. Requests that would exceed the burst
// wait briefly instead of failing — only sustained flooding is rejected
// with 429.
type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*clientLimiter
	perMin   float64
	burst    int
}

// newRateLimiter creates the limiter: default 120 requests/min with a burst
// of 240. Override with OPENFIELD_RATE_LIMIT_RPM / _BURST.
func newRateLimiter() *rateLimiter {
	perMin := 120.0
	burst := 240
	rl := &rateLimiter{
		visitors: make(map[string]*clientLimiter),
		perMin:   perMin,
		burst:    burst,
	}
	go rl.gcLoop()
	return rl
}

// middleware returns the gin middleware enforcing the limit. The real client
// IP comes from gin's ClientIP (honors trusted proxies).
func (rl *rateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		rl.mu.Lock()
		entry, ok := rl.visitors[ip]
		if !ok {
			entry = &clientLimiter{
				limiter: rate.NewLimiter(rate.Limit(rl.perMin/60.0), rl.burst),
			}
			rl.visitors[ip] = entry
		}
		entry.lastSeen = time.Now()
		rl.mu.Unlock()

		// Tokens refill continuously; a short reservation wait absorbs
		// bursts without failing legitimate page loads.
		if err := entry.limiter.Wait(c.Request.Context()); err != nil {
			// Request cancelled while waiting — abort quietly.
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		c.Next()
	}
}

// gcLoop drops buckets idle for over an hour so the map does not grow with
// the client population.
func (rl *rateLimiter) gcLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		for ip, entry := range rl.visitors {
			if time.Since(entry.lastSeen) > time.Hour {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}
