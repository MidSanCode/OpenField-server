package model

import "time"

// User represents a user in the system.
type User struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	Nickname          string `json:"nickname"`
	Email             string `json:"email"`
	AvatarURL         string `json:"avatar_url"`
	BannerURL         string `json:"banner_url"`
	Role              string `json:"role"`
	NeedsRegistration bool   `json:"needs_registration"`
	Bio               string `json:"bio"`
	IsVerified        bool   `json:"is_verified"`
	StorageQuota      int64  `json:"storage_quota"`
	StorageUsed       int64  `json:"storage_used"`
	// StorageBucket is the logical storage bucket the user's files live in
	// (empty means the default bucket). Files uploaded to one bucket stay
	// there; switching buckets moves only the quota, not existing objects.
	StorageBucket  string    `json:"storage_bucket,omitempty"`
	PasswordHash   string    `json:"-"`
	OAuth2Provider string    `json:"oauth2_provider"`
	OAuth2ID       string    `json:"oauth2_id"`
	OAuth2Username string    `json:"oauth2_username"`
	VerifiedNote   string    `json:"verified_note"`
	VerifiedBy     string    `json:"verified_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
	// HideFollowLists hides the user's followers/following/friends lists from
	// everyone except the user themself. Anonymity is not otherwise affected:
	// the counts are zeroed for other viewers once enabled.
	HideFollowLists bool `json:"hide_follow_lists"`
	// IsFollowing is whether the requesting user follows this user.
	IsFollowing bool `json:"is_following,omitempty"`
	// IsFriend is whether the requesting user and this user follow each
	// other (friends).
	IsFriend bool `json:"is_friend,omitempty"`
	// Permissions are populated on /users/me when requested.
	Permissions []string `json:"permissions,omitempty"`
	// E2EEPublicKey is the X25519 public key this user publishes for
	// end-to-end-encrypted conversations (empty when not set).
	E2EEPublicKey string `json:"e2ee_public_key,omitempty"`
	// CheckinStreak is the user's current consecutive daily-sign-in streak.
	CheckinStreak int64 `json:"checkin_streak,omitempty"`
	// PinHash is the bcrypt hash of the user's payment PIN, used only to
	// authorize outgoing payments (transfers). Never serialized to clients.
	PinHash string `json:"-"`
	// HasPin reports whether the user has set a payment PIN. Prompts the client
	// to set one before the first payment.
	HasPin bool `json:"has_pin"`
	// MemberLevel is the user's purchased membership tier (0 = none). While the
	// membership is active (MemberExpiresAt in the future) every exp grant is
	// scaled by the tier multiplier; the level stays stored after expiry so a
	// renewal can extend from where the user left off.
	MemberLevel int64 `json:"member_level"`
	// MemberExpiresAt is when the membership expires (null when the user has
	// never had an active membership).
	MemberExpiresAt *time.Time `json:"member_expires_at,omitempty"`
	// AutoRenew is the opt-in automatic-renewal flag: while true the membership
	// sweeper re-charges the current tier near expiry and extends it by 30 days.
	// It is switched off automatically when a renewal fails (e.g. insufficient
	// balance).
	AutoRenew bool `json:"auto_renew"`
	// Region is the user's locale region name (e.g. "中国"), used to derive
	// the timezone for day boundaries and the display language for server
	// notifications. Empty string means the server defaults apply.
	Region string `json:"region,omitempty"`
	// Lang is the display language override for server messages, derived from
	// the region when not set explicitly.
	Lang string `json:"lang,omitempty"`
	// NameColor is the hex color the user chose for their display name
	// (e.g. "#E64A19"). Empty means the default theme color is used. Which
	// values are allowed depends on the membership tier: Lv.1 members may only
	// pick from a fixed preset palette, Lv.2+ may type any valid hex.
	NameColor string `json:"name_color,omitempty"`
	// NameColorTo is the second hex color of a gradient display name (Lv.3+
	// members). Empty means the name is a single solid color.
	NameColorTo string `json:"name_color_to,omitempty"`
	// NameDynamic enables an animated (shifting) gradient display name,
	// available to Lv.4 members only.
	NameDynamic bool `json:"name_dynamic,omitempty"`
	// NameColors is the full gradient color list (1-6 hex colors). When it has
	// two or more entries it takes precedence over the legacy
	// NameColor/NameColorTo pair; a single entry behaves like a solid color.
	NameColors NameColorList `json:"name_colors,omitempty"`
	// NameGradientDirection is the linear-gradient orientation for multi-color
	// display names: left_right (default), right_left, top_bottom,
	// bottom_top, top_left_bottom_right, bottom_left_top_right.
	NameGradientDirection string `json:"name_gradient_direction,omitempty"`
	// AvatarFrame is reserved for the upcoming avatar-frame feature. It holds a
	// frame key (e.g. "gold") while empty means the default frame is used.
	AvatarFrame string `json:"avatar_frame,omitempty"`
	// Status is the moderation status of the account: "active" (default) or
	// "banned". A banned account cannot log in.
	Status string `json:"status,omitempty"`
	// BannedUntil is when a temporary ban lifts automatically. NULL means a
	// permanent ban (or no ban when Status is active).
	BannedUntil *time.Time `json:"banned_until,omitempty"`
	// IsBot marks an automated account created by a human owner through the
	// bots API. Bot accounts behave like normal users everywhere (they can
	// send messages, post, etc.) but render with a robot badge next to their
	// name and cannot log in interactively — they authenticate with a static
	// API token issued to their owner.
	IsBot bool `json:"is_bot"`
	// BotOwnerID is the human user who created this bot (nil for humans and
	// legacy rows that pre-date the bot accounts migration). The pointer type
	// keeps Scan honest about NULL values rather than failing the whole login.
	BotOwnerID *int64 `json:"bot_owner_id,omitempty"`
	// DeletedAt is set when the user requested account deletion. While set,
	// the account is hidden everywhere and login is refused. A purge job
	// erases the row 30 days later.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	// LastSeenAt is the timestamp of the user's last heartbeat. A user is
	// considered online while LastSeenAt is within the freshness window
	// (see repository.IsUserOnline).
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// Online mirrors the freshness check so JSON consumers do not have to
	// hit the clock themselves. Recomputed on every scan.
	Online bool `json:"online,omitempty"`
}

// Post represents a text post with optional attachments.
type Post struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Content     string    `json:"content"`
	Visibility  string    `json:"visibility,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ReplyCount  int64     `json:"reply_count,omitempty"`
	ViewCount   int64     `json:"view_count,omitempty"`
	UniqueViews int64     `json:"unique_views,omitempty"`
	// TipTotal is the sum of non-refunded tips (net amount credited to the
	// author) on this post, in cents.
	TipTotal int64 `json:"tip_total,omitempty"`
	// Reactions holds reaction counts keyed by reaction type (e.g. like).
	Reactions map[string]int64 `json:"reactions,omitempty"`
	// MyReaction is the authenticated viewer's own reaction, when present.
	MyReaction string `json:"my_reaction,omitempty"`
	// Favorited reports whether the authenticated viewer favorited this post.
	Favorited bool `json:"favorited,omitempty"`
	// FavoriteCount is the total number of users who favorited this post.
	FavoriteCount int64 `json:"favorite_count,omitempty"`
	// Tags are the free-form labels the author attached to the post. They
	// drive feed filtering and the hashtag picker on the post card.
	Tags       []string `json:"tags,omitempty"`
	Username   string   `json:"username,omitempty"`
	Nickname   string   `json:"nickname,omitempty"`
	AvatarURL  string   `json:"avatar_url,omitempty"`
	IsVerified bool     `json:"is_verified,omitempty"`
	// IsBot mirrors the author's bot flag so clients render the robot badge
	// next to the name without an extra lookup.
	IsBot bool `json:"is_bot,omitempty"`
	// MemberLevel/MemberActive are the author's current membership tier (0 =
	// none) and whether it is still active. Driver-end denormalization so
	// clients can render the member tier badge next to the author name.
	MemberLevel     int64      `json:"member_level,omitempty"`
	MemberActive    bool       `json:"member_active,omitempty"`
	MemberExpiresAt *time.Time `json:"member_expires_at,omitempty"`
	// NameColor/NameColorTo/NameDynamic mirror the author's custom display-name
	// styling so names render identically in feeds and profiles.
	NameColor             string        `json:"name_color,omitempty"`
	NameColorTo           string        `json:"name_color_to,omitempty"`
	NameDynamic           bool          `json:"name_dynamic,omitempty"`
	NameColors            NameColorList `json:"name_colors,omitempty"`
	NameGradientDirection string        `json:"name_gradient_direction,omitempty"`
	AvatarFrame           string        `json:"avatar_frame,omitempty"`
	Attachments           []Attachment  `json:"attachments,omitempty"`
	// Check is the red-packet style check attached to this post, when present.
	Check *Check `json:"check,omitempty"`
	// QuotedPostID references the post this post quotes or reposts (0 when
	// the post is not a quote/repost). The column is ON DELETE SET NULL, so
	// a non-zero id whose [QuotedPost] is nil means the quoted post was
	// deleted — clients render a placeholder.
	QuotedPostID int64 `json:"quoted_post_id,omitempty"`
	// QuotedPost is the nested quoted post, resolved one level deep (the
	// quoted post never carries its own quoted_post). Nil when the id is 0,
	// the quoted post is gone, or the viewer may not see it.
	QuotedPost *Post `json:"quoted_post,omitempty"`
}

