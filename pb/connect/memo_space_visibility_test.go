package connect

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPrivateSpace creates a space owned by `owner` with `member` as an
// ACTIVE member, and returns the space.
func seedPrivateSpace(t *testing.T, app core.App, uid string, owner, member *core.Record) *core.Record {
	t.Helper()
	spaces, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)
	space := core.NewRecord(spaces)
	space.Set("uid", uid)
	space.Set("title", "Private Space")
	space.Set("owner", owner.Id)
	require.NoError(t, app.Save(space))

	members, err := app.FindCollectionByNameOrId("space_members")
	require.NoError(t, err)
	row := core.NewRecord(members)
	row.Set("space", space.Id)
	row.Set("user", member.Id)
	row.Set("status", "ACTIVE")
	row.Set("role", "USER")
	require.NoError(t, app.Save(row))

	return space
}

// seedMemoInSpace creates a memo with the given visibility inside the
// space.
func seedMemoInSpace(t *testing.T, app core.App, space *core.Record, author *core.Record, visibility, content string) *core.Record {
	t.Helper()
	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	memo := core.NewRecord(memos)
	memo.Set("author", author.Id)
	memo.Set("content", content)
	memo.Set("visibility", visibility)
	memo.Set("space", space.Id)
	require.NoError(t, app.Save(memo))
	return memo
}

// TestMemoSpaceRelation_StrippedForNonMember pins that a non-member
// does not learn which space a memo lives in.
//
// The space is private; the memo's own visibility is independent —
// a PUBLIC memo in a private space is still readable — but its space
// RELATION must not be disclosed, because the space uid is a URL
// slug that would let anyone confirm the space exists.
func TestMemoSpaceRelation_StrippedForNonMember(t *testing.T) {
	app := newHookedApp(t)
	owner := newUser(t, app, "spaceowner")
	member := newUser(t, app, "spacemember")
	outsider := newUser(t, app, "spaceoutsider")

	space := seedPrivateSpace(t, app, "stripped-space", owner, member)
	publicMemo := seedMemoInSpace(t, app, space, owner, "PUBLIC", "a public memo in a private space")

	// The member sees the relation.
	ctx := withActiveSpaceIDs(context.Background(), app, member.Id)
	out := recordToMemo(ctx, app, publicMemo)
	require.NotNil(t, out)
	require.NotNil(t, out.Space, "a member must see the space relation")
	assert.Equal(t, "spaces/stripped-space", *out.Space)

	// The outsider does not.
	ctx = withActiveSpaceIDs(context.Background(), app, outsider.Id)
	out = recordToMemo(ctx, app, publicMemo)
	require.NotNil(t, out)
	assert.Nil(t, out.Space, "a non-member must NOT see the space relation")
	// ...but the memo itself is still readable: its visibility is
	// independent of the space's privacy.
	assert.Equal(t, "a public memo in a private space", out.Content)
}

// TestMemoSpaceRelation_StrippedForAnonymous pins the anonymous case.
func TestMemoSpaceRelation_StrippedForAnonymous(t *testing.T) {
	app := newHookedApp(t)
	owner := newUser(t, app, "anonowner")
	member := newUser(t, app, "anonmember")

	space := seedPrivateSpace(t, app, "anon-space", owner, member)
	publicMemo := seedMemoInSpace(t, app, space, owner, "PUBLIC", "visible to all")

	ctx := withActiveSpaceIDs(context.Background(), app, "")
	out := recordToMemo(ctx, app, publicMemo)
	require.NotNil(t, out)
	assert.Nil(t, out.Space, "an anonymous viewer must NOT see the space relation")
}

// TestMemoSpaceRelation_SpaceVisibilityStillMemberOnly pins that the
// hybrid did not weaken the SPACE memo visibility: a memo with
// visibility=SPACE is still invisible to a non-member.
func TestMemoSpaceRelation_SpaceVisibilityStillMemberOnly(t *testing.T) {
	app := newHookedApp(t)
	owner := newUser(t, app, "spacevisowner")
	member := newUser(t, app, "spacevismember")
	outsider := newUser(t, app, "spacevisoutsider")

	space := seedPrivateSpace(t, app, "vis-space", owner, member)
	spaceMemo := seedMemoInSpace(t, app, space, owner, "SPACE", "members only")

	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)

	// The outsider must not pass the memo's own rule.
	ok, err := app.CanAccessRecord(spaceMemo, &core.RequestInfo{Method: "GET", Auth: outsider}, memos.ViewRule)
	require.NoError(t, err)
	assert.False(t, ok, "a SPACE-visibility memo must stay invisible to a non-member")

	// The member must.
	ok, err = app.CanAccessRecord(spaceMemo, &core.RequestInfo{Method: "GET", Auth: member}, memos.ViewRule)
	require.NoError(t, err)
	assert.True(t, ok, "a SPACE-visibility memo must stay visible to a member")
}

// TestActiveSpaceIDs_OneQueryPerRequest pins that the membership set
// is computed with ONE query, not one per memo. A list can carry up
// to maxPageSize (1000) rows, so a per-memo check would be an N+1.
func TestActiveSpaceIDs_OneQueryPerRequest(t *testing.T) {
	app := newHookedApp(t)
	owner := newUser(t, app, "oneqowner")
	member := newUser(t, app, "oneqmember")
	space := seedPrivateSpace(t, app, "oneq-space", owner, member)

	set := activeSpaceIDs(app, member.Id)
	assert.Contains(t, set, space.Id, "the member's space must be in the set")
	assert.Len(t, set, 1, "the set must contain exactly the member's spaces")

	// A non-member gets an empty set.
	outsider := newUser(t, app, "oneqoutsider")
	assert.Empty(t, activeSpaceIDs(app, outsider.Id),
		"a non-member must get an empty set, which strips every space relation")
}
