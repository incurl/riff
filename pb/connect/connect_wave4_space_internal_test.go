package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestCreateSpace_RunsCreateHook asserts the space create path runs
// OnRecordCreateRequest: the owner is stamped from the caller and the creator
// becomes the first ACTIVE ADMIN. Before the bridge existed the save failed
// validation (owner is Required), so creating a space over Connect never
// worked — and even a successful insert would have left the creator unable to
// read their own space.
func TestCreateSpace_RunsCreateHook(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewSpaceServer(app)
	ctx := withViewer(context.Background(), alice)

	space, err := srv.CreateSpace(ctx, &v1.CreateSpaceRequest{
		Space: &v1.Space{Title: "PO-33 Labs"},
	})
	require.NoError(t, err)
	assert.Equal(t, "spaces/po-33-labs", space.GetName())
	assert.Equal(t, "PO-33 Labs", space.GetTitle())
	assert.Equal(t, v1.SpaceMember_ADMIN, space.GetCurrentUserRole(), "the creator is the first ADMIN")
	assert.Equal(t, int32(1), space.GetMemberCount())

	// The owner column is Required, so it has to have been stamped.
	rec, err := findSpaceByUID(app, "po-33-labs")
	require.NoError(t, err)
	assert.Equal(t, alice.Id, rec.GetString("owner"))

	// Because the creator is already a member, the space is listable.
	list, err := srv.ListSpaces(ctx, &v1.ListSpacesRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetSpaces(), 1)
	assert.Equal(t, "spaces/po-33-labs", list.GetSpaces()[0].GetName())
}

// TestDeleteSpace_RunsCascadeHook asserts deleting a space removes its assigned
// memos, which the OnRecordDeleteRequest cascade owns.
func TestDeleteSpace_RunsCascadeHook(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	spaces := NewSpaceServer(app)
	memos := NewMemoServer(app)
	ctx := withViewer(context.Background(), alice)

	spaceID := mustCreateSpace(t, app, spaces, ctx, "Doomed")
	spaceName := "spaces/" + lookupSpaceUID(app, spaceID)

	created, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "inside the space", Visibility: v1.Visibility_SPACE, Space: &spaceName},
	})
	require.NoError(t, err)

	_, err = spaces.DeleteSpace(ctx, &v1.DeleteSpaceRequest{Name: spaceName})
	require.NoError(t, err)

	_, err = app.FindRecordById("memos", memoIDFromName(created.GetName()))
	assert.Error(t, err, "the space cascade must remove its memos")
}

// TestGetSpaceMember asserts one membership can be read by a fellow member and
// not by an outsider.
func TestGetSpaceMember(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	carol := newUser(t, app, "carol")
	srv := NewSpaceServer(app)
	ctx := withViewer(context.Background(), alice)

	mustCreateSpace(t, app, srv, ctx, "Labs")
	name := "spaces/labs/members/alice"

	member, err := srv.GetSpaceMember(ctx, &v1.GetSpaceMemberRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, "users/alice", member.GetUser())
	assert.Equal(t, v1.SpaceMember_ADMIN, member.GetRole())

	_, err = srv.GetSpaceMember(withViewer(context.Background(), carol), &v1.GetSpaceMemberRequest{Name: name})
	require.Error(t, err, "a non-member must not see the space's members")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestUpdateSpaceMember covers promotion, demotion and the last-ADMIN
// invariant, all enforced by the space_members update hook.
func TestUpdateSpaceMember(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewSpaceServer(app)
	aliceCtx := withViewer(context.Background(), alice)
	bobCtx := withViewer(context.Background(), bob)

	spaceID := mustCreateSpace(t, app, srv, aliceCtx, "Labs")
	spaceName := "spaces/" + lookupSpaceUID(app, spaceID)

	// Invite bob and have him accept, so he holds a USER membership.
	_, err := srv.CreateSpaceInvitation(aliceCtx, &v1.CreateSpaceInvitationRequest{
		Parent:          spaceName,
		SpaceInvitation: &v1.SpaceInvitation{Invitee: "users/bob", Role: v1.SpaceMember_USER},
	})
	require.NoError(t, err)
	_, err = srv.AcceptSpaceInvitation(bobCtx, &v1.AcceptSpaceInvitationRequest{
		Name: spaceName + "/invitations/bob",
	})
	require.NoError(t, err)

	// Demoting the only ADMIN must be refused.
	_, err = srv.UpdateSpaceMember(aliceCtx, &v1.UpdateSpaceMemberRequest{
		SpaceMember: &v1.SpaceMember{Name: spaceName + "/members/alice", Role: v1.SpaceMember_USER},
	})
	require.Error(t, err, "the last active ADMIN must not be demoted")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// A plain member cannot promote themselves.
	_, err = srv.UpdateSpaceMember(bobCtx, &v1.UpdateSpaceMemberRequest{
		SpaceMember: &v1.SpaceMember{Name: spaceName + "/members/bob", Role: v1.SpaceMember_ADMIN},
	})
	require.Error(t, err, "only an active ADMIN may change a role")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// The ADMIN promotes bob.
	promoted, err := srv.UpdateSpaceMember(aliceCtx, &v1.UpdateSpaceMemberRequest{
		SpaceMember: &v1.SpaceMember{Name: spaceName + "/members/bob", Role: v1.SpaceMember_ADMIN},
	})
	require.NoError(t, err)
	assert.Equal(t, v1.SpaceMember_ADMIN, promoted.GetRole())

	// With a second ADMIN in place, alice can step down.
	demoted, err := srv.UpdateSpaceMember(bobCtx, &v1.UpdateSpaceMemberRequest{
		SpaceMember: &v1.SpaceMember{Name: spaceName + "/members/alice", Role: v1.SpaceMember_USER},
	})
	require.NoError(t, err)
	assert.Equal(t, v1.SpaceMember_USER, demoted.GetRole())
	assert.Equal(t, "USER", membershipRow(t, app, spaceID, alice.Id).GetString("role"))
}

// TestUpdateSpaceMember_RejectsUnsupportedMaskField asserts the update mask is
// checked rather than ignored.
func TestUpdateSpaceMember_RejectsUnsupportedMaskField(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewSpaceServer(app)
	ctx := withViewer(context.Background(), alice)

	mustCreateSpace(t, app, srv, ctx, "Labs")

	_, err := srv.UpdateSpaceMember(ctx, &v1.UpdateSpaceMemberRequest{
		SpaceMember: &v1.SpaceMember{Name: "spaces/labs/members/alice", Role: v1.SpaceMember_USER},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"user"}},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestDeleteSpaceMember_AdminRemovesOther asserts an ADMIN can remove another
// member.
func TestDeleteSpaceMember_AdminRemovesOther(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewSpaceServer(app)
	aliceCtx := withViewer(context.Background(), alice)
	bobCtx := withViewer(context.Background(), bob)

	spaceID := mustCreateSpace(t, app, srv, aliceCtx, "Labs")
	spaceName := "spaces/" + lookupSpaceUID(app, spaceID)

	_, err := srv.CreateSpaceInvitation(aliceCtx, &v1.CreateSpaceInvitationRequest{
		Parent:          spaceName,
		SpaceInvitation: &v1.SpaceInvitation{Invitee: "users/bob", Role: v1.SpaceMember_USER},
	})
	require.NoError(t, err)
	_, err = srv.AcceptSpaceInvitation(bobCtx, &v1.AcceptSpaceInvitationRequest{
		Name: spaceName + "/invitations/bob",
	})
	require.NoError(t, err)

	_, err = srv.DeleteSpaceMember(aliceCtx, &v1.DeleteSpaceMemberRequest{
		Name: spaceName + "/members/bob",
	})
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid}", "", 1, 0,
		map[string]any{"sid": spaceID, "uid": bob.Id})
	require.NoError(t, err)
	assert.Empty(t, rows, "bob's membership must be gone")
}

