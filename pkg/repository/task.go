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

// ErrMakeupToday reports trying to make up today (it must be claimed directly).
var ErrMakeupToday = errors.New("today must be claimed via the normal sign-in")

// ErrMakeupFuture reports trying to make up a date after today.
var ErrMakeupFuture = errors.New("cannot make up a future date")

// ErrMakeupBeforeRegistration reports trying to make up a date that precedes
// the user's account creation.
var ErrMakeupBeforeRegistration = errors.New("cannot make up a date before registration")

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

// UserRepo exposes the underlying user repository (e.g. to load calendar data).
func (r *TaskRepository) UserRepo() *UserRepository {
	return r.userRepo
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
// currencyAmount and makeupCost are coin-denominated (e.g. 5 for 5 金币);
// they are scaled to cents when the wallet is adjusted.
//
// It is idempotent per calendar day (in the user's timezone): the second call
// within the same day returns granted=false without side effects.
func (r *TaskRepository) Checkin(userID, expAmount, currencyAmount, makeupCost int64, isMakeup bool, loc *time.Location, now time.Time) (granted bool, expGranted int64, streak int64, err error) {
	if loc == nil {
		loc = time.UTC
	}
	today := cycleKeyFor(now.In(loc))

	tx, err := database.DB.Begin()
	if err != nil {
		return false, 0, 0, fmt.Errorf("failed to begin checkin: %w", err)
	}
	defer tx.Rollback()

	var exp int64
	var currentStreak int64
	var lastBonusAt *time.Time
	var memberLevel int64
	var memberExpiresAt *time.Time
	if err := tx.QueryRow(
		"SELECT exp, checkin_streak, last_daily_bonus_at, member_level, member_expires_at FROM users WHERE id = $1 FOR UPDATE",
		userID,
	).Scan(&exp, &currentStreak, &lastBonusAt, &memberLevel, &memberExpiresAt); err != nil {
		return false, 0, 0, fmt.Errorf("failed to load user for checkin: %w", err)
	}

	alreadyToday := lastBonusAt != nil && isSameUTCDay(*lastBonusAt, now, loc)
	if alreadyToday {
		return false, 0, currentStreak, nil
	}

	newStreak := currentStreak
	if lastBonusAt != nil && isSameUTCDay(*lastBonusAt, now.AddDate(0, 0, -1), loc) {
		newStreak = currentStreak + 1
	} else if isMakeup {
		// A make-up renews the streak for the missed day even when the last
		// sign-in was more than a day ago, as long as it is not ancient.
		if lastBonusAt == nil || now.Sub(*lastBonusAt).Hours() > 72 {
			return false, 0, currentStreak, ErrMakeupGapTooLarge
		}
		newStreak = currentStreak + 1
	} else {
		newStreak = 1
	}

	if isMakeup {
		// Charge the make-up price from the sender wallet.
		if err := adjustBalanceTx(tx, userID, -model.MoneyScale*makeupCost, userID, "makeup", "补签扣款"); err != nil {
			return false, 0, currentStreak, err
		}
	} else {
		// Grant the daily currency.
		if err := adjustBalanceTx(tx, userID, model.MoneyScale*currencyAmount, userID, "checkin", "每日签到奖励"); err != nil {
			return false, 0, currentStreak, err
		}
	}

	delta := model.ApplyMemberExp(expAmount, memberLevel, memberExpiresAt, now)
	if _, err := tx.Exec(
		"UPDATE users SET exp = exp + $2, checkin_streak = $3, last_daily_bonus_at = NOW(), updated_at = NOW() WHERE id = $1",
		userID, delta, newStreak,
	); err != nil {
		return false, 0, currentStreak, fmt.Errorf("failed to update user streak: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, 0, currentStreak, err
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
			return false, 0, newStreak, err
		}
		_ = r.awardMilestones(userID, newStreak)
	}

	return true, delta, newStreak, nil
}

// MakeupCheckin is a convenience wrapper for a paid make-up sign-in.
func (r *TaskRepository) MakeupCheckin(userID, expAmount, makeupCost int64, loc *time.Location, now time.Time) (bool, int64, int64, error) {
	return r.Checkin(userID, expAmount, 0, makeupCost, true, loc, now)
}

// ListSignedDates returns the calendar days ("YYYY-MM-DD") the user has already
// signed in on, from the daily_login task completions, oldest first.
func ListSignedDates(userID int64) ([]string, error) {
	t, err := getTaskByCode("daily_login")
	if err != nil {
		return nil, err
	}
	rows, err := database.DB.Query(
		`SELECT cycle_key FROM task_completions
		 WHERE user_id = $1 AND task_id = $2
		   AND cycle_key ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
		 ORDER BY cycle_key ASC`,
		userID, t.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query signed dates: %w", err)
	}
	defer rows.Close()
	dates := make([]string, 0)
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("failed to scan signed date: %w", err)
		}
		dates = append(dates, d)
	}
	return dates, rows.Err()
}

