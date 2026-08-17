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
	authPublic     authLevel = iota // no auth required
	authRequired                    // valid JWT required
	authPermission                  // valid JWT + permission required
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

	// The gateway does not own the schema, but it still auto-upgrades when the
	// database is missing required tables/columns (fresh DB, or a new version
	// that added parameters). All DDL is idempotent, so this heals production
	// databases without operator intervention.
	if err := database.RunMigrationsIfEnabled(cfg); err != nil {
		log.Fatalf("failed to run database migrations: %v", err)
	}

	permFactory := middleware.NewPermissionMiddlewareFactory()

	// Routes: HTTP method + API path pattern -> target service + auth requirement.
	// Patterns support ":param" wildcards matched per path segment.
	routes := []*route{
		// ---- auth (public) ----
		{http.MethodGet, "/api/v1/auth/providers", cfg.Services.Account, authPublic, ""},
		{http.MethodGet, "/api/v1/auth/oidc/login", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/oidc/bind", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/auth/oidc/callback", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/oidc/callback", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/login", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/refresh", cfg.Services.Account, authPublic, ""},
		{http.MethodPost, "/api/v1/auth/register", cfg.Services.Account, authRequired, ""},

		// ---- capabilities (public introspection) ----
		{http.MethodGet, "/api/v1/capabilities", cfg.Services.Account, authPublic, ""},

		// ---- account ----
		{http.MethodGet, "/api/v1/users/me/permissions", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/users/me", cfg.Services.Account, authRequired, "account.view"},
		{http.MethodPut, "/api/v1/users/me", cfg.Services.Account, authPermission, "account.profile.edit"},
		{http.MethodPut, "/api/v1/users/me/e2ee-key", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/users/me/avatar", cfg.Services.Account, authPermission, "account.avatar.edit"},
		{http.MethodPost, "/api/v1/users/me/banner", cfg.Services.Account, authPermission, "account.banner.edit"},
		{http.MethodPost, "/api/v1/users/me/claim-daily-bonus", cfg.Services.Account, authRequired, ""},
		{http.MethodPut, "/api/v1/users/me/locale", cfg.Services.Account, authRequired, ""},
		{http.MethodPut, "/api/v1/users/me/privacy", cfg.Services.Account, authRequired, ""},
		{http.MethodPut, "/api/v1/users/me/name-style", cfg.Services.Account, authPermission, "account.profile.edit"},
		{http.MethodPut, "/api/v1/users/me/storage-bucket", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/users/storage-buckets", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/users/me/pin", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/users/me/pin/verify", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/users/search", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/users/:user_id", cfg.Services.Account, authPublic, ""},
		{http.MethodPut, "/api/v1/users/:user_id/exp", cfg.Services.Account, authPermission, "user.adjust_exp"},
		{http.MethodPut, "/api/v1/users/:user_id/membership", cfg.Services.Account, authPermission, "user.membership.grant"},
		{http.MethodPost, "/api/v1/users/:user_id/punishments", cfg.Services.Account, authPermission, "user.punish"},
		{http.MethodGet, "/api/v1/users/:user_id/punishments", cfg.Services.Account, authPermission, "user.punish"},

		// ---- membership ----
		{http.MethodGet, "/api/v1/membership", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/membership/purchase", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/membership/purchases", cfg.Services.Account, authRequired, ""},

		// ---- storage ----
		{http.MethodPost, "/api/v1/attachments", cfg.Services.Storage, authPermission, "storage.upload"},
		{http.MethodGet, "/api/v1/attachments", cfg.Services.Storage, authPermission, "storage.list"},
		{http.MethodGet, "/api/v1/attachments/:id", cfg.Services.Storage, authPermission, "storage.get"},
		{http.MethodDelete, "/api/v1/attachments/:id", cfg.Services.Storage, authPermission, "storage.delete"},
		{http.MethodPost, "/api/v1/attachments/chunk/init", cfg.Services.Storage, authPermission, "storage.upload"},
		{http.MethodGet, "/api/v1/attachments/chunk/:upload_id", cfg.Services.Storage, authPermission, "storage.upload"},
		{http.MethodPost, "/api/v1/attachments/chunk/:upload_id/:index", cfg.Services.Storage, authPermission, "storage.upload"},
		{http.MethodPost, "/api/v1/attachments/chunk/:upload_id/complete", cfg.Services.Storage, authPermission, "storage.upload"},
		{http.MethodGet, "/api/v1/storage/usage", cfg.Services.Storage, authPermission, "storage.list"},

		// ---- posts ----
		{http.MethodPost, "/api/v1/posts", cfg.Services.Posts, authPermission, "posts.create"},
		{http.MethodGet, "/api/v1/posts", cfg.Services.Posts, authPublic, ""},
		{http.MethodGet, "/api/v1/posts/:id", cfg.Services.Posts, authPublic, ""},
		{http.MethodPut, "/api/v1/posts/:id", cfg.Services.Posts, authPermission, "posts.edit"},
		{http.MethodDelete, "/api/v1/posts/:id", cfg.Services.Posts, authPermission, "posts.delete"},
		{http.MethodGet, "/api/v1/posts/:id/replies", cfg.Services.Posts, authPublic, ""},
		{http.MethodPost, "/api/v1/posts/:id/replies", cfg.Services.Posts, authPermission, "posts.reply.create"},
		{http.MethodPut, "/api/v1/posts/:id/replies/:reply_id", cfg.Services.Posts, authPermission, "posts.reply.edit"},
		{http.MethodDelete, "/api/v1/posts/:id/replies/:reply_id", cfg.Services.Posts, authPermission, "posts.reply.delete"},
		{http.MethodGet, "/api/v1/users/:user_id/posts", cfg.Services.Posts, authPublic, ""},
		{http.MethodPut, "/api/v1/posts/:id/reactions", cfg.Services.Posts, authPermission, "posts.react"},
		{http.MethodDelete, "/api/v1/posts/:id/reactions", cfg.Services.Posts, authPermission, "posts.react"},
		{http.MethodPost, "/api/v1/posts/:id/favorite", cfg.Services.Posts, authPermission, "posts.favorite"},
		{http.MethodDelete, "/api/v1/posts/:id/favorite", cfg.Services.Posts, authPermission, "posts.favorite"},
		{http.MethodPost, "/api/v1/posts/:id/replies/:reply_id/favorite", cfg.Services.Posts, authPermission, "posts.favorite"},
		{http.MethodDelete, "/api/v1/posts/:id/replies/:reply_id/favorite", cfg.Services.Posts, authPermission, "posts.favorite"},
		{http.MethodGet, "/api/v1/users/:user_id/favorites/posts", cfg.Services.Posts, authRequired, ""},
		{http.MethodGet, "/api/v1/users/:user_id/favorites/replies", cfg.Services.Posts, authRequired, ""},

		// ---- follows ----
		{http.MethodPost, "/api/v1/users/:user_id/follow", cfg.Services.Account, authPermission, "account.follow"},
		{http.MethodDelete, "/api/v1/users/:user_id/follow", cfg.Services.Account, authPermission, "account.follow"},
		{http.MethodGet, "/api/v1/users/:user_id/followers", cfg.Services.Account, authPublic, ""},
		{http.MethodGet, "/api/v1/users/:user_id/following", cfg.Services.Account, authPublic, ""},
		{http.MethodGet, "/api/v1/users/:user_id/friends", cfg.Services.Account, authPublic, ""},

		// ---- wallet ----
		{http.MethodGet, "/api/v1/wallet", cfg.Services.Account, authPermission, "wallet.view"},
		{http.MethodPost, "/api/v1/wallet/adjust", cfg.Services.Account, authPermission, "wallet.manage"},

		// ---- tasks / experience / transfers ----
		{http.MethodGet, "/api/v1/tasks", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/tasks/daily-login/claim", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/tasks/daily-login/makeup", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/tasks/:code/claim", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/exp/history", cfg.Services.Account, authRequired, ""},
		{http.MethodGet, "/api/v1/transfers", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/transfers", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/transfers/:id/accept", cfg.Services.Account, authRequired, ""},
		{http.MethodPost, "/api/v1/transfers/:id/decline", cfg.Services.Account, authRequired, ""},

		// ---- chat ----
		{http.MethodGet, "/api/v1/consent-requests", cfg.Services.Chat, authPermission, "chat.request.approve"},
		{http.MethodPost, "/api/v1/consent-requests/:id/accept", cfg.Services.Chat, authPermission, "chat.request.approve"},
		{http.MethodPost, "/api/v1/consent-requests/:id/decline", cfg.Services.Chat, authPermission, "chat.request.approve"},
		{http.MethodGet, "/api/v1/conversations", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations", cfg.Services.Chat, authPermission, "chat.group.create"},
		{http.MethodPost, "/api/v1/conversations/start", cfg.Services.Chat, authPermission, "chat.request.send"},
		{http.MethodGet, "/api/v1/conversations/public", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodGet, "/api/v1/conversations/:id", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodGet, "/api/v1/conversations/:id/e2ee-keys", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/e2ee-keys", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPost, "/api/v1/conversations/:id/invite", cfg.Services.Chat, authPermission, "chat.group.invite"},
		{http.MethodPut, "/api/v1/conversations/:id/note", cfg.Services.Chat, authPermission, "chat.note.edit"},
		{http.MethodPut, "/api/v1/conversations/:id/group-nickname", cfg.Services.Chat, authPermission, "chat.group.nickname"},
		{http.MethodPut, "/api/v1/conversations/:id/notify-level", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/read", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/typing", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/leave", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPost, "/api/v1/conversations/:id/join", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPut, "/api/v1/conversations/:id/settings", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPut, "/api/v1/conversations/:id/title", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPut, "/api/v1/conversations/:id/avatar", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPost, "/api/v1/conversations/:id/mute-all", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodDelete, "/api/v1/conversations/:id/mute-all", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodDelete, "/api/v1/conversations/:id", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodDelete, "/api/v1/conversations/:id/members/:user_id", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPut, "/api/v1/conversations/:id/members/:user_id/role", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPut, "/api/v1/conversations/:id/members/:user_id/title", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodPost, "/api/v1/conversations/:id/members/:user_id/mute", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodDelete, "/api/v1/conversations/:id/members/:user_id/mute", cfg.Services.Chat, authPermission, "chat.group.manage"},
		{http.MethodGet, "/api/v1/conversations/:id/messages", cfg.Services.Chat, authPermission, "chat.view"},
		{http.MethodPost, "/api/v1/conversations/:id/messages", cfg.Services.Chat, authPermission, "chat.send"},
		{http.MethodPut, "/api/v1/conversations/:id/messages/:message_id", cfg.Services.Chat, authPermission, "chat.edit"},
		{http.MethodDelete, "/api/v1/conversations/:id/messages/:message_id", cfg.Services.Chat, authPermission, "chat.delete"},

		// ---- push (realtime) ----
		{http.MethodGet, "/api/v1/ws", cfg.Services.Push, authRequired, ""},
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
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.Use(middleware.CORS(cfg))

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

		// On public routes, still honor a valid Bearer token so downstream
		// services can personalize reads (e.g. my_reaction, is_following,
		// authenticated view tracking). An invalid/absent token simply leaves
		// the request anonymous.
		if !authenticated {
			if header := c.GetHeader("Authorization"); strings.HasPrefix(header, "Bearer ") {
				if id, err := middleware.ParseToken(strings.TrimPrefix(header, "Bearer "), cfg.JWT.SecretKey); err == nil {
					userID = id
					authenticated = true
				}
			}
		}

		if rt.level != authPublic {
			// Validate JWT. The token normally arrives via the Authorization
			// header, but browsers cannot set custom headers on WebSocket
			// connections, so the realtime client also sends it as a "token"
			// query parameter. Accept either form.
			tokenStr := ""
			if header := c.GetHeader("Authorization"); strings.HasPrefix(header, "Bearer ") {
				tokenStr = strings.TrimPrefix(header, "Bearer ")
			} else if q := c.Query("token"); q != "" {
				tokenStr = q
			}
			if tokenStr == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization token"})
				return
			}
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
