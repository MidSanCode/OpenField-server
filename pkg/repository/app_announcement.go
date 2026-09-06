package repository

import (
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ListAppAnnouncements returns app announcements, newest first. activeOnly
// restricts to the entries clients should display; false returns the full
// history (admin management view).
func ListAppAnnouncements(activeOnly bool) ([]model.AppAnnouncement, error) {
	query := "SELECT id, title, content, COALESCE(creator_id, 0), active, created_at FROM app_announcements"
	if activeOnly {
		query += " WHERE active = TRUE"
	}
	query += " ORDER BY created_at DESC LIMIT 200"
	rows, err := database.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list app announcements: %w", err)
	}
	defer rows.Close()

	out := []model.AppAnnouncement{}
	for rows.Next() {
		a := model.AppAnnouncement{}
		if err := rows.Scan(&a.ID, &a.Title, &a.Content, &a.CreatorID, &a.Active, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan app announcement: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAppAnnouncement inserts a new active announcement.
func CreateAppAnnouncement(title, content string, creatorID int64) (*model.AppAnnouncement, error) {
	a := &model.AppAnnouncement{}
	err := database.DB.QueryRow(
		"INSERT INTO app_announcements (title, content, creator_id) VALUES ($1, $2, $3) RETURNING id, title, content, COALESCE(creator_id, 0), active, created_at",
		title, content, creatorID,
	).Scan(&a.ID, &a.Title, &a.Content, &a.CreatorID, &a.Active, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create app announcement: %w", err)
	}
	return a, nil
}

// SetAppAnnouncementActive toggles an announcement's active state.
func SetAppAnnouncementActive(id int64, active bool) error {
	res, err := database.DB.Exec(
		"UPDATE app_announcements SET active = $2 WHERE id = $1", id, active,
	)
	if err != nil {
		return fmt.Errorf("failed to update app announcement: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
