package repository

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
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
	post.ReplyCount, err = r.replyCount(post.ID)
	if err != nil {
		return nil, err
	}
	if err := r.populateStats([]model.Post{*post}, []int64{post.ID}); err != nil {
		return nil, err
	}
	return post, nil
}

// GetByID retrieves a post by ID with author info and attachments.
func (r *PostRepository) GetByID(id int64) (*model.Post, error) {
	post := &model.Post{}
	err := database.DB.QueryRow(
		`SELECT p.id, p.user_id, p.content, p.created_at, p.updated_at, u.username, u.nickname, u.avatar_url, u.is_verified
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.id = $1`,
		id,
	).Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified)
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
	post.ReplyCount, err = r.replyCount(post.ID)
	if err != nil {
		return nil, err
	}
	if err := r.populateStats([]model.Post{*post}, []int64{post.ID}); err != nil {
		return nil, err
	}
	return post, nil
}

// GetByIDWithViewer retrieves a post by ID, additionally populating the
// viewer's own reaction so the client can render active reaction state.
func (r *PostRepository) GetByIDWithViewer(id, viewerID int64) (*model.Post, error) {
	post, err := r.GetByID(id)
	if err != nil || post == nil {
		return post, err
	}
	if viewerID > 0 {
		post.MyReaction, err = r.GetMyReaction(post.ID, viewerID)
		if err != nil {
			return nil, err
		}
	}
	return post, nil
}

// RecordView registers a view of a post. The total counter always increments;
// unique views are tracked per (post, viewer_key) so repeat visits by the same
// viewer do not inflate the unique count.
func (r *PostRepository) RecordView(postID int64, viewerKey string) error {
	if _, err := database.DB.Exec(
		"UPDATE posts SET view_count = view_count + 1 WHERE id = $1", postID,
	); err != nil {
		return fmt.Errorf("failed to increment view count: %w", err)
	}
	if _, err := database.DB.Exec(
		"INSERT INTO post_views (post_id, viewer_key) VALUES ($1, $2) ON CONFLICT (post_id, viewer_key) DO NOTHING",
		postID, viewerKey,
	); err != nil {
		return fmt.Errorf("failed to record unique view: %w", err)
	}
	return nil
}

// SetReaction upserts the authenticated user's reaction on a post.
func (r *PostRepository) SetReaction(postID, userID int64, reaction string) error {
	if _, err := database.DB.Exec(
		`INSERT INTO post_reactions (post_id, user_id, reaction) VALUES ($1, $2, $3)
		 ON CONFLICT (post_id, user_id) DO UPDATE SET reaction = EXCLUDED.reaction`,
		postID, userID, reaction,
	); err != nil {
		return fmt.Errorf("failed to set reaction: %w", err)
	}
	return nil
}

// RemoveReaction clears the authenticated user's reaction on a post.
func (r *PostRepository) RemoveReaction(postID, userID int64) error {
	if _, err := database.DB.Exec(
		"DELETE FROM post_reactions WHERE post_id = $1 AND user_id = $2", postID, userID,
	); err != nil {
		return fmt.Errorf("failed to remove reaction: %w", err)
	}
	return nil
}

// GetMyReaction returns the user's reaction for a post ("" when none).
func (r *PostRepository) GetMyReaction(postID, userID int64) (string, error) {
	var reaction string
	err := database.DB.QueryRow(
		"SELECT reaction FROM post_reactions WHERE post_id = $1 AND user_id = $2", postID, userID,
	).Scan(&reaction)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get my reaction: %w", err)
	}
	return reaction, nil
}

// populateStats fills view counts and reaction tallies for the given posts.
func (r *PostRepository) populateStats(posts []model.Post, postIDs []int64) error {
	if len(postIDs) == 0 {
		return nil
	}
	byID := make(map[int64]*model.Post, len(posts))
	for i := range posts {
		byID[posts[i].ID] = &posts[i]
	}

	// Unique views per post.
	rows, err := database.DB.Query(
		"SELECT post_id, COUNT(*) FROM post_views WHERE post_id = ANY($1) GROUP BY post_id",
		pq.Array(postIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to count unique views: %w", err)
	}
	for rows.Next() {
		var postID, count int64
		if err := rows.Scan(&postID, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan unique view count: %w", err)
		}
		if p, ok := byID[postID]; ok {
			p.UniqueViews = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	// Total views stored on the posts table.
	rows, err = database.DB.Query(
		"SELECT id, view_count FROM posts WHERE id = ANY($1)", pq.Array(postIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to query view counts: %w", err)
	}
	for rows.Next() {
		var postID, count int64
		if err := rows.Scan(&postID, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan view count: %w", err)
		}
		if p, ok := byID[postID]; ok {
			p.ViewCount = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	// Reaction tallies per post.
	rows, err = database.DB.Query(
		"SELECT post_id, reaction, COUNT(*) FROM post_reactions WHERE post_id = ANY($1) GROUP BY post_id, reaction",
		pq.Array(postIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to count reactions: %w", err)
	}
	for rows.Next() {
		var postID, count int64
		var reaction string
		if err := rows.Scan(&postID, &reaction, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan reaction count: %w", err)
		}
		if p, ok := byID[postID]; ok {
			if p.Reactions == nil {
				p.Reactions = make(map[string]int64)
			}
			p.Reactions[reaction] = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}
	return nil
}

// List retrieves paginated posts with author info, attachments and reply counts.
func (r *PostRepository) List(page, limit int) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.created_at, p.updated_at, u.username, u.nickname, u.avatar_url, u.is_verified,
		        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count
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
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.ReplyCount); err != nil {
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
	if err := r.populateStats(posts, postIDs); err != nil {
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
		`SELECT p.id, p.user_id, p.content, p.created_at, p.updated_at, u.username, u.nickname, u.avatar_url, u.is_verified,
		        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count
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
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.CreatedAt, &post.UpdatedAt, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.ReplyCount); err != nil {
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
	if err := r.populateStats(posts, postIDs); err != nil {
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

func (r *PostRepository) replyCount(postID int64) (int64, error) {
	var count int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM post_replies WHERE post_id = $1 AND deleted_at IS NULL",
		postID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count replies: %w", err)
	}
	return count, nil
}
