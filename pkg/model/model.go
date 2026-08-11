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
	OAuth2Username    string    `json:"oauth2_username"`
	VerifiedNote      string    `json:"verified_note"`
	VerifiedBy        string    `json:"verified_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	// Exp is the user's lifetime accumulated experience points. The display
	// level is derived from exp on the client; the server only stores the
	// raw total so a level formula change doesn't need a migration.
	Exp int64 `json:"exp"`
	// Level is recomputed from [Exp] and returned alongside it for clients
	// that don't run the formula locally.
	Level int `json:"level"`
	// LastDailyBonusAt tracks when the user last received the daily login
	// exp grant (used by the server to gate the next grant to once per day).
	LastDailyBonusAt *time.Time `json:"last_daily_bonus_at,omitempty"`
	// FollowerCount/FollowingCount are populated on profile reads.
	FollowerCount  int64 `json:"follower_count,omitempty"`
	FollowingCount int64 `json:"following_count,omitempty"`
	// IsFollowing is whether the requesting user follows this user.
	IsFollowing bool `json:"is_following,omitempty"`
	// Permissions are populated on /users/me when requested.
	Permissions []string `json:"permissions,omitempty"`
	// E2EEPublicKey is the X25519 public key this user publishes for
	// end-to-end-encrypted conversations (empty when not set).
	E2EEPublicKey string `json:"e2ee_public_key,omitempty"`
	// CheckinStreak is the user's current consecutive daily-sign-in streak.
	CheckinStreak int64 `json:"checkin_streak,omitempty"`
	// Region is the user's locale region name (e.g. "中国"), used to derive
	// the timezone for day boundaries and the display language for server
	// notifications. Empty string means the server defaults apply.
	Region string `json:"region,omitempty"`
	// Lang is the display language override for server messages, derived from
	// the region when not set explicitly.
	Lang string `json:"lang,omitempty"`
}

// Post represents a text post with optional attachments.
type Post struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ReplyCount  int64     `json:"reply_count,omitempty"`
	ViewCount   int64     `json:"view_count,omitempty"`
	UniqueViews int64     `json:"unique_views,omitempty"`
	// Reactions holds reaction counts keyed by reaction type (e.g. like).
	Reactions map[string]int64 `json:"reactions,omitempty"`
	// MyReaction is the authenticated viewer's own reaction, when present.
	MyReaction  string       `json:"my_reaction,omitempty"`
	Username    string       `json:"username,omitempty"`
	Nickname    string       `json:"nickname,omitempty"`
	AvatarURL   string       `json:"avatar_url,omitempty"`
	IsVerified  bool         `json:"is_verified,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// PostReply represents a reply to a post.
