package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
)

func runCORS(cfg *config.Config, origin string) string {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS(cfg))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Header().Get("Access-Control-Allow-Origin")
}

// An empty allow-list must NOT mean "allow every origin": that made the
// strict-looking configuration (allowed_origins: []) silently permissive.
func TestCORSEmptyListDoesNotAllowArbitraryOrigin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AllowedOrigins = nil
	cfg.Server.AllowAllOrigins = false

	if got := runCORS(cfg, "https://evil.example.com"); got != "" {
		t.Fatalf("empty allow-list echoed %q for an arbitrary origin; want no CORS header", got)
	}

	// Local development origins remain usable so Flutter web dev servers work.
	if got := runCORS(cfg, "http://localhost:54532"); got != "http://localhost:54532" {
		t.Fatalf("localhost origin got %q; want it echoed", got)
	}
}

// Explicit origins are honoured and echoed precisely (never as a wildcard).
func TestCORSExplicitOrigin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AllowedOrigins = []string{"https://app.example.com"}

	if got := runCORS(cfg, "https://app.example.com"); got != "https://app.example.com" {
		t.Fatalf("configured origin got %q; want it echoed", got)
	}
	if got := runCORS(cfg, "https://other.example.com"); got != "" {
		t.Fatalf("unconfigured origin got %q; want no CORS header", got)
	}
}

// Only an explicit opt-in produces the wildcard.
func TestCORSAllowAllOptIn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AllowAllOrigins = true

	if got := runCORS(cfg, "https://anything.example.com"); got != "*" {
		t.Fatalf("allow_all_origins gave %q; want *", got)
	}
}

// HSTS is advertised only on a connection that was actually secure, and the
// X-Forwarded-Proto signal from a TLS-terminating proxy is honoured.
func TestSecurityHeadersHSTS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name     string
		proto    string
		wantHSTS bool
	}{
		{"plain http", "", false},
		{"forwarded https", "https", true},
		{"forwarded https mixed case", "HTTPS", true},
		{"forwarded http", "http", false},
	}
	for _, c := range cases {
		r := gin.New()
		r.Use(SecurityHeaders())
		r.GET("/x", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if c.proto != "" {
			req.Header.Set("X-Forwarded-Proto", c.proto)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		got := w.Header().Get("Strict-Transport-Security")
		if c.wantHSTS && got == "" {
			t.Errorf("%s: no HSTS header", c.name)
		}
		if !c.wantHSTS && got != "" {
			t.Errorf("%s: HSTS sent over an insecure connection: %q", c.name, got)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", c.name)
		}
	}
}
