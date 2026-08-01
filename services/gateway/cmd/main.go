package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
)

// serviceTarget maps an API prefix to the internal service base URL.
type serviceTarget struct {
	base       *url.URL
	proxy      *httputil.ReverseProxy
	authLevel  authLevel
	permission string
}

type authLevel int

const (
	authPublic  authLevel = iota // no auth required
	authRequired                 // valid JWT required
	authPermission               // valid JWT + permission required
)

// route is a single gateway routing rule. Patterns use gin-style ":param"
// segments that match any single path segment.
type route struct {
	method     string
	pattern    string
	serviceURL string
	level      authLevel
	permission string
}

// matchRoute selects the most specific route for a method + path.
// Routes with more literal segments win; ties broken by first definition.
func matchRoute(routes []*route, method, path string) *route {
	pathSegs := splitSegments(path)
	var best *route
	bestScore := -1
	for _, r := range routes {
		if r.method != method {
			continue
		}
		patSegs := splitSegments(r.pattern)
		if len(patSegs) != len(pathSegs) {
			continue
		}
		params := 0
		ok := true
		for j := range patSegs {
			if strings.HasPrefix(patSegs[j], ":") {
				params++
			} else if patSegs[j] != pathSegs[j] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		score := (len(patSegs)-params)*1000 + len(patSegs)
		if score > bestScore {
			best = r
			bestScore = score
		}
	}
	return best
}

// splitSegments splits a URL path into non-empty "/" separated segments.
func splitSegments(path string) []string {
	parts := strings.Split(path, "/")
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

func main() {
	logger.Init()

	configPath := os.Getenv("OPENFIELD_CONFIG")
	if configPath == "" {
		configPath = "config/config.local.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	if cfg.Server.Mode != "" {
		gin.SetMode(cfg.Server.Mode)
	}

	if err := database.Connect(cfg); err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close()

	permFactory := middleware.NewPermissionMiddlewareFactory()

	// Routes: HTTP method + API path pattern -> target service + auth requirement.
	// Patterns support ":param" wildcards matched per path segment.
	routes := []*route{
		// ---- auth (public) ----
		{http.MethodGet, "/api/v1/auth/providers", cfg.Services.Account, authPublic, ""},
		{http.MethodGet, "/api/v1/auth/oidc/login", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/oidc/bind", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/auth/oidc/callback", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/login", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/refresh", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/register", cfg.Services.Account, authRequired, ""},

		// ---- account ----
		{http.MethodGet, "/api/v1/users/me/permissions", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/users/me", cfg.Services.Account, authRequired, "account.view"},
		{http.MethodPut, "/api/v1/users/me", cfg.Services.Account, authPermission, "account.profile.edit"},
		{http.MethodPost, "/api/v1/users/me/avatar", cfg.Services.Account, authPermission, "account.avatar.edit"},
		{http.MethodPost, "/api/v1/users/me/banner", cfg.Services.Account, authPermission, "account.banner.edit"},
		{http.MethodGet, "/api/v1/users/search", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/users/:user_id", cfg.Services.Account, authRequired, "account.view"},

		// ---- storage ----
		{http.MethodPost, "/api/v1/attachments", cfg.Services.Storage, authPermission, "storage.upload"},
		{http.MethodGet, "/api/v1/attachments", cfg.Services.Storage, authPermission, "storage.list"},
		{http.MethodGet, "/api/v1/attachments/:id", cfg.Services.Storage, authPermission, "storage.get"},
		{http.MethodDelete, "/api/v1/attachments/:id", cfg.Services.Storage, authPermission, "storage.delete"},
		{http.MethodGet, "/api/v1/storage/usage", cfg.Services.Storage, authPermission, "storage.list"},

		// ---- posts ----
		{http.MethodPost, "/api/v1/posts", cfg.Services.Posts, authPermission, "posts.create"},
		{http.MethodGet, "/api/v1/posts", cfg.Services.Posts, authPermission, "posts.view"},
		{http.MethodGet, "/api/v1/posts/:id", cfg.Services.Posts, authPermission, "posts.view"},
		{http.MethodPut, "/api/v1/posts/:id", cfg.Services.Posts, authPermission, "posts.edit"},
		{http.MethodDelete, "/api/v1/posts/:id", cfg.Services.Posts, authPermission, "posts.delete"},
		{http.MethodGet, "/api/v1/posts/:id/replies", cfg.Services.Posts, authPermission, "posts.view"},
		{http.MethodPost, "/api/v1/posts/:id/replies", cfg.Services.Posts, authPermission, "posts.reply.create"},
		{http.MethodPut, "/api/v1/posts/:id/replies/:reply_id", cfg.Services.Posts, authPermission, "posts.reply.edit"},
		{http.MethodDelete, "/api/v1/posts/:id/replies/:reply_id", cfg.Services.Posts, authPermission, "posts.reply.delete"},
		{http.MethodGet, "/api/v1/users/:user_id/posts", cfg.Services.Posts, authPermission, "posts.view"},

		// ---- chat ----
		{http.MethodGet, "/api/v1/consent-requests", cfg.Services.Chat, authPermission, "chat.request.approve"},
		{http.MethodPost, "/api/v1/consent-requests/:id/accept", cfg.Services.Chat, authPermission, "chat.request.approve"},
		{http.MethodPost, "/api/v1/consent-requests/:id/decline", cfg.Services.Chat, authPermission, "chat.request.approve"},
		{http.MethodGet, "/api/v1/conversations", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations", cfg.Services.Chat, authPermission, "chat.group.create"},
		{http.MethodPost, "/api/v1/conversations/start", cfg.Services.Chat, authPermission, "chat.request.send"},
		{http.MethodGet, "/api/v1/conversations/:id", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/invite", cfg.Services.Chat, authPermission, "chat.group.invite"},
		{http.MethodPut, "/api/v1/conversations/:id/note", cfg.Services.Chat, authPermission, "chat.note.edit"},
		{http.MethodPut, "/api/v1/conversations/:id/group-nickname", cfg.Services.Chat, authPermission, "chat.group.nickname"},
		{http.MethodPost, "/api/v1/conversations/:id/read", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/leave", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodDelete, "/api/v1/conversations/:id/members/:user_id", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodGet, "/api/v1/conversations/:id/messages", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/messages", cfg.Services.Chat, authPermission, "chat.send"},
		{http.MethodPut, "/api/v1/conversations/:id/messages/:message_id", cfg.Services.Chat, authPermission, "chat.edit"},
		{http.MethodDelete, "/api/v1/conversations/:id/messages/:message_id", cfg.Services.Chat, authPermission, "chat.delete"},
	}

	// Build reverse proxies per service.
	proxies := make(map[string]*serviceTarget)
	for _, r := range routes {
		if _, ok := proxies[r.serviceURL]; !ok {
			u, err := url.Parse(r.serviceURL)
			if err != nil {
				log.Fatalf("invalid service URL %q: %v", r.serviceURL, err)
			}
			proxies[r.serviceURL] = &serviceTarget{
				base:  u,
				proxy: httputil.NewSingleHostReverseProxy(u),
			}
		}
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(logger.GinLogger())

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		method := c.Request.Method
		rt := matchRoute(routes, method, path)
		if rt == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		best := proxies[rt.serviceURL]

		// Auth handling.
		userID, authenticated := middleware.GetUserID(c)
		if rt.level != authPublic {
			// Validate JWT.
			header := c.GetHeader("Authorization")
			if header == "" || !strings.HasPrefix(header, "Bearer ") {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
				return
			}
			tokenStr := strings.TrimPrefix(header, "Bearer ")
			id, err := middleware.ParseToken(tokenStr, cfg.JWT.SecretKey)
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
				return
			}
			userID = id
			authenticated = true
		}

		if rt.level == authPermission {
			allowed, err := permFactory.Permit(userID, rt.permission)
			if err != nil {
				logger.Log.Error("permission check failed", "error", err, "user_id", userID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "permission check failed"})
				return
			}
			if !allowed {
				c.JSON(http.StatusForbidden, gin.H{"error": "permission denied", "permission": rt.permission})
				return
			}
		}

		// Forward with the authenticated user id.
		if authenticated {
			c.Request.Header.Set(middleware.UserIDHeader, itoa(userID))
		} else {
			c.Request.Header.Del(middleware.UserIDHeader)
		}
		// Ensure the request path keeps the full /api/v1 prefix.
		best.proxy.ServeHTTP(c.Writer, c.Request)
	})

	addr := ":" + cfg.Server.Port
	logger.Log.Info("gateway starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start gateway: %v", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
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