// RecomputeStreak derives the consecutive sign-in streak from the recorded
// signed dates: it counts how many days back from today (or yesterday when
// today is not yet signed) are all signed in a row.
func RecomputeStreak(signed []string, loc *time.Location, now time.Time) int64 {
	signedSet := make(map[string]struct{}, len(signed))
	for _, d := range signed {
		signedSet[d] = struct{}{}
	}

	start := now.In(loc)
	if _, ok := signedSet[cycleKeyFor(start)]; !ok {
		// Today not signed yet: the running streak still counts from yesterday.
		start = start.AddDate(0, 0, -1)
	}

	var streak int64
	for {
		if _, ok := signedSet[cycleKeyFor(start)]; !ok {
			break
		}
		streak++
		start = start.AddDate(0, 0, -1)
	}
	return streak
}

// MakeupByDate pays to sign in a specific past calendar day that was missed.
//
//	dateKey: "YYYY-MM-DD" of the missed day (must be before today and after the
//	user's registration; today is handled by Checkin).
//	expAmount: exp granted for the made-up day.
//	makeupCost: currency charged for the make-up.
//
// It records the day as signed, refunds/propagates the streak accordingly and
// is idempotent: signing an already-signed day returns granted=false.
func (r *TaskRepository) MakeupByDate(userID int64, dateKey string, expAmount, makeupCost int64, loc *time.Location, now time.Time) (bool, int64, int64, error) {
	if loc == nil {
		loc = time.UTC
	}
	if _, err := time.ParseInLocation("2006-01-02", dateKey, loc); err != nil {
		return false, 0, 0, fmt.Errorf("invalid makeup date: %w", err)
	}
	todayKey := cycleKeyFor(now.In(loc))
	if dateKey == todayKey {
		return false, 0, 0, ErrMakeupToday
	}
	if dateKey > todayKey {
		return false, 0, 0, ErrMakeupFuture
	}

	user, err := r.userRepo.GetByID(userID)
	if err != nil {
		return false, 0, 0, err
	}
	if user == nil {
		return false, 0, 0, ErrNotFound
	}
	regKey := cycleKeyFor(user.CreatedAt.In(loc))
	if dateKey < regKey {
		return false, 0, 0, ErrMakeupBeforeRegistration
	}

	t, err := getTaskByCode("daily_login")
	if err != nil {
		return false, 0, 0, err
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return false, 0, 0, fmt.Errorf("failed to begin makeup: %w", err)
	}
	defer tx.Rollback()

	// Insert the completion first: the unique constraint makes the check-and-
	// charge atomic, so a concurrent duplicate never charges twice.
	res, err := tx.Exec(
		`INSERT INTO task_completions (user_id, task_id, cycle_key) VALUES ($1, $2, $3)`,
		userID, t.ID, dateKey,
	)
	if err != nil {
		if isUniqueViolation(err) {
			signed, listErr := ListSignedDates(userID)
			if listErr != nil {
				return false, 0, 0, listErr
			}
			return false, 0, RecomputeStreak(signed, loc, now), nil
		}
		return false, 0, 0, fmt.Errorf("failed to record makeup day: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		signed, listErr := ListSignedDates(userID)
		if listErr != nil {
			return false, 0, 0, listErr
		}
		return false, 0, RecomputeStreak(signed, loc, now), nil
	}

	// Charge the make-up price (fails when the wallet is too empty).
	if err := adjustBalanceTx(tx, userID, -model.MoneyScale*makeupCost, userID, "makeup", "补签扣款"); err != nil {
		return false, 0, 0, err
	}
	delta := model.ApplyMemberExp(expAmount, user.MemberLevel, user.MemberExpiresAt, now)
	if _, err := tx.Exec(
		"UPDATE users SET exp = exp + $2, updated_at = NOW() WHERE id = $1",
		userID, delta,
	); err != nil {
		return false, 0, 0, fmt.Errorf("failed to grant makeup exp: %w", err)
	}

	// Recompute the streak from all signed days inside the transaction so the
	// made-up day is reflected consistently.
	var signed []string
	signedTx, err := tx.Query(
		`SELECT cycle_key FROM task_completions
		 WHERE user_id = $1 AND task_id = $2
		   AND cycle_key ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'`,
		userID, t.ID,
	)
	if err != nil {
		return false, 0, 0, fmt.Errorf("failed to query signed days after makeup: %w", err)
	}
	for signedTx.Next() {
		var d string
		if err := signedTx.Scan(&d); err != nil {
			signedTx.Close()
			return false, 0, 0, fmt.Errorf("failed to scan signed day: %w", err)
		}
		signed = append(signed, d)
	}
	signedTx.Close()
	newStreak := RecomputeStreak(signed, loc, now)
	if _, err := tx.Exec(
		"UPDATE users SET checkin_streak = $2 WHERE id = $1",
		userID, newStreak,
	); err != nil {
		return false, 0, 0, fmt.Errorf("failed to update streak after makeup: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, 0, 0, err
	}

	_ = r.expRepo.Add(userID, delta, model.ExpReasonMakeup, "补签 "+dateKey)
	_ = r.awardMilestones(userID, newStreak)

	return true, delta, newStreak, nil
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
	expGranted, err := r.grantTaskReward(userID, t, "")
	if err != nil {
		return 0, 0, err
	}
	return expGranted, t.RewardCurrency, nil
}

// grantTaskReward atomically credits exp + currency and records completion.
// A unique violation on the completion insert (a concurrent claim won) aborts
// the whole write so rewards are never double-granted. It returns the exp
// actually granted (the task reward scaled by the member multiplier).
func (r *TaskRepository) grantTaskReward(userID int64, t *model.Task, cycleKey string) (int64, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin task reward: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO task_completions (user_id, task_id, cycle_key) VALUES ($1, $2, $3)`,
		userID, t.ID, cycleKey,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrAlreadyClaimed
		}
		return 0, fmt.Errorf("failed to record completion: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return 0, ErrAlreadyClaimed
	}

	var memberLevel int64
	var memberExpiresAt *time.Time
	if err := tx.QueryRow(
		"SELECT member_level, member_expires_at FROM users WHERE id = $1 FOR UPDATE",
		userID,
	).Scan(&memberLevel, &memberExpiresAt); err != nil {
		return 0, fmt.Errorf("failed to load user membership for task reward: %w", err)
	}
	delta := model.ApplyMemberExp(t.RewardExp, memberLevel, memberExpiresAt, time.Now())

	if _, err := tx.Exec(
		"UPDATE users SET exp = exp + $2, updated_at = NOW() WHERE id = $1",
		userID, delta,
	); err != nil {
		return 0, fmt.Errorf("failed to grant task exp: %w", err)
	}
	if t.RewardCurrency > 0 {
		if err := adjustBalanceTx(tx, userID, model.MoneyScale*t.RewardCurrency, userID, "task_reward", t.Name); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit task reward: %w", err)
	}
	return delta, r.expRepo.Add(userID, delta, model.ExpReasonTask, t.Name)
}
