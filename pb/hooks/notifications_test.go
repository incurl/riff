package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotification_MEMO_COMMENT_NotifiesParentAuthor confirms a
// comment memo (via memo_relations) produces a MEMO_COMMENT row
// for the parent memo's author.
func TestNotification_MEMO_COMMENT_NotifiesParentAuthor(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")
	bob := newUserForHook(t, app, "bob")

	memos, relations := bootstrapMemoAndRelations(t, app)
	parent := mustInsertMemo(t, app, memos, "hello", "PUBLIC", alice.Id, "")
	// Bob comments on Alice's memo.
	comment := mustInsertMemo(t, app, memos, "nice take", "PUBLIC", bob.Id, "")
	mustInsertRelation(t, app, relations, comment.Id, parent.Id, "COMMENT")

	row := notifRow(t, app, alice.Id, "MEMO_COMMENT")
	require.NotNil(t, row, "alice should have a MEMO_COMMENT row")
	payload := decodeNotificationPayload(t, row)
	assert.Equal(t, "memos/"+comment.Id, payload["memo"])
	assert.Equal(t, "memos/"+parent.Id, payload["related_memo"])
	assert.Equal(t, bob.Id, payload["from_user"].(map[string]any)["id"], "from_user is the commenter")
}

// TestNotification_MEMO_COMMENT_SkipsSelfComment: when the parent
// author comments on their own memo, no inbox row is created.
func TestNotification_MEMO_COMMENT_SkipsSelfComment(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")

	memos, relations := bootstrapMemoAndRelations(t, app)
	parent := mustInsertMemo(t, app, memos, "self", "PUBLIC", alice.Id, "")
	comment := mustInsertMemo(t, app, memos, "thinking out loud", "PUBLIC", alice.Id, "")
	mustInsertRelation(t, app, relations, comment.Id, parent.Id, "COMMENT")

	assert.Nil(t, notifRow(t, app, alice.Id, "MEMO_COMMENT"), "self-comment must not produce an inbox row")
}

// TestNotification_MEMO_MENTION_NotifiesMentioned: a memo with
// @bob in the body delivers a MEMO_MENTION to bob.
func TestNotification_MEMO_MENTION_NotifiesMentioned(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")
	bobRec := newUserForHook(t, app, "bob")

	memos, _ := bootstrapMemoAndRelations(t, app)
	memo := mustInsertMemo(t, app, memos, "pinging @bob for the patch", "PUBLIC", alice.Id, "")

	row := notifRow(t, app, bobRec.Id, "MEMO_MENTION")
	require.NotNil(t, row, "bob should have a MEMO_MENTION row")
	payload := decodeNotificationPayload(t, row)
	assert.Equal(t, "memos/"+memo.Id, payload["memo"])
}

// TestNotification_MEMO_MENTION_SkipsAuthor: alice mentioning
// herself in her own memo is not a thing she would otherwise miss.
func TestNotification_MEMO_MENTION_SkipsAuthor(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")

	memos, _ := bootstrapMemoAndRelations(t, app)
	mustInsertMemo(t, app, memos, "talking to @alice in the mirror", "PUBLIC", alice.Id, "")

	assert.Nil(t, notifRow(t, app, alice.Id, "MEMO_MENTION"))
}

// TestNotification_MEMO_MENTION_DedupesAcrossUpdates confirms a
// re-save of a memo with the same mentions does not pile up
// duplicate inbox rows.
func TestNotification_MEMO_MENTION_DedupesAcrossUpdates(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")
	bobRec := newUserForHook(t, app, "bob")

	memos, _ := bootstrapMemoAndRelations(t, app)
	rec := mustInsertMemo(t, app, memos, "first @bob ping", "PUBLIC", alice.Id, "")
	// Touch a non-content field — the after-update hook
	// short-circuits when content is unchanged, so the test stays
	// honest about the "dedupe across the SAME mentions" half of
	// the design.
	rec.Set("tags", []string{"later"})
	require.NoError(t, app.Save(rec))

	rows, err := app.FindRecordsByFilter("notifications",
		"user={:uid} && type='MEMO_MENTION'", "", 0, 0,
		map[string]any{"uid": bobRec.Id},
	)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "an update with unchanged content must not re-notify")
}

// TestNotification_SpaceInvitation_ArchiveOnAccept: the inbox
// row for a SPACE_INVITATION is flipped to read=true when the
// invitee accepts (status INVITED -> ACTIVE).
func TestNotification_SpaceInvitation_ArchiveOnAccept(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")
	bob := newUserForHook(t, app, "bob")

	spaces, sm := bootstrapSpacesAndMembers(t, app)
	space := mustInsertSpace(t, app, spaces, "Labs", alice.Id)
	mustInsertSpaceInvite(t, app, sm, space.Id, bob.Id, "USER")

	row := notifRow(t, app, bob.Id, "SPACE_INVITATION")
	require.NotNil(t, row, "the invite should have produced a SPACE_INVITATION row")
	require.False(t, row.GetBool("read"), "the new row is unread")

	mustAcceptInvite(t, app, sm, space.Id, bob.Id)

	after, err := app.FindRecordById("notifications", row.Id)
	require.NoError(t, err)
	assert.True(t, after.GetBool("read"), "the row must be archived on accept")
}

// TestNotification_SpaceInvitation_DeleteOnDecline: declining a
// pending invite (status=INVITED) deletes the matching inbox row.
func TestNotification_SpaceInvitation_DeleteOnDecline(t *testing.T) {
	app := newHookedAppForHooks(t)
	alice := newUserForHook(t, app, "alice")
	bob := newUserForHook(t, app, "bob")

	spaces, sm := bootstrapSpacesAndMembers(t, app)
	space := mustInsertSpace(t, app, spaces, "Labs", alice.Id)
	mustInsertSpaceInvite(t, app, sm, space.Id, bob.Id, "USER")

	row := notifRow(t, app, bob.Id, "SPACE_INVITATION")
	require.NotNil(t, row)

	mustDeclineInvite(t, app, sm, space.Id, bob.Id)

	_, err := app.FindRecordById("notifications", row.Id)
	assert.Error(t, err, "the row must be deleted on decline")
}
