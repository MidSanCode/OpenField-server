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

const campCols = `c.id, c.name, c.description, c.creator_id, c.is_visible, c.direct_join, c.member_post, c.member_pin,
		(SELECT COUNT(*) FROM camp_members cm WHERE cm.camp_id = c.id) AS member_count,
		(SELECT COUNT(*) FROM posts p WHERE p.camp_id = c.id) AS post_count,
		c.created_at, c.updated_at`

const campScan = `&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberPost, &c.MemberPin,
		&c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt`

// Create inserts a camp and adds the creator as its owner. memberPost/
// memberPin seed the camp's permission switches.
func (r *CampRepository) Create(creatorID int64, name, description string, isVisible, directJoin, memberPost, memberPin bool) (*model.Camp, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin camp create: %w", err)
	}
	defer tx.Rollback()

	camp := &model.Camp{}
	err = tx.QueryRow(
		`INSERT INTO camps (name, description, creator_id, is_visible, direct_join, member_post, member_pin)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, name, description, creator_id, is_visible, direct_join, member_post, member_pin, created_at, updated_at`,
		name, description, creatorID, isVisible, directJoin, memberPost, memberPin,
	).Scan(&camp.ID, &camp.Name, &camp.Description, &camp.CreatorID, &camp.IsVisible, &camp.DirectJoin, &camp.MemberPost, &camp.MemberPin, &camp.CreatedAt, &camp.UpdatedAt)
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
	camp.MyRole = model.CampRoleOwner
	return camp, nil
}

