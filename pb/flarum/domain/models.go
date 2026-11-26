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
	// SuspendedUntil is when the account's suspension ends, zero when it
	// is not suspended. Upstream models an indefinite suspension as a date
	// far in the future.
	SuspendedUntil time.Time
	// SuspendReason is why the account was suspended, visible to
	// moderators only.
	SuspendReason string
	// SuspendMessage is what the suspended account is shown. It is
	// visible to the suspended account and to moderators.
	SuspendMessage string
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
	//
	// It carries the discussion's VISIBILITY (open or hidden). Locking and
	// pinning used to be encoded here too, as `locked` and `sticky`, which
	// made the two mutually exclusive: a pinned discussion could not be
	// locked, and locking a pinned one silently unpinned it. Upstream keeps
	// them in two independent boolean columns (`is_locked`, `is_sticky`), so
	// the state column now carries visibility only and the two flags have
	// their own fields.
	State string
	// IsLocked reports whether the discussion accepts no new replies.
	IsLocked bool
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
	//
	// A tag with NO position is a SECONDARY tag, and a tag with a
	// position and no parent is a PRIMARY PARENT — upstream's
	// distinction, and the one the count rules are built on.
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
	// DefaultSort is the sort a tag's discussion list opens with.
	//
	// It is stored as the ALIAS that appears in a URL rather than the
	// API sort behind it, so that it round-trips through the dropdown
	// unchanged — upstream's rule, and the reason a tag's sort survives
	// an extension that comes and goes.
	DefaultSort string
	// LastPostedAt is when the tag's last discussion was posted.
	LastPostedAt time.Time
	// LastPostedDiscussion is the tag's last discussion.
	LastPostedDiscussion string
	// LastPostedUserID is the author of the tag's last discussion.
	LastPostedUserID string
}

