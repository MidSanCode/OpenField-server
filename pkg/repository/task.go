package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ErrAlreadyClaimed reports a task (or daily sign-in) that was already claimed
// for the current cycle.
var ErrAlreadyClaimed = errors.New("already claimed")

// ErrNotEligible reports a task claim before the progress requirements are met.
var ErrNotEligible = errors.New("task not eligible")

// ErrMakeupGapTooLarge reports a make-up sign-in attempt when the last sign-in
// is too far in the past to sensibly renew.
var ErrMakeupGapTooLarge = errors.New("makeup gap too large")

// TaskRepository handles the tasks catalog, the daily sign-in streak and task
// claim rewards.
type TaskRepository struct {
	userRepo *UserRepository
	expRepo  *ExpRepository
}

// NewTaskRepository creates a new TaskRepository.
func NewTaskRepository() *TaskRepository {
	return &TaskRepository{
		userRepo: NewUserRepository(),
		expRepo:  NewExpRepository(),
	}
}

// ListTaskCodes maps a task code to an SQL count used to derive one-time
// progress. Keys without an entry always report progress 0 (never eligible).
var listTaskCounts = map[string]string{
	"first_post":   "SELECT COUNT(*) FROM posts WHERE user_id = $1",
	"posts_10":     "SELECT COUNT(*) FROM posts WHERE user_id = $1",
	"first_reply":  "SELECT COUNT(*) FROM post_replies WHERE user_id = $1 AND deleted_at IS NULL",
	"first_follow": "SELECT COUNT(*) FROM user_follows WHERE follower_id = $1",
	"follow_10":    "SELECT COUNT(*) FROM user_follows WHERE follower_id = $1",
	"first_upload": "SELECT COUNT(*) FROM attachments WHERE user_id = $1",
	"first_chat":   "SELECT COUNT(*) FROM messages WHERE sender_id = $1",
}

// cycleKeyFor returns the cycle key for a given time in the user's timezone.
// Daily tasks (sign-in) use YYYY-MM-DD so they can be claimed once per day.
func cycleKeyFor(t time.Time) string {
	return t.Format("2006-01-02")
}

