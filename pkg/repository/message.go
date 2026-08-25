package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// MessageRepository handles chat message database operations.
type MessageRepository struct{}

// NewMessageRepository creates a new MessageRepository.
func NewMessageRepository() *MessageRepository {
	return &MessageRepository{}
}

// parseMentions decodes the JSONB mentions column (a JSON array of user ids)
// into a []int64. Empty/missing data yields an empty, non-nil slice.
func parseMentions(data []byte) ([]int64, error) {
	if len(data) == 0 {
		return []int64{}, nil
	}
	var mentions []int64
	if err := json.Unmarshal(data, &mentions); err != nil {
		return nil, err
	}
	if mentions == nil {
		mentions = []int64{}
	}
	return mentions, nil
}

// Create inserts a new message with optional attachments and returns it with sender info.
// [mentions] is the list of explicitly mentioned user IDs, plus -1 when
// @everyone is used; an empty/nil slice means no mentions. When [checkID] > 0
// the message carries that check (kind = 'check').
func (r *MessageRepository) Create(conversationID, senderID int64, content string, replyToID *int64, attachmentIDs []int64, mentions []int64, checkID int64) (*model.Message, error) {
	if mentions == nil {
		mentions = []int64{}
	}
	kind := "text"
	if checkID > 0 {
		kind = "check"
	}
	// The mentions column is JSONB. Store it as a JSON array of user ids so it
	// round-trips with the JSON decoder below (pq.Array/pq.Int64Array expect a
	// Postgres "{...}" literal, which does not match the stored JSON).
	mentionsJSON, err := json.Marshal(mentions)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mentions: %w", err)
	}
	msg := &model.Message{}
	if err := database.DB.QueryRow(
		`INSERT INTO messages (conversation_id, sender_id, kind, content, reply_to_id, mentions, check_id)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb, NULLIF($7, 0))
		 RETURNING id, conversation_id, sender_id, content, reply_to_id, edited_at, deleted_at, created_at`,
		conversationID, senderID, kind, content, replyToID, string(mentionsJSON), checkID,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt); err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}
	msg.Mentions = append([]int64(nil), mentions...)

	if len(attachmentIDs) > 0 {
		for _, attID := range attachmentIDs {
			if _, err := database.DB.Exec(
				"INSERT INTO message_attachments (message_id, attachment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
				msg.ID, attID,
			); err != nil {
				return nil, fmt.Errorf("failed to attach message attachment: %w", err)
			}
		}
	}

	if _, err := database.DB.Exec(
		"UPDATE conversations SET updated_at = NOW() WHERE id = $1",
		conversationID,
	); err != nil {
		return nil, fmt.Errorf("failed to touch conversation: %w", err)
	}

	full, err := r.getWithSender(msg.ID)
	if err != nil {
		return nil, err
	}
	// Preserve the mentions we just inserted even if the SELECT filter
	// strips them for some reason.
	if len(full.Mentions) == 0 && len(msg.Mentions) > 0 {
		full.Mentions = msg.Mentions
	}
	return full, nil
}

