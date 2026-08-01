package repository

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/permission"
)

// PermissionRepository handles permission system database operations.
type PermissionRepository struct{}

// NewPermissionRepository creates a new PermissionRepository.
func NewPermissionRepository() *PermissionRepository {
	return &PermissionRepository{}
}

// GetEffectivePermissions returns the union of all permissions for a user.
func (r *PermissionRepository) GetEffectivePermissions(userID int64) ([]string, error) {
	rows, err := database.DB.Query(
		`SELECT DISTINCT gp.permission_key
		 FROM user_groups ug
		 JOIN group_permissions gp ON gp.group_id = ug.group_id
		 WHERE ug.user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query permissions: %w", err)
	}
	defer rows.Close()

	perms := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("failed to scan permission: %w", err)
		}
		perms = append(perms, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return perms, nil
}

// HasPermission reports whether the user has the given permission.
func (r *PermissionRepository) HasPermission(userID int64, key string) (bool, error) {
	perms, err := r.GetEffectivePermissions(userID)
	if err != nil {
		return false, err
	}
	for _, p := range perms {
		if p == key {
			return true, nil
		}
	}
	return false, nil
}

// GetUserGroups returns the groups a user belongs to.
func (r *PermissionRepository) GetUserGroups(userID int64) ([]model.Group, error) {
	rows, err := database.DB.Query(
		`SELECT g.id, g.name, g.description, g.is_default, g.created_at
		 FROM groups g
		 JOIN user_groups ug ON ug.group_id = g.id
		 WHERE ug.user_id = $1
		 ORDER BY g.is_default DESC, g.id ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query user groups: %w", err)
	}
	defer rows.Close()

	groups := make([]model.Group, 0)
	for rows.Next() {
		var g model.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.IsDefault, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return groups, nil
}

// EnsureUserInDefaultGroup adds the user to the default group (if exists).
func (r *PermissionRepository) EnsureUserInDefaultGroup(userID int64) error {
	_, err := database.DB.Exec(
		`INSERT INTO user_groups (user_id, group_id)
		 SELECT $1, id FROM groups WHERE is_default = TRUE
		 ON CONFLICT DO NOTHING`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("failed to add user to default group: %w", err)
	}
	return nil
}

// ListPermissions returns the full permission catalog.
func (r *PermissionRepository) ListPermissions() ([]model.Permission, error) {
	rows, err := database.DB.Query(
		"SELECT key, name, description FROM permissions ORDER BY key ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query permissions: %w", err)
	}
	defer rows.Close()

	perms := make([]model.Permission, 0)
	for rows.Next() {
		var p model.Permission
		if err := rows.Scan(&p.Key, &p.Name, &p.Description); err != nil {
			return nil, fmt.Errorf("failed to scan permission: %w", err)
		}
		perms = append(perms, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return perms, nil
}

// ListGroups returns all groups with their permission keys.
func (r *PermissionRepository) ListGroups() ([]model.Group, error) {
	rows, err := database.DB.Query(
		"SELECT id, name, description, is_default, created_at FROM groups ORDER BY is_default DESC, id ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query groups: %w", err)
	}
	defer rows.Close()

	groups := make([]model.Group, 0)
	var groupIDs []int64
	for rows.Next() {
		var g model.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.IsDefault, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan group: %w", err)
		}
		groups = append(groups, g)
		groupIDs = append(groupIDs, g.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	permMap, err := r.permissionsForGroups(groupIDs)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		groups[i].Permissions = permMap[groups[i].ID]
	}
	return groups, nil
}

func (r *PermissionRepository) permissionsForGroups(groupIDs []int64) (map[int64][]string, error) {
	result := make(map[int64][]string)
	if len(groupIDs) == 0 {
		return result, nil
	}
	rows, err := database.DB.Query(
		"SELECT group_id, permission_key FROM group_permissions WHERE group_id = ANY($1)",
		pq.Array(groupIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query group permissions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var gid int64
		var key string
		if err := rows.Scan(&gid, &key); err != nil {
			return nil, fmt.Errorf("failed to scan group permission: %w", err)
		}
		result[gid] = append(result[gid], key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return result, nil
}

// GetGroupByID returns a single group with permissions.
func (r *PermissionRepository) GetGroupByID(id int64) (*model.Group, error) {
	var g model.Group
	err := database.DB.QueryRow(
		"SELECT id, name, description, is_default, created_at FROM groups WHERE id = $1",
		id,
	).Scan(&g.ID, &g.Name, &g.Description, &g.IsDefault, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	perms, err := r.permissionsForGroups([]int64{g.ID})
	if err != nil {
		return nil, err
	}
	g.Permissions = perms[g.ID]
	return &g, nil
}

// CreateGroup creates a new group.
func (r *PermissionRepository) CreateGroup(name, description string) (*model.Group, error) {
	var g model.Group
	err := database.DB.QueryRow(
		"INSERT INTO groups (name, description) VALUES ($1, $2) RETURNING id, name, description, is_default, created_at",
		name, description,
	).Scan(&g.ID, &g.Name, &g.Description, &g.IsDefault, &g.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGroupPermissions replaces a group's permission set.
func (r *PermissionRepository) UpdateGroupPermissions(groupID int64, keys []string) error {
	if _, err := database.DB.Exec("DELETE FROM group_permissions WHERE group_id = $1", groupID); err != nil {
		return fmt.Errorf("failed to clear group permissions: %w", err)
	}
	for _, key := range keys {
		if _, err := database.DB.Exec(
			"INSERT INTO group_permissions (group_id, permission_key) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			groupID, key,
		); err != nil {
			return fmt.Errorf("failed to add group permission: %w", err)
		}
	}
	return nil
}

// AddUserToGroup adds a user to a group.
func (r *PermissionRepository) AddUserToGroup(userID, groupID int64) error {
	_, err := database.DB.Exec(
		"INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		userID, groupID,
	)
	if err != nil {
		return fmt.Errorf("failed to add user to group: %w", err)
	}
	return nil
}

// RemoveUserFromGroup removes a user from a group.
func (r *PermissionRepository) RemoveUserFromGroup(userID, groupID int64) error {
	_, err := database.DB.Exec("DELETE FROM user_groups WHERE user_id = $1 AND group_id = $2", userID, groupID)
	if err != nil {
		return fmt.Errorf("failed to remove user from group: %w", err)
	}
	return nil
}

// DeleteGroup removes a group and its memberships.
func (r *PermissionRepository) DeleteGroup(groupID int64) error {
	_, err := database.DB.Exec("DELETE FROM groups WHERE id = $1 AND is_default = FALSE", groupID)
	if err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}
	return nil
}

var _ = permission.DefaultGroupName
