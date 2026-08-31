package repository

import (
	"database/sql"
	"fmt"
	"time"

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
func (r *AttachmentRepository) Create(userID int64, objectKey, originalName, mimeType string, sizeBytes int64, url, thumbURL, visibility, bucket, sha256 string) (*model.Attachment, error) {
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"INSERT INTO attachments (user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, sha256) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id, user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, sha256, created_at",
		userID, objectKey, originalName, mimeType, sizeBytes, url, thumbURL, visibility, bucket, sha256,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.SHA256, &att.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create attachment: %w", err)
	}
	return att, nil
}

// GetByHash returns the most recent public attachment (or one owned by the
// given user) whose content hash matches. It backs upload deduplication: an
// identical file already in the cloud is reused instead of stored again.
func (r *AttachmentRepository) GetByHash(hash string, userID int64) (*model.Attachment, error) {
	if hash == "" {
		return nil, nil
	}
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, sha256, created_at FROM attachments WHERE sha256 = $1 AND (visibility = 'public' OR user_id = $2) ORDER BY id DESC LIMIT 1",
		hash, userID,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.SHA256, &att.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return att, nil
}

// GetByID retrieves an attachment by ID.
func (r *AttachmentRepository) GetByID(id int64) (*model.Attachment, error) {
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, created_at FROM attachments WHERE id = $1",
		id,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return att, nil
}