// ListTasks returns every task with the user's progress and claimability.
// isMakeupToday reports an on-going make-up so the daily task can be claimed.
func (r *TaskRepository) ListTasks(userID int64, loc *time.Location, now time.Time) ([]model.TaskState, int64, bool, error) {
	if loc == nil {
		loc = time.UTC
	}

	user, err := r.userRepo.GetByID(userID)
	if err != nil {
		return nil, 0, false, err
	}
	if user == nil {
		return nil, 0, false, ErrNotFound
	}

	today := cycleKeyFor(now.In(loc))

	rows, err := database.DB.Query(
		`SELECT t.id, t.code, t.kind, t.name, t.description, t.reward_exp, t.reward_currency, t.target, t.sort
		 FROM tasks t ORDER BY t.sort ASC, t.id ASC`,
	)
	if err != nil {
		return nil, 0, false, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	states := make([]model.TaskState, 0)
	for rows.Next() {
		var t model.Task
		if err := rows.Scan(&t.ID, &t.Code, &t.Kind, &t.Name, &t.Description, &t.RewardExp, &t.RewardCurrency, &t.Target, &t.Sort); err != nil {
			return nil, 0, false, fmt.Errorf("failed to scan task: %w", err)
		}
		state, err := r.computeState(userID, user, &t, today, now, loc)
		if err != nil {
			return nil, 0, false, err
		}
		states = append(states, *state)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, fmt.Errorf("rows error: %w", err)
	}
	return states, user.CheckinStreak, false, nil
}

func (r *TaskRepository) computeState(userID int64, user *model.User, t *model.Task, today string, now time.Time, loc *time.Location) (*model.TaskState, error) {
	state := &model.TaskState{Task: *t}

	switch model.TaskKind(t.Kind) {
	case model.TaskKindOnce:
		// One-time achievements: progress is the underlying activity count.
		var progress int64
		if q, ok := listTaskCounts[t.Code]; ok {
			if err := database.DB.QueryRow(q, userID).Scan(&progress); err != nil {
				return nil, fmt.Errorf("failed to compute progress for %s: %w", t.Code, err)
			}
		}
		state.Progress = progress
		done, err := hasCompletion(userID, t.ID, "")
		if err != nil {
			return nil, err
		}
		state.Completed = done
		state.Claimable = !done && progress >= int64(t.Target)

	case model.TaskKindStreak:
		// Streak tasks: progress is the current consecutive sign-in streak.
		state.Progress = user.CheckinStreak
		if t.Code == "daily_login" {
			// Daily sign-in task: claimable once per day; completion recorded
			// under the day's cycle key.
			done, err := hasCompletion(userID, t.ID, today)
			if err != nil {
				return nil, err
			}
			state.Completed = done
			state.Claimable = !done
			state.Progress = boolToInt64(done)
			return state, nil
		}
		// Milestone streaks (login_3/7/30): claimable once forever when the
		// streak reaches the target.
		done, err := hasCompletion(userID, t.ID, "")
		if err != nil {
			return nil, err
		}
		state.Completed = done
		state.Claimable = !done && user.CheckinStreak >= int64(t.Target)

	default:
		state.Claimable = false
	}
	return state, nil
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func hasCompletion(userID, taskID int64, cycleKey string) (bool, error) {
	var exists bool
	err := database.DB.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM task_completions WHERE user_id = $1 AND task_id = $2 AND cycle_key = $3)`,
		userID, taskID, cycleKey,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check task completion: %w", err)
	}
	return exists, nil
}

// getTaskByCode loads a task definition by its code.
func getTaskByCode(code string) (*model.Task, error) {
	t := &model.Task{}
	err := database.DB.QueryRow(
		`SELECT id, code, kind, name, description, reward_exp, reward_currency, target, sort
		 FROM tasks WHERE code = $1`,
		code,
	).Scan(&t.ID, &t.Code, &t.Kind, &t.Name, &t.Description, &t.RewardExp, &t.RewardCurrency, &t.Target, &t.Sort)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query task: %w", err)
	}
	return t, nil
}

// Checkin performs the daily sign-in (or its make-up variant) for a user.
//
//	expAmount / currencyAmount: rewards granted on a real sign-in.
//	makeupCost: currency charged for a make-up; a make-up renews the streak and
//	gives exp but never the daily currency.
//	isMakeup: whether this is a paid make-up sign-in.
//
// It is idempotent per calendar day (in the user's timezone): the second call
// within the same day returns granted=false without side effects.
func (r *TaskRepository) Checkin(userID, expAmount, currencyAmount, makeupCost int64, isMakeup bool, loc *time.Location, now time.Time) (granted bool, streak int64, err error) {
	if loc == nil {
		loc = time.UTC
	}
	today := cycleKeyFor(now.In(loc))

	tx, err := database.DB.Begin()
	if err != nil {
		return false, 0, fmt.Errorf("failed to begin checkin: %w", err)
	}
	defer tx.Rollback()

	var exp int64
	var currentStreak int64
	var lastBonusAt *time.Time
	if err := tx.QueryRow(
		"SELECT exp, checkin_streak, last_daily_bonus_at FROM users WHERE id = $1 FOR UPDATE",
		userID,
	).Scan(&exp, &currentStreak, &lastBonusAt); err != nil {
		return false, 0, fmt.Errorf("failed to load user for checkin: %w", err)
	}

	alreadyToday := lastBonusAt != nil && isSameUTCDay(*lastBonusAt, now, loc)
	if alreadyToday {
		return false, currentStreak, nil
	}

	newStreak := currentStreak
	if lastBonusAt != nil && isSameUTCDay(*lastBonusAt, now.AddDate(0, 0, -1), loc) {
		newStreak = currentStreak + 1
	} else if isMakeup {
		// A make-up renews the streak for the missed day even when the last
		// sign-in was more than a day ago, as long as it is not ancient.
		if lastBonusAt == nil || now.Sub(*lastBonusAt).Hours() > 72 {
			return false, currentStreak, ErrMakeupGapTooLarge
		}
		newStreak = currentStreak + 1
	} else {
		newStreak = 1
	}

	if isMakeup {
		// Charge the make-up price from the sender wallet.
		if err := adjustBalanceTx(tx, userID, -makeupCost, userID, "makeup", "补签扣款"); err != nil {
			return false, currentStreak, err
		}
	} else {
		// Grant the daily currency.
		if err := adjustBalanceTx(tx, userID, currencyAmount, userID, "checkin", "每日签到奖励"); err != nil {
			return false, currentStreak, err
		}
	}

	delta := expAmount
	if _, err := tx.Exec(
		"UPDATE users SET exp = exp + $2, checkin_streak = $3, last_daily_bonus_at = NOW(), updated_at = NOW() WHERE id = $1",
		userID, delta, newStreak,
	); err != nil {
		return false, currentStreak, fmt.Errorf("failed to update user streak: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, currentStreak, err
	}

	reason := model.ExpReasonDailyBonus
	desc := "每日签到"
	if isMakeup {
		reason = model.ExpReasonMakeup
		desc = "补签续期"
	}
	_ = r.expRepo.Add(userID, delta, reason, desc)

	// Record the daily task completion (and any reached milestones) outside
	// the wallet transaction so uniqueness collisions never roll back rewards.
	if t, err := getTaskByCode("daily_login"); err == nil {
		if err := r.recordCompletion(userID, t.ID, today); err != nil {
			return false, newStreak, err
		}
		_ = r.awardMilestones(userID, newStreak)
	}

	return true, newStreak, nil
}

// MakeupCheckin is a convenience wrapper for a paid make-up sign-in.
func (r *TaskRepository) MakeupCheckin(userID, expAmount, makeupCost int64, loc *time.Location, now time.Time) (bool, int64, error) {
	return r.Checkin(userID, expAmount, 0, makeupCost, true, loc, now)
}

// recordCompletion marks a task as completed for the requested cycle.
func (r *TaskRepository) recordCompletion(userID, taskID int64, cycleKey string) error {
	_, err := database.DB.Exec(
		`INSERT INTO task_completions (user_id, task_id, cycle_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		userID, taskID, cycleKey,
	)
	if err != nil {
		return fmt.Errorf("failed to record task completion: %w", err)
	}
	return nil
}

// awardMilestones idempotently grants any streak milestone (login_3/7/30) the
// user's current streak has reached but has not claimed yet.
func (r *TaskRepository) awardMilestones(userID, streak int64) error {
	for _, code := range []string{"login_3", "login_7", "login_30"} {
		t, err := getTaskByCode(code)
		if err != nil {
			continue
		}
		if streak < int64(t.Target) {
			continue
		}
		done, err := hasCompletion(userID, t.ID, "")
		if err != nil {
			continue
		}
		if done {
			continue
		}
		if err := r.grantTaskReward(userID, t, ""); err != nil {
			return err
		}
	}
	return nil
}

// ClaimOnce claims a one-time achievement task. Returns ErrNotEligible when the
// progress requirements are unmet and ErrAlreadyClaimed when already done.
func (r *TaskRepository) ClaimOnce(userID int64, code string) (int64, int64, error) {
	t, err := getTaskByCode(code)
	if err != nil {
		return 0, 0, err
	}
	if model.TaskKind(t.Kind) != model.TaskKindOnce {
		return 0, 0, ErrNotEligible
	}
	var progress int64
	if q, ok := listTaskCounts[t.Code]; ok {
		if err := database.DB.QueryRow(q, userID).Scan(&progress); err != nil {
			return 0, 0, fmt.Errorf("failed to compute progress: %w", err)
		}
	}
	if progress < int64(t.Target) {
		return 0, 0, ErrNotEligible
	}
	done, err := hasCompletion(userID, t.ID, "")
	if err != nil {
		return 0, 0, err
	}
	if done {
		return 0, 0, ErrAlreadyClaimed
	}
	if err := r.grantTaskReward(userID, t, ""); err != nil {
		return 0, 0, err
	}
	return t.RewardExp, t.RewardCurrency, nil
}

// grantTaskReward atomically credits exp + currency and records completion.
// A unique violation on the completion insert (a concurrent claim won) aborts
// the whole write so rewards are never double-granted.
func (r *TaskRepository) grantTaskReward(userID int64, t *model.Task, cycleKey string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin task reward: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO task_completions (user_id, task_id, cycle_key) VALUES ($1, $2, $3)`,
		userID, t.ID, cycleKey,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyClaimed
		}
		return fmt.Errorf("failed to record completion: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrAlreadyClaimed
	}

	if _, err := tx.Exec(
		"UPDATE users SET exp = exp + $2, updated_at = NOW() WHERE id = $1",
		userID, t.RewardExp,
	); err != nil {
		return fmt.Errorf("failed to grant task exp: %w", err)
	}
	if t.RewardCurrency > 0 {
		if err := adjustBalanceTx(tx, userID, t.RewardCurrency, userID, "task_reward", t.Name); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit task reward: %w", err)
	}
	return r.expRepo.Add(userID, t.RewardExp, model.ExpReasonTask, t.Name)
}
