package repository

import (
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// MaxReportsPerHour caps how many reports one user may file in an hour,
// regardless of target. Reports are cheap to file and the whole point is to
// be heard, so this is generous; it exists to stop a single account from
// flooding the moderation queue.
const MaxReportsPerHour = 20

// ReportRepository handles the moderation report inbox (举报).
type ReportRepository struct{}

// NewReportRepository creates a new ReportRepository.
func NewReportRepository() *ReportRepository {
	return &ReportRepository{}
}

// Create files a report. It validates that the target exists and belongs to
// the expected kind of content, enforces the per-user hourly budget, and
// enforces one open (pending) report per reporter+target via the partial
// unique index. Returns ErrNotFound when the target does not exist,
// ErrAlreadyReported when the caller already has a pending report against
// the same target, and ErrReportRateLimit when the caller is over budget.
func (r *ReportRepository) Create(reporterID int64, targetType string, targetID int64, reason string) (*model.Report, error) {
	if err := r.validateTarget(targetType, targetID); err != nil {
		return nil, err
	}

	var recent int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM reports WHERE reporter_id = $1 AND created_at > NOW() - INTERVAL '1 hour'",
		reporterID,
	).Scan(&recent); err != nil {
		return nil, fmt.Errorf("failed to count recent reports: %w", err)
	}
	if recent >= MaxReportsPerHour {
		return nil, ErrReportRateLimit
	}

	rep := &model.Report{}
	err := database.DB.QueryRow(
		`INSERT INTO reports (reporter_id, target_type, target_id, reason)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, reporter_id, target_type, target_id, reason, status, created_at`,
		reporterID, targetType, targetID, reason,
	).Scan(&rep.ID, &rep.ReporterID, &rep.TargetType, &rep.TargetID, &rep.Reason, &rep.Status, &rep.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyReported
		}
		return nil, fmt.Errorf("failed to create report: %w", err)
	}
	return rep, nil
}

// validateTarget confirms the reported row exists and is of the claimed kind.
// The report table itself has no FK on (target_type, target_id) because the
// target is polymorphic across posts/messages/users, so the existence check
// happens here instead.
func (r *ReportRepository) validateTarget(targetType string, targetID int64) error {
	switch targetType {
	case model.ReportTargetPost:
		return r.expectExists("posts", targetID)
	case model.ReportTargetMessage:
		return r.expectExists("messages", targetID)
	case model.ReportTargetUser:
		return r.expectExists("users", targetID)
	}
	return fmt.Errorf("unknown report target type %q", targetType)
}

func (r *ReportRepository) expectExists(table string, id int64) error {
	var exists bool
	err := database.DB.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1)", id,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check %s target: %w", table, err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// ListMine returns the caller's reports, newest first. The reporter name is
// denormalized via a join so clients can render who filed each one.
func (r *ReportRepository) ListMine(reporterID int64, limit int) ([]model.Report, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := database.DB.Query(
		`SELECT r.id, r.reporter_id, u.username, r.target_type, r.target_id, r.reason,
		        r.status, r.reviewer_id, r.reviewer_username, r.review_note, r.created_at, r.reviewed_at
		 FROM reports r
		 JOIN users u ON u.id = r.reporter_id
		 WHERE r.reporter_id = $1
		 ORDER BY r.created_at DESC
		 LIMIT $2`,
		reporterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list reports: %w", err)
	}
	defer rows.Close()

	out := []model.Report{}
	for rows.Next() {
		rep := model.Report{}
		if err := rows.Scan(&rep.ID, &rep.ReporterID, &rep.ReporterName, &rep.TargetType, &rep.TargetID, &rep.Reason, &rep.Status, &rep.ReviewerID, &rep.ReviewerUsername, &rep.ReviewNote, &rep.CreatedAt, &rep.ReviewedAt); err != nil {
			return nil, fmt.Errorf("failed to scan report: %w", err)
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}