// GetByIDsOwnedBy returns the attachments matching the given ids that are
// owned by userID. Mismatched ids are silently skipped so callers can use
// the returned count to detect "any missing?" without inspecting the input.
func (r *AttachmentRepository) GetByIDsOwnedBy(userID int64, ids []int64) ([]model.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := database.DB.Query(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, created_at FROM attachments WHERE user_id = $1 AND id = ANY($2)",
		userID, pq.Array(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query attachments: %w", err)
	}
	defer rows.Close()
	atts := make([]model.Attachment, 0)
	for rows.Next() {
		var att model.Attachment
		if err := rows.Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		atts = append(atts, att)
	}
	return atts, rows.Err()
}

// GetByObjectKey retrieves an attachment by object key.
func (r *AttachmentRepository) GetByObjectKey(objectKey string) (*model.Attachment, error) {
	att := &model.Attachment{}
	err := database.DB.QueryRow(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, created_at FROM attachments WHERE object_key = $1",
		objectKey,
	).Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.CreatedAt)
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
		`SELECT a.id, a.user_id, a.object_key, a.original_name, a.mime_type, a.size_bytes, a.url, a.thumb_url, a.bucket, a.created_at
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
		if err := rows.Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Bucket, &att.CreatedAt); err != nil {
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
		`SELECT pa.post_id, a.id, a.user_id, a.object_key, a.original_name, a.mime_type, a.size_bytes, a.url, a.thumb_url, a.visibility, a.bucket, a.created_at
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
		if err := rows.Scan(&postID, &att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.CreatedAt); err != nil {
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

// BucketUsage aggregates attachment count and size per storage bucket for a
// single user.
type BucketUsage struct {
	Bucket    string `json:"bucket"`
	Count     int64  `json:"count"`
	SizeBytes int64  `json:"size_bytes"`
}

// UsageByUser returns total count/size and a per-bucket breakdown of the
// user's attachments, newest-first within each bucket. Backs the client's
// storage statistics view.
func (r *AttachmentRepository) UsageByUser(userID int64) (total BucketUsage, buckets []BucketUsage, err error) {
	rows, err := database.DB.Query(
		"SELECT COALESCE(bucket, ''), COUNT(*), COALESCE(SUM(size_bytes), 0) FROM attachments WHERE user_id = $1 GROUP BY bucket ORDER BY SUM(size_bytes) DESC",
		userID,
	)
	if err != nil {
		return total, nil, fmt.Errorf("failed to aggregate user usage: %w", err)
	}
	defer rows.Close()

	buckets = make([]BucketUsage, 0)
	for rows.Next() {
		var b BucketUsage
		if err := rows.Scan(&b.Bucket, &b.Count, &b.SizeBytes); err != nil {
			return total, nil, fmt.Errorf("failed to scan bucket usage: %w", err)
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return total, nil, fmt.Errorf("rows error: %w", err)
	}
	for _, b := range buckets {
		total.Count += b.Count
		total.SizeBytes += b.SizeBytes
	}
	return total, buckets, nil
}

// ListByUser lists attachments uploaded by a user.
func (r *AttachmentRepository) ListByUser(userID int64, limit int) ([]model.Attachment, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := database.DB.Query(
		"SELECT id, user_id, object_key, original_name, mime_type, size_bytes, url, thumb_url, visibility, bucket, created_at FROM attachments WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2",
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query user attachments: %w", err)
	}
	defer rows.Close()

	atts := make([]model.Attachment, 0)
	for rows.Next() {
		var att model.Attachment
		if err := rows.Scan(&att.ID, &att.UserID, &att.ObjectKey, &att.OriginalName, &att.MimeType, &att.SizeBytes, &att.URL, &att.ThumbURL, &att.Visibility, &att.Bucket, &att.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		atts = append(atts, att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return atts, nil
}

// RecyclableAttachment is an uploaded file that never landed anywhere (no
// post, reply, or chat message references it) and is old enough to recycle.
type RecyclableAttachment struct {
	ID        int64
	Bucket    string
	ObjectKey string
}

// ListRecyclableAttachments returns unreferenced attachments older than
// minAgeSeconds, restricted to the given logical buckets. Profiles are
// protected too: avatar/banner URLs in users are excluded from cleanup.
func (r *AttachmentRepository) ListRecyclableAttachments(buckets []string, minAgeSeconds int, limit int) ([]RecyclableAttachment, error) {
	query := `SELECT a.id, COALESCE(a.bucket, ''), a.object_key
		FROM attachments a
		WHERE a.created_at < NOW() - ($1 || ' seconds')::interval
		  AND NOT EXISTS (SELECT 1 FROM post_attachments pa WHERE pa.attachment_id = a.id)
		  AND NOT EXISTS (SELECT 1 FROM reply_attachments ra WHERE ra.attachment_id = a.id)
		  AND NOT EXISTS (SELECT 1 FROM message_attachments ma WHERE ma.attachment_id = a.id)
		  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_url = a.url OR u.banner_url = a.url)`
	args := []interface{}{minAgeSeconds}
	if len(buckets) > 0 {
		query += fmt.Sprintf(" AND a.bucket = ANY($%d)", len(args)+1)
		args = append(args, pq.Array(buckets))
	}
	query += fmt.Sprintf(" ORDER BY a.id LIMIT %d", limit)
	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list recyclable attachments: %w", err)
	}
	defer rows.Close()

	items := []RecyclableAttachment{}
	for rows.Next() {
		var item RecyclableAttachment
		if err := rows.Scan(&item.ID, &item.Bucket, &item.ObjectKey); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteAttachmentRow removes the attachment row after its object has been
// deleted from the store.
func (r *AttachmentRepository) DeleteByID(id int64) error {
	_, err := database.DB.Exec("DELETE FROM attachments WHERE id = $1", id)
	return err
}

// ArmBurn stamps the burn-after-view deadline of an attachment the first time
// someone OTHER than the uploader views it. The conditional UPDATE makes the
// arm idempotent and race-safe: concurrent viewers collapse into one stamp.
// Returns the (possibly pre-existing) burn_at. When the attachment does not
// exist, or belongs to the caller (the uploader never triggers the burn), the
// returned found flag is false.
func (r *AttachmentRepository) ArmBurn(id, viewerID int64, burnSeconds int) (burnAt *time.Time, found bool, err error) {
	res, err := database.DB.Exec(
		`UPDATE attachments
		    SET burn_at = NOW() + ($2 * INTERVAL '1 second')
		  WHERE id = $1 AND user_id <> $3 AND burn_at IS NULL`,
		id, burnSeconds, viewerID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to arm attachment burn: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		var t time.Time
		if err := database.DB.QueryRow(`SELECT burn_at FROM attachments WHERE id = $1`, id).Scan(&t); err != nil {
			return nil, false, fmt.Errorf("failed to load armed burn_at: %w", err)
		}
		return &t, true, nil
	}
	// No row updated: either someone else armed it first, or it does not
	// exist / belongs to the caller. Report the existing state, if any.
	var existing *time.Time
	err = database.DB.QueryRow(`SELECT burn_at FROM attachments WHERE id = $1 AND user_id <> $2`, id, viewerID).Scan(&existing)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to load attachment burn state: %w", err)
	}
	return existing, true, nil
}

// SweepDueBurnedAttachments returns the attachments whose burn_after-view
// deadline has passed. The caller deletes each object from the store and then
// the row via DeleteByID. burnedAt is stamped with the stored burn_at (not
// now()) so callers can reason about timing from the returned value.
func (r *AttachmentRepository) SweepDueBurnedAttachments(limit int) ([]model.Attachment, error) {
	rows, err := database.DB.Query(
		`SELECT id, user_id, object_key, bucket, url, COALESCE(thumb_url, ''), burn_at
		   FROM attachments
		  WHERE burn_at IS NOT NULL AND burn_at <= NOW()
		  ORDER BY burn_at ASC
		  LIMIT $1`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query burned attachments: %w", err)
	}
	defer rows.Close()

	due := make([]model.Attachment, 0)
	for rows.Next() {
		var a model.Attachment
		if err := rows.Scan(&a.ID, &a.UserID, &a.ObjectKey, &a.Bucket, &a.URL, &a.ThumbURL, &a.BurnAt); err != nil {
			return nil, fmt.Errorf("failed to scan burned attachment: %w", err)
		}
		due = append(due, a)
	}
	return due, rows.Err()
}