// PostReply represents a reply to a post.
type PostReply struct {
	ID                    int64         `json:"id"`
	PostID                int64         `json:"post_id"`
	UserID                int64         `json:"user_id"`
	Content               string        `json:"content"`
	ParentID              *int64        `json:"parent_id,omitempty"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
	DeletedAt             *time.Time    `json:"deleted_at,omitempty"`
	Username              string        `json:"username,omitempty"`
	Nickname              string        `json:"nickname,omitempty"`
	AvatarURL             string        `json:"avatar_url,omitempty"`
	IsVerified            bool          `json:"is_verified,omitempty"`
	IsBot                 bool          `json:"is_bot,omitempty"`
	MemberLevel           int64         `json:"member_level,omitempty"`
	MemberActive          bool          `json:"member_active,omitempty"`
	MemberExpiresAt       *time.Time    `json:"member_expires_at,omitempty"`
	NameColor             string        `json:"name_color,omitempty"`
	NameColorTo           string        `json:"name_color_to,omitempty"`
	NameDynamic           bool          `json:"name_dynamic,omitempty"`
	NameColors            NameColorList `json:"name_colors,omitempty"`
	NameGradientDirection string        `json:"name_gradient_direction,omitempty"`
	AvatarFrame           string        `json:"avatar_frame,omitempty"`
	// ParentContent is a preview of the parent reply's content (for nested threads).
	ParentContent string `json:"parent_content,omitempty"`
	// ParentName is the parent reply author's display name.
	ParentName string `json:"parent_name,omitempty"`
	// Favorited reports whether the authenticated viewer favorited this reply.
	Favorited bool `json:"favorited,omitempty"`
	// FavoriteCount is the total number of users who favorited this reply.
	FavoriteCount int64        `json:"favorite_count,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
}

