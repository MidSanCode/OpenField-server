package repository

import (
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// GroupAnnouncementRepository handles per-conversation announcements.
type GroupAnnouncementRepository struct{}

// NewGroupAnnouncementRepository creates a new repository.
func NewGroupAnnouncementRepository() *GroupAnnouncementRepository {
	return &GroupAnnouncementRepository{}
}

const announcementCols = `a.id, a.conversation_id, a.creator_id, a.title, a.content, a.created_at, a.updated_at,
		COALESCE(NULLIF(u.nickname, ''), u.username) AS creator_name`


// Create inserts an announcement.
func (r *GroupAnnouncementRepository) Create(convID, creatorID int64, title, content string) (*model.GroupAnnouncement, error) {
	a := &model.GroupAnnouncement{}
	err := database.DB.QueryRow(
		`INSERT INTO group_announcements (conversation_id, creator_id, title, content)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, conversation_id, creator_id, title, content, created_at, updated_at,
		           COALESCE(NULLIF((SELECT nickname FROM users WHERE id = $2), ''), (SELECT username FROM users WHERE id = $2))`,
		convID, creatorID, title, content,
	).Scan(&a.ID, &a.ConversationID, &a.CreatorID, &a.Title, &a.Content, &a.CreatedAt, &a.UpdatedAt, &a.CreatorName)
	if err != nil {
		return nil, fmt.Errorf("failed to create announcement: %w", err)
	}
	return a, nil
}

// ListByConversation returns announcements, newest first.
func (r *GroupAnnouncementRepository) ListByConversation(convID int64, limit int) ([]model.GroupAnnouncement, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := database.DB.Query(
		"SELECT "+announcementCols+" FROM group_announcements a JOIN users u ON u.id = a.creator_id WHERE a.conversation_id = $1 ORDER BY a.created_at DESC LIMIT "+fmt.Sprintf("%d", limit),
		convID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list announcements: %w", err)
	}
	defer rows.Close()
	out := []model.GroupAnnouncement{}
	for rows.Next() {
		a := model.GroupAnnouncement{}
		if err := rows.Scan(&a.ID, &a.ConversationID, &a.CreatorID, &a.Title, &a.Content, &a.CreatedAt, &a.UpdatedAt, &a.CreatorName); err != nil {
			return nil, fmt.Errorf("failed to scan announcement: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Delete removes one announcement.
func (r *GroupAnnouncementRepository) Delete(id, convID int64) error {
	res, err := database.DB.Exec("DELETE FROM group_announcements WHERE id = $1 AND conversation_id = $2", id, convID)
	if err != nil {
		return fmt.Errorf("failed to delete announcement: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GroupTodoRepository handles per-conversation shared checklists.
type GroupTodoRepository struct{}

// NewGroupTodoRepository creates a new repository.
func NewGroupTodoRepository() *GroupTodoRepository {
	return &GroupTodoRepository{}
}

// Create inserts a todo entry.
func (r *GroupTodoRepository) Create(convID, creatorID int64, title string) (*model.GroupTodo, error) {
	t := &model.GroupTodo{}
	err := database.DB.QueryRow(
		`INSERT INTO group_todos (conversation_id, creator_id, title)
		 VALUES ($1, $2, $3)
		 RETURNING id, conversation_id, creator_id, title, done, done_by, created_at, completed_at,
		           COALESCE(NULLIF((SELECT nickname FROM users WHERE id = $2), ''), (SELECT username FROM users WHERE id = $2))`,
		convID, creatorID, title,
	).Scan(&t.ID, &t.ConversationID, &t.CreatorID, &t.Title, &t.Done, &t.DoneBy, &t.CreatedAt, &t.CompletedAt, &t.CreatorName)
	if err != nil {
		return nil, fmt.Errorf("failed to create todo: %w", err)
	}
	return t, nil
}

// ListByConversation returns open todos first (oldest first), then completed.
func (r *GroupTodoRepository) ListByConversation(convID int64, limit int) ([]model.GroupTodo, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := database.DB.Query(
		`SELECT t.id, t.conversation_id, t.creator_id, t.title, t.done, t.done_by, t.created_at, t.completed_at,
		        COALESCE(NULLIF(u.nickname, ''), u.username) AS creator_name
		 FROM group_todos t JOIN users u ON u.id = t.creator_id
		 WHERE t.conversation_id = $1
		 ORDER BY t.done ASC, t.created_at ASC LIMIT $2`,
		convID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list todos: %w", err)
	}
	defer rows.Close()
	out := []model.GroupTodo{}
	for rows.Next() {
		t := model.GroupTodo{}
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.CreatorID, &t.Title, &t.Done, &t.DoneBy, &t.CreatedAt, &t.CompletedAt, &t.CreatorName); err != nil {
			return nil, fmt.Errorf("failed to scan todo: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetDone toggles the done state (any member may check items off).
func (r *GroupTodoRepository) SetDone(id, convID, actorID int64, done bool) error {
	res, err := database.DB.Exec(
		`UPDATE group_todos SET done = $3,
		        done_by = CASE WHEN $3 THEN $4 ELSE 0 END,
		        completed_at = CASE WHEN $3 THEN NOW() ELSE NULL END
		 WHERE id = $1 AND conversation_id = $2`,
		id, convID, done, actorID,
	)
	if err != nil {
		return fmt.Errorf("failed to update todo: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one todo (creator or managers; the handler checks role).
func (r *GroupTodoRepository) Delete(id, convID int64) error {
	res, err := database.DB.Exec("DELETE FROM group_todos WHERE id = $1 AND conversation_id = $2", id, convID)
	if err != nil {
		return fmt.Errorf("failed to delete todo: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountOwnedGroups returns how many groups the user owns (created).
func CountOwnedGroups(userID int64) (int64, error) {
	var n int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM conversations WHERE type = 'group' AND owner_id = $1", userID,
	).Scan(&n)
	return n, err
}

// GroupFile is one attachment shared into a conversation via message
// attachments, surfaced as the group's shared file list.
type GroupFile struct {
	MessageID  int64            `json:"message_id"`
	SenderID   int64            `json:"sender_id"`
	SenderName string           `json:"sender_name,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	Attachment model.Attachment `json:"attachment"`
}

// ListConversationFiles returns attachments shared into a conversation,
// newest first.
func ListConversationFiles(convID int64, beforeID, limit int64) ([]GroupFile, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	query := `SELECT ma.message_id, m.sender_id,
			COALESCE(NULLIF(u.nickname, ''), u.username) AS sender_name, m.created_at,
			a.id, a.user_id, a.object_key, a.original_name, a.mime_type, a.size_bytes,
			a.url, a.thumb_url, a.preview_url, a.burn_at, a.created_at
		 FROM message_attachments ma
		 JOIN messages m ON m.id = ma.message_id AND m.deleted_at IS NULL
		 JOIN users u ON u.id = m.sender_id
		 JOIN attachments a ON a.id = ma.attachment_id
		 WHERE m.conversation_id = $1`
	args := []interface{}{convID}
	if beforeID > 0 {
		query += " AND ma.message_id < $2"
		args = append(args, beforeID)
	}
	query += fmt.Sprintf(" ORDER BY ma.message_id DESC LIMIT %d", limit)
	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversation files: %w", err)
	}
	defer rows.Close()

	out := []GroupFile{}
	for rows.Next() {
		var f GroupFile
		if err := rows.Scan(&f.MessageID, &f.SenderID, &f.SenderName, &f.CreatedAt,
			&f.Attachment.ID, &f.Attachment.UserID, &f.Attachment.ObjectKey, &f.Attachment.OriginalName,
			&f.Attachment.MimeType, &f.Attachment.SizeBytes, &f.Attachment.URL, &f.Attachment.ThumbURL,
			&f.Attachment.PreviewURL, &f.Attachment.BurnAt, &f.Attachment.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan conversation file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
