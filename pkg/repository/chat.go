package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ConversationRepository handles chat conversation database operations.
type ConversationRepository struct{}

// NewConversationRepository creates a new ConversationRepository.
func NewConversationRepository() *ConversationRepository {
	return &ConversationRepository{}
}

// CreatePrivate creates a private conversation with two active members. When
// [encrypted] is true the conversation is created with end-to-end encryption
// enabled, so the group key can be initialized as soon as both members have
// published identity keys.
func (r *ConversationRepository) CreatePrivate(userA, userB int64, encrypted bool) (*model.Conversation, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	conv := &model.Conversation{}
	err = tx.QueryRow(
		"INSERT INTO conversations (type, owner_id, encrypted) VALUES ('private', $1, $2) RETURNING id, type, title, avatar_url, owner_id, created_at, updated_at, is_public, allow_join, mute_all_until, encrypted",
		userA, encrypted,
	).Scan(&conv.ID, &conv.Type, &conv.Title, &conv.AvatarURL, &conv.OwnerID, &conv.CreatedAt, &conv.UpdatedAt, &conv.IsPublic, &conv.AllowJoin, &conv.MuteAllUntil, &conv.Encrypted)
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
		"INSERT INTO conversations (type, title, owner_id) VALUES ('group', $1, $2) RETURNING id, type, title, avatar_url, owner_id, created_at, updated_at, is_public, allow_join, mute_all_until, encrypted",
		title, ownerID,
	).Scan(&conv.ID, &conv.Type, &conv.Title, &conv.AvatarURL, &conv.OwnerID, &conv.CreatedAt, &conv.UpdatedAt, &conv.IsPublic, &conv.AllowJoin, &conv.MuteAllUntil, &conv.Encrypted)
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
		"SELECT id, type, title, avatar_url, owner_id, created_at, updated_at, is_public, allow_join, mute_all_until, encrypted FROM conversations WHERE id = $1",
		id,
	).Scan(&conv.ID, &conv.Type, &conv.Title, &conv.AvatarURL, &conv.OwnerID, &conv.CreatedAt, &conv.UpdatedAt, &conv.IsPublic, &conv.AllowJoin, &conv.MuteAllUntil, &conv.Encrypted)
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
		`SELECT cm.conversation_id, cm.user_id, cm.role, cm.note, cm.group_nickname, cm.title, cm.status, cm.added_by, cm.created_at,
		        cm.muted_until, cm.notify_level,
		        u.username, u.nickname, u.avatar_url, u.is_verified, u.e2ee_public_key`+authorMemberCols+`
		 FROM conversation_members cm
		 JOIN users u ON cm.user_id = u.id
		 WHERE cm.conversation_id = $1 AND cm.user_id = $2`,
		conversationID, userID,
	).Scan(&member.ConversationID, &member.UserID, &member.Role, &member.Note, &member.GroupNickname, &member.Title, &member.Status, &member.AddedBy, &member.CreatedAt, &member.MutedUntil, &member.NotifyLevel, &member.Username, &member.Nickname, &member.AvatarURL, &member.IsVerified, &member.E2EEPublicKey, &member.IsBot, &member.MemberLevel, &member.MemberExpiresAt, &member.NameColor, &member.NameColorTo, &member.NameDynamic, &member.NameColors, &member.NameGradientDirection, &member.AvatarFrame)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	applyMemberStatus(&member.MemberLevel, &member.MemberExpiresAt, &member.MemberActive)
	return member, nil
}