// Attachment represents a file stored in RustFS.
type Attachment struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	ObjectKey    string `json:"-"`
	OriginalName string `json:"original_name"`
	MimeType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	URL          string `json:"url"`
	ThumbURL     string `json:"thumb_url,omitempty"`
	// PreviewURL is a mid-size compressed rendition (longest edge 1440px)
	// generated alongside the thumbnail on upload. Clients show it for
	// quick viewing and fetch the original only on explicit request.
	PreviewURL string `json:"preview_url,omitempty"`
	Visibility string `json:"visibility"`
	// Bucket is the logical storage bucket the object was uploaded to.
	Bucket string `json:"bucket,omitempty"`
	// SHA256 is the content hash of the stored bytes, used to deduplicate
	// uploads. It is never exposed to clients.
	SHA256 string `json:"-"`
	// BurnAt is the burn-after-view deadline: set the first time a user other
	// than the uploader views the attachment; the storage sweeper deletes the
	// object and row once it passes. NULL = not armed (never viewed / not on a
	// burn message). Clients compare it against now() to show a countdown or a
	// "burned" placeholder.
	BurnAt    *time.Time `json:"burn_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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
	IsBot      bool       `json:"is_bot,omitempty"`
	// MemberLevel/MemberActive/name styling mirror the member's user record so
	// chat listings render the tier badge and colored name without extra joins.
	MemberLevel           int64         `json:"member_level,omitempty"`
	MemberActive          bool          `json:"member_active,omitempty"`
	MemberExpiresAt       *time.Time    `json:"member_expires_at,omitempty"`
	NameColor             string        `json:"name_color,omitempty"`
	NameColorTo           string        `json:"name_color_to,omitempty"`
	NameDynamic           bool          `json:"name_dynamic,omitempty"`
	NameColors            NameColorList `json:"name_colors,omitempty"`
	NameGradientDirection string        `json:"name_gradient_direction,omitempty"`
	AvatarFrame           string        `json:"avatar_frame,omitempty"`
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
	ID                          int64         `json:"id"`
	ConversationID              int64         `json:"conversation_id"`
	SenderID                    int64         `json:"sender_id"`
	Kind                        string        `json:"kind"` // text | check | system.join | system.leave | system.mute | system.unmute | system.mute.all | system.unmute.all
	Content                     string        `json:"content"`
	CheckID                     int64         `json:"check_id,omitempty"`
	ReplyToID                   *int64        `json:"reply_to_id,omitempty"`
	EditedAt                    *time.Time    `json:"edited_at,omitempty"`
	DeletedAt                   *time.Time    `json:"deleted_at,omitempty"`
	CreatedAt                   time.Time     `json:"created_at"`
	SenderName                  string        `json:"sender_name,omitempty"`
	SenderAvatar                string        `json:"sender_avatar,omitempty"`
	SenderVerified              bool          `json:"sender_verified,omitempty"`
	SenderIsBot                 bool          `json:"sender_is_bot,omitempty"`
	SenderMemberLevel           int64         `json:"sender_member_level,omitempty"`
	SenderMemberActive          bool          `json:"sender_member_active,omitempty"`
	SenderMemberExpiresAt       *time.Time    `json:"sender_member_expires_at,omitempty"`
	SenderNameColor             string        `json:"sender_name_color,omitempty"`
	SenderNameColorTo           string        `json:"sender_name_color_to,omitempty"`
	SenderNameDynamic           bool          `json:"sender_name_dynamic,omitempty"`
	SenderNameColors            NameColorList `json:"sender_name_colors,omitempty"`
	SenderNameGradientDirection string        `json:"sender_name_gradient_direction,omitempty"`
	SenderAvatarFrame           string        `json:"sender_avatar_frame,omitempty"`
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
	// Burn-after-read: BurnSeconds > 0 arms the message for self-destruction
	// after it has been read. BurnAt is stamped the first time a recipient
	// (never the sender) reads the message; clients render a countdown from
	// it and the chat service sweeper soft-deletes the row once it passes.
	BurnSeconds int        `json:"burn_seconds,omitempty"`
	BurnAt      *time.Time `json:"burn_at,omitempty"`
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
	Balance   Cents     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WalletTransaction represents a single balance change on a user's wallet.
type WalletTransaction struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	Amount       Cents     `json:"amount"`
	BalanceAfter Cents     `json:"balance_after"`
	Type         string    `json:"type"`
	Description  string    `json:"description"`
	OperatorID   int64     `json:"operator_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// PunishmentType enumerates the moderation actions recorded in user_punishments.
type PunishmentType string

// Supported punishment types.
const (
	PunishWarning PunishmentType = "warning"  // 警告：仅提醒，无限制
	PunishDemerit PunishmentType = "demerit"  // 记过：累积分，配套模板记录
	PunishRevoke  PunishmentType = "revoke"   // 剥夺权限：revoke 指定的 permission_key
	PunishTempBan PunishmentType = "temp_ban" // 暂时封禁：expires_at 前禁止登录
	PunishBan     PunishmentType = "ban"      // 永久封禁：禁止登录
	PunishUnban   PunishmentType = "unban"    // 解除封禁：恢复登录（历史保留）
	PunishRestore PunishmentType = "restore"  // 恢复权限：移除指定的 permission_key 封禁
)

// Punishment is one recorded moderation action on a user.
type Punishment struct {
	ID            int64          `json:"id"`
	UserID        int64          `json:"user_id"`
	OperatorID    int64          `json:"operator_id,omitempty"`
	Type          PunishmentType `json:"type"`
	PermissionKey string         `json:"permission_key,omitempty"`
	Reason        string         `json:"reason"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}