// ListByConversation retrieves messages for a conversation (newest last).
func (r *MessageRepository) ListByConversation(conversationID int64, beforeID int64, limit int) ([]model.Message, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := `SELECT m.id, m.conversation_id, m.sender_id, m.kind, COALESCE(m.check_id, 0) AS check_id, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at, m.mentions,
	                 COALESCE(NULLIF(u.nickname, ''), u.username) AS sender_name, u.avatar_url, u.is_verified`+authorMemberCols+`
	          FROM messages m
	          JOIN users u ON m.sender_id = u.id
	          WHERE m.conversation_id = $1 AND m.deleted_at IS NULL`
	args := []interface{}{conversationID}
	if beforeID > 0 {
		query += " AND m.id < $2"
		args = append(args, beforeID)
	}
	query += " ORDER BY m.created_at DESC, m.id DESC LIMIT " + fmt.Sprintf("%d", limit)

	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	defer rows.Close()

	msgs := make([]model.Message, 0)
	var msgIDs []int64
	for rows.Next() {
		var m model.Message
		var mentionsData []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.Kind, &m.CheckID, &m.Content, &m.ReplyToID, &m.EditedAt, &m.DeletedAt, &m.CreatedAt, &mentionsData, &m.SenderName, &m.SenderAvatar, &m.SenderVerified, &m.SenderMemberLevel, &m.SenderMemberExpiresAt, &m.SenderNameColor, &m.SenderNameColorTo, &m.SenderNameDynamic, &m.SenderNameColors, &m.SenderNameGradientDirection, &m.SenderAvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		applyMemberStatus(&m.SenderMemberLevel, &m.SenderMemberExpiresAt, &m.SenderMemberActive)
		m.Mentions, err = parseMentions(mentionsData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse message mentions: %w", err)
		}
		msgs = append(msgs, m)
		msgIDs = append(msgIDs, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(msgs, msgIDs); err != nil {
		return nil, err
	}

	if err := r.populateReplyPreviews(msgs); err != nil {
		return nil, err
	}

	// reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// MessageSearchFilter narrows a chat history search. Zero values are ignored,
// so callers only set the criteria they want to enforce. Content and file name
// filters use case-insensitive substring matching; From/To bound CreatedAt
// inclusively; HasAttachments requires at least one attachment.
type MessageSearchFilter struct {
	Content        string
	SenderID       int64
	From           *time.Time
	To             *time.Time
	HasAttachments bool
	FileName       string
}

// Search retrieves messages in a conversation matching the filter, newest
// first (opposite of ListByConversation, which pages chronologically). Only
// regular text messages are considered; system notices are not searchable.
func (r *MessageRepository) Search(conversationID int64, f MessageSearchFilter, limit int) ([]model.Message, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := `SELECT m.id, m.conversation_id, m.sender_id, m.kind, COALESCE(m.check_id, 0) AS check_id, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at, m.mentions,
	                 COALESCE(NULLIF(u.nickname, ''), u.username) AS sender_name, u.avatar_url, u.is_verified`+authorMemberCols+`
	          FROM messages m
	          JOIN users u ON m.sender_id = u.id
	          WHERE m.conversation_id = $1 AND m.deleted_at IS NULL AND m.kind = 'text'`
	args := []interface{}{conversationID}
	if f.Content != "" {
		args = append(args, "%"+f.Content+"%")
		query += fmt.Sprintf(" AND m.content ILIKE $%d", len(args))
	}
	if f.SenderID > 0 {
		args = append(args, f.SenderID)
		query += fmt.Sprintf(" AND m.sender_id = $%d", len(args))
	}
	if f.From != nil {
		args = append(args, *f.From)
		query += fmt.Sprintf(" AND m.created_at >= $%d", len(args))
	}
	if f.To != nil {
		args = append(args, *f.To)
		query += fmt.Sprintf(" AND m.created_at <= $%d", len(args))
	}
	if f.HasAttachments || f.FileName != "" {
		cond := "EXISTS (SELECT 1 FROM message_attachments ma JOIN attachments a ON ma.attachment_id = a.id WHERE ma.message_id = m.id"
		if f.FileName != "" {
			args = append(args, "%"+f.FileName+"%")
			cond += fmt.Sprintf(" AND a.original_name ILIKE $%d", len(args))
		}
		query += " AND " + cond + ")"
	}
	query += " ORDER BY m.created_at DESC, m.id DESC LIMIT " + fmt.Sprintf("%d", limit)

	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}
	defer rows.Close()

	msgs := make([]model.Message, 0)
	var msgIDs []int64
	for rows.Next() {
		var m model.Message
		var mentionsData []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.Kind, &m.CheckID, &m.Content, &m.ReplyToID, &m.EditedAt, &m.DeletedAt, &m.CreatedAt, &mentionsData, &m.SenderName, &m.SenderAvatar, &m.SenderVerified, &m.SenderMemberLevel, &m.SenderMemberExpiresAt, &m.SenderNameColor, &m.SenderNameColorTo, &m.SenderNameDynamic, &m.SenderNameColors, &m.SenderNameGradientDirection, &m.SenderAvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		applyMemberStatus(&m.SenderMemberLevel, &m.SenderMemberExpiresAt, &m.SenderMemberActive)
		m.Mentions, err = parseMentions(mentionsData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse message mentions: %w", err)
		}
		msgs = append(msgs, m)
		msgIDs = append(msgIDs, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(msgs, msgIDs); err != nil {
		return nil, err
	}

	if err := r.populateReplyPreviews(msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *MessageRepository) getWithSender(id int64) (*model.Message, error) {
	msg := &model.Message{}
	var mentionsData []byte
	err := database.DB.QueryRow(
		`SELECT m.id, m.conversation_id, m.sender_id, m.kind, COALESCE(m.check_id, 0) AS check_id, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at, m.mentions,
		        COALESCE(NULLIF(u.nickname, ''), u.username) AS sender_name, u.avatar_url, u.is_verified`+authorMemberCols+`
		 FROM messages m
		 JOIN users u ON m.sender_id = u.id
		 WHERE m.id = $1`,
		id,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Kind, &msg.CheckID, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt, &mentionsData, &msg.SenderName, &msg.SenderAvatar, &msg.SenderVerified, &msg.SenderMemberLevel, &msg.SenderMemberExpiresAt, &msg.SenderNameColor, &msg.SenderNameColorTo, &msg.SenderNameDynamic, &msg.SenderNameColors, &msg.SenderNameGradientDirection, &msg.SenderAvatarFrame)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	applyMemberStatus(&msg.SenderMemberLevel, &msg.SenderMemberExpiresAt, &msg.SenderMemberActive)
	msg.Mentions, err = parseMentions(mentionsData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message mentions: %w", err)
	}

	if err := r.populateReplyPreview(msg); err != nil {
		return nil, err
	}

	atts, err := r.attachmentsForMessage(msg.ID)
	if err != nil {
		return nil, err
	}
	msg.Attachments = atts
	return msg, nil
}

// GetByID retrieves a message by ID (including soft-deleted ones, to allow owner checks).
func (r *MessageRepository) GetByID(id int64) (*model.Message, error) {
	msg := &model.Message{}
	err := database.DB.QueryRow(
		`SELECT id, conversation_id, sender_id, kind, content, reply_to_id, edited_at, deleted_at, created_at
		 FROM messages WHERE id = $1`,
		id,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Kind, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// CreateSystem inserts a system message (e.g. "X joined the chat") authored by
// an actor user, and returns it with sender info. System messages carry a kind
// like system.join/system.leave that clients use for localized rendering.
func (r *MessageRepository) CreateSystem(conversationID, actorID int64, kind string) (*model.Message, error) {
	msg := &model.Message{}
	err := database.DB.QueryRow(
		`INSERT INTO messages (conversation_id, sender_id, kind, content)
		 VALUES ($1, $2, $3, '')
		 RETURNING id, conversation_id, sender_id, kind, content, reply_to_id, edited_at, deleted_at, created_at`,
		conversationID, actorID, kind,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Kind, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create system message: %w", err)
	}

	if _, err := database.DB.Exec(
		"UPDATE conversations SET updated_at = NOW() WHERE id = $1",
		conversationID,
	); err != nil {
		return nil, fmt.Errorf("failed to touch conversation: %w", err)
	}

	msg, err = r.getWithSender(msg.ID)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// Update edits a message's content (owner only) and stamps edited_at.
func (r *MessageRepository) Update(id, userID int64, content string) (*model.Message, error) {
	msg, err := r.GetByID(id)
	if err != nil {
		return nil, err
	}
	if msg == nil || msg.SenderID != userID {
		return nil, sql.ErrNoRows
	}
	if msg.DeletedAt != nil {
		return nil, ErrDeletedMessage
	}

	result, err := database.DB.Exec(
		"UPDATE messages SET content = $3, edited_at = NOW() WHERE id = $1 AND sender_id = $2 AND deleted_at IS NULL",
		id, userID, content,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update message: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return r.getWithSender(id)
}

// Delete soft-deletes a message (owner only).
func (r *MessageRepository) Delete(id, userID int64) error {
	msg, err := r.GetByID(id)
	if err != nil {
		return err
	}
	if msg == nil || msg.SenderID != userID {
		return sql.ErrNoRows
	}
	if msg.DeletedAt != nil {
		return ErrDeletedMessage
	}

	result, err := database.DB.Exec(
		"UPDATE messages SET deleted_at = NOW() WHERE id = $1 AND sender_id = $2 AND deleted_at IS NULL",
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// populateReplyPreview fills ReplyToName/ReplyToContent for a single message
// from the message it quotes (content is blanked if the quoted message was
// soft-deleted).
func (r *MessageRepository) populateReplyPreview(msg *model.Message) error {
	if msg.ReplyToID == nil || *msg.ReplyToID <= 0 {
		return nil
	}
	err := database.DB.QueryRow(
		`SELECT CASE WHEN m.deleted_at IS NULL THEN COALESCE(m.content, '') ELSE '' END,
		        COALESCE(NULLIF(u.nickname, ''), u.username)
		 FROM messages m
		 LEFT JOIN users u ON m.sender_id = u.id
		 WHERE m.id = $1`,
		*msg.ReplyToID,
	).Scan(&msg.ReplyToContent, &msg.ReplyToName)
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}

// populateReplyPreviews fills ReplyToName/ReplyToContent for a batch of messages.
func (r *MessageRepository) populateReplyPreviews(msgs []model.Message) error {
	ids := make([]int64, 0, len(msgs))
	seen := make(map[int64]bool)
	for i := range msgs {
		if msgs[i].ReplyToID == nil {
			continue
		}
		id := *msgs[i].ReplyToID
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}

	rows, err := database.DB.Query(
		`SELECT m.id, CASE WHEN m.deleted_at IS NULL THEN COALESCE(m.content, '') ELSE '' END,
		        COALESCE(NULLIF(u.nickname, ''), u.username)
		 FROM messages m
		 LEFT JOIN users u ON m.sender_id = u.id
		 WHERE m.id = ANY($1)`,
		pq.Array(ids),
	)
	if err != nil {
		return fmt.Errorf("failed to query reply previews: %w", err)
	}
	defer rows.Close()

	type preview struct {
		name    string
		content string
	}
	previews := make(map[int64]preview)
	for rows.Next() {
		var id int64
		var p preview
		if err := rows.Scan(&id, &p.content, &p.name); err != nil {
			return fmt.Errorf("failed to scan reply preview: %w", err)
		}
		previews[id] = p
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	for i := range msgs {
		if msgs[i].ReplyToID == nil {
			continue
		}
		if p, ok := previews[*msgs[i].ReplyToID]; ok {
			msgs[i].ReplyToName = p.name
			msgs[i].ReplyToContent = p.content
		}
	}
	return nil
}

func (r *MessageRepository) populateAttachments(msgs []model.Message, msgIDs []int64) error {
	if len(msgIDs) == 0 {
		return nil
	}
	rows, err := database.DB.Query(
		`SELECT ma.message_id, a.id, a.user_id, a.original_name, a.mime_type, a.size_bytes, a.url, a.thumb_url, a.created_at
		 FROM message_attachments ma
		 JOIN attachments a ON ma.attachment_id = a.id
		 WHERE ma.message_id = ANY($1)
		 ORDER BY a.created_at ASC`,
		pq.Array(msgIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to query message attachments: %w", err)
	}
	defer rows.Close()

	attachments := make(map[int64][]model.Attachment)
	for rows.Next() {
		var msgID int64
		var a model.Attachment
		if err := rows.Scan(&msgID, &a.ID, &a.UserID, &a.OriginalName, &a.MimeType, &a.SizeBytes, &a.URL, &a.ThumbURL, &a.CreatedAt); err != nil {
			return fmt.Errorf("failed to scan message attachment: %w", err)
		}
		attachments[msgID] = append(attachments[msgID], a)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}
	for i := range msgs {
		msgs[i].Attachments = attachments[msgs[i].ID]
	}
	return nil
}

func (r *MessageRepository) attachmentsForMessage(msgID int64) ([]model.Attachment, error) {
	rows, err := database.DB.Query(
		`SELECT a.id, a.user_id, a.original_name, a.mime_type, a.size_bytes, a.url, a.thumb_url, a.created_at
		 FROM message_attachments ma
		 JOIN attachments a ON ma.attachment_id = a.id
		 WHERE ma.message_id = $1
		 ORDER BY a.created_at ASC`,
		msgID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query message attachments: %w", err)
	}
	defer rows.Close()

	atts := make([]model.Attachment, 0)
	for rows.Next() {
		var a model.Attachment
		if err := rows.Scan(&a.ID, &a.UserID, &a.OriginalName, &a.MimeType, &a.SizeBytes, &a.URL, &a.ThumbURL, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		atts = append(atts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return atts, nil
}
