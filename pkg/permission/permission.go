// Package permission enumerates the permission keys used for role-based
// authorization across the gateway and services.
package permission

// Permission keys — one per feature.
const (
	// Account
	AccountView         = "account.view"
	AccountProfileEdit  = "account.profile.edit"
	AccountAvatarEdit   = "account.avatar.edit"
	AccountBannerEdit   = "account.banner.edit"
	AccountVerify       = "account.verify"
	AccountFollow       = "account.follow"
	UserAdjustExp       = "user.adjust_exp"
	UserMembershipGrant = "user.membership.grant"
	UserPunish          = "user.punish"

	// Storage
	StorageUpload = "storage.upload"
	StorageList   = "storage.list"
	StorageGet    = "storage.get"
	StorageDelete = "storage.delete"

	// Chat
	ChatView           = "chat.view"
	ChatSend           = "chat.send"
	ChatEdit           = "chat.edit"
	ChatDelete         = "chat.delete"
	ChatRequestSend    = "chat.request.send"
	ChatRequestApprove = "chat.request.approve"
	ChatGroupCreate    = "chat.group.create"
	ChatGroupInvite    = "chat.group.invite"
	ChatGroupManage    = "chat.group.manage"
	ChatNoteEdit       = "chat.note.edit"
	ChatGroupNickname  = "chat.group.nickname"

	// Posts
	PostsView        = "posts.view"
	PostsCreate      = "posts.create"
	PostsEdit        = "posts.edit"
	PostsDelete      = "posts.delete"
	PostsReplyCreate = "posts.reply.create"
	PostsReplyEdit   = "posts.reply.edit"
	PostsReplyDelete = "posts.reply.delete"
	PostsReact       = "posts.react"
	PostsFavorite    = "posts.favorite"

	// Wallet
	WalletView   = "wallet.view"
	WalletManage = "wallet.manage"

	// Plugins
	PluginManage = "plugin.manage"

	// App announcements (server-wide notices)
	AppAnnouncementsManage = "app.announcements.manage"
)

// AdminPermissions returns the permission keys that must never be granted to
// the default "everyone" group: they let the holder act on other users'
// accounts or on server-wide state (mint currency, change exp/membership,
// punish or unban, verify accounts, manage plugins and announcements).
//
// Keep this list in sync when adding a new administrative key: a key that is
// missing here is handed to every registered user by seedDefaultGroup.
func AdminPermissions() []string {
	return []string{
		AccountVerify,
		UserAdjustExp,
		UserMembershipGrant,
		UserPunish,
		WalletManage,
		PluginManage,
		AppAnnouncementsManage,
	}
}

// DefaultGroupPermissions returns the permission keys the default "everyone"
// group is granted: ordinary features every registered user may use, with no
// authority over other users or over server-wide state.
func DefaultGroupPermissions() []string {
	all := All()
	admin := make(map[string]struct{}, len(AdminPermissions()))
	for _, key := range AdminPermissions() {
		admin[key] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, key := range all {
		if _, isAdmin := admin[key]; isAdmin {
			continue
		}
		out = append(out, key)
	}
	return out
}

// IsAdminPermission reports whether key is an administrative permission that
// must not be granted to the default group.
func IsAdminPermission(key string) bool {
	for _, adminKey := range AdminPermissions() {
		if adminKey == key {
			return true
		}
	}
	return false
}

// All returns every known permission key.
func All() []string {
	return []string{
		AccountView, AccountProfileEdit, AccountAvatarEdit, AccountBannerEdit, AccountVerify, AccountFollow, UserAdjustExp, UserMembershipGrant, UserPunish,
		StorageUpload, StorageList, StorageGet, StorageDelete,
		ChatView, ChatSend, ChatEdit, ChatDelete, ChatRequestSend, ChatRequestApprove,
		ChatGroupCreate, ChatGroupInvite, ChatGroupManage, ChatNoteEdit, ChatGroupNickname,
		PostsView, PostsCreate, PostsEdit, PostsDelete, PostsReplyCreate, PostsReplyEdit, PostsReplyDelete, PostsReact, PostsFavorite,
		WalletView, WalletManage,
		PluginManage,
		AppAnnouncementsManage,
	}
}

// DefaultGroupName is the built-in "everyone" group, granted the ordinary
// features in DefaultGroupPermissions — never the administrative keys.
const DefaultGroupName = "所有人"

// AdminGroupName is the built-in group carrying the administrative permission
// keys. Operators add the accounts that should be administrators to it.
const AdminGroupName = "管理员"
