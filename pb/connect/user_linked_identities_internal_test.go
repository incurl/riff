package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestLinkedIdentity_CreateListDelete walks the full lifecycle:
// the OAuth code exchange succeeds, a row is written, the list
// surfaces it, and delete removes it. The OAuth mocks are the
// same fixtures TestSignInWithSSO_NewUser uses; the linked
// identity RPC reuses the resolveIdpAndExchange helper, so
// this test pins the reuse without re-stating the OAuth flow.
func TestLinkedIdentity_CreateListDelete(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	baseURL, providerName, cleanup := ssoTestServer(t, "code-abc", "google-sub-1", "alice@example.com", "Alice")
	defer cleanup()
	installSsoIdp(t, app, baseURL, providerName, providerName, "Test IdP")

	// The empty initial state: no linked identities.
	list, err := srv.ListLinkedIdentities(ctx, &v1.ListLinkedIdentitiesRequest{Parent: "users/alice"})
	require.NoError(t, err)
	assert.Empty(t, list.GetLinkedIdentities())

	// Create.
	created, err := srv.CreateLinkedIdentity(ctx, &v1.CreateLinkedIdentityRequest{
		Parent:      "users/alice",
		IdpName:     "identity-providers/" + providerName,
		Code:        "code-abc",
		RedirectUri: "http://localhost/oauth/callback",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.GetName())
	assert.Equal(t, "identity-providers/"+providerName, created.GetIdpName())
	assert.Equal(t, "alice@example.com", created.GetExternUid())

	// List now contains the row.
	list, err = srv.ListLinkedIdentities(ctx, &v1.ListLinkedIdentitiesRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, list.GetLinkedIdentities(), 1)
	assert.Equal(t, created.GetName(), list.GetLinkedIdentities()[0].GetName())

	// Idempotency: a second create with the same IdP must not
	// fail. The handler returns the existing row.
	again, err := srv.CreateLinkedIdentity(ctx, &v1.CreateLinkedIdentityRequest{
		Parent:      "users/alice",
		IdpName:     "identity-providers/" + providerName,
		Code:        "code-abc",
		RedirectUri: "http://localhost/oauth/callback",
	})
	require.NoError(t, err)
	assert.Equal(t, created.GetName(), again.GetName())

	// Delete.
	_, err = srv.DeleteLinkedIdentity(ctx, &v1.DeleteLinkedIdentityRequest{Name: created.GetName()})
	require.NoError(t, err)

	list, err = srv.ListLinkedIdentities(ctx, &v1.ListLinkedIdentitiesRequest{Parent: "users/alice"})
	require.NoError(t, err)
	assert.Empty(t, list.GetLinkedIdentities())
}

// TestLinkedIdentity_NonOwnerCannotList guards the read scope:
// a non-admin caller cannot list another user's links.
func TestLinkedIdentity_NonOwnerCannotList(t *testing.T) {
	app := newHookedApp(t)
	_ = newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)
	ctxB := withViewer(context.Background(), bob)

	_, err := srv.ListLinkedIdentities(ctxB, &v1.ListLinkedIdentitiesRequest{Parent: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestLinkedIdentity_RequiresAuth is the anonymous-caller guard.
func TestLinkedIdentity_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewUserServer(app)

	_, err := srv.ListLinkedIdentities(context.Background(), &v1.ListLinkedIdentitiesRequest{Parent: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = srv.CreateLinkedIdentity(context.Background(), &v1.CreateLinkedIdentityRequest{Parent: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = srv.DeleteLinkedIdentity(context.Background(), &v1.DeleteLinkedIdentityRequest{Name: "users/alice/linkedIdentities/x"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestLinkedIdentity_DeleteMissing is the not-found path: a
// request for a non-existent id is a NotFound, not a 500.
func TestLinkedIdentity_DeleteMissing(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	_, err := srv.DeleteLinkedIdentity(ctx, &v1.DeleteLinkedIdentityRequest{
		Name: "users/alice/linkedIdentities/does-not-exist",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
