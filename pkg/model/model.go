package model

import "time"

// User represents a user in the system.
type User struct {
	ID                int64     `json:"id"`
	Username          string    `json:"username"`
	Nickname          string    `json:"nickname"`
	Email             string    `json:"email"`
	AvatarURL         string    `json:"avatar_url"`
	BannerURL         string    `json:"banner_url"`
	Role              string    `json:"role"`
	NeedsRegistration bool      `json:"needs_registration"`
	Bio               string    `json:"bio"`
	IsVerified        bool      `json:"is_verified"`
	StorageQuota      int64     `json:"storage_quota"`
	StorageUsed       int64     `json:"storage_used"`
	PasswordHash      string    `json:"-"`
	OAuth2Provider    string    `json:"oauth2_provider"`
	OAuth2ID          string    `json:"oauth2_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	// Permissions are populated on /users/me when requested.
	Permissions []string `json:"permissions,omitempty"`
}

// Post represents a text post with optional attachments.
type Post struct {
	ID          int64        `json:"id"`
	UserID      int64        `json:"user_id"`
	Content     string       `json:"content"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	ReplyCount  int64        `json:"reply_count,omitempty"`
	Username    string       `json:"username,omitempty"`
	Nickname    string       `json:"nickname,omitempty"`
	AvatarURL   string       `json:"avatar_url,omitempty"`
	IsVerified  bool         `json:"is_verified,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// PostReply represents a reply to a post.
type PostReply struct {
	ID        int64      `json:"id"`
	PostID    int64      `json:"post_id"`
	UserID    int64      `json:"user_id"`
	Content   string     `json:"content"`
	ParentID  *int64     `json:"parent_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	Username  string     `json:"username,omitempty"`
	Nickname  string     `json:"nickname,omitempty"`
	AvatarURL string     `json:"avatar_url,omitempty"`
	IsVerified bool      `json:"is_verified,omitempty"`
}

// Attachment represents a file stored in RustFS.
type Attachment struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	ObjectKey    string    `json:"-"`
	OriginalName string    `json:"original_name"`
	MimeType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"created_at"`
}

// Conversation represents a private or group chat conversation.
type Conversation struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"` // private | group
	Title      string    `json:"title"`
	AvatarURL  string    `json:"avatar_url"`
	OwnerID    int64     `json:"owner_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// LastMessage is the latest message preview for lists.
	LastMessage *Message `json:"last_message,omitempty"`
	Unread      int64    `json:"unread,omitempty"`
}

// ConversationMember represents a membership of a user in a conversation.
type ConversationMember struct {
	ConversationID int64     `json:"conversation_id"`
	UserID         int64     `json:"user_id"`
	Role           string    `json:"role"` // owner | admin | member
	Note           string    `json:"note"` // remark set by the member for the other side (private)
	GroupNickname  string    `json:"group_nickname"`
	Status         string    `json:"status"` // pending | active | declined
	AddedBy        int64     `json:"added_by"`
	CreatedAt      time.Time `json:"created_at"`
	Username       string    `json:"username,omitempty"`
	Nickname       string    `json:"nickname,omitempty"`
	AvatarURL      string    `json:"avatar_url,omitempty"`
	IsVerified     bool      `json:"is_verified,omitempty"`
}

// ConsentRequest represents a pending consent request for a private chat or group invite.
type ConsentRequest struct {
	ID             int64      `json:"id"`
	Type           string     `json:"type"` // private_chat | group_invite
	RequesterID    int64      `json:"requester_id"`
	TargetUserID   int64      `json:"target_user_id"`
	ConversationID *int64     `json:"conversation_id,omitempty"`
	GroupID        *int64     `json:"group_id,omitempty"`
	Message        string     `json:"message"`
	Status         string     `json:"status"` // pending | accepted | declined
	CreatedAt      time.Time  `json:"created_at"`
	RespondedAt    *time.Time `json:"responded_at,omitempty"`
	RequesterName  string     `json:"requester_name,omitempty"`
	RequesterAvatar string    `json:"requester_avatar,omitempty"`
	GroupTitle     string     `json:"group_title,omitempty"`
}

// Message represents a chat message within a conversation.
type Message struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversation_id"`
	SenderID       int64      `json:"sender_id"`
	Content        string     `json:"content"`
	ReplyToID      *int64     `json:"reply_to_id,omitempty"`
	EditedAt       *time.Time `json:"edited_at,omitempty"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	SenderName     string     `json:"sender_name,omitempty"`
	SenderAvatar   string     `json:"sender_avatar,omitempty"`
	SenderVerified bool       `json:"sender_verified,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
}

// Group represents a user group used for permission management.
type Group struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	IsDefault   bool      `json:"is_default"`
	CreatedAt   time.Time `json:"created_at"`
	Permissions []string  `json:"permissions,omitempty"`
}

// Permission represents a single feature permission.
type Permission struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