// ListMembers lists active members of a conversation with user info.
func (r *ConversationRepository) ListMembers(conversationID int64) ([]model.ConversationMember, error) {
	rows, err := database.DB.Query(
		`SELECT cm.conversation_id, cm.user_id, cm.role, cm.note, cm.group_nickname, cm.title, cm.status, cm.added_by, cm.created_at,
		        cm.muted_until, cm.notify_level,
		        u.username, u.nickname, u.avatar_url, u.is_verified, u.e2ee_public_key`+authorMemberCols+`
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
		if err := rows.Scan(&member.ConversationID, &member.UserID, &member.Role, &member.Note, &member.GroupNickname, &member.Title, &member.Status, &member.AddedBy, &member.CreatedAt, &member.MutedUntil, &member.NotifyLevel, &member.Username, &member.Nickname, &member.AvatarURL, &member.IsVerified, &member.E2EEPublicKey, &member.IsBot, &member.MemberLevel, &member.MemberExpiresAt, &member.NameColor, &member.NameColorTo, &member.NameDynamic, &member.NameColors, &member.NameGradientDirection, &member.AvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan member: %w", err)
		}
		applyMemberStatus(&member.MemberLevel, &member.MemberExpiresAt, &member.MemberActive)
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
		        c.is_public, c.allow_join, c.mute_all_until, c.encrypted,
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
		if err := rows.Scan(&rw.conv.ID, &rw.conv.Type, &rw.conv.Title, &rw.conv.AvatarURL, &rw.conv.OwnerID, &rw.conv.CreatedAt, &rw.conv.UpdatedAt, &rw.conv.IsPublic, &rw.conv.AllowJoin, &rw.conv.MuteAllUntil, &rw.conv.Encrypted, &rw.note, &rw.lastMsg); err != nil {
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
		`SELECT m.conversation_id, m.id, m.sender_id, m.kind, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at,
		        COALESCE(NULLIF(u.nickname, ''), u.username) AS sender_name, u.avatar_url, u.is_verified`+authorMemberCols+`
		 FROM messages m
		 JOIN users u ON m.sender_id = u.id
		 WHERE m.conversation_id = ANY($1) AND m.deleted_at IS NULL
		 ORDER BY m.created_at DESC`,
		pq.Array(convIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query last messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m model.Message
		if err := rows.Scan(&m.ConversationID, &m.ID, &m.SenderID, &m.Kind, &m.Content, &m.ReplyToID, &m.EditedAt, &m.DeletedAt, &m.CreatedAt, &m.SenderName, &m.SenderAvatar, &m.SenderVerified, &m.SenderIsBot, &m.SenderMemberLevel, &m.SenderMemberExpiresAt, &m.SenderNameColor, &m.SenderNameColorTo, &m.SenderNameDynamic, &m.SenderNameColors, &m.SenderNameGradientDirection, &m.SenderAvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		applyMemberStatus(&m.SenderMemberLevel, &m.SenderMemberExpiresAt, &m.SenderMemberActive)
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
		userID, pq.Array(convIDs),
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
		`SELECT cm.conversation_id, cm.user_id, cm.role, cm.note, cm.group_nickname, cm.title, cm.status, cm.added_by, cm.created_at,
		        cm.muted_until, cm.notify_level,
		        u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`
		 FROM conversation_members cm
		 JOIN users u ON cm.user_id = u.id
		 WHERE cm.conversation_id = ANY($1) AND cm.user_id <> $2 AND cm.status = 'active'`,
		pq.Array(convIDs), userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query private display: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var member model.ConversationMember
		if err := rows.Scan(&member.ConversationID, &member.UserID, &member.Role, &member.Note, &member.GroupNickname, &member.Title, &member.Status, &member.AddedBy, &member.CreatedAt, &member.MutedUntil, &member.NotifyLevel, &member.Username, &member.Nickname, &member.AvatarURL, &member.IsVerified, &member.IsBot, &member.MemberLevel, &member.MemberExpiresAt, &member.NameColor, &member.NameColorTo, &member.NameDynamic, &member.NameColors, &member.NameGradientDirection, &member.AvatarFrame); err != nil {
			return nil, fmt.Errorf("failed to scan member: %w", err)
		}
		applyMemberStatus(&member.MemberLevel, &member.MemberExpiresAt, &member.MemberActive)
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

// UpdateMemberTitle sets the title shown next to a member's nickname in the
// chat. Only group owners/admins may change titles, which is enforced by the
// caller (handler).
func (r *ConversationRepository) UpdateMemberTitle(conversationID, userID int64, title string) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET title = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, title,
	)
	if err != nil {
		return fmt.Errorf("failed to update member title: %w", err)
	}
	return nil
}

