package repository

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// CampRepository handles 贴吧-style camp communities.
type CampRepository struct{}

// NewCampRepository creates a new CampRepository.
func NewCampRepository() *CampRepository {
	return &CampRepository{}
}

const campCols = `c.id, c.name, c.description, c.creator_id, c.is_visible, c.direct_join,
		(SELECT COUNT(*) FROM camp_members cm WHERE cm.camp_id = c.id) AS member_count,
		(SELECT COUNT(*) FROM posts p WHERE p.camp_id = c.id) AS post_count,
		c.created_at, c.updated_at`

const campScan = `&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin,
		&c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt`

// Create inserts a camp and adds the creator as its owner.
func (r *CampRepository) Create(creatorID int64, name, description string, isVisible, directJoin bool) (*model.Camp, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin camp create: %w", err)
	}
	defer tx.Rollback()

	camp := &model.Camp{}
	err = tx.QueryRow(
		`INSERT INTO camps (name, description, creator_id, is_visible, direct_join)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, name, description, creator_id, is_visible, direct_join, created_at, updated_at`,
		name, description, creatorID, isVisible, directJoin,
	).Scan(&camp.ID, &camp.Name, &camp.Description, &camp.CreatorID, &camp.IsVisible, &camp.DirectJoin, &camp.CreatedAt, &camp.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create camp: %w", err)
	}
	if _, err := tx.Exec(
		"INSERT INTO camp_members (camp_id, user_id, role) VALUES ($1, $2, 'owner') ON CONFLICT DO NOTHING",
		camp.ID, creatorID,
	); err != nil {
		return nil, fmt.Errorf("failed to add camp owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	camp.IsMember = true
	return camp, nil
}

// GetByID loads one camp; userID personalizes IsMember (0 = anonymous).
func (r *CampRepository) GetByID(id, userID int64) (*model.Camp, error) {
	c := &model.Camp{}
	err := database.DB.QueryRow(
		"SELECT "+campCols+" FROM camps c WHERE c.id = $1", id,
	).Scan([]interface{}{&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt}...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get camp: %w", err)
	}
	if err := scanCampRowExtras(c, userID); err != nil {
		return nil, err
	}
	return c, nil
}

func scanCampRowExtras(c *model.Camp, userID int64) error {
	if userID > 0 {
		var n int
		if err := database.DB.QueryRow(
			"SELECT COUNT(*) FROM camp_members WHERE camp_id = $1 AND user_id = $2", c.ID, userID,
		).Scan(&n); err != nil {
			return err
		}
		c.IsMember = n > 0
	}
	return nil
}

// List returns visible camps (all of them for members/admins pass-through is
// not tracked here: hidden camps are simply excluded from the public list).
// userID personalizes IsMember.
func (r *CampRepository) List(userID int64, query string, limit int) ([]model.Camp, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	sqlText := "SELECT " + campCols + " FROM camps c WHERE c.is_visible = TRUE"
	args := []interface{}{}
	if query != "" {
		sqlText += " AND c.name ILIKE $1"
		args = append(args, "%"+query+"%")
	}
	sqlText += " ORDER BY c.updated_at DESC LIMIT " + fmt.Sprintf("%d", limit)
	rows, err := database.DB.Query(sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list camps: %w", err)
	}
	defer rows.Close()

	out := []model.Camp{}
	for rows.Next() {
		c := model.Camp{}
		if err := rows.Scan([]interface{}{&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt}...); err != nil {
			return nil, fmt.Errorf("failed to scan camp: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if userID > 0 && len(out) > 0 {
		ids := make([]int64, 0, len(out))
		for _, c := range out {
			ids = append(ids, c.ID)
		}
		mine, err := memberCampIDs(userID, ids)
		if err == nil {
			set := make(map[int64]bool, len(mine))
			for _, id := range mine {
				set[id] = true
			}
			for i := range out {
				out[i].IsMember = set[out[i].ID]
			}
		}
	}
	return out, nil
}

// ListMine returns camps the user belongs to (including hidden ones).
func (r *CampRepository) ListMine(userID int64, limit int) ([]model.Camp, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := database.DB.Query(
		"SELECT "+campCols+" FROM camps c JOIN camp_members cm ON cm.camp_id = c.id AND cm.user_id = $1 ORDER BY c.updated_at DESC LIMIT "+fmt.Sprintf("%d", limit),
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list my camps: %w", err)
	}
	defer rows.Close()

	out := []model.Camp{}
	for rows.Next() {
		c := model.Camp{}
		if err := rows.Scan([]interface{}{&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt}...); err != nil {
			return nil, fmt.Errorf("failed to scan camp: %w", err)
		}
		c.IsMember = true
		out = append(out, c)
	}
	return out, rows.Err()
}

func memberCampIDs(userID int64, campIDs []int64) ([]int64, error) {
	rows, err := database.DB.Query(
		"SELECT camp_id FROM camp_members WHERE user_id = $1 AND camp_id = ANY($2)",
		userID, pq.Array(campIDs),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Update mutates name/description/visibility/direct-join (creator only).
func (r *CampRepository) Update(id, creatorID int64, name, description *string, isVisible, directJoin *bool) error {
	res, err := database.DB.Exec(
		`UPDATE camps SET
			name = COALESCE($3, name),
			description = COALESCE($4, description),
			is_visible = COALESCE($5, is_visible),
			direct_join = COALESCE($6, direct_join),
			updated_at = NOW()
		 WHERE id = $1 AND creator_id = $2`,
		id, creatorID, name, description, isVisible, directJoin,
	)
	if err != nil {
		return fmt.Errorf("failed to update camp: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a camp (creator only).
func (r *CampRepository) Delete(id, creatorID int64) error {
	res, err := database.DB.Exec("DELETE FROM camps WHERE id = $1 AND creator_id = $2", id, creatorID)
	if err != nil {
		return fmt.Errorf("failed to delete camp: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// IsMember reports membership.
func (r *CampRepository) IsMember(campID, userID int64) (bool, error) {
	var n int
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM camp_members WHERE camp_id = $1 AND user_id = $2", campID, userID,
	).Scan(&n)
	return n > 0, err
}

// Join adds the user; returns false when the camp does not exist.
func (r *CampRepository) Join(campID, userID int64) (bool, error) {
	res, err := database.DB.Exec(
		"INSERT INTO camp_members (camp_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT DO NOTHING",
		campID, userID,
	)
	if err != nil {
		return false, fmt.Errorf("failed to join camp: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return true, nil // already a member
	}
	if _, err := database.DB.Exec("UPDATE camps SET updated_at = NOW() WHERE id = $1", campID); err != nil {
		return false, err
	}
	return true, nil
}

// Leave removes the membership; the creator cannot leave their own camp.
func (r *CampRepository) Leave(campID, userID int64) error {
	res, err := database.DB.Exec(
		"DELETE FROM camp_members WHERE camp_id = $1 AND user_id = $2", campID, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to leave camp: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountByCreator returns how many camps a user has created.
func (r *CampRepository) CountByCreator(creatorID int64) (int64, error) {
	var n int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM camps WHERE creator_id = $1", creatorID,
	).Scan(&n)
	return n, err
}
