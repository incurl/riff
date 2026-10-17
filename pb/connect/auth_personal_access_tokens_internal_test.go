// Tests for the PAT branch of the auth interceptor.
//
// These tests live in package connect so they can build the same
// in-process connect.Server the production code does, mint a real
// PAT through UserServer.CreatePersonalAccessToken, and observe the
// Bearer round-trip end to end. The whole exercise is the
// regression test for the bug where memos_pat_* tokens were
// silently dropped by withAuth and every memo RPC returned an
// empty / anonymous response.
package connect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// newAuthedConnectServer wires AuthService onto a connect.Server
// guarded by the production withAuth interceptor, then mounts it on
// a fresh httptest.Server. The AuthService is sufficient to prove
// the interceptor populated the context, because
// AuthService.GetCurrentUser is the thinnest "echo CurrentUser(ctx)
// back" RPC in the package.
func newAuthedConnectServer(t *testing.T, app core.App) *httptest.Server {
	t.Helper()

	srv := connect.NewServer(withAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, NewAuthServer(app, nil))

	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return httpSrv
}

// callGetCurrentUser sends a Connect GetCurrentUser with the given
// Authorization header and returns the response. Empty header
// exercises the anonymous path; "Bearer <tok>" exercises the
// interceptor's PAT (or JWT) branch.
func callGetCurrentUser(t *testing.T, httpSrv *httptest.Server, authHeader string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		httpSrv.URL+"/memos.api.v1.AuthService/GetCurrentUser",
		strings.NewReader("{}"),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestWithAuth_PersonalAccessToken_ResolvesUser is the headline
// regression: a memos_pat_* Bearer must round-trip through
// withAuth and reach the handler with CurrentUser(ctx) set to the
// PAT's owning user. Before the patch, withAuth only tried
// FindAuthRecordByToken, which cannot parse a non-JWS string, so
// the request looked anonymous and GetCurrentUser returned 401.
func TestWithAuth_PersonalAccessToken_ResolvesUser(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := users.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	raw := created.GetToken()
	require.True(t, strings.HasPrefix(raw, personalAccessTokenPrefix),
		"raw token must use the riff prefix, got %q", raw)

	httpSrv := newAuthedConnectServer(t, app)

	// Anonymous request still fails (regression cover: the PAT
	// branch must not have weakened the unauthenticated guard).
	anon := callGetCurrentUser(t, httpSrv, "")
	defer anon.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode,
		"no token must still be rejected when the PAT branch is in play")

	// The PAT round-trip: this is the case that broke before.
	pat := callGetCurrentUser(t, httpSrv, "Bearer "+raw)
	defer pat.Body.Close()
	assert.Equal(t, http.StatusOK, pat.StatusCode,
		"a valid memos_pat_* token must authenticate")

	// last_used_at must have been stamped as a side-effect. The
	// contract is that the resolver does not have to do this, but
	// the production implementation chooses to, so the test pins
	// it shut.
	rows, err := app.FindRecordsByFilter(
		personalAccessTokenCollection, "user={:u}", "", 0, 0,
		map[string]any{"u": alice.Id},
	)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].GetDateTime("last_used_at").IsZero(),
		"a successful PAT verify must stamp last_used_at")
}

// TestWithAuth_PersonalAccessToken_WrongSecretFails covers a PAT
// that decodes to the right shape but whose secret bytes do not
// match any stored hash. The interceptor must treat it as
// anonymous.
func TestWithAuth_PersonalAccessToken_WrongSecretFails(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := users.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	raw := created.GetToken()

	// Flip one character inside the entropy suffix (skip the
	// prefix so the shape check still passes).
	flipped := raw[:len(personalAccessTokenPrefix)] +
		flipChar(raw[len(personalAccessTokenPrefix):])

	httpSrv := newAuthedConnectServer(t, app)
	resp := callGetCurrentUser(t, httpSrv, "Bearer "+flipped)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a PAT with the right shape but a wrong secret must be rejected")
}

