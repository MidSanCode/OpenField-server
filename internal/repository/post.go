package repository

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/internal/database"
	"github.com/openfield/server/internal/model"
)

// PostRepository handles post-related database operations.
type PostRepository struct {
	attachmentRepo *AttachmentRepository
}

// NewPostRepository creates a new PostRepository.
func NewPostRepository() *PostRepository {
	return &PostRepository{
		attachmentRepo: NewAttachmentRepository(),
	}
}

// Create creates a new post with optional attachments.
func (r *PostRepository) Create(userID int64, content string, attachmentIDs []int64) (*model.Post, error) {
	post := &model.Post{}
	err := database.DB.QueryRow(
		"INSERT INTO posts (user_id, content) VALUES ($1, $2) RETURNING id, user_id, content, created_at, updated_at",
		userID, content,
	).Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create post: %w", err)
	}

	if len(attachmentIDs) > 0 {
		if err := r.attachmentRepo.AttachToPost(post.ID, attachmentIDs); err != nil {
			return nil, err
		}
	}

	post.Attachments, err = r.attachmentRepo.GetByPostID(post.ID)
	if err != nil {
		return nil, err
	}
	return post, nil
}

// GetByID retrieves a post by ID with author info and attachments.
func (r *PostRepository) GetByID(id int64) (*model.Post, error) {
	post := &model.Post{}
	err := database.DB.QueryRow(
		`SELECT p.id, p.user_id, p.content, p.created_at, p.updated_at, u.username, u.nickname, u.avatar_url
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.id = $1`,
		id,
	).Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt, &post.Username, &post.Nickname, &post.AvatarURL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	post.Attachments, err = r.attachmentRepo.GetByPostID(post.ID)
	if err != nil {
		return nil, err
	}
	return post, nil
}

// List retrieves paginated posts with author info and attachments.
func (r *PostRepository) List(page, limit int) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.created_at, p.updated_at, u.username, u.nickname, u.avatar_url
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 ORDER BY p.created_at DESC
		 LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list posts: %w", err)
	}
	defer rows.Close()

	posts := make([]model.Post, 0)
	var postIDs []int64
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt, &post.Username, &post.Nickname, &post.AvatarURL); err != nil {
			return nil, fmt.Errorf("failed to scan post: %w", err)
		}
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(posts, postIDs); err != nil {
		return nil, err
	}
	return posts, nil
}

// ListByUser retrieves paginated posts by a specific user.
func (r *PostRepository) ListByUser(userID int64, page, limit int) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.created_at, p.updated_at, u.username, u.nickname, u.avatar_url
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.user_id = $1
		 ORDER BY p.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list user posts: %w", err)
	}
	defer rows.Close()

	posts := make([]model.Post, 0)
	var postIDs []int64
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt, &post.Username, &post.Nickname, &post.AvatarURL); err != nil {
			return nil, fmt.Errorf("failed to scan post: %w", err)
		}
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(posts, postIDs); err != nil {
		return nil, err
	}
	return posts, nil
}

func (r *PostRepository) populateAttachments(posts []model.Post, postIDs []int64) error {
	if len(postIDs) == 0 {
		return nil
	}
	attachments, err := r.attachmentRepo.AttachmentsForPosts(postIDs)
	if err != nil {
		return err
	}
	for i := range posts {
		posts[i].Attachments = attachments[posts[i].ID]
	}
	return nil
}

// Delete deletes a post by ID (only by owner).
func (r *PostRepository) Delete(id, userID int64) error {
	result, err := database.DB.Exec("DELETE FROM posts WHERE id = $1 AND user_id = $2", id, userID)
	if err != nil {
		return fmt.Errorf("failed to delete post: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Update updates a post's content and attachments (only by owner).
func (r *PostRepository) Update(id, userID int64, content string, attachmentIDs []int64) (*model.Post, error) {
	result, err := database.DB.Exec("UPDATE posts SET content = $3, updated_at = NOW() WHERE id = $1 AND user_id = $2", id, userID, content)
	if err != nil {
		return nil, fmt.Errorf("failed to update post: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}

	if err := r.ReplaceAttachments(id, attachmentIDs); err != nil {
		return nil, err
	}

	return r.GetByID(id)
}

// DeleteAttachmentsForPost removes all attachment links for a post.
func (r *PostRepository) DeleteAttachmentsForPost(postID int64) error {
	if _, err := database.DB.Exec("DELETE FROM post_attachments WHERE post_id = $1", postID); err != nil {
		return fmt.Errorf("failed to delete post attachments: %w", err)
	}
	return nil
}

// ReplaceAttachments replaces the attachments linked to a post.
func (r *PostRepository) ReplaceAttachments(postID int64, attachmentIDs []int64) error {
	if err := r.DeleteAttachmentsForPost(postID); err != nil {
		return err
	}
	if len(attachmentIDs) > 0 {
		if err := r.attachmentRepo.AttachToPost(postID, attachmentIDs); err != nil {
			return err
		}
	}
	return nil
}