// UpdateNotifyLevel updates the member's chat-notification preference for a
// conversation. Accepts "all", "mentions" or "off"; any other value is
// rejected by the caller.
func (r *ConversationRepository) UpdateNotifyLevel(conversationID, userID int64, level string) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET notify_level = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, level,
	)
	if err != nil {
		return fmt.Errorf("failed to update notify level: %w", err)
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

// Delete hard-deletes a conversation. Messages, memberships and consent
// requests are removed by their ON DELETE CASCADE constraints.
func (r *ConversationRepository) Delete(conversationID int64) error {
	_, err := database.DB.Exec("DELETE FROM conversations WHERE id = $1", conversationID)
	if err != nil {
		return fmt.Errorf("failed to delete conversation: %w", err)
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

// UpdateGroupSettings sets a group's visibility, self-join policy and whether
// the conversation is end-to-end-encrypted.
func (r *ConversationRepository) UpdateGroupSettings(conversationID int64, isPublic, allowJoin, encrypted bool) error {
	_, err := database.DB.Exec(
		"UPDATE conversations SET is_public = $2, allow_join = $3, encrypted = $4 WHERE id = $1",
		conversationID, isPublic, allowJoin, encrypted,
	)
	if err != nil {
		return fmt.Errorf("failed to update group settings: %w", err)
	}
	return nil
}

// UpdateGroupTitle renames a group conversation.
func (r *ConversationRepository) UpdateGroupTitle(conversationID int64, title string) error {
	_, err := database.DB.Exec(
		"UPDATE conversations SET title = $2, updated_at = NOW() WHERE id = $1",
		conversationID, title,
	)
	if err != nil {
		return fmt.Errorf("failed to update group title: %w", err)
	}
	return nil
}

// UpdateGroupAvatar sets a group conversation's avatar image URL.
func (r *ConversationRepository) UpdateGroupAvatar(conversationID int64, avatarURL string) error {
	_, err := database.DB.Exec(
		"UPDATE conversations SET avatar_url = $2, updated_at = NOW() WHERE id = $1",
		conversationID, avatarURL,
	)
	if err != nil {
		return fmt.Errorf("failed to update group avatar: %w", err)
	}
	return nil
}

// ListPublicGroups returns public groups matching a search query with the
// requesting user's membership flag and an active member count.
func (r *ConversationRepository) ListPublicGroups(userID int64, query string, limit int) ([]model.Conversation, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	pattern := "%" + query + "%"
	rows, err := database.DB.Query(
		`SELECT c.id, c.type, c.title, c.avatar_url, c.owner_id, c.created_at, c.updated_at,
		        c.is_public, c.allow_join, c.mute_all_until, c.encrypted,
		        COUNT(cm.user_id) FILTER (WHERE cm.status = 'active') AS member_count,
		        EXISTS (SELECT 1 FROM conversation_members me
		                WHERE me.conversation_id = c.id AND me.user_id = $1 AND me.status = 'active') AS is_member
		 FROM conversations c
		 LEFT JOIN conversation_members cm ON cm.conversation_id = c.id
		 WHERE c.type = 'group' AND c.is_public = TRUE AND c.title ILIKE $2
		 GROUP BY c.id
		 ORDER BY c.updated_at DESC
		 LIMIT $3`,
		userID, pattern, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list public groups: %w", err)
	}
	defer rows.Close()

	groups := make([]model.Conversation, 0)
	for rows.Next() {
		var g model.Conversation
		if err := rows.Scan(&g.ID, &g.Type, &g.Title, &g.AvatarURL, &g.OwnerID, &g.CreatedAt, &g.UpdatedAt, &g.IsPublic, &g.AllowJoin, &g.MuteAllUntil, &g.Encrypted, &g.MemberCount, &g.IsMember); err != nil {
			return nil, fmt.Errorf("failed to scan public group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return groups, nil
}

// SetMemberMute sets (or clears) a member's individual mute expiry.
func (r *ConversationRepository) SetMemberMute(conversationID, userID int64, until *time.Time) error {
	_, err := database.DB.Exec(
		"UPDATE conversation_members SET muted_until = $3 WHERE conversation_id = $1 AND user_id = $2",
		conversationID, userID, until,
	)
	if err != nil {
		return fmt.Errorf("failed to set member mute: %w", err)
	}
	return nil
}

// SetGroupMuteAll sets (or clears) the group-wide mute expiry.
func (r *ConversationRepository) SetGroupMuteAll(conversationID int64, until *time.Time) error {
	_, err := database.DB.Exec(
		"UPDATE conversations SET mute_all_until = $2 WHERE id = $1",
		conversationID, until,
	)
	if err != nil {
		return fmt.Errorf("failed to set group mute: %w", err)
	}
	return nil
}

// IsUserMuted reports whether a member is currently unable to send messages:
// either by an individual mute or (for regular members only) a group-wide mute.
func (r *ConversationRepository) IsUserMuted(conversationID, userID int64) (bool, error) {
	var role string
	var mutedUntil *time.Time
	var muteAllUntil *time.Time
	err := database.DB.QueryRow(
		`SELECT cm.role, cm.muted_until, c.mute_all_until
		 FROM conversation_members cm
		 JOIN conversations c ON c.id = cm.conversation_id
		 WHERE cm.conversation_id = $1 AND cm.user_id = $2 AND cm.status = 'active'`,
		conversationID, userID,
	).Scan(&role, &mutedUntil, &muteAllUntil)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check mute status: %w", err)
	}
	if mutedUntil != nil && mutedUntil.After(time.Now()) {
		return true, nil
	}
	if role != "owner" && role != "admin" && muteAllUntil != nil && muteAllUntil.After(time.Now()) {
		return true, nil
	}
	return false, nil
}

// CurrentE2EEVersion returns the highest key version stored for a conversation
// (0 when no envelope exists yet).
func (r *ConversationRepository) CurrentE2EEVersion(conversationID int64) (int64, error) {
	var version int64
	err := database.DB.QueryRow(
		"SELECT COALESCE(MAX(version), 0) FROM conversation_e2ee_keys WHERE conversation_id = $1",
		conversationID,
	).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("failed to read e2ee version: %w", err)
	}
	return version, nil
}

// PutE2EEKeys stores a batch of encrypted group-key envelopes for a
// conversation. Envelopes arrive as a list of (target_user_id, ciphertext);
// when a previous version exists the new envelopes are written under the next
// version number, otherwise version 1.
func (r *ConversationRepository) PutE2EEKeys(conversationID int64, envelopes map[int64]string) (int64, error) {
	if len(envelopes) == 0 {
		return 0, nil
	}
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Lock the conversation row to serialize concurrent key rotations for the
	// same conversation. Aggregate queries (COALESCE(MAX(...))) cannot be used
	// with FOR UPDATE directly, so the lock targets the parent row instead.
	if _, err := tx.Exec(
		"SELECT id FROM conversations WHERE id = $1 FOR UPDATE",
		conversationID,
	); err != nil {
		return 0, fmt.Errorf("failed to lock conversation: %w", err)
	}

	var version int64
	if err := tx.QueryRow(
		"SELECT COALESCE(MAX(version), 0) FROM conversation_e2ee_keys WHERE conversation_id = $1",
		conversationID,
	).Scan(&version); err != nil {
		return 0, fmt.Errorf("failed to read e2ee version: %w", err)
	}
	version++

	for targetUserID, ciphertext := range envelopes {
		if _, err := tx.Exec(
			"INSERT INTO conversation_e2ee_keys (conversation_id, version, target_user_id, ciphertext) VALUES ($1, $2, $3, $4)",
			conversationID, version, targetUserID, ciphertext,
		); err != nil {
			return 0, fmt.Errorf("failed to store e2ee key: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit e2ee keys: %w", err)
	}
	return version, nil
}

// ListE2EEKeyEnvelopes returns the latest group-key envelope for each member of
// the conversation, joined with their published public keys. Members without a
// stored envelope still appear with an empty ciphertext.
func (r *ConversationRepository) ListE2EEKeyEnvelopes(conversationID int64) ([]model.E2EEKeyEnvelope, error) {
	rows, err := database.DB.Query(
		`SELECT e.conversation_id, e.version, e.target_user_id, e.ciphertext, e.created_at
		 FROM conversation_e2ee_keys e
		 JOIN (
		   SELECT target_user_id, MAX(version) AS version
		   FROM conversation_e2ee_keys
		   WHERE conversation_id = $1
		   GROUP BY target_user_id
		 ) latest ON latest.target_user_id = e.target_user_id AND latest.version = e.version`,
		conversationID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list e2ee envelopes: %w", err)
	}
	defer rows.Close()

	envelopes := make([]model.E2EEKeyEnvelope, 0)
	for rows.Next() {
		var e model.E2EEKeyEnvelope
		if err := rows.Scan(&e.ConversationID, &e.Version, &e.TargetUserID, &e.Ciphertext, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan e2ee envelope: %w", err)
		}
		envelopes = append(envelopes, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return envelopes, nil
}

// GetE2EEEnvelopeFor returns the current user's envelope for a conversation.
func (r *ConversationRepository) GetE2EEEnvelopeFor(conversationID, userID int64) (*model.E2EEKeyEnvelope, error) {
	var e model.E2EEKeyEnvelope
	err := database.DB.QueryRow(
		"SELECT id, conversation_id, version, target_user_id, ciphertext, created_at FROM conversation_e2ee_keys WHERE conversation_id = $1 AND target_user_id = $2 ORDER BY version DESC LIMIT 1",
		conversationID, userID,
	).Scan(&e.ID, &e.ConversationID, &e.Version, &e.TargetUserID, &e.Ciphertext, &e.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read e2ee envelope: %w", err)
	}
	return &e, nil
}

// ListMemberE2EEPublicKeys returns the published E2EE public key for every
// active member of a conversation.
func (r *ConversationRepository) ListMemberE2EEPublicKeys(conversationID int64) (map[int64]string, error) {
	rows, err := database.DB.Query(
		`SELECT cm.user_id, u.e2ee_public_key
		 FROM conversation_members cm
		 JOIN users u ON u.id = cm.user_id
		 WHERE cm.conversation_id = $1 AND cm.status = 'active'`,
		conversationID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list member e2ee keys: %w", err)
	}
	defer rows.Close()

	keys := make(map[int64]string)
	for rows.Next() {
		var userID int64
		var pub string
		if err := rows.Scan(&userID, &pub); err != nil {
			return nil, fmt.Errorf("failed to scan e2ee public key: %w", err)
		}
		keys[userID] = pub
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return keys, nil
}
