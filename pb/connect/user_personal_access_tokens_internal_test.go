package connect

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestPersonalAccessToken_CreateThenList pins the headline behaviour:
// the raw token is returned on Create and never again, the bcrypt
// hash on the row matches the raw token, and a follow-up List
// surfaces the row's metadata.
func TestPersonalAccessToken_CreateThenList(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	out, err := srv.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent:        "users/alice",
		Description:   "ci runner",
		ExpiresInDays: 30,
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out.GetToken(), personalAccessTokenPrefix),
		"raw token must use the riff prefix, got %q", out.GetToken())
	require.NotEmpty(t, out.GetPersonalAccessToken().GetName())
	require.NotNil(t, out.GetPersonalAccessToken().GetCreatedAt())
	require.NotNil(t, out.GetPersonalAccessToken().GetExpiresAt(),
		"expires_in_days=30 must set the proto ExpiresAt field")

	rows, err := app.FindRecordsByFilter("personal_access_tokens", "user={:u}", "", 0, 0, map[string]any{"u": alice.Id})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, bcrypt.CompareHashAndPassword(
		[]byte(rows[0].GetString("token_hash")), []byte(out.GetToken())),
		"the persisted hash must verify against the raw token")

	list, err := srv.ListPersonalAccessTokens(ctx, &v1.ListPersonalAccessTokensRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, list.GetPersonalAccessTokens(), 1)
	listed := list.GetPersonalAccessTokens()[0]
	assert.Equal(t, out.GetPersonalAccessToken().GetName(), listed.GetName())
	assert.Equal(t, "ci runner", listed.GetDescription())
	assert.NotNil(t, listed.GetCreatedAt())
	// The list response must never carry the raw token. The proto has
	// no field that can hold it (the raw value only lives on the wire
	// for CreatePersonalAccessTokenResponse), so a successful
	// ListPersonalAccessToken + visible Description without the
	// token-shape is the strongest invariant we can assert here.
}

// TestPersonalAccessToken_DeleteByOwner ensures only the owner can
// revoke their own token, and the row is gone afterwards.
func TestPersonalAccessToken_DeleteByOwner(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)
	ctxA := withViewer(context.Background(), alice)
	ctxB := withViewer(context.Background(), bob)

	out, err := srv.CreatePersonalAccessToken(ctxA, &v1.CreatePersonalAccessTokenRequest{Parent: "users/alice"})
	require.NoError(t, err)
	name := out.GetPersonalAccessToken().GetName()

	// Bob is not allowed to revoke alice's token.
	_, err = srv.DeletePersonalAccessToken(ctxB, &v1.DeletePersonalAccessTokenRequest{Name: name})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Alice can.
	_, err = srv.DeletePersonalAccessToken(ctxA, &v1.DeletePersonalAccessTokenRequest{Name: name})
	require.NoError(t, err)

	// The row is gone.
	rows, err := app.FindRecordsByFilter("personal_access_tokens", "user={:u}", "", 0, 0, map[string]any{"u": alice.Id})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestPersonalAccessToken_NeverExpires pins the "0 = never" semantics:
// a request with expires_in_days=0 leaves the proto ExpiresAt unset
// and the row's `expires_at` column empty.
func TestPersonalAccessToken_NeverExpires(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	out, err := srv.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent:        "users/alice",
		ExpiresInDays: 0,
	})
	require.NoError(t, err)
	assert.Nil(t, out.GetPersonalAccessToken().GetExpiresAt(),
		"expires_in_days=0 must leave the proto ExpiresAt unset")

	rows, err := app.FindRecordsByFilter("personal_access_tokens", "user={:u}", "", 0, 0, map[string]any{"u": alice.Id})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].GetDateTime("expires_at").IsZero(),
		"the row's expires_at column must stay empty for non-expiring tokens")
}

// TestPersonalAccessToken_NegativeDaysIsRejected is the request-validation
// guard: a negative expires_in_days is an InvalidArgument, not a
// silently-clamped zero.
func TestPersonalAccessToken_NegativeDaysIsRejected(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	_, err := srv.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent:        "users/alice",
		ExpiresInDays: -1,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestPersonalAccessToken_RequiresAuth is the anonymous-caller guard.
func TestPersonalAccessToken_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewUserServer(app)

	_, err := srv.ListPersonalAccessTokens(context.Background(), &v1.ListPersonalAccessTokensRequest{Parent: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = srv.CreatePersonalAccessToken(context.Background(), &v1.CreatePersonalAccessTokenRequest{Parent: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = srv.DeletePersonalAccessToken(context.Background(), &v1.DeletePersonalAccessTokenRequest{Name: "users/alice/personalAccessTokens/x"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
