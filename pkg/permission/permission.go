package permission

// Permission keys — one per feature.
const (
	// Account
	AccountView        = "account.view"
	AccountProfileEdit = "account.profile.edit"
	AccountAvatarEdit  = "account.avatar.edit"
	AccountBannerEdit  = "account.banner.edit"
	AccountVerify      = "account.verify"
	AccountFollow      = "account.follow"

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

	// Wallet
	WalletView   = "wallet.view"
	WalletManage = "wallet.manage"
)

// All returns every known permission key.
func All() []string {
	return []string{
		AccountView, AccountProfileEdit, AccountAvatarEdit, AccountBannerEdit, AccountVerify, AccountFollow,
		StorageUpload, StorageList, StorageGet, StorageDelete,
		ChatView, ChatSend, ChatEdit, ChatDelete, ChatRequestSend, ChatRequestApprove,
		ChatGroupCreate, ChatGroupInvite, ChatGroupManage, ChatNoteEdit, ChatGroupNickname,
		PostsView, PostsCreate, PostsEdit, PostsDelete, PostsReplyCreate, PostsReplyEdit, PostsReplyDelete, PostsReact,
		WalletView, WalletManage,
	}
}

// DefaultGroupName is the built-in "everyone" group with full permissions.
const DefaultGroupName = "所有人"
