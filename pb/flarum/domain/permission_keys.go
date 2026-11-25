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

	// The like powers. The likes extension registers
	// `discussion.likePosts` and seeds it for the member group.
	"discussion.likePosts": PermissionLikePost,

	// The tag powers. The tags extension registers `discussion.tag`
	// (seeded for the moderators) and `bypassTagCounts`, and its admin
	// page sends the same names.
	"discussion.tag":  PermissionTagDiscussion,
	"bypassTagCounts": PermissionBypassTagCounts,

	// The gamification powers. `fof/gamification` registers
	// `votePosts` and `canSeeVotes` (its `Access\PostPolicy` reads both
	// against a discussion), and its admin page sends the same names.
	"votePosts":    PermissionVotePost,
	"canSeeVotes":  PermissionSeeVoters,
	"canSeeVoters": PermissionSeeVoters,

	// The poll powers. `fof/polls` registers `polls.vote`,
	// `polls.changeVote` and `polls.viewResultsWithoutVoting` (its
	// `Access\PollPolicy` reads them against a discussion), and it
	// registers the tag-scoped `discussion.polls.*` pair the tags
	// extension's permission scoping uses.
	"polls.vote":                                PermissionVotePoll,
	"polls.changeVote":                          PermissionChangePollVote,
	"polls.seeVoters":                           PermissionSeePollVoters,
	"polls.viewResultsWithoutVoting":            PermissionSeePollVoteCount,
	"discussion.polls.vote":                     PermissionVotePoll,
	"discussion.polls.viewResultsWithoutVoting": PermissionSeePollVoteCount,
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
// that several upstream names map onto takes the CANONICAL name —
// the one the extension's own admin page sends — so the result does
// not depend on Go's map iteration order.
//
// The canonical name wins over a tag-scoped spelling of the same
// power: `polls.vote` is the name `fof/polls` registers, and
// `discussion.polls.vote` is the tag-scoped variant the tags
// extension's scoping uses. Both mean the same power, so the reverse
// map must pick the canonical one — a reverse map that answered
// `discussion.polls.vote` would write a name the extension's own
// admin page does not show.
func reversePermissionKeys(table map[string]string) map[string]string {
	reverse := make(map[string]string, len(table))
	for _, upstream := range sortedKeys(table) {
		forum := table[upstream]
		if _, taken := reverse[forum]; taken {
			continue
		}
		reverse[forum] = upstream
	}

	// A canonical name overrides a tag-scoped one that sorted ahead of
	// it.
	for forum, upstream := range reverse {
		if canonical, ok := canonicalPermissionKeys[forum]; ok && canonical != upstream {
			reverse[forum] = canonical
		}
	}

	return reverse
}

// canonicalPermissionKeys names the canonical upstream spelling of a
// forum permission key.
//
// A key that appears here wins the reverse mapping even when a
// tag-scoped spelling of the same power sorts ahead of it.
var canonicalPermissionKeys = map[string]string{
	PermissionVotePoll:         "polls.vote",
	PermissionSeePollVoteCount: "polls.viewResultsWithoutVoting",
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

// The tag scope prefix.
//
// Upstream encodes a tag's permission scope IN THE KEY: a row for a
// restricted tag is `tag5.viewForum`, and the admin page writes that
// spelling (see upstream's `addTagsPermissionScope.tsx`, which posts
// `tag${tag.id()}.${permission}`).
//
// This forum has a separate `scope` COLUMN for the same thing, so the
// key arrives carrying its scope and the two have to be split apart:
// `ForumPermissionKey` returns the key and the scope, and the caller
// stores them in their own places.
const tagScopePrefix = "tag"

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
	return translatePermissionKey(upstream)
}

// ForumPermissionKeyAndScope translates an upstream permission key into
// the forum's own, and splits off a tag scope.
//
// Upstream's tag-scoped key is `tag{id}.{permission}`, and the id
// numbers the tag's permission scope. The forum stores the scope in
// its own COLUMN, so the two are separated here: the key becomes the
// forum's own name and the scope becomes the tag's id.
//
// The id is the tag's own id, and THIS PORT'S tag ids are PocketBase
// ids — 15 characters of digits and letters — not upstream's integers.
// A parser that demanded a NUMERIC id therefore never split a scoped
// key written by this port's own admin grid, and the grant was stored
// as a GLOBAL row: a restricted tag that granted access to everybody.
// The id is validated as an id, not as a number.
//
// A key that names no tag scope is returned with an empty scope.
func ForumPermissionKeyAndScope(upstream string) (string, string) {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return "", ""
	}

	// A tag-scoped key: `tag5.viewForum`.
	if rest, ok := strings.CutPrefix(upstream, tagScopePrefix); ok {
		id, remainder, found := strings.Cut(rest, ".")
		if found && isTagScopeID(id) {
			return translatePermissionKey(remainder), id
		}
	}

	return translatePermissionKey(upstream), ""
}

// translatePermissionKey translates an upstream permission key into the
// forum's own, without looking for a tag scope.
//
// It is the inner half of [ForumPermissionKeyAndScope]: the scope
// search must not call back into it, or the two would recurse into
// each other until the stack overflows.
func translatePermissionKey(upstream string) string {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return ""
	}

	if forum, known := upstreamPermissionKeys[upstream]; known {
		return forum
	}

	return upstream
}

// isTagScopeID reports whether a string can be the id part of a
// tag-scoped permission key.
//
// It is the tag's own id, and this port's tag ids are PocketBase ids —
// 15 characters of digits and letters. Upstream's are integers, so both
// spellings have to be accepted.
//
// The check exists to tell a SCOPED key from a plain permission whose
// name merely starts with "tag": `tagBypassTagCounts` is a permission the
// forum may know, and it names no tag. The rule is that the id must be a
// run of id characters — letters and digits — and must not be empty.
func isTagScopeID(value string) bool {
	if value == "" {
		return false
	}

	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}

	return true
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
