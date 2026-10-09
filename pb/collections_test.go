// Tests for the Phase 2 collections: spaces, space_members, memos,
// memo_relations, reactions, patches, samples.
//
// The collection definitions live under pb/migrations. The import
// below is for its side effect (each migration registers itself via
// init()).
package pb_test

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/incurl/riff/pb/migrations"
)

// TestCollections_AllPresent asserts every collection Phase 2 adds
// has been created by the migration runner.
func TestCollections_AllPresent(t *testing.T) {
	app := newPhase2App(t)

	want := []string{
		"spaces",
		"space_members",
		"memos",
		"memo_relations",
		"reactions",
		"patches",
		"samples",
	}
	for _, name := range want {
		col, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "collection %q should exist after migrations", name)
		assert.Equal(t, core.CollectionTypeBase, col.Type, "%q should be a base collection", name)
	}
}

// TestSpaces_Schema asserts the spaces collection has the expected
// fields and access rules.
func TestSpaces_Schema(t *testing.T) {
	app := newPhase2App(t)

	spaces, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)

	for _, name := range []string{"uid", "title", "description", "icon", "owner"} {
		assert.NotNil(t, spaces.Fields.GetByName(name), "spaces.%s must exist", name)
	}

	uidIdx := spaces.GetIndex("idx_spaces_uid")
	require.NotEmpty(t, uidIdx, "idx_spaces_uid must exist")
}

// TestMemos_VisibilityRule exercises the four-way visibility
// audience defined in the Memos 0.31 design doc: PRIVATE /
// PROTECTED / PUBLIC / SPACE.
//
// Scenario:
//
//  1. Create two users, alice (the author) and bob (a non-member).
//  2. Create a space owned by alice.
//  3. Create four memos under alice: one of each visibility, with
//     the SPACE memo assigned to alice's space.
//  4. For each memo, ask CanAccessRecord from bob's RequestInfo.
//     Expect: PRIVATE hidden, PROTECTED visible, PUBLIC visible,
//     SPACE hidden (bob is not a space member).
//  5. Add bob to the space as an ACTIVE USER member.
//  6. Re-check: now bob can see the SPACE memo.
func TestMemos_VisibilityRule(t *testing.T) {
	app := newPhase2App(t)

	cols := mustCols(t, app, "users", "memos", "spaces", "space_members", "reactions")
	users, memos, spaces, sm := cols[0], cols[1], cols[2], cols[3]

	alice := newAuthRecord(t, app, users, "alice", "alice@example.com", "passw0rd!")
	bob := newAuthRecord(t, app, users, "bob", "bob@example.com", "passw0rd!")

	space := core.NewRecord(spaces)
	space.Set("uid", "po-33-labs")
	space.Set("title", "PO-33 Labs")
	space.Set("description", "Community patches and sample packs.")
	space.Set("icon", "🔬")
	space.Set("owner", alice.Id)
	require.NoError(t, app.Save(space))

	mk := func(visibility string, setSpace bool) *core.Record {
		r := core.NewRecord(memos)
		r.Set("author", alice.Id)
		r.Set("content", "test memo: "+visibility)
		r.Set("visibility", visibility)
		r.Set("type", "note")
		if setSpace {
			r.Set("space", space.Id)
		}
		require.NoError(t, app.Save(r))
		return r
	}
	private := mk("PRIVATE", false)
	protected := mk("PROTECTED", false)
	public := mk("PUBLIC", false)
	spaceMemo := mk("SPACE", true)

	bobInfo := &core.RequestInfo{Method: http.MethodGet, Auth: bob}

	ok, err := app.CanAccessRecord(private, bobInfo, memos.ViewRule)
	require.NoError(t, err)
	assert.False(t, ok, "bob must not see alice's PRIVATE memo")

	ok, err = app.CanAccessRecord(protected, bobInfo, memos.ViewRule)
	require.NoError(t, err)
	assert.True(t, ok, "bob must see alice's PROTECTED memo")

	ok, err = app.CanAccessRecord(public, bobInfo, memos.ViewRule)
	require.NoError(t, err)
	assert.True(t, ok, "bob must see alice's PUBLIC memo")

	ok, err = app.CanAccessRecord(spaceMemo, bobInfo, memos.ViewRule)
	require.NoError(t, err)
	assert.False(t, ok, "bob must not see alice's SPACE memo before being a member")

	membership := core.NewRecord(sm)
	membership.Set("space", space.Id)
	membership.Set("user", bob.Id)
	membership.Set("status", "ACTIVE")
	membership.Set("role", "USER")
	require.NoError(t, app.Save(membership))

	ok, err = app.CanAccessRecord(spaceMemo, bobInfo, memos.ViewRule)
	require.NoError(t, err)
	assert.True(t, ok, "bob must see alice's SPACE memo after joining as ACTIVE USER")
}

// TestSpaceMembers_UniqueConstraint asserts the (space, user) unique
// index prevents duplicate relationship rows.
func TestSpaceMembers_UniqueConstraint(t *testing.T) {
	app := newPhase2App(t)

	cols := mustCols(t, app, "users", "spaces", "space_members", "memos")
	users, spaces, sm := cols[0], cols[1], cols[2]

	alice := newAuthRecord(t, app, users, "alice2", "alice2@example.com", "passw0rd!")
	space := core.NewRecord(spaces)
	space.Set("uid", "space-x")
	space.Set("title", "Space X")
	space.Set("owner", alice.Id)
	require.NoError(t, app.Save(space))

	m1 := core.NewRecord(sm)
	m1.Set("space", space.Id)
	m1.Set("user", alice.Id)
	m1.Set("status", "ACTIVE")
	m1.Set("role", "ADMIN")
	require.NoError(t, app.Save(m1))

	m2 := core.NewRecord(sm)
	m2.Set("space", space.Id)
	m2.Set("user", alice.Id)
	m2.Set("status", "ACTIVE")
	m2.Set("role", "USER")
	err := app.Save(m2)
	require.Error(t, err, "duplicate (space, user) must be rejected by the unique index")
}

// TestCollections_SupportNewestFirstSorting asserts every riff
// collection can be ordered by `created` descending.
//
// The Connect list handlers page through records with a `-created`
// sort. PocketBase rejects an unknown sort field with
// `invalid sort field "created"`, and the handlers surface that as an
// internal error, so a collection missing the timestamp field takes
// its whole list endpoint down. Only `memos` and `notifications` were
// created with timestamps; the rest were patched in later.
func TestCollections_SupportNewestFirstSorting(t *testing.T) {
	app := newPhase2App(t)

	names := []string{
		"spaces",
		"space_members",
		"memos",
		"memo_relations",
		"reactions",
		"patches",
		"samples",
		"notifications",
	}
	for _, name := range names {
		col, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "collection %q must exist", name)
		assert.NotNilf(t, col.Fields.GetByName("created"), "%s must expose a `created` field", name)

		rows, err := app.FindRecordsByFilter(name, "", "-created", 10, 0)
		require.NoErrorf(t, err, "sorting %s by -created must be supported", name)
		assert.Emptyf(t, rows, "%s should be empty on a fresh install", name)
	}
}

// TestCollections_HaveUpdatedTimestamp asserts the collections the
// timestamp migration touches also expose `updated`, which the
// converters read for update_time.
func TestCollections_HaveUpdatedTimestamp(t *testing.T) {
	app := newPhase2App(t)

	for _, name := range []string{"spaces", "space_members", "memo_relations", "reactions", "patches", "samples"} {
		col, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "collection %q must exist", name)
		assert.NotNilf(t, col.Fields.GetByName("updated"), "%s must expose an `updated` field", name)
	}
}
