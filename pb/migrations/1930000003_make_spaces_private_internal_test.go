package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMakeSpacesPrivate_Rules pins that the `spaces` collection is
// scoped to its ACTIVE members.
func TestMakeSpacesPrivate_Rules(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	col, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)

	const memberRule = `space_members_via_space.user.id ?= @request.auth.id` +
		` && space_members_via_space.status = "ACTIVE"`

	require.NotNil(t, col.ListRule)
	assert.Equal(t, memberRule, *col.ListRule, "the list rule must be member-scoped")
	require.NotNil(t, col.ViewRule)
	assert.Equal(t, memberRule, *col.ViewRule, "the view rule must be member-scoped")

	require.NotNil(t, col.UpdateRule)
	assert.Contains(t, *col.UpdateRule, `space_members_via_space.role = "ADMIN"`,
		"an ACTIVE ADMIN must be able to update the space")
	require.NotNil(t, col.DeleteRule)
	assert.Contains(t, *col.DeleteRule, `space_members_via_space.role = "ADMIN"`,
		"an ACTIVE ADMIN must be able to delete the space")
}

// TestMakeSpacesPrivate_MembershipGatesAccess is the load-bearing
// test: the rule must actually admit a member and refuse a
// non-member. A rule that parses but does not resolve would leave
// every space invisible — or every space visible.
func TestMakeSpacesPrivate_MembershipGatesAccess(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	member := core.NewRecord(users)
	member.Set("username", "spacemember")
	member.Set("email", "spacemember@example.test")
	member.Set("password", "memberpassw0rd")
	require.NoError(t, app.Save(member))

	outsider := core.NewRecord(users)
	outsider.Set("username", "spaceoutsider")
	outsider.Set("email", "spaceoutsider@example.test")
	outsider.Set("password", "outsiderpassw0rd")
	require.NoError(t, app.Save(outsider))

	spaces, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)

	space := core.NewRecord(spaces)
	space.Set("uid", "private-space")
	space.Set("title", "Private Space")
	space.Set("owner", member.Id)
	require.NoError(t, app.Save(space))

	// The member row.
	members, err := app.FindCollectionByNameOrId("space_members")
	require.NoError(t, err)
	row := core.NewRecord(members)
	row.Set("space", space.Id)
	row.Set("user", member.Id)
	row.Set("status", "ACTIVE")
	row.Set("role", "USER")
	require.NoError(t, app.Save(row))

	info := &core.RequestInfo{Method: "GET"}

	// The member may see it.
	ok, err := app.CanAccessRecord(space, &core.RequestInfo{Method: "GET", Auth: member}, spaces.ListRule)
	require.NoError(t, err)
	assert.True(t, ok, "an ACTIVE member must be able to read the space")

	// The outsider may not.
	ok, err = app.CanAccessRecord(space, &core.RequestInfo{Method: "GET", Auth: outsider}, spaces.ListRule)
	require.NoError(t, err)
	assert.False(t, ok, "a non-member must NOT be able to read the space")

	// An anonymous caller may not.
	ok, err = app.CanAccessRecord(space, info, spaces.ListRule)
	require.NoError(t, err)
	assert.False(t, ok, "an anonymous caller must NOT be able to read the space")
}

// TestMakeSpacesPrivate_InvitedIsNotActive pins that a pending
// invitee cannot see the space until they accept.
func TestMakeSpacesPrivate_InvitedIsNotActive(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	invitee := core.NewRecord(users)
	invitee.Set("username", "invitee")
	invitee.Set("email", "invitee@example.test")
	invitee.Set("password", "inviteepassw0rd")
	require.NoError(t, app.Save(invitee))

	spaces, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)
	space := core.NewRecord(spaces)
	space.Set("uid", "invited-space")
	space.Set("title", "Invited Space")
	space.Set("owner", invitee.Id)
	require.NoError(t, app.Save(space))

	members, err := app.FindCollectionByNameOrId("space_members")
	require.NoError(t, err)
	row := core.NewRecord(members)
	row.Set("space", space.Id)
	row.Set("user", invitee.Id)
	row.Set("status", "INVITED")
	row.Set("role", "USER")
	require.NoError(t, app.Save(row))

	ok, err := app.CanAccessRecord(space, &core.RequestInfo{Method: "GET", Auth: invitee}, spaces.ListRule)
	require.NoError(t, err)
	assert.False(t, ok, "a pending invitee must NOT see the space before accepting")
}
