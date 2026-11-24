package domain

import (
	"slices"
	"strings"
)

// The permission keys the admin application speaks, and the keys the
// forum stores.
//
// Upstream Flarum's permission keys are namespaced by the model they
// act on: `discussion.reply`, `discussion.rename`, `discussion.editPosts`,
// `discussion.hidePosts`, `discussion.viewIpsPosts`. Its POLICIES read
// them through a prefix — `DiscussionPolicy::can()` asks for
// `'discussion.'.$ability`, and `PostPolicy::can()` asks for
// `$ability.'Posts'`.
//
// This port's keys are FLAT: `reply`, `editDiscussion`, `editPost`,
// `hidePost`, `deletePost`. The forum's own handlers ask the permission
// service for those, and the seeded rows carry them.
//
// The admin application, however, is upstream's own frontend and it
// speaks upstream's names — `PermissionsPage.tsx` registers
// `permission: 'discussion.reply'`, and `PermissionDropdown.tsx` POSTs
// that key verbatim to `/api/permission`.
//
// So the admin surface has to TRANSLATE: a key that arrives from the
// admin application is upstream's name and is stored as the forum's
// flat name, and a key that goes out to the admin application is the
// forum's flat name and is sent as upstream's name.
//
// Without the translation the admin's permissions page is inert: every
// key it writes is a key the forum never reads, so granting a
// permission changes nothing. That is the same class of bug as the
// group-id constants in `Group.ts` — a vendored frontend whose
// identifiers do not match the backend's.

// upstreamPermissionKeys maps an upstream permission key onto the
// forum's own.
//
// The map is the authoritative one: the admin application sends
// upstream's names, and these are the keys the forum's handlers and
// its seeded rows use.
//
// The keys the forum's POLICIES reach through upstream's prefixes are
// included explicitly, because upstream's prefixes are not mechanical:
//
//   - upstream's `DiscussionPolicy::can($ability)` asks for
//     `discussion.<ability>`, and the seeded moderation powers are
//     `discussion.rename`, `discussion.hide` and `discussion.delete`.
//   - upstream's `PostPolicy::can($ability)` asks for
//     `<ability>Posts`, and the seeded powers are
//     `discussion.editPosts` and `discussion.hidePosts`.
//
// Note that upstream has NO `discussion.deletePosts` row in its seed
// (it seeds `discussion.editPosts` and `discussion.hidePosts`), even
// though its admin page registers a `discussion.deletePosts` control.
var upstreamPermissionKeys = map[string]string{
	// The view and participation powers, which upstream's seed writes
	// with their bare names.
	"viewForum":        PermissionViewForum,
	"startDiscussion":  PermissionStartDiscussion,
	"searchUsers":      PermissionSearchUsers,
	"discussion.reply": PermissionReply,

	// The discussion moderation powers.
	"discussion.rename": PermissionEditDiscussion,
	"discussion.hide":   PermissionHideDiscussion,
	"discussion.delete": PermissionDeleteDiscussion,

	// The post moderation powers.
	"discussion.editPosts":   PermissionEditPost,
	"discussion.hidePosts":   PermissionHidePost,
	"discussion.deletePosts": PermissionDeletePost,

	// The extension moderation powers. Each is registered by the
	// extension that owns the action, as upstream's admin pages do:
	// `flarum/lock` sends `discussion.lock`, `flarum/sticky` sends
	// `discussion.sticky`, and `flarum/suspend` sends `user.suspend`.
	"discussion.lock":   PermissionLockDiscussion,
	"discussion.sticky": PermissionStickyDiscussion,
	"user.suspend":      PermissionSuspendUser,

	// The gamification powers. `fof/gamification` registers
	// `votePosts` and `canSeeVotes` (its `Access\PostPolicy` reads both
	// against a discussion), and its admin page sends the same names.
	"votePosts":    PermissionVotePost,
	"canSeeVotes":  PermissionSeeVoters,
	"canSeeVoters": PermissionSeeVoters,
}

// forumPermissionKeys is the reverse of upstreamPermissionKeys: the
// forum's own key names to the upstream names the admin application
// expects.
//
// It is built from the same table, so the two can never drift apart.
var forumPermissionKeys = reversePermissionKeys(upstreamPermissionKeys)

// reversePermissionKeys inverts a permission-key table.
//
// A key that maps to itself (`viewForum`) is its own reverse. A key
// that several upstream names map onto (there is none today, but the
// table is free to grow) takes the FIRST upstream name in a stable
// order, so the result does not depend on Go's map iteration order.
func reversePermissionKeys(table map[string]string) map[string]string {
	reverse := make(map[string]string, len(table))
	for _, upstream := range sortedKeys(table) {
		forum := table[upstream]
		if _, taken := reverse[forum]; taken {
			continue
		}
		reverse[forum] = upstream
	}
	return reverse
}

// sortedKeys returns a table's keys in a stable order.
func sortedKeys(table map[string]string) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// ForumPermissionKey translates an upstream permission key into the
// forum's own.
//
// A key that is not in the table is passed through UNCHANGED. That is
// deliberate: the keys are free-form strings, and an extension may
// register its own. A key the forum does not know is stored under its
// own name, so it round-trips — it simply grants nothing, because no
// handler asks for it.
//
// The forum's own keys are also passed through unchanged, so a client
// that speaks the forum's dialect (an API consumer rather than the
// admin page) still works. The check is "is this a key we know the
// upstream name of", which is unambiguous because the two dialects
// name disjoint sets of things.
func ForumPermissionKey(upstream string) string {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return ""
	}

	if forum, known := upstreamPermissionKeys[upstream]; known {
		return forum
	}

	return upstream
}

// UpstreamPermissionKey translates one of the forum's own permission
// keys into the upstream name the admin application expects.
//
// A key with no upstream name is passed through unchanged: it is an
// extension's own key, and the admin page will show it verbatim.
func UpstreamPermissionKey(forum string) string {
	forum = strings.TrimSpace(forum)
	if forum == "" {
		return ""
	}

	if upstream, known := forumPermissionKeys[forum]; known {
		return upstream
	}

	return forum
}