type PostReply struct {
	ID         int64      `json:"id"`
	PostID     int64      `json:"post_id"`
	UserID     int64      `json:"user_id"`
	Content    string     `json:"content"`
	ParentID   *int64     `json:"parent_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	Username   string     `json:"username,omitempty"`
	Nickname   string     `json:"nickname,omitempty"`
	AvatarURL  string     `json:"avatar_url,omitempty"`
	IsVerified bool       `json:"is_verified,omitempty"`
	// ParentContent is a preview of the parent reply's content (for nested threads).
	ParentContent string `json:"parent_content,omitempty"`
	// ParentName is the parent reply author's display name.
	ParentName  string       `json:"parent_name,omitempty"`
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
	ThumbURL     string    `json:"thumb_url,omitempty"`
	Visibility   string    `json:"visibility"`
	CreatedAt    time.Time `json:"created_at"`
}

// Conversation represents a private or group chat conversation.
type Conversation struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"` // private | group
	Title     string    `json:"title"`
	AvatarURL string    `json:"avatar_url"`
	OwnerID   int64     `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// IsPublic makes the group searchable and viewable by non-members.
	IsPublic bool `json:"is_public"`
	// AllowJoin lets users join the group without an invitation when it is public.
	AllowJoin bool `json:"allow_join"`
	// MuteAllUntil, when set in the future, mutes every non-staff member.
	MuteAllUntil *time.Time `json:"mute_all_until,omitempty"`
	// Encrypted marks the conversation as end-to-end-encrypted: messages are
	// stored as ciphertext and only clients holding the group key can decrypt.
	Encrypted bool `json:"encrypted"`
	// MemberCount is populated on public group listings.
	MemberCount int64 `json:"member_count,omitempty"`
	// IsMember is whether the requesting user belongs to the group (public listings).
	IsMember bool `json:"is_member,omitempty"`
	// LastMessage is the latest message preview for lists.
	LastMessage *Message `json:"last_message,omitempty"`
	Unread      int64    `json:"unread,omitempty"`
}

// ConversationMember represents a membership of a user in a conversation.
type ConversationMember struct {
	ConversationID int64  `json:"conversation_id"`
	UserID         int64  `json:"user_id"`
	Role           string `json:"role"` // owner | admin | member
	Note           string `json:"note"` // remark set by the member for the other side (private)
	GroupNickname  string `json:"group_nickname"`
	// Title is a label set by group admins/owner that renders next to the
	// member's nickname in chat (e.g. "VIP", "管理员"). Distinct from
	// [GroupNickname], which is a self-set nickname for the conversation.
	Title     string    `json:"title"`
	Status    string    `json:"status"` // pending | active | declined
	AddedBy   int64     `json:"added_by"`
	CreatedAt time.Time `json:"created_at"`
	// MutedUntil, when set in the future, silences the member in this group.
	MutedUntil *time.Time `json:"muted_until,omitempty"`
	Username   string     `json:"username,omitempty"`
	Nickname   string     `json:"nickname,omitempty"`
	AvatarURL  string     `json:"avatar_url,omitempty"`
	IsVerified bool       `json:"is_verified,omitempty"`
	// E2EEPublicKey is the member's published X25519 public key for encrypted
	// group-key envelopes (empty when the member has no key published).
	E2EEPublicKey string `json:"e2ee_public_key,omitempty"`
	// NotifyLevel controls how chat events for this conversation reach the
	// user. "all" = every message, "mentions" = only when explicitly
	// mentioned or @everyone is used, "off" = mute entirely.
	NotifyLevel string `json:"notify_level,omitempty"`
}

// ConsentRequest represents a pending consent request for a private chat or group invite.
type ConsentRequest struct {
	ID              int64      `json:"id"`
	Type            string     `json:"type"` // private_chat | group_invite
	RequesterID     int64      `json:"requester_id"`
	TargetUserID    int64      `json:"target_user_id"`
	ConversationID  *int64     `json:"conversation_id,omitempty"`
	GroupID         *int64     `json:"group_id,omitempty"`
	Message         string     `json:"message"`
	Status          string     `json:"status"` // pending | accepted | declined
	CreatedAt       time.Time  `json:"created_at"`
	RespondedAt     *time.Time `json:"responded_at,omitempty"`
	RequesterName   string     `json:"requester_name,omitempty"`
	RequesterAvatar string     `json:"requester_avatar,omitempty"`
	GroupTitle      string     `json:"group_title,omitempty"`
	// Encrypted indicates the requester asked for an end-to-end encrypted
	// private chat. Honored when the recipient accepts.
	Encrypted bool `json:"encrypted,omitempty"`
}

// Message represents a chat message within a conversation.
type Message struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversation_id"`
	SenderID       int64      `json:"sender_id"`
	Kind           string     `json:"kind"` // text | system.join | system.leave | system.mute | system.unmute | system.mute.all | system.unmute.all
	Content        string     `json:"content"`
	ReplyToID      *int64     `json:"reply_to_id,omitempty"`
	EditedAt       *time.Time `json:"edited_at,omitempty"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	SenderName     string     `json:"sender_name,omitempty"`
	SenderAvatar   string     `json:"sender_avatar,omitempty"`
	SenderVerified bool       `json:"sender_verified,omitempty"`
	// ReplyToName/ReplyToContent are previews of the replied-to message so
	// clients can render a quote without loading the full thread.
	ReplyToName    string       `json:"reply_to_name,omitempty"`
	ReplyToContent string       `json:"reply_to_content,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	// Mentions lists the user IDs explicitly @-mentioned in this message,
	// including the special "everyone" token (-1) when @everyone is used.
	// Stored server-side so notification logic doesn't have to re-parse the
	// content on every push.
	Mentions []int64 `json:"mentions,omitempty"`
}

// E2EEKeyEnvelope is a group key encrypted to a single member's public key.
type E2EEKeyEnvelope struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversation_id"`
	Version        int64     `json:"version"`
	TargetUserID   int64     `json:"target_user_id"`
	Ciphertext     string    `json:"ciphertext"` // base64(AES-256-GCM(plaintext_group_key))
	CreatedAt      time.Time `json:"created_at"`
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

// Wallet represents a user's balance.
type Wallet struct {
	UserID    int64     `json:"user_id"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WalletTransaction represents a single balance change on a user's wallet.
type WalletTransaction struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	Amount       int64     `json:"amount"`
	BalanceAfter int64     `json:"balance_after"`
	Type         string    `json:"type"`
	Description  string    `json:"description"`
	OperatorID   int64     `json:"operator_id"`
	CreatedAt    time.Time `json:"created_at"`
}
