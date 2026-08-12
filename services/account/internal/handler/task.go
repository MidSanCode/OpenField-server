package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// TaskHandler handles the tasks list, daily sign-in streak, make-up sign-in and
// experience history.
type TaskHandler struct {
	taskRepo *repository.TaskRepository
	expRepo  *repository.ExpRepository
	gameCfg  config.GameConfig
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler() *TaskHandler {
	return &TaskHandler{
		taskRepo: repository.NewTaskRepository(),
		expRepo:  repository.NewExpRepository(),
	}
}

// SetGameConfig attaches the gameplay config.
func (h *TaskHandler) SetGameConfig(c config.GameConfig) {
	h.gameCfg = c
}

// ListTasks returns the task catalog with the user's progress and the current
// sign-in streak.
func (h *TaskHandler) ListTasks(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	loc := h.gameCfg.Location()
	tasks, streak, _, err := h.taskRepo.ListTasks(userID, loc, time.Now())
	if err != nil {
		logger.Log.Error("failed to list tasks", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tasks"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"tasks":       tasks,
		"streak":      streak,
		"makeup_cost": h.gameCfg.EffectiveMakeupCost(),
	})
}

// ClaimDailyLogin claims the daily sign-in task, granting the day's exp and
// currency and updating the consecutive-day streak.
func (h *TaskHandler) ClaimDailyLogin(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	loc := h.gameCfg.Location()
	expAmt := h.gameCfg.EffectiveDailyBonus()
	curAmt := h.gameCfg.EffectiveDailyCurrency()
	granted, streak, err := h.taskRepo.Checkin(userID, expAmt, curAmt, 0, false, loc, time.Now())
	if err != nil {
		logger.Log.Error("failed to claim daily login", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to claim daily login"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"granted":  granted,
		"exp":      expAmt,
		"currency": curAmt,
		"streak":   streak,
	})
}

// MakeupCheckin performs a paid make-up sign-in: it renews the streak and
// grants exp, but never the daily currency.
func (h *TaskHandler) MakeupCheckin(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	loc := h.gameCfg.Location()
	expAmt := h.gameCfg.EffectiveDailyBonus()
	cost := h.gameCfg.EffectiveMakeupCost()
	granted, streak, err := h.taskRepo.MakeupCheckin(userID, expAmt, cost, loc, time.Now())
	if err != nil {
		if err == repository.ErrInsufficientBalance {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
			return
		}
		if err == repository.ErrMakeupGapTooLarge {
			c.JSON(http.StatusConflict, gin.H{"error": "make-up only covers the last three days"})
			return
		}
		logger.Log.Error("failed to make up checkin", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to make up checkin"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"granted": granted,
		"exp":     expAmt,
		"cost":    cost,
		"streak":  streak,
	})
}

// CheckinCalendar returns the user's sign-in history as a calendar: the date
// range (registration → today), which days are already signed, and today's
// claim status.
func (h *TaskHandler) CheckinCalendar(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	loc := h.gameCfg.Location()
	now := time.Now()

	user, err := h.taskRepo.UserRepo().GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to load user for calendar", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load user"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	signed, err := repository.ListSignedDates(userID)
	if err != nil {
		logger.Log.Error("failed to list signed dates", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list signed dates"})
		return
	}

	today := now.In(loc)
	reg := user.CreatedAt.In(loc)

	// Build the calendar grid: all dates from registration through today.
	type calendarDay struct {
		Date    string `json:"date"`
		Weekday string `json:"weekday"`
		Signed  bool   `json:"signed"`
		IsToday bool   `json:"is_today"`
		DaysAgo int    `json:"days_ago"`
		Before  bool   `json:"before"` // not yet reachable (future)
	}
	signedSet := make(map[string]bool, len(signed))
	for _, d := range signed {
		signedSet[d] = true
	}

	from := time.Date(reg.Year(), reg.Month(), reg.Day(), 0, 0, 0, 0, loc)
	to := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	if to.Before(from) {
		to = from
	}

	todayKey := cycleKeyHelp(to)
	days := make([]calendarDay, 0)
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		key := cycleKeyHelp(d)
		days = append(days, calendarDay{
			Date:    d.Format("2006-01-02"),
			Weekday: d.Format("Mon"),
			Signed:  signedSet[key],
			IsToday: key == todayKey,
			Before:  false,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"registered_at": reg.Format("2006-01-02"),
		"today":         todayKey,
		"signed":        signed,
		"streak":        repository.RecomputeStreak(signed, loc, now),
		"makeup_cost":   h.gameCfg.EffectiveMakeupCost(),
		"makeup_exp":    h.gameCfg.EffectiveDailyBonus(),
		"days":          days,
	})
}

// MakeupByDate pays to sign in a specific missed past calendar day.
func (h *TaskHandler) MakeupByDate(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	loc := h.gameCfg.Location()

	var body struct {
		Date string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Date == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "date is required"})
		return
	}

	expAmt := h.gameCfg.EffectiveDailyBonus()
	cost := h.gameCfg.EffectiveMakeupCost()
	granted, streak, err := h.taskRepo.MakeupByDate(userID, body.Date, expAmt, cost, loc, time.Now())
	if err != nil {
		if err == repository.ErrInsufficientBalance {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
			return
		}
		if err == repository.ErrMakeupToday ||
			err == repository.ErrMakeupFuture ||
			err == repository.ErrMakeupBeforeRegistration {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logger.Log.Error("failed to make up date", "error", err, "user_id", userID, "date", body.Date)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to make up checkin"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"granted":   granted,
		"date":      body.Date,
		"exp":       expAmt,
		"cost":      cost,
		"streak":    streak,
	})
}

func cycleKeyHelp(t time.Time) string {
	return t.Format("2006-01-02")
}

// ClaimOneTime claims a one-time achievement task reward.
func (h *TaskHandler) ClaimOneTime(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	code := c.Param("code")
	expAward, curAward, err := h.taskRepo.ClaimOnce(userID, code)
	if err != nil {
		switch err {
		case repository.ErrNotFound:
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		case repository.ErrNotEligible:
			c.JSON(http.StatusConflict, gin.H{"error": "task requirements not met"})
		case repository.ErrAlreadyClaimed:
			c.JSON(http.StatusConflict, gin.H{"error": "task already claimed"})
		default:
			logger.Log.Error("failed to claim task", "error", err, "code", code)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to claim task"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"claimed": true, "exp": expAward, "currency": curAward})
}

// ListExpHistory returns the user's experience award history, newest first.
func (h *TaskHandler) ListExpHistory(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	entries, err := h.expRepo.List(userID, limit)
	if err != nil {
		logger.Log.Error("failed to list exp history", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list exp history"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}