// TestWithAuth_PersonalAccessToken_NonExistentTokenFails covers a
// well-formed but never-minted PAT. The collection's *Rule fields
// are nil, so the collection REST API cannot authorise this case;
// the interceptor must reach the same verdict.
func TestWithAuth_PersonalAccessToken_NonExistentTokenFails(t *testing.T) {
	app := newHookedApp(t)
	httpSrv := newAuthedConnectServer(t, app)

	// 32 base64url chars of zeros after the prefix; the bytes are
	// valid but no row will ever carry the matching bcrypt digest.
	raw := personalAccessTokenPrefix + strings.Repeat("A", 32)

	resp := callGetCurrentUser(t, httpSrv, "Bearer "+raw)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a never-minted PAT must be rejected")
}

// TestWithAuth_PersonalAccessToken_RevokedTokenFails covers the
// Delete path. After the row is gone, the same token must be
// rejected. This pins the "delete is irrevocable" contract that
// the SPA relies on.
func TestWithAuth_PersonalAccessToken_RevokedTokenFails(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := users.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	raw := created.GetToken()
	name := created.GetPersonalAccessToken().GetName()

	_, err = users.DeletePersonalAccessToken(ctx, &v1.DeletePersonalAccessTokenRequest{Name: name})
	require.NoError(t, err)

	httpSrv := newAuthedConnectServer(t, app)
	resp := callGetCurrentUser(t, httpSrv, "Bearer "+raw)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a revoked PAT must be rejected")
}

// TestWithAuth_PersonalAccessToken_ExpiredTokenFails is the
// "expires_at is honoured" guard. We forge a row directly because
// the RPCs do not let the test code back-date the column without
// racing the autodate.
func TestWithAuth_PersonalAccessToken_ExpiredTokenFails(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := users.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	raw := created.GetToken()

	// Back-date the row's expires_at to a moment in the past. The
	// minted bcrypt hash stays valid; only the timestamp moves.
	rows, err := app.FindRecordsByFilter(
		personalAccessTokenCollection, "user={:u}", "", 0, 0,
		map[string]any{"u": alice.Id},
	)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows[0].Set("expires_at", time.Now().Add(-time.Hour))
	require.NoError(t, app.Save(rows[0]))

	httpSrv := newAuthedConnectServer(t, app)
	resp := callGetCurrentUser(t, httpSrv, "Bearer "+raw)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"an expired PAT must be rejected")
}

// TestResolvePersonalAccessToken_Unit exercises the resolver
// directly (not through HTTP) so the negative cases are readable
// at a glance. The HTTP-level tests above already cover the happy
// path through the full interceptor.
func TestResolvePersonalAccessToken_Unit(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")

	// Unknown prefix: resolver returns (nil, nil) and does not
	// touch the DB.
	rec, err := resolvePersonalAccessToken(app, "not-a-pat")
	require.NoError(t, err)
	assert.Nil(t, rec)

	// Well-formed prefix, no row: resolver returns (nil, nil).
	rec, err = resolvePersonalAccessToken(app, personalAccessTokenPrefix+strings.Repeat("A", 32))
	require.NoError(t, err)
	assert.Nil(t, rec)

	// Mint and resolve.
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)
	created, err := users.CreatePersonalAccessToken(ctx, &v1.CreatePersonalAccessTokenRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	raw := created.GetToken()

	rec, err = resolvePersonalAccessToken(app, raw)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, alice.Id, rec.Id,
		"the resolver must hand back the PAT's owning user record")
}

// flipChar is a tiny helper used by the wrong-secret test: it
// substitutes the first non-underscore character of the suffix with
// the next ASCII byte, which is enough to make the bcrypt compare
// fail without changing the token's prefix or shape.
func flipChar(s string) string {
	if s == "" {
		return "A"
	}
	first := s[0]
	if first == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}
