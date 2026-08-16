package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// PostReplyRepository handles post reply database operations.
type PostReplyRepository struct{}

// NewPostReplyRepository creates a new PostReplyRepository.
func NewPostReplyRepository() *PostReplyRepository {
	return &PostReplyRepository{}
}

// Create creates a new reply on a post with optional attachments.
func (r *PostReplyRepository) Create(postID, userID int64, content string, parentID *int64, attachmentIDs []int64) (*model.PostReply, error) {
	reply := &model.PostReply{}
	err := database.DB.QueryRow(
		"INSERT INTO post_replies (post_id, user_id, content, parent_id) VALUES ($1, $2, $3, $4) RETURNING id, post_id, user_id, content, parent_id, created_at, updated_at",
		postID, userID, content, parentID,
	).Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create reply: %w", err)
	}

	if len(attachmentIDs) > 0 {
		for _, attID := range attachmentIDs {
			if _, err := database.DB.Exec(
				"INSERT INTO reply_attachments (reply_id, attachment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
				reply.ID, attID,
			); err != nil {
				return nil, fmt.Errorf("failed to attach attachment to reply: %w", err)
			}
		}
	}

	return r.getWithDetails(reply.ID, 0)
}

// GetByID retrieves a reply by ID.
func (r *PostReplyRepository) GetByID(id int64) (*model.PostReply, error) {
	return r.getWithDetails(id, 0)
}

// GetByIDWithViewer retrieves a reply by ID, populating the viewer's favorite state.
func (r *PostReplyRepository) GetByIDWithViewer(id, viewerID int64) (*model.PostReply, error) {
	return r.getWithDetails(id, viewerID)
}

// ListByPost retrieves replies for a post, excluding soft-deleted ones.
func (r *PostReplyRepository) ListByPost(postID int64, page, limit int, viewerID int64) ([]model.PostReply, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT pr.id, pr.post_id, pr.user_id, pr.content, pr.parent_id, pr.created_at, pr.updated_at, pr.deleted_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`
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
	ptrReplies := make([]*model.PostReply, 0)
	for rows.Next() {
		var reply model.PostReply
		if err := rows.Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt, &reply.DeletedAt, &reply.Username, &reply.Nickname, &reply.AvatarURL, &reply.IsVerified, &reply.MemberLevel, &reply.MemberExpiresAt, &reply.NameColor, &reply.NameColorTo, &reply.NameDynamic, &reply.AvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan reply: %w", err)
		}
		applyMemberStatus(&reply.MemberLevel, &reply.MemberExpiresAt, &reply.MemberActive)
		replies = append(replies, reply)
		ptrReplies = append(ptrReplies, &replies[len(replies)-1])
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateDetails(ptrReplies); err != nil {
		return nil, err
	}
	if err := r.populateFavorited(ptrReplies, viewerID); err != nil {
		return nil, err
	}
	return replies, nil
}

// Update updates a reply's content and attachments (only by owner).
func (r *PostReplyRepository) Update(id, userID int64, content string, attachmentIDs []int64) (*model.PostReply, error) {
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

	if err := r.ReplaceAttachments(id, attachmentIDs); err != nil {
		return nil, err
	}
	return r.GetByID(id)
}

// ReplaceAttachments replaces the attachments linked to a reply.
func (r *PostReplyRepository) ReplaceAttachments(replyID int64, attachmentIDs []int64) error {
	if _, err := database.DB.Exec("DELETE FROM reply_attachments WHERE reply_id = $1", replyID); err != nil {
		return fmt.Errorf("failed to delete reply attachments: %w", err)
	}
	for _, attID := range attachmentIDs {
		if _, err := database.DB.Exec(
			"INSERT INTO reply_attachments (reply_id, attachment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			replyID, attID,
		); err != nil {
			return fmt.Errorf("failed to attach attachment to reply: %w", err)
		}
	}
	return nil
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

// getWithDetails fetches a reply with author info, parent preview and attachments.
func (r *PostReplyRepository) getWithDetails(id, viewerID int64) (*model.PostReply, error) {
	reply := &model.PostReply{}
	err := database.DB.QueryRow(
		`SELECT pr.id, pr.post_id, pr.user_id, pr.content, pr.parent_id, pr.created_at, pr.updated_at, pr.deleted_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`
		 FROM post_replies pr
		 JOIN users u ON pr.user_id = u.id
		 WHERE pr.id = $1`,
		id,
	).Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt, &reply.DeletedAt, &reply.Username, &reply.Nickname, &reply.AvatarURL, &reply.IsVerified, &reply.MemberLevel, &reply.MemberExpiresAt, &reply.NameColor, &reply.NameColorTo, &reply.NameDynamic, &reply.AvatarFrame)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get reply: %w", err)
	}
	applyMemberStatus(&reply.MemberLevel, &reply.MemberExpiresAt, &reply.MemberActive)

	if err := r.populateDetails([]*model.PostReply{reply}); err != nil {
		return nil, err
	}
	if err := r.populateFavorited([]*model.PostReply{reply}, viewerID); err != nil {
		return nil, err
	}
	return reply, nil
}

