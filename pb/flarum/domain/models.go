// Package domain holds the forum's domain models and the services that
// operate on them.
//
// The models are plain Go structs, decoupled from PocketBase: the
// repository layer maps records onto them, and the API layer maps them
// onto JSON:API resources. A plugin that needs a field of its own adds it
// through the extender mechanism rather than by reaching into the
// record.
package domain

import "time"

// Discussion states.
const (
	// DiscussionOpen is a discussion anyone with the permission may
	// reply to.
	DiscussionOpen = "open"
	// DiscussionLocked is a discussion that accepts no new replies.
	DiscussionLocked = "locked"
	// DiscussionHidden is a discussion left out of the lists.
	DiscussionHidden = "hidden"
	// DiscussionSticky is a discussion pinned to the top of its list.
	DiscussionSticky = "sticky"
)

// Post states.
const (
	// PostVisible is a post shown in the discussion.
	PostVisible = "visible"
	// PostHidden is a post hidden by a moderator.
	PostHidden = "hidden"
	// PostDeleted is a post removed by its author or a moderator.
	PostDeleted = "deleted"
)

// User is a forum account.
//
// It is the forum's own projection; the authoritative record lives in the
// shared PocketBase `users` collection and is only ever read.
type User struct {
	// ID is the shared record's id.
	ID string
	// Username is the account's name.
	Username string
	// DisplayName is the name shown in the UI.
	DisplayName string
	// AvatarURL is the account's avatar, empty when it has none.
	AvatarURL string
	// IsAdmin reports whether the account administers the forum.
	IsAdmin bool
	// JoinedAt is when the account joined the forum.
	JoinedAt time.Time
	// LastSeenAt is when the account was last seen.
	LastSeenAt time.Time
	// Groups holds the ids of the account's groups.
	Groups []string
	// DiscussionCount is how many discussions the account started.
	DiscussionCount int
	// CommentCount is how many posts the account wrote.
	CommentCount int
	// UnreadNotificationCount is how many unread notifications the
	// account has.
	UnreadNotificationCount int
}

// Group is a permission group.
type Group struct {
	// ID is the group's id.
	ID string
	// NameSingular is the name for one member, e.g. "Moderator".
	NameSingular string
	// NamePlural is the name for several members, e.g. "Moderators".
	NamePlural string
	// Color is the badge color, empty when unset.
	Color string
	// Icon is the badge icon classes, empty when unset.
	Icon string
	// IsHidden keeps the group out of profiles and lists.
	IsHidden bool
	// Position orders the group in the admin UI.
	Position int
	// MemberCount is how many accounts are in the group.
	MemberCount int
}

// Discussion is a thread.
type Discussion struct {
	// ID is the discussion's id.
	ID string
	// Title is the discussion's title.
	Title string
	// Slug is the discussion's URL slug.
	Slug string
	// UserID is the account that started the discussion.
	UserID string
	// CreatedAt is when the discussion was created.
	CreatedAt time.Time
	// LastPostedAt is when the discussion last received a reply.
	LastPostedAt time.Time
	// LastPostedUserID is the account that posted last.
	LastPostedUserID string
	// LastPostNumber is the number of the last post.
	LastPostNumber int
	// CommentCount is how many posts the discussion has.
	CommentCount int
	// ParticipantCount is how many distinct accounts posted in it.
	ParticipantCount int
	// State is one of the Discussion* constants.
	State string
	// IsSticky reports whether the discussion is pinned.
	IsSticky bool
	// IsHidden reports whether the discussion is hidden.
	IsHidden bool
	// HiddenAt is when the discussion was hidden, zero when it is not.
	HiddenAt time.Time
	// HiddenUserID is the account that hid it.
	HiddenUserID string
	// FirstPostID is the id of the discussion's first post.
	FirstPostID string
	// LastPostID is the id of the discussion's last post.
	LastPostID string
	// PostIDs holds the ids of the discussion's posts, in order.
	PostIDs []string
	// TagIDs holds the ids of the discussion's tags.
	TagIDs []string
	// ViewCount is how many times the discussion was opened.
	ViewCount int
}

// Post is a reply or a discussion's first post.
type Post struct {
	// ID is the post's id.
	ID string
	// DiscussionID is the discussion the post belongs to.
	DiscussionID string
	// UserID is the account that wrote the post.
	UserID string
	// Number is the post's position in the discussion, starting at 1.
	Number int
	// Content is the post's Markdown source.
	Content string
	// ContentHTML is the post's rendered body.
	ContentHTML string
	// ContentType is the source's format, e.g. "text/html".
	ContentType string
	// CreatedAt is when the post was written.
	CreatedAt time.Time
	// EditedAt is when the post was last edited, zero when never.
	EditedAt time.Time
	// EditedUserID is the account that last edited it.
	EditedUserID string
	// HiddenAt is when the post was hidden, zero when it is not.
	HiddenAt time.Time
	// HiddenUserID is the account that hid it.
	HiddenUserID string
	// IsHidden reports whether the post is hidden.
	IsHidden bool
	// IPAddress is the address the post was written from.
	IPAddress string
	// Number is the post's number within the discussion.
	Type string
}

