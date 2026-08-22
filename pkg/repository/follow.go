package repository

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// userColumns joined against user_follows (or any table sharing column names
// such as created_at), qualified with the aliased users table to avoid
// ambiguous column references.
var joinUserColumns = "u." + strings.ReplaceAll(userColumns, ", ", ", u.")

// FollowRepository handles user-to-user follow relationships.
type FollowRepository struct{}

// NewFollowRepository creates a new FollowRepository.
func NewFollowRepository() *FollowRepository {
	return &FollowRepository{}
}

// Follow makes followerID follow followeeID (idempotent).
func (r *FollowRepository) Follow(followerID, followeeID int64) error {
	if followerID == followeeID {
		return ErrNoSuchRow
	}
	if _, err := database.DB.Exec(
		"INSERT INTO user_follows (follower_id, followee_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		followerID, followeeID,
	); err != nil {
		return fmt.Errorf("failed to follow user: %w", err)
	}
	return nil
}

// Unfollow removes the follow relationship (idempotent).
func (r *FollowRepository) Unfollow(followerID, followeeID int64) error {
	if _, err := database.DB.Exec(
		"DELETE FROM user_follows WHERE follower_id = $1 AND followee_id = $2",
		followerID, followeeID,
	); err != nil {
		return fmt.Errorf("failed to unfollow user: %w", err)
	}
	return nil
}

// IsFollowing reports whether followerID currently follows followeeID.
func (r *FollowRepository) IsFollowing(followerID, followeeID int64) (bool, error) {
	var exists bool
	err := database.DB.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM user_follows WHERE follower_id = $1 AND followee_id = $2)",
		followerID, followeeID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check follow status: %w", err)
	}
	return exists, nil
}

// AreMutual reports whether userA and userB follow each other (friends).
func (r *FollowRepository) AreMutual(a, b int64) (bool, error) {
	var exists bool
	err := database.DB.QueryRow(
		`SELECT EXISTS (
			SELECT 1 FROM user_follows f1
			JOIN user_follows f2 ON f1.follower_id = f2.followee_id AND f2.follower_id = f1.followee_id
			WHERE f1.follower_id = $1 AND f1.followee_id = $2
		)`,
		a, b,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check mutual follow status: %w", err)
	}
	return exists, nil
}

// CountFollowers returns how many users follow the given user.
func (r *FollowRepository) CountFollowers(userID int64) (int64, error) {
	var count int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM user_follows WHERE followee_id = $1", userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count followers: %w", err)
	}
	return count, nil
}

// CountFollowing returns how many users the given user follows.
func (r *FollowRepository) CountFollowing(userID int64) (int64, error) {
	var count int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM user_follows WHERE follower_id = $1", userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count following: %w", err)
	}
	return count, nil
}

// ListFollowers returns the users who follow the given user, paginated.
func (r *FollowRepository) ListFollowers(userID int64, page, limit int) ([]model.User, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		"SELECT "+joinUserColumns+` FROM users u
		 JOIN user_follows f ON f.follower_id = u.id
		 WHERE f.followee_id = $1
		 ORDER BY f.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list followers: %w", err)
	}
	defer rows.Close()

	return scanUsers(rows)
}

// ListFollowing returns the users the given user follows, paginated.
func (r *FollowRepository) ListFollowing(userID int64, page, limit int) ([]model.User, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		"SELECT "+joinUserColumns+` FROM users u
		 JOIN user_follows f ON f.followee_id = u.id
		 WHERE f.follower_id = $1
		 ORDER BY f.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list following: %w", err)
	}
	defer rows.Close()

	return scanUsers(rows)
}

// ListFriends returns the users who mutually follow the given user (friends),
// ordered by when the mutual relationship was established.
func (r *FollowRepository) ListFriends(userID int64, page, limit int) ([]model.User, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		"SELECT "+joinUserColumns+` FROM users u
		 JOIN user_follows f1 ON f1.followee_id = u.id
		 JOIN user_follows f2 ON f2.follower_id = u.id AND f2.followee_id = $1
		 WHERE f1.follower_id = $1
		 ORDER BY f1.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list friends: %w", err)
	}
	defer rows.Close()

	return scanUsers(rows)
}

// MutualFollowerIDs returns the ids of every user who mutually follows the
// given user, without pagination. Used to scope realtime pushes of
// friends-only posts.
func (r *FollowRepository) MutualFollowerIDs(userID int64) ([]int64, error) {
	rows, err := database.DB.Query(
		`SELECT f1.follower_id FROM user_follows f1
		 JOIN user_follows f2 ON f1.followee_id = f2.follower_id AND f2.followee_id = f1.follower_id
		 WHERE f1.followee_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list mutual follower ids: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan mutual follower id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanUsers(rows *sql.Rows) ([]model.User, error) {
	users := make([]model.User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, *user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return users, nil
}
