package main

import (
	"net"
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

// clientKey returns the bucket key for a request: the real TCP peer address,
// never a value taken from a client-supplied header.
//
// c.ClientIP() is only trustworthy once trusted proxies are configured, and
// even then it reflects the left-most X-Forwarded-For entry when the peer is
// trusted — which is exactly the entry a client can forge when the gateway is
// exposed directly. Keying the limiter on the socket peer keeps the bucket
// unspoofable; a legitimate reverse proxy in front of the gateway collapses
// all clients onto one bucket, which is why deployments behind a proxy should
// set server.trusted_proxies and let resolveClientIP handle XFF.
func (rl *rateLimiter) clientKey(c *gin.Context) string {
	if c.Request == nil {
		return "unknown"
	}
	return gatewayPeerIP(c.Request)
}

// gatewayPeerIP extracts the remote address of the TCP peer without consulting
// any header.
func gatewayPeerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// middleware returns the gin middleware enforcing the limit. The bucket key is
// the real TCP peer address.
func (rl *rateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := rl.clientKey(c)
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
