package migrations

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// Seed creates the built-in groups and the default permissions.
//
// It runs after the core migrations and is idempotent: a group or a
// permission that already exists is left alone.
//
// The built-in groups are the implicit ones Flarum models as groups:
// guest, member and admin. The ADMIN group is a real row so an
// operator can grant it to somebody; guest and member are implicit and
// are resolved by the permission service, not by a row.
func Seed(app core.App, registry *repository.Registry) error {
	if err := seedGroups(app, registry); err != nil {
		return err
	}
	return seedPermissions(app, registry)
}

// builtInGroups are the groups the forum ships with.
//
// The guest and member groups are REAL rows, not implicit names: the
// permissions collection references them through a relation, so a row
// is what makes "every member may reply" expressible.
//
// Their well-known ids are the ids the permission service resolves an
// actor's group set against, so the two stay in step.
var builtInGroups = []struct {
	// ID is the group's well-known id.
	ID string
	// Position orders the group in the admin UI.
	Position int
}{
	{domain.GroupGuestID, -2},
	{domain.GroupMemberID, -1},
	{domain.GroupModeratorID, -1},
	{domain.GroupAdminID, 0},
}

// seedGroups creates the built-in groups.
func seedGroups(app core.App, registry *repository.Registry) error {
	collection, err := app.FindCollectionByNameOrId(registry.Physical("groups"))
	if err != nil {
		return err
	}

	for _, group := range builtInGroups {
		names := domain.GroupNames[group.ID]

		existing, err := app.FindFirstRecordByFilter(collection, "name_singular = {:name}",
			map[string]any{"name": names[0]})
		if err == nil && existing != nil {
			continue
		}

		record := core.NewRecord(collection)
		record.Set("id", group.ID)
		record.Set("name_singular", names[0])
		record.Set("name_plural", names[1])
		record.Set("position", group.Position)
		if err := app.Save(record); err != nil {
			return errors.Wrapf(err, "forum: could not create the %q group (%v)", group.ID, err)
		}
	}
	return nil
}

// defaultPermissions are the permissions each built-in group holds.
//
// The rows mirror upstream Flarum's default group permissions
// (framework/core/migrations/2018_07_21_000100_seed_default_group_permissions.php):
//
//   - the guest group gets only the read-only capability — a forum is
//     readable by anyone, but an anonymous caller may not post;
//   - the member group gets the powers every member has: starting a
//     discussion, replying, searching users, and liking;
//   - the MODERATOR group gets the moderation powers: renaming,
//     deleting and hiding a discussion, and editing or hiding any
//     post.
//
// The moderation powers belong to the moderator group and NOT to the
// member group, and that is load-bearing: a permission row for the
// member group is inherited by EVERY signed-in caller, so a row there
// would grant the moderation power to all of them. Upstream's
// policies make the same distinction: the author of a post may edit
// their own, and anyone else needs the moderator's permission.
//
// `editPost` and `deletePost` are deliberately NOT seeded for the
// member group: upstream grants them through the post's POLICY (the
// author) and through `discussion.editPosts` (the moderators), and a
// member-group row would mean "every member may edit every post".
var defaultPermissions = map[string][]string{
	domain.GroupGuestID: {
		domain.PermissionViewForum,
	},
	domain.GroupMemberID: {
		domain.PermissionViewForum,
		domain.PermissionStartDiscussion,
		domain.PermissionReply,
		domain.PermissionLikePost,
		domain.PermissionSearchUsers,
		// Voting is a participation power, so it belongs to the member
		// group: upstream's default permissions grant `votePosts` to
		// everybody who can post.
		domain.PermissionVotePost,
		// The leaderboard is readable by any member; it holds nothing
		// private, only who has been active.
		permissionViewRankingPage,
		// Voting in a poll is a participation power, exactly like
		// voting on a post: upstream grants `polls.vote` to everybody
		// who may take part in a discussion.
		domain.PermissionVotePoll,
		// The poll powers a member holds: voting in one, and changing a
		// vote they already cast (which the poll's own setting can also
		// grant to everybody).
		domain.PermissionChangePollVote,
		// Attaching a file is a participation power, exactly like
		// replying: upstream's README tells the operator to grant
		// `fof-upload.upload` on the permissions page, and this port
		// seeds it for the member group instead so attachments work on
		// a fresh forum — the operator can revoke it in the grid. A
		// member may also delete the file they uploaded; hiding or
		// deleting SOMEBODY ELSE's is a moderator power.
		domain.PermissionUploadFiles,
		domain.PermissionDeleteOwnUploads,
	},
	domain.GroupModeratorID: {
		domain.PermissionViewForum,
		domain.PermissionStartDiscussion,
		domain.PermissionReply,
		domain.PermissionLikePost,
		domain.PermissionSearchUsers,
		domain.PermissionEditDiscussion,
		domain.PermissionDeleteDiscussion,
		domain.PermissionHideDiscussion,
		domain.PermissionEditPost,
		domain.PermissionDeletePost,
		domain.PermissionHidePost,
		// The extension powers, which upstream puts in the moderator
		// group too: `flarum/lock` and `flarum/sticky` add a
		// `discussion.*` row each, and `flarum/suspend` a `user.suspend`.
		// They live here rather than in the member group because a
		// member-group row is inherited by every signed-in caller.
		domain.PermissionLockDiscussion,
		domain.PermissionStickyDiscussion,
		domain.PermissionSuspendUser,
		// The gamification powers: `fof/gamification` registers
		// `votePosts` and `canSeeVotes`, and both are moderator-tier —
		// a member may vote, but seeing WHO voted is a separate,
		// stricter permission upstream keeps apart.
		domain.PermissionVotePost,
		// Seeing WHO voted is a stricter power than voting, so it stays
		// with the moderators.
		domain.PermissionSeeVoters,
		// The leaderboard is a whole surface, and upstream gates it with
		// its own permission (`fof.gamification.viewRankingPage`).
		permissionViewRankingPage,
		// Changing a discussion's tags is a moderator power: upstream's
		// `discussion.tag` is seeded for the moderator group, and the
		// AUTHOR's own right to tag their discussion is a POLICY
		// (upstream's `DiscussionPolicy::tag()`), not a group row.
		domain.PermissionTagDiscussion,
		// Bypassing the tag count minimums is a moderator power too.
		domain.PermissionBypassTagCounts,
		// The tag administration powers: creating, editing and
		// deleting tags.
		domain.PermissionCreateTag,
		domain.PermissionEditTag,
		domain.PermissionDeleteTag,
		// Seeing WHO voted in a poll is a stricter power than voting,
		// so it stays with the moderators — upstream keeps it apart for
		// the same reason.
		domain.PermissionSeePollVoters,
		// Seeing a poll's results WITHOUT having voted in it is a third
		// power: upstream lets a poll hide its results until somebody
		// votes, and which of the two the reader gets is the
		// operator's choice.
		domain.PermissionSeePollVoteCount,
		// The upload moderation powers: hiding and deleting ANOTHER
		// account's files, and the shared library (a shared file
		// belongs to the forum rather than to its uploader, so every
		// write to one asks for `upload-shared-files` instead of
		// ownership).
		domain.PermissionUploadFiles,
		domain.PermissionDeleteOwnUploads,
		domain.PermissionDeleteUserUploads,
		domain.PermissionAccessSharedFiles,
		domain.PermissionUploadSharedFiles,
	},
}

