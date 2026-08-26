package handler

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// BotHandler manages bot accounts owned by the signed-in human user.
type BotHandler struct {
	userRepo *repository.UserRepository
}

// NewBotHandler creates a new BotHandler.
func NewBotHandler() *BotHandler {
	return &BotHandler{userRepo: repository.NewUserRepository()}
}

// Create registers a new bot account owned by the requesting user. The plain
// API token is returned exactly once — only its SHA-256 hash is stored, so a
// lost token can only be replaced (regenerate), never recovered.
func (h *BotHandler) Create(c *gin.Context) {
	ownerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Username string `json:"username" binding:"required"`
		Nickname string `json:"nickname" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !botUsernameRe.MatchString(req.Username) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username must be 3-32 letters, digits or underscores, nickname required"})
		return
	}
	nickname := strings.TrimSpace(req.Nickname)
	if nickname == "" || len([]rune(nickname)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nickname must be 1-100 characters"})
		return
	}

	// Bots must not spawn further bots.
	if repository.RequesterIsBot(ownerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "bot accounts cannot create bots"})
		return
	}

	count, err := repository.CountBotsOwned(ownerID)
	if err == nil && count >= repository.MaxBotsPerOwner {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "bot limit reached",
			"limit": repository.MaxBotsPerOwner,
		})
		return
	}

	bot, err := h.userRepo.CreateBot(req.Username, nickname, ownerID)
	if err != nil {
		if errors.Is(err, repository.ErrUsernameTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "username already taken"})
			return
		}
		logger.Log.Error("failed to create bot", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create bot"})
		return
	}

	// Bots join the default group so they carry ordinary user permissions
	// (chat.send, posts.create, ...) just like humans do.
	_ = repository.NewPermissionRepository().EnsureUserInDefaultGroup(bot.ID)

	token, err := repository.IssueBotToken(bot.ID)
	if err != nil {
		logger.Log.Error("failed to issue bot token", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create bot"})
		return
	}

	logger.Log.Info("bot created", "owner_id", ownerID, "bot_id", bot.ID)
	c.JSON(http.StatusCreated, gin.H{
		"user":  bot,
		"token": token,
	})
}

// List returns the bots owned by the requesting user.
func (h *BotHandler) List(c *gin.Context) {
	ownerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	bots, err := repository.ListBotsByOwner(ownerID)
	if err != nil {
		logger.Log.Error("failed to list bots", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list bots"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"bots":  bots,
		"limit": repository.MaxBotsPerOwner,
	})
}

// Regenerate replaces a bot's API token. The old token stops working
// immediately (single row update), which is also the recovery path for a
// leaked token.
func (h *BotHandler) Regenerate(c *gin.Context) {
	ownerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	botID, ok := h.ownedBotID(c, ownerID)
	if !ok {
		return // response already written
	}
	token, err := repository.IssueBotToken(botID)
	if err != nil {
		logger.Log.Error("failed to regenerate bot token", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to regenerate token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// Delete removes a bot account entirely. Message/post rows cascade with the
// user row, matching how the rest of the system treats account deletion.
func (h *BotHandler) Delete(c *gin.Context) {
	ownerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	botID, ok := h.ownedBotID(c, ownerID)
	if !ok {
		return
	}
	if err := repository.DeleteBot(botID, ownerID); err != nil {
		if errors.Is(err, repository.ErrBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "bot not found"})
			return
		}
		logger.Log.Error("failed to delete bot", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete bot"})
		return
	}
	logger.Log.Info("bot deleted", "owner_id", ownerID, "bot_id", botID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ownedBotID parses :id and verifies the bot belongs to the requester,
// writing the error response itself when anything is off.
func (h *BotHandler) ownedBotID(c *gin.Context, ownerID int64) (int64, bool) {
	botID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || botID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bot id"})
		return 0, false
	}
	err = repository.CheckBotOwnership(botID, ownerID)
	switch {
	case errors.Is(err, repository.ErrBotNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "bot not found"})
		return 0, false
	case errors.Is(err, repository.ErrBotNotOwned):
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bot"})
		return 0, false
	case err != nil:
		logger.Log.Error("failed to load bot", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load bot"})
		return 0, false
	}
	return botID, true
}

// botUsernameRe constrains bot usernames: 3-32 letters, digits, underscores.
var botUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)
