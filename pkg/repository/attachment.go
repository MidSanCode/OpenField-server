package repository

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// AttachmentRepository handles attachment database operations.
type AttachmentRepository struct{}

// NewAttachmentRepository creates a new AttachmentRepository.
func NewAttachmentRepository() *AttachmentRepository {
	return &AttachmentRepository{}
}

// Create inserts an attachment record.
func (r *AttachmentRepository) Create(userID int64, objectKey, originalName, mimeType string, sizeBytes int64, url string) (*model.Attachment, error) {
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"INSERT INTO attachments (user_id, object_key, original_name, mime_type, size_bytes, url) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, user_id, object_key, original_name, mime_type, size_bytes, url, created_at",
		userID, objectKey, originalName, mimeType, sizeBytes, url,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create attachment: %w", err)
	}
	return att, nil
}

// GetByID retrieves an attachment by ID.
func (r *AttachmentRepository) GetByID(id int64) (*model.Attachment, error) {
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, created_at FROM attachments WHERE id = $1",
		id,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return att, nil
}

// GetByObjectKey retrieves an attachment by object key.
func (r *AttachmentRepository) GetByObjectKey(objectKey string) (*model.Attachment, error) {
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, created_at FROM attachments WHERE object_key = $1",
		objectKey,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return att, nil
}

// Delete removes an attachment record by ID.
func (r *AttachmentRepository) Delete(id int64) error {
	_, err := database.DB.Exec("DELETE FROM attachments WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("failed to delete attachment: %w", err)
	}
	return nil
}

// AttachToPost links attachments to a post.
func (r *AttachmentRepository) AttachToPost(postID int64, attachmentIDs []int64) error {
	for _, attID := range attachmentIDs {
		if _, err := database.DB.Exec(
			"INSERT INTO post_attachments (post_id, attachment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			postID, attID,
		); err != nil {
			return fmt.Errorf("failed to attach attachment to post: %w", err)
		}
	}
	return nil
}

// GetByPostID retrieves attachments for a post, ordered by creation.
func (r *AttachmentRepository) GetByPostID(postID int64) ([]model.Attachment, error) {
	rows, err := database.DB.Query(
		`SELECT a.id, a.user_id, a.object_key, a.original_name, a.mime_type, a.size_bytes, a.url, a.created_at
		 FROM attachments a
		 JOIN post_attachments pa ON pa.attachment_id = a.id
		 WHERE pa.post_id = $1
		 ORDER BY a.created_at ASC`,
		postID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query post attachments: %w", err)
	}
	defer rows.Close()

	atts := make([]model.Attachment, 0)
	for rows.Next() {
		var att model.Attachment
		if err := rows.Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		atts = append(atts, att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return atts, nil
}

// AttachmentsForPosts fetches attachments for multiple post IDs.
func (r *AttachmentRepository) AttachmentsForPosts(postIDs []int64) (map[int64][]model.Attachment, error) {
	result := make(map[int64][]model.Attachment)
	if len(postIDs) == 0 {
		return result, nil
	}

	rows, err := database.DB.Query(
		`SELECT pa.post_id, a.id, a.user_id, a.object_key, a.original_name, a.mime_type, a.size_bytes, a.url, a.created_at
		 FROM post_attachments pa
		 JOIN attachments a ON pa.attachment_id = a.id
		 WHERE pa.post_id = ANY($1)
		 ORDER BY a.created_at ASC`,
		pq.Array(postIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query attachments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var postID int64
		var att model.Attachment
		if err := rows.Scan(&postID, &att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		result[postID] = append(result[postID], att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return result, nil
}

// SumSizeByUser returns the total size in bytes of a user's attachments.
func (r *AttachmentRepository) SumSizeByUser(userID int64) (int64, error) {
	var total int64
	err := database.DB.QueryRow(
		"SELECT COALESCE(SUM(size_bytes), 0) FROM attachments WHERE user_id = $1",
		userID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("failed to sum user attachments: %w", err)
	}
	return total, nil
}

// ListByUser lists attachments uploaded by a user.
func (r *AttachmentRepository) ListByUser(userID int64, limit int) ([]model.Attachment, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := database.DB.Query(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, created_at FROM attachments WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2",
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query user attachments: %w", err)
	}
	defer rows.Close()

	atts := make([]model.Attachment, 0)
	for rows.Next() {
		var att model.Attachment
		if err := rows.Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		atts = append(atts, att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return atts, nil
}