// permissionViewRankingPage gates the leaderboard.
//
// It is upstream's own name (`fof.gamification.viewRankingPage`), and it
// is NOT in the domain package because it belongs to the gamification
// extension rather than to the forum's core — but the permission service
// resolves permissions by name, so the seed has to write it under the name
// the handler asks for.
const permissionViewRankingPage = "viewRankingPage"

// seedPermissions writes the default permission rows.
//
// The rows are written per group id, and the implicit groups (guest,
// member, moderator) use their well-known ids rather than a collection
// row: the permission service resolves them by name.
func seedPermissions(app core.App, registry *repository.Registry) error {
	collection, err := app.FindCollectionByNameOrId(registry.Physical("permissions"))
	if err != nil {
		return err
	}

	// The moderation powers used to be seeded for the MEMBER group,
	// which meant every signed-in caller inherited them: any member
	// could edit or delete any post. Upstream puts them in the
	// moderator group, and the member-group rows are removed so an
	// instance seeded by an older build does not keep them.
	//
	// Only the rows this seed itself wrote are removed — an operator's
	// own row for a built-in group is left alone, because there is no
	// way to tell it apart from one of ours by name alone.
	for _, permission := range memberGroupModerationPermissions {
		existing, err := app.FindFirstRecordByFilter(collection,
			"group = {:group} && permission = {:permission} && scope = ''",
			map[string]any{"group": domain.GroupMemberID, "permission": permission})
		if err != nil || existing == nil {
			continue
		}

		if err := app.Delete(existing); err != nil {
			return errors.Wrapf(err, "forum: could not remove the %q permission from the member group", permission)
		}
	}

	for groupID, permissions := range defaultPermissions {
		for _, permission := range permissions {
			existing, err := app.FindFirstRecordByFilter(collection,
				"group = {:group} && permission = {:permission} && scope = ''",
				map[string]any{"group": groupID, "permission": permission})
			if err == nil && existing != nil {
				continue
			}

			record := core.NewRecord(collection)
			record.Set("group", groupID)
			record.Set("permission", permission)
			record.Set("scope", "")
			record.Set("allowed", true)
			if err := app.Save(record); err != nil {
				return errors.Wrapf(err, "forum: could not seed the %q permission", permission)
			}
		}
	}
	return nil
}

// memberGroupModerationPermissions are the moderation powers that must
// NOT be in the member group.
//
// A member-group row is inherited by every signed-in caller, so a row
// here grants the moderation power to all of them. Upstream's default
// group permissions keep these in the moderator group.
var memberGroupModerationPermissions = []string{
	domain.PermissionEditPost,
	domain.PermissionDeletePost,
	domain.PermissionHidePost,
}