// TestSpaceInvitation_GetAndRevoke covers the invitation read and revoke paths.
func TestSpaceInvitation_GetAndRevoke(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	carol := newUser(t, app, "carol")
	srv := NewSpaceServer(app)
	aliceCtx := withViewer(context.Background(), alice)

	mustCreateSpace(t, app, srv, aliceCtx, "Labs")
	invitationName := "spaces/labs/invitations/bob"

	_, err := srv.CreateSpaceInvitation(aliceCtx, &v1.CreateSpaceInvitationRequest{
		Parent:          "spaces/labs",
		SpaceInvitation: &v1.SpaceInvitation{Invitee: "users/bob", Role: v1.SpaceMember_ADMIN},
	})
	require.NoError(t, err)

	// The invitee can read their own invitation, complete with the space.
	got, err := srv.GetSpaceInvitation(withViewer(context.Background(), bob), &v1.GetSpaceInvitationRequest{Name: invitationName})
	require.NoError(t, err)
	assert.Equal(t, "users/bob", got.GetInvitee())
	assert.Equal(t, v1.SpaceMember_ADMIN, got.GetRole())
	require.NotNil(t, got.Space, "the invitee needs the space to render the prompt")
	assert.Equal(t, "Labs", got.Space.GetTitle())

	// A third party cannot.
	_, err = srv.GetSpaceInvitation(withViewer(context.Background(), carol), &v1.GetSpaceInvitationRequest{Name: invitationName})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// The admin revokes it.
	_, err = srv.DeleteSpaceInvitation(aliceCtx, &v1.DeleteSpaceInvitationRequest{Name: invitationName})
	require.NoError(t, err)

	_, err = srv.GetSpaceInvitation(withViewer(context.Background(), bob), &v1.GetSpaceInvitationRequest{Name: invitationName})
	assert.Error(t, err, "the invitation must be gone after revocation")

}

// TestDeleteSpaceMember_GuardsAndSelfLeave asserts the last active ADMIN cannot
// be removed, while a member may leave of their own accord.
func TestDeleteSpaceMember_GuardsAndSelfLeave(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewSpaceServer(app)
	aliceCtx := withViewer(context.Background(), alice)
	bobCtx := withViewer(context.Background(), bob)

	spaceID := mustCreateSpace(t, app, srv, aliceCtx, "Labs")
	spaceName := "spaces/" + lookupSpaceUID(app, spaceID)

	// The sole ADMIN cannot remove themselves.
	_, err := srv.DeleteSpaceMember(aliceCtx, &v1.DeleteSpaceMemberRequest{
		Name: spaceName + "/members/alice",
	})
	require.Error(t, err, "the last active ADMIN must not be removable")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Bob joins, then leaves on his own.
	_, err = srv.CreateSpaceInvitation(aliceCtx, &v1.CreateSpaceInvitationRequest{
		Parent:          spaceName,
		SpaceInvitation: &v1.SpaceInvitation{Invitee: "users/bob", Role: v1.SpaceMember_USER},
	})
	require.NoError(t, err)
	_, err = srv.AcceptSpaceInvitation(bobCtx, &v1.AcceptSpaceInvitationRequest{
		Name: spaceName + "/invitations/bob",
	})
	require.NoError(t, err)

	_, err = srv.DeleteSpaceMember(bobCtx, &v1.DeleteSpaceMemberRequest{
		Name: spaceName + "/members/bob",
	})
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid}", "", 1, 0,
		map[string]any{"sid": spaceID, "uid": bob.Id})
	require.NoError(t, err)
	assert.Empty(t, rows, "bob's membership must be gone")
}
