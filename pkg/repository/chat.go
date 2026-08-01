package repository

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ConversationRepository handles chat conversation database operations.
type ConversationRepository struct{}

// NewConversationRepository creates a new ConversationRepository.
func NewConversationRepository() *ConversationRepository {
	return &ConversationRepository{}
}

// CreatePrivate creates a private conversation with two active members.
func (r *ConversationRepository) CreatePrivate(userA, userB int64) (*model.Conversation, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	conv := &model.Conversation{}
	err = tx.QueryRow(
		"INSERT INTO conversations (type, owner_id) VALUES ('private', $1) RETURNING id, type, title, avatar_url, owner_id, created_at, updated_at",
		userA,
	).Scan(&conv.ID, &conv.Type, &conv.Title, &conv.AvatarURL, &conv.OwnerID, &conv.CreatedAt, &conv.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create conversation: %w", err)
	}

	for _, uid := range []int64{userA, userB} {
		if _, err := tx.Exec(
			"INSERT INTO conversation_members (conversation_id, user_id, role, status, added_by) VALUES ($1, $2, 'member', 'active', $3)",
			conv.ID, uid, userA,
		); err != nil {
			return nil, fmt.Errorf("failed to add member: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return conv, nil
}

// CreateGroup creates a group conversation owned by a user.
func (r *ConversationRepository) CreateGroup(ownerID int64, title string) (*model.Conversation, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	conv := &model.Conversation{}
	err = tx.QueryRow(
		"INSERT INTO conversations (type, title, owner_id) VALUES ('group', $1, $2) RETURNING id, type, title, avatar_url, owner_id, created_at, updated_at",
		title, ownerID,
	).Scan(&conv.ID, &conv.Type, &conv.Title, &conv.AvatarURL, &conv.OwnerID, &conv.CreatedAt, &conv.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create group: %w", err)
	}

	if _, err := tx.Exec(
		"INSERT INTO conversation_members (conversation_id, user_id, role, status, added_by) VALUES ($1, $2, 'owner', 'active', $2)",
		conv.ID, ownerID,
	); err != nil {
		return nil, fmt.Errorf("failed to add owner member: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return conv, nil
}

// GetByID retrieves a conversation by ID.
func (r *ConversationRepository) GetByID(id int64) (*model.Conversation, error) {
	conv := &model.Conversation{}
	err := database.DB.QueryRow(
		"SELECT id, type, title, avatar_url, owner_id, created_at, updated_at FROM conversations WHERE id = $1",
		id,
	).Scan(&conv.ID, &conv.Type, &conv.Title, &conv.AvatarURL, &conv.OwnerID, &conv.CreatedAt, &conv.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return conv, nil
}

// IsMember reports whether the user is an active member of the conversation.
func (r *ConversationRepository) IsMember(conversationID, userID int64) (bool, error) {
	var count int
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM conversation_members WHERE conversation_id = $1 AND user_id = $2 AND status = 'active'",
		conversationID, userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check membership: %w", err)
	}
	return count > 0, nil
}

// GetMember retrieves a member record.
func (r *ConversationRepository) GetMember(conversationID, userID int64) (*model.ConversationMember, error) {
	member := &model.ConversationMember{}
	err := database.DB.QueryRow(
		`SELECT cm.conversation_id, cm.user_id, cm.role, cm.note, cm.group_nickname, cm.status, cm.added_by, cm.created_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified
		 FROM conversation_members cm
		 JOIN users u ON cm.user_id = u.id
		 WHERE cm.conversation_id = $1 AND cm.user_id = $2`,
		conversationID, userID,
	).Scan(&member.ConversationID, &member.UserID, &member.Role, &member.Note, &member.GroupNickname, &member.Status, &member.AddedBy, &member.CreatedAt, &member.Username, &member.Nickname, &member.AvatarURL, &member.IsVerified)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return member, nil
}

// ListMembers lists active members of a conversation with user info.
func (r *ConversationRepository) ListMembers(conversationID int64) ([]model.ConversationMember, error) {
	rows, err := database.DB.Query(
		`SELECT cm.conversation_id, cm.user_id, cm.role, cm.note, cm.group_nickname, cm.status, cm.added_by, cm.created_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified
		 FROM conversation_members cm
		 JOIN users u ON cm.user_id = u.id
		 WHERE cm.conversation_id = $1 AND cm.status = 'active'
		 ORDER BY cm.created_at ASC`,
		conversationID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list members: %w", err)
	}
	defer rows.Close()

	members := make([]model.ConversationMember, 0)
	for rows.Next() {
		var member model.ConversationMember
		if err := rows.Scan(&member.ConversationID, &member.UserID, &member.Role, &member.Note, &member.GroupNickname, &member.Status, &member.AddedBy, &member.CreatedAt, &member.Username, &member.Nickname, &member.AvatarURL, &member.IsVerified); err != nil {
			return nil, fmt.Errorf("failed to scan member: %w", err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return members, nil
}

// ListForUser lists the user's conversations with last message and unread counts.
func (r *ConversationRepository) ListForUser(userID int64) ([]model.Conversation, error) {
	rows, err := database.DB.Query(
		`SELECT c.id, c.type, c.title, c.avatar_url, c.owner_id, c.created_at, c.updated_at,
		        cm.note, cm.last_read_message_id
		 FROM conversations c
		 JOIN conversation_members cm ON cm.conversation_id = c.id
		 WHERE cm.user_id = $1 AND cm.status = 'active'
		 ORDER BY c.updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}
	defer rows.Close()

	type row struct {
		conv    model.Conversation
		note    string
		lastMsg int64
	}
	rowsData := make([]row, 0)
	var convIDs []int64
	for rows.Next() {
		var rw row
		if err := rows.Scan(&rw.conv.ID, &rw.conv.Type, &rw.conv.Title, &rw.conv.AvatarURL, &rw.conv.OwnerID, &rw.conv.CreatedAt, &rw.conv.UpdatedAt, &rw.note, &rw.lastMsg); err != nil {
			return nil, fmt.Errorf("failed to scan conversation: %w", err)
		}
		rowsData = append(rowsData, rw)
		convIDs = append(convIDs, rw.conv.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	// last messages
	lastMsgs, err := r.lastMessages(convIDs)
	if err != nil {
		return nil, err
	}
	// unread counts
	unread, err := r.unreadCounts(userID, convIDs)
	if err != nil {
		return nil, err
	}
	// other member display for private chats + group names from members
	others, err := r.privateDisplay(userID, convIDs)
	if err != nil {
		return nil, err
	}

	convs := make([]model.Conversation, 0, len(rowsData))
	for _, rw := range rowsData {
		conv := rw.conv
		if m, ok := lastMsgs[conv.ID]; ok {
			conv.LastMessage = &m
		}
		conv.Unread = unread[conv.ID]
		if conv.Type == "private" {
			if other, ok := others[conv.ID]; ok {
				// display the note the user set, plus the other side's identity
				conv.Title = other.Nickname
				if other.Note != "" {
					conv.Title = other.Note
				}
				conv.AvatarURL = other.AvatarURL
			}
		}
		convs = append(convs, conv)
	}
	return convs, nil
}

func (r *ConversationRepository) lastMessages(convIDs []int64) (map[int64]model.Message, error) {
	result := make(map[int64]model.Message)
	if len(convIDs) == 0 {
		return result, nil
	}
	rows, err := database.DB.Query(
		`SELECT m.conversation_id, m.id, m.sender_id, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified
		 FROM messages m
		 JOIN users u ON m.sender_id = u.id
		 WHERE m.conversation_id = ANY($1) AND m.deleted_at IS NULL
		 ORDER BY m.created_at DESC`,
		convIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query last messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m model.Message
		if err := rows.Scan(&m.ConversationID, &m.ID, &m.SenderID, &m.Content, &m.ReplyToID, &m.EditedAt, &m.DeletedAt, &m.CreatedAt, &m.SenderName, &m.SenderAvatar, &m.SenderVerified); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		if _, exists := result[m.ConversationID]; !exists {
			result[m.ConversationID] = m
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return result, nil
}

func (r *ConversationRepository) unreadCounts(userID int64, convIDs []int64) (map[int64]int64, error) {
	result := make(map[int64]int64)
	if len(convIDs) == 0 {
		return result, nil
	}
	rows, err := database.DB.Query(
		`SELECT cm.conversation_id,
		        COUNT(m.id) FILTER (WHERE m.id > cm.last_read_message_id AND m.sender_id <> $1 AND m.deleted_at IS NULL) AS unread
		 FROM conversation_members cm
		 LEFT JOIN messages m ON m.conversation_id = cm.conversation_id
		 WHERE cm.user_id = $1 AND cm.conversation_id = ANY($2)
		 GROUP BY cm.conversation_id`,
		userID, convIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query unread counts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var convID, unread int64
		if err := rows.Scan(&convID, &unread); err != nil {
			return nil, fmt.Errorf("failed to scan unread: %w", err)
		}
		result[convID] = unread
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return result, nil
}

// privateDisplay returns the other member of each private conversation for the user.
func (r *ConversationRepository) privateDisplay(userID int64, convIDs []int64) (map[int64]model.ConversationMember, error) {
	result := make(map[int64]model.ConversationMember)
	if len(convIDs) == 0 {
		return result, nil
	}
	rows, err := database.DB.Query(
		`SELECT cm.conversation_id, cm.user_id, cm.role, cm.note, cm.group_nickname, cm.status, cm.added_by, cm.created_at,
		        u.username, u.nickname, u.avatar_url, u.is_verified
		 FROM conversation_members cm
		 JOIN users u ON cm.user_id = u.id
		 WHERE cm.conversation_id = ANY($1) AND cm.user_id <> $2 AND cm.status = 'active'`,
		convIDs, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query private display: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var member model.ConversationMember
		if err := rows.Scan(&member.ConversationID, &member.UserID, &member.Role, &member.Note, &member.GroupNickname, &member.Status, &member.AddedBy, &member.CreatedAt, &member.Username, &member.Nickname, &member.AvatarURL, &member.IsVerified); err != nil {
			return nil, fmt.Errorf("failed to scan member: %w", err)
		}
		// only keep members from private conversations (a group will return many rows)
		if _, exists := result[member.ConversationID]; !exists {
			result[member.ConversationID] = member
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return result, nil
}

// UpdateNote sets the user's note (remark) about a conversation.
// For private chats the note applies to the other participant.
func (r *ConversationRepository) UpdateNote(conversationID, userID int64, note string) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET note = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, note,
	)
	if err != nil {
		return fmt.Errorf("failed to update note: %w", err)
	}
	return nil
}

// UpdateGroupNickname sets the user's display nickname within a group.
func (r *ConversationRepository) UpdateGroupNickname(conversationID, userID int64, nickname string) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET group_nickname = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, nickname,
	)
	if err != nil {
		return fmt.Errorf("failed to update group nickname: %w", err)
	}
	return nil
}

// AddMember adds a member to a conversation with the given status.
func (r *ConversationRepository) AddMember(conversationID, userID, addedBy int64, role, status string) error {
	_, err := database.DB.Exec(
		`INSERT INTO conversation_members (conversation_id, user_id, role, status, added_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (conversation_id, user_id) DO UPDATE SET status = $4, added_by = $5`,
		conversationID, userID, role, status, addedBy,
	)
	if err != nil {
		return fmt.Errorf("failed to add member: %w", err)
	}
	return nil
}

// RemoveMember removes a member from a conversation.
func (r *ConversationRepository) RemoveMember(conversationID, userID int64) error {
	_, err := database.DB.Exec(
		"DELETE FROM conversation_members WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to remove member: %w", err)
	}
	return nil
}

// SetMemberRole updates a member's role.
func (r *ConversationRepository) SetMemberRole(conversationID, userID int64, role string) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET role = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("failed to set member role: %w", err)
	}
	return nil
}

// UpdateLastReadMessage marks the user's last read message in a conversation.
func (r *ConversationRepository) UpdateLastReadMessage(conversationID, userID, messageID int64) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET last_read_message_id = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, messageID,
	)
	if err != nil {
		return fmt.Errorf("failed to update last read message: %w", err)
	}
	return nil
}

// GetPrivatePeer returns the other participant's user ID in a private conversation.
func (r *ConversationRepository) GetPrivatePeer(conversationID, userID int64) (int64, error) {
	var peer int64
	err := database.DB.QueryRow(
		"SELECT user_id FROM conversation_members WHERE conversation_id = $1 AND user_id <> $2 AND status = 'active' LIMIT 1",
		conversationID, userID,
	).Scan(&peer)
	if err != nil {
		return 0, err
	}
	return peer, nil
}