// Notification is an in-app notification.
type Notification struct {
	// ID is the notification's id.
	ID string
	// UserID is the account the notification belongs to.
	UserID string
	// Type is the notification kind, e.g. "postLiked".
	Type string
	// DiscussionID is the related discussion, when there is one.
	DiscussionID string
	// PostID is the related post, when there is one.
	PostID string
	// Subject is the notification's title.
	Subject string
	// Excerpt is the notification's summary.
	Excerpt string
	// FromUserID is the account that caused the notification.
	FromUserID string
	// ReadAt is when the notification was read, zero when it is not.
	ReadAt time.Time
	// CreatedAt is when the notification was created.
	CreatedAt time.Time
	// IsRead reports whether the notification was read.
	IsRead bool
}

// Tag is a discussion category.
//
// Tags live in the Tags plugin; the type lives here so the core domain
// can reference a tag id without depending on the plugin package.
type Tag struct {
	// ID is the tag's id.
	ID string
	// Name is the tag's name.
	Name string
	// Slug is the tag's URL slug.
	Slug string
	// Description is the tag's description.
	Description string
	// ParentID is the parent tag's id, empty for a root tag.
	ParentID string
	// Color is the tag's color, empty when unset.
	Color string
	// Icon is the tag's icon classes, empty when unset.
	Icon string
	// Position orders the tag among its siblings.
	Position int
	// IsHidden keeps discussions with this tag out of the All list.
	IsHidden bool
	// IsPrimary marks a primary tag.
	IsPrimary bool
	// IsRestricted gives the tag its own permission scope.
	IsRestricted bool
	// DiscussionCount is how many discussions carry the tag.
	DiscussionCount int
	// ChildIDs holds the ids of the tag's children.
	ChildIDs []string
}

// Permission keys.
//
// The set mirrors upstream Flarum's v1 scope, restricted to what the
// forum core and the first-party plugins need.
const (
	// PermissionStartDiscussion allows starting a discussion.
	PermissionStartDiscussion = "startDiscussion"
	// PermissionEditDiscussion allows renaming a discussion.
	PermissionEditDiscussion = "editDiscussion"
	// PermissionDeleteDiscussion allows deleting a discussion.
	PermissionDeleteDiscussion = "deleteDiscussion"
	// PermissionHideDiscussion allows hiding a discussion.
	PermissionHideDiscussion = "hideDiscussion"
	// PermissionReply allows replying in a discussion.
	PermissionReply = "reply"
	// PermissionEditPost allows editing a post.
	PermissionEditPost = "editPost"
	// PermissionDeletePost allows deleting a post.
	PermissionDeletePost = "deletePost"
	// PermissionHidePost allows hiding a post.
	PermissionHidePost = "hidePost"
	// PermissionLikePost allows liking a post.
	PermissionLikePost = "likePost"
	// PermissionViewForum allows seeing the forum at all.
	PermissionViewForum = "viewForum"
	// PermissionSearchUsers allows searching for accounts.
	PermissionSearchUsers = "searchUsers"
	// PermissionCreateGroup allows creating a group.
	PermissionCreateGroup = "createGroup"
	// PermissionEditGroup allows editing a group.
	PermissionEditGroup = "editGroup"
	// PermissionDeleteGroup allows deleting a group.
	PermissionDeleteGroup = "deleteGroup"
	// PermissionCreateTag allows creating a tag.
	PermissionCreateTag = "createTag"
	// PermissionEditTag allows editing a tag.
	PermissionEditTag = "editTag"
	// PermissionDeleteTag allows deleting a tag.
	PermissionDeleteTag = "deleteTag"
	// PermissionAdministrate allows administering the forum.
	PermissionAdministrate = "administrate"
)

// Group IDs of the built-in groups.
//
// They are stable so a permission row can name its group, and they
// satisfy PocketBase's rule that a record id is EXACTLY 15 characters
// long — which is why the ids are terse.
//
// The guest and member groups are REAL rows, not implicit names: the
// permissions collection references them through a relation, so a row
// is what makes "every member may reply" expressible. The permission
// service resolves an actor's group set against these ids, so the two
// stay in step.
const (
	// GroupGuestID is the group of anonymous callers.
	GroupGuestID = "forumgroupguest"
	// GroupMemberID is the group of every signed-in account.
	GroupMemberID = "forumgroupmembr"
	// GroupAdminID is the group of forum administrators.
	GroupAdminID = "forumgroupadmin"
	// GroupModeratorID is the group of forum moderators.
	//
	// Upstream Flarum has a moderator group, and the default
	// permissions put the MODERATION powers in it: renaming a
	// discussion, deleting one, editing and hiding any post. The
	// member group holds only the powers every member has (starting a
	// discussion, replying, searching users).
	//
	// The distinction is what keeps a group-scoped permission from
	// meaning "everybody": a row for the member group is inherited by
	// every signed-in caller, so a moderation row there would grant
	// the moderation power to all of them.
	GroupModeratorID = "forumgroupmoder"
)

// GroupNames are the display names of the built-in groups.
var GroupNames = map[string][2]string{
	GroupGuestID:     {"Guest", "Guests"},
	GroupMemberID:    {"Member", "Members"},
	GroupAdminID:     {"Admin", "Admins"},
	GroupModeratorID: {"Moderator", "Moderators"},
}