// GetByID loads one camp; userID personalizes IsMember and MyRole
// (0 = anonymous).
func (r *CampRepository) GetByID(id, userID int64) (*model.Camp, error) {
	c := &model.Camp{}
	err := database.DB.QueryRow(
		"SELECT "+campCols+" FROM camps c WHERE c.id = $1", id,
	).Scan([]interface{}{&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberPost, &c.MemberPin, &c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt}...)
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

// scanCampRowExtras personalizes a camp with the viewer's membership state:
// IsMember plus MyRole ("owner"/"admin"/"member", "" for non-members). The
// creator is always owner, even when their roster row predates role
// semantics (the v26 backfill also repairs those rows).
func scanCampRowExtras(c *model.Camp, userID int64) error {
	if userID > 0 {
		var role string
		err := database.DB.QueryRow(
			"SELECT role FROM camp_members WHERE camp_id = $1 AND user_id = $2", c.ID, userID,
		).Scan(&role)
		switch {
		case err == sql.ErrNoRows:
			c.IsMember = false
			c.MyRole = ""
		case err != nil:
			return err
		default:
			c.MyRole = role
			c.IsMember = role != ""
		}
		if c.CreatorID == userID {
			c.MyRole = model.CampRoleOwner
			c.IsMember = true
		}
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
		if err := rows.Scan([]interface{}{&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberPost, &c.MemberPin, &c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt}...); err != nil {
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
				if out[i].IsMember {
					role, err := r.GetRole(out[i].ID, userID)
					if err == nil {
						out[i].MyRole = role
					}
				}
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
		if err := rows.Scan([]interface{}{&c.ID, &c.Name, &c.Description, &c.CreatorID, &c.IsVisible, &c.DirectJoin, &c.MemberPost, &c.MemberPin, &c.MemberCount, &c.PostCount, &c.CreatedAt, &c.UpdatedAt}...); err != nil {
			return nil, fmt.Errorf("failed to scan camp: %w", err)
		}
		c.IsMember = true
		c.MyRole = model.CampRoleMember
		if c.CreatorID == userID {
			c.MyRole = model.CampRoleOwner
		}
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

// Update mutates camp settings. actors with the owner role may change
// everything; admins may change everything except the permission switches
// (those stay owner-only). Pass nil pointers for fields to leave unchanged.
// Returns ErrNotFound when the camp does not exist and ErrForbidden when the
// actor's role is insufficient.
func (r *CampRepository) Update(id, actorID int64, name, description *string, isVisible, directJoin, memberPost, memberPin *bool) error {
	camp, err := r.GetByID(id, actorID)
	if err != nil {
		return err
	}
	if camp == nil {
		return ErrNotFound
	}
	rank := model.CampRoleRank(camp.MyRole)
	if rank < model.CampRoleRank(model.CampRoleAdmin) {
		return ErrForbidden
	}
	if (memberPost != nil || memberPin != nil) && rank < model.CampRoleRank(model.CampRoleOwner) {
		return ErrForbidden
	}
	res, err := database.DB.Exec(
		`UPDATE camps SET
			name = COALESCE($3, name),
			description = COALESCE($4, description),
			is_visible = COALESCE($5, is_visible),
			direct_join = COALESCE($6, direct_join),
			member_post = COALESCE($7, member_post),
			member_pin = COALESCE($8, member_pin),
			updated_at = NOW()
		 WHERE id = $1`,
		id, actorID, name, description, isVisible, directJoin, memberPost, memberPin,
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

// Join adds the user as a plain member; returns false when the camp does not
// exist. Promoting an existing member to admin/owner goes through
// SetMemberRole, not this method.
func (r *CampRepository) Join(campID, userID int64) (bool, error) {
	res, err := database.DB.Exec(
		"INSERT INTO camp_members (camp_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT (camp_id, user_id) DO NOTHING",
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

// GetRole returns the caller's camp role ("" when not a member). The creator
// is always "owner", even without a roster row.
func (r *CampRepository) GetRole(campID, userID int64) (string, error) {
	if userID == 0 {
		return "", nil
	}
	var creatorID int64
	err := database.DB.QueryRow(
		"SELECT creator_id FROM camps WHERE id = $1", campID,
	).Scan(&creatorID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to load camp role: %w", err)
	}
	if creatorID == userID {
		return model.CampRoleOwner, nil
	}
	var role string
	err = database.DB.QueryRow(
		"SELECT role FROM camp_members WHERE camp_id = $1 AND user_id = $2", campID, userID,
	).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read camp role: %w", err)
	}
	return role, nil
}

// ListMembers returns the camp roster with display identities, highest roles
// first. Every member may read the roster; the handler gates access.
func (r *CampRepository) ListMembers(campID int64, limit int) ([]model.CampMember, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := database.DB.Query(
		`SELECT cm.camp_id, cm.user_id, cm.role, u.username, u.nickname, u.avatar_url, u.is_verified, cm.created_at
		 FROM camp_members cm
		 JOIN users u ON u.id = cm.user_id
		 WHERE cm.camp_id = $1
		 ORDER BY CASE cm.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, cm.created_at
		 LIMIT $2`,
		campID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list camp members: %w", err)
	}
	defer rows.Close()

	out := []model.CampMember{}
	for rows.Next() {
		var m model.CampMember
		if err := rows.Scan(&m.CampID, &m.UserID, &m.Role, &m.Username, &m.Nickname, &m.AvatarURL, &m.IsVerified, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("failed to scan camp member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMember inserts a roster row (admin invite). Returns false when the user
// was already a member.
func (r *CampRepository) AddMember(campID, userID int64, role string) (bool, error) {
	res, err := database.DB.Exec(
		"INSERT INTO camp_members (camp_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT (camp_id, user_id) DO NOTHING",
		campID, userID, role,
	)
	if err != nil {
		return false, fmt.Errorf("failed to add camp member: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetMemberRole promotes/demotes an existing member; ErrNotFound when the
// user is not on the roster. The owner's role is immutable here.
func (r *CampRepository) SetMemberRole(campID, userID int64, role string) error {
	res, err := database.DB.Exec(
		"UPDATE camp_members SET role = $3 WHERE camp_id = $1 AND user_id = $2 AND role <> 'owner'",
		campID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("failed to set camp member role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveMember deletes a roster row; ErrNotFound when the user is not a
// member. Unlike Leave it does not special-case the creator — the handler
// enforces who may remove whom.
func (r *CampRepository) RemoveMember(campID, userID int64) error {
	res, err := database.DB.Exec(
		"DELETE FROM camp_members WHERE camp_id = $1 AND user_id = $2", campID, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to remove camp member: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
