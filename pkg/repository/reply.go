package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// PostReplyRepository handles post reply database operations.
type PostReplyRepository struct{}

// NewPostReplyRepository creates a new PostReplyRepository.
func NewPostReplyRepository() *PostReplyRepository {
	return &PostReplyRepository{}
}

// Create creates a new reply on a post.
func (r *PostReplyRepository) Create(postID, userID int64, content string, parentID *int64) (*model.PostReply, error) {
	reply := &model.PostReply{}
	err := database.DB.QueryRow(
		"INSERT INTO post_replies (post_id, user_id, content, parent_id) VALUES ($1, $2, $3, $4) RETURNING id, post_id, user_id, content, parent_id, created_at, updated_at",
		postID, userID, content, parentID,
	).Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create reply: %w", err)
	}
	return reply, nil
}

// GetByID retrieves a reply by ID.
func (r *PostReplyRepository) GetByID(id int64) (*model.PostReply, error) {
	reply := &model.PostReply{}
	err := database.DB.QueryRow(
		`SELECT pr.id, pr.post_id, pr.user_id, pr.content, pr.parent_id, pr.created_at, pr.updated_at, pr.deleted_at,
		        u.username, u.nickname, u.avatar_url
		 FROM post_replies pr
		 JOIN users u ON pr.user_id = u.id
		 WHERE pr.id = $1`,
		id,
	).Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt, &reply.DeletedAt, &reply.Username, &reply.Nickname, &reply.AvatarURL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// ListByPost retrieves replies for a post, excluding soft-deleted ones.
func (r *PostReplyRepository) ListByPost(postID int64, page, limit int) ([]model.PostReply, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT pr.id, pr.post_id, pr.user_id, pr.content, pr.parent_id, pr.created_at, pr.updated_at, pr.deleted_at,
		        u.username, u.nickname, u.avatar_url
		 FROM post_replies pr
		 JOIN users u ON pr.user_id = u.id
		 WHERE pr.post_id = $1 AND pr.deleted_at IS NULL
		 ORDER BY pr.created_at ASC
		 LIMIT $2 OFFSET $3`,
		postID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list replies: %w", err)
	}
	defer rows.Close()

	replies := make([]model.PostReply, 0)
	for rows.Next() {
		var reply model.PostReply
		if err := rows.Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt, &reply.DeletedAt, &reply.Username, &reply.Nickname, &reply.AvatarURL); err != nil {
			return nil, fmt.Errorf("failed to scan reply: %w", err)
		}
		replies = append(replies, reply)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return replies, nil
}

// Update updates a reply's content (only by owner).
func (r *PostReplyRepository) Update(id, userID int64, content string) (*model.PostReply, error) {
	result, err := database.DB.Exec(
		"UPDATE post_replies SET content = $3, updated_at = NOW() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL",
		id, userID, content,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update reply: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return r.GetByID(id)
}

// Delete soft-deletes a reply (only by owner).
func (r *PostReplyRepository) Delete(id, userID int64) error {
	now := time.Now()
	result, err := database.DB.Exec(
		"UPDATE post_replies SET deleted_at = $3, updated_at = NOW() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL",
		id, userID, now,
	)
	if err != nil {
		return fmt.Errorf("failed to delete reply: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
