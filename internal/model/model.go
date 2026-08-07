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
	StorageQuota      int64     `json:"storage_quota"`
	StorageUsed       int64     `json:"storage_used"`
	PasswordHash      string    `json:"-"`
	OAuth2Provider    string    `json:"oauth2_provider"`
	OAuth2ID          string    `json:"oauth2_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Post represents a text post with optional attachments.
type Post struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// populated on read queries
	Username    string       `json:"username,omitempty"`
	Nickname    string       `json:"nickname,omitempty"`
	AvatarURL   string       `json:"avatar_url,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
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

// Message represents a chat message.
type Message struct {
	ID         int64     `json:"id"`
	SenderID   int64     `json:"sender_id"`
	ReceiverID int64     `json:"receiver_id"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}