// FavoriteReply records the user's favorite on a reply (idempotent).
func (r *PostReplyRepository) FavoriteReply(replyID, userID int64) error {
	if _, err := database.DB.Exec(
		"INSERT INTO reply_favorites (reply_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		replyID, userID,
	); err != nil {
		return fmt.Errorf("failed to favorite reply: %w", err)
	}
	return nil
}

// UnfavoriteReply removes the user's favorite from a reply (idempotent).
func (r *PostReplyRepository) UnfavoriteReply(replyID, userID int64) error {
	if _, err := database.DB.Exec(
		"DELETE FROM reply_favorites WHERE reply_id = $1 AND user_id = $2",
		replyID, userID,
	); err != nil {
		return fmt.Errorf("failed to unfavorite reply: %w", err)
	}
	return nil
}

// IsReplyFavorited reports whether the user favorited the given reply.
func (r *PostReplyRepository) IsReplyFavorited(replyID, userID int64) (bool, error) {
	var exists bool
	err := database.DB.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM reply_favorites WHERE reply_id = $1 AND user_id = $2)",
		replyID, userID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check reply favorite: %w", err)
	}
	return exists, nil
}

// ListFavoriteReplies retrieves the replies favorited by the given user.
func (r *PostReplyRepository) ListFavoriteReplies(userID int64, page, limit int) ([]model.PostReply, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT pr.id, pr.post_id, pr.user_id, pr.content, pr.parent_id, pr.created_at, pr.updated_at, pr.deleted_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`
		 FROM post_replies pr
		 JOIN users u ON pr.user_id = u.id
		 JOIN reply_favorites fv ON fv.reply_id = pr.id
		 WHERE fv.user_id = $1
		 ORDER BY fv.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list favorite replies: %w", err)
	}
	defer rows.Close()

	replies := make([]model.PostReply, 0)
	ptrReplies := make([]*model.PostReply, 0)
	for rows.Next() {
		var reply model.PostReply
		if err := rows.Scan(&reply.ID, &reply.PostID, &reply.UserID, &reply.Content, &reply.ParentID, &reply.CreatedAt, &reply.UpdatedAt, &reply.DeletedAt, &reply.Username, &reply.Nickname, &reply.AvatarURL, &reply.IsVerified, &reply.MemberLevel, &reply.MemberExpiresAt, &reply.NameColor, &reply.NameColorTo, &reply.NameDynamic, &reply.AvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan reply: %w", err)
		}
		applyMemberStatus(&reply.MemberLevel, &reply.MemberExpiresAt, &reply.MemberActive)
		reply.Favorited = true
		replies = append(replies, reply)
		ptrReplies = append(ptrReplies, &replies[len(replies)-1])
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateDetails(ptrReplies); err != nil {
		return nil, err
	}
	if err := r.populateFavorited(ptrReplies, userID); err != nil {
		return nil, err
	}
	return replies, nil
}

// populateFavorited marks which of the given replies the viewer favorited and
// fills total favorite counts.
func (r *PostReplyRepository) populateFavorited(replies []*model.PostReply, viewerID int64) error {
	if len(replies) == 0 {
		return nil
	}
	replyIDs := make([]int64, 0, len(replies))
	byID := make(map[int64]*model.PostReply, len(replies))
	for _, rp := range replies {
		replyIDs = append(replyIDs, rp.ID)
		byID[rp.ID] = rp
	}

	rows, err := database.DB.Query(
		"SELECT reply_id, COUNT(*) FROM reply_favorites WHERE reply_id = ANY($1) GROUP BY reply_id",
		pq.Array(replyIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to count reply favorites: %w", err)
	}
	for rows.Next() {
		var replyID, count int64
		if err := rows.Scan(&replyID, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan reply favorite count: %w", err)
		}
		if rp, ok := byID[replyID]; ok {
			rp.FavoriteCount = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	if viewerID > 0 {
		favRows, err := database.DB.Query(
			"SELECT reply_id FROM reply_favorites WHERE reply_id = ANY($1) AND user_id = $2",
			pq.Array(replyIDs), viewerID,
		)
		if err != nil {
			return fmt.Errorf("failed to query reply favorites: %w", err)
		}
		for favRows.Next() {
			var replyID int64
			if err := favRows.Scan(&replyID); err != nil {
				favRows.Close()
				return fmt.Errorf("failed to scan reply favorite: %w", err)
			}
			if rp, ok := byID[replyID]; ok {
				rp.Favorited = true
			}
		}
		favRows.Close()
		if err := favRows.Err(); err != nil {
			return fmt.Errorf("rows error: %w", err)
		}
	}
	return nil
}

// populateDetails fills parent preview and attachments for the given replies.
func (r *PostReplyRepository) populateDetails(replies []*model.PostReply) error {
	parentIDs := make(map[int64]struct{})
	for _, rp := range replies {
		if rp.ParentID != nil {
			parentIDs[*rp.ParentID] = struct{}{}
		}
	}

	if len(parentIDs) > 0 {
		ids := make([]int64, 0, len(parentIDs))
		for id := range parentIDs {
			ids = append(ids, id)
		}
		rows, err := database.DB.Query(
			`SELECT pr.id, pr.content, pr.deleted_at,
			        COALESCE(NULLIF(u.nickname, ''), u.username)
			 FROM post_replies pr
			 JOIN users u ON pr.user_id = u.id
			 WHERE pr.id = ANY($1)`,
			pq.Array(ids),
		)
		if err != nil {
			return fmt.Errorf("failed to query parent replies: %w", err)
		}
		parentPreview := make(map[int64]struct {
			Content string
			Name    string
		})
		for rows.Next() {
			var id int64
			var content, name string
			var deletedAt *time.Time
			if err := rows.Scan(&id, &content, &deletedAt, &name); err != nil {
				rows.Close()
				return fmt.Errorf("failed to scan parent reply: %w", err)
			}
			if deletedAt == nil {
				parentPreview[id] = struct {
					Content string
					Name    string
				}{content, name}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows error: %w", err)
		}
		for _, rp := range replies {
			if rp.ParentID != nil {
				if p, ok := parentPreview[*rp.ParentID]; ok {
					rp.ParentContent = p.Content
					rp.ParentName = p.Name
				}
			}
		}
	}

	replyIDs := make([]int64, 0, len(replies))
	for _, rp := range replies {
		replyIDs = append(replyIDs, rp.ID)
	}
	if len(replyIDs) == 0 {
		return nil
	}

	attRows, err := database.DB.Query(
		`SELECT ra.reply_id, a.id, a.user_id, a.original_name, a.mime_type, a.size_bytes, a.url, a.thumb_url, a.created_at
		 FROM reply_attachments ra
		 JOIN attachments a ON ra.attachment_id = a.id
		 WHERE ra.reply_id = ANY($1)
		 ORDER BY a.created_at ASC`,
		pq.Array(replyIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to query reply attachments: %w", err)
	}
	defer attRows.Close()

	attachments := make(map[int64][]model.Attachment)
	for attRows.Next() {
		var replyID int64
		var a model.Attachment
		if err := attRows.Scan(&replyID, &a.ID, &a.UserID, &a.OriginalName, &a.MimeType, &a.SizeBytes, &a.URL, &a.ThumbURL, &a.CreatedAt); err != nil {
			return fmt.Errorf("failed to scan reply attachment: %w", err)
		}
		attachments[replyID] = append(attachments[replyID], a)
	}
	if err := attRows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}
	for _, rp := range replies {
		rp.Attachments = attachments[rp.ID]
	}
	return nil
}