// HasPosition reports whether the tag carries a position.
//
// A tag with no position is a SECONDARY tag; a tag with a position is
// a PRIMARY tag — and a primary tag with no parent is a PRIMARY PARENT,
// which is what the count rules count.
func (t *Tag) HasPosition() bool { return t.Position != 0 || t.IsPrimary }

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
	// PermissionTagDiscussion allows changing a discussion's tags.
	//
	// It is upstream's `discussion.tag`, which the tags extension
	// registers and seeds for the MODERATOR group.
	//
	// It is a separate power from the one that lets the discussion's
	// AUTHOR change the tags, because the author rule is a POLICY and
	// this is a permission: upstream's `DiscussionPolicy::tag()` asks
	// for the policy first and falls back to the permission.
	PermissionTagDiscussion = "tagDiscussion"
	// PermissionBypassTagCounts allows starting a discussion without
	// meeting the tag count minimums.
	//
	// It is upstream's `bypassTagCounts`.
	PermissionBypassTagCounts = "bypassTagCounts"
	// PermissionCreateTag allows creating a tag.
	PermissionCreateTag = "createTag"
	// PermissionEditTag allows editing a tag.
	PermissionEditTag = "editTag"
	// PermissionDeleteTag allows deleting a tag.
	PermissionDeleteTag = "deleteTag"
	// PermissionLockDiscussion allows locking and unlocking a discussion.
	//
	// It is upstream's `discussion.lock`, which its `flarum/lock`
	// extension registers and its `Lock\Access\DiscussionPolicy`
	// reads.
	PermissionLockDiscussion = "lock"
	// PermissionStickyDiscussion allows pinning and unpinning a discussion.
	//
	// It is upstream's `discussion.sticky`, registered by `flarum/sticky`.
	PermissionStickyDiscussion = "sticky"
	// PermissionSuspendUser allows suspending an account.
	//
	// It is upstream's `user.suspend`, registered by `flarum/suspend`.
	PermissionSuspendUser = "suspendUser"
	// PermissionVotePost allows voting on a post.
	//
	// It is upstream's `discussion.votePosts`, which `fof/gamification`
	// registers and its `Access\PostPolicy::vote()` reads against the
	// POST'S DISCUSSION.
	PermissionVotePost = "votePost"
	// PermissionSeeVoteCount allows seeing a post's vote TALLY.
	//
	// It is upstream's `discussion.canSeeVotes` — a power of its own, and
	// NOT the same one as seeing who cast the votes: upstream's
	// `Access\PostPolicy` asks `canSeeVotes` for the score and
	// `canSeeVoters` for the names, and a reader may hold either without
	// the other. Mapping both onto one key made the admin grid unable to
	// express the difference, and granted the names to everyone who could
	// see a number.
	PermissionSeeVoteCount = "canSeeVotes"
	// PermissionSeeVoters allows seeing WHO voted.
	//
	// It is upstream's `discussion.canSeeVoters`. The frontend reaches it
	// through the post's `seeVoters` attribute, and the voters modal is
	// rendered only when it is true.
	PermissionSeeVoters = "canSeeVoters"
	// PermissionViewRankingPage allows seeing the leaderboard.
	//
	// It is upstream's `fof.gamification.viewRankingPage`, the extension's
	// own key: `fof/gamification`'s admin page registers the checkbox
	// under that name, and its `LeaderboardResource` denies the request
	// when the actor cannot.
	PermissionViewRankingPage = "viewRankingPage"
	// PermissionVotePoll allows voting in a poll.
	//
	// It is upstream's `polls.vote`, which `fof/polls` registers and its
	// `Access\PollPolicy::vote()` reads. It is separate from the post
	// vote permission on purpose: a poll's vote is a different act with
	// a different scope, and upstream scopes it to the discussion.
	PermissionVotePoll = "votePoll"
	// PermissionChangePollVote allows changing a vote that was already
	// cast.
	//
	// It is upstream's `polls.changeVote`. It is separate from the vote
	// permission because upstream asks for it ONLY when the new
	// selection differs from the old one — so a voter who submits the
	// same selection twice is not refused for a vote they did not
	// change.
	PermissionChangePollVote = "changePollVote"
	// PermissionSeePollVoters allows seeing WHO voted in a poll.
	//
	// It is upstream's `polls.seeVoters`. Seeing the voters is a
	// stricter power than seeing the counts, which is a third power
	// (`PermissionSeePollVoteCount`).
	PermissionSeePollVoters = "seePollVoters"
	// PermissionSeePollVoteCount allows seeing a poll's counts without
	// having voted in it.
	//
	// It is upstream's `polls.viewResultsWithoutVoting`. It is a
	// separate power from the vote permission because upstream lets a
	// poll hide its results until somebody has voted: the reader may
	// vote OR see the results, and which one is granted is the
	// operator's choice.
	PermissionSeePollVoteCount = "seePollVoteCount"
	// PermissionUploadFiles allows uploading a file.
	//
	// It is upstream's `fof-upload.upload`, the base permission the
	// extension's `Upload` command asserts before it stores anything.
	// Every other upload power is layered on top of it.
	PermissionUploadFiles = "upload"
	// PermissionViewUserUploads allows listing ANOTHER account's uploads.
	//
	// It is upstream's `fof-upload.viewUserUploads`. Without it the
	// listing only ever describes the caller's own files, which is
	// what its `FileResource::scope()` narrows to.
	PermissionViewUserUploads = "viewUserUploads"
	// PermissionAccessSharedFiles allows listing the shared file
	// library.
	//
	// It is upstream's `fof-upload.access-shared-files`.
	PermissionAccessSharedFiles = "accessSharedFiles"
	// PermissionUploadSharedFiles allows uploading, hiding and deleting
	// a SHARED file.
	//
	// It is upstream's `fof-upload.upload-shared-files`. A shared file
	// belongs to the forum rather than to its uploader, so every write
	// to one asks for this permission instead of ownership.
	PermissionUploadSharedFiles = "uploadSharedFiles"
	// PermissionDeleteOwnUploads allows deleting a file one uploaded.
	//
	// It is upstream's `fof-upload.deleteOwnUploads`, and it is
	// SEPARATE from the ownership rule on purpose: upstream's
	// `FilePolicy::delete()` asks `owns() AND this permission`, so an
	// operator can let a member upload without letting them delete —
	// the media manager then shows no delete button.
	PermissionDeleteOwnUploads = "deleteOwnUploads"
	// PermissionDeleteUserUploads allows hiding and deleting another
	// account's uploads.
	//
	// It is upstream's `fof-upload.deleteUserUploads` — the moderator
	// tier of the media manager.
	PermissionDeleteUserUploads = "deleteUserUploads"
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
