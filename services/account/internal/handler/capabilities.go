package handler

import (
	"net/http"
	"runtime"

	"github.com/gin-gonic/gin"
)

// CapabilitiesHandler reports the optional features this server build
// understands, so the client can show a compatibility matrix in settings
// and gracefully degrade when a feature is missing on either side.
type CapabilitiesHandler struct {
	version string
}

// NewCapabilitiesHandler returns a handler that emits the static capability
// list. Pass the server version (build-time injected) so the client can pin
// bugs to a specific release.
func NewCapabilitiesHandler(version string) *CapabilitiesHandler {
	return &CapabilitiesHandler{version: version}
}

// ServerCapabilities enumerates the optional features the server supports.
// Add a key here whenever a new optional capability is rolled out so the
// client can detect missing pieces.
var ServerCapabilities = map[string]bool{
	// Auth & identity
	"auth.password_login":    true,
	"auth.oidc_login":        true,
	"user.password_register": true,
	"user.e2ee_key":          true,
	"user.exp_levels":        true,
	"user.daily_bonus":       true,
	"user.adjust_exp":        true,
	// Chat features
	"chat.private_chat":      true,
	"chat.group_chat":        true,
	"chat.public_groups":     true,
	"chat.e2ee":              true,
	"chat.encrypted_private": true,
	"chat.mentions":          true,
	"chat.notify_level":      true,
	"chat.member_titles":     true,
	// Posts
	"posts.create":     true,
	"posts.replies":    true,
	"posts.reactions":  true,
	"posts.favorites":  true,
	"posts.visibility": true,
	// Storage
	"storage.uploads":         true,
	"storage.chunked_uploads": true,
	// Realtime
	"realtime.websocket": true,
}

// Capabilities returns the capability map plus build metadata. Open to
// unauthenticated clients so the settings page can show a comparison before
// the user logs in.
func (h *CapabilitiesHandler) Capabilities(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":      h.version,
		"go_version":   runtime.Version(),
		"capabilities": ServerCapabilities,
	})
}
