// Internal tests for the Connect handlers.
//
// These live in package connect rather than connect_test because the
// auth interceptor stashes the caller's record under an unexported
// context key; simulating an authenticated request means writing that
// key directly.
package connect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"

	"github.com/incurl/riff/internal/testutil"
	_ "github.com/incurl/riff/pb/migrations"
)

// newTestApp bootstraps a migrated PocketBase app in a per-test temp
// dir. Mirrors newPhase2App in the pb package, which is out of reach
// from an internal test package.
func newTestApp(t *testing.T) core.App {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_connect_internal_test",
	})
	t.Cleanup(func() {
		_ = app.ClearBootstrap()
	})

	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	testutil.DisableLogPersistence(app)
	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")
	return app
}

// withViewer returns a context whose CurrentUser is rec, i.e. the
// state the auth interceptor produces for a valid Bearer token.
func withViewer(ctx context.Context, rec *core.Record) context.Context {
	return context.WithValue(ctx, ctxKeyAuthRecord{}, rec)
}

// newUser creates a `users` auth record with a hashed password.
func newUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	rec := core.NewRecord(users)
	rec.Set("username", username)
	rec.Set("email", username+"@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	return rec
}

// TestSpaceService_ListSpaces_ActiveMemberSeesSpace guards the listing
// query, which sorts spaces by `created`. Before the timestamp
// migration added that field PocketBase rejected the sort with
// `invalid sort field "created"` and the handler returned an internal
// error, so this test pins the regression shut.
func TestSpaceService_ListSpaces_ActiveMemberSeesSpace(t *testing.T) {
	app := newTestApp(t)

	spaces, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)
	sm, err := app.FindCollectionByNameOrId("space_members")
	require.NoError(t, err)

	alice := newUser(t, app, "alice")

	space := core.NewRecord(spaces)
	space.Set("uid", "po-33-labs")
	space.Set("title", "PO-33 Labs")
	space.Set("owner", alice.Id)
	require.NoError(t, app.Save(space))

	membership := core.NewRecord(sm)
	membership.Set("space", space.Id)
	membership.Set("user", alice.Id)
	membership.Set("status", "ACTIVE")
	membership.Set("role", "ADMIN")
	require.NoError(t, app.Save(membership))

	srv := NewSpaceServer(app)
	resp, err := srv.ListSpaces(withViewer(context.Background(), alice), &v1.ListSpacesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetSpaces(), 1)
	assert.Equal(t, "spaces/po-33-labs", resp.GetSpaces()[0].GetName())
}

// TestSpaceService_ListSpaces_RequiresAuth asserts anonymous callers
// are rejected rather than silently receiving an empty list.
func TestSpaceService_ListSpaces_RequiresAuth(t *testing.T) {
	app := newTestApp(t)
	srv := NewSpaceServer(app)

	_, err := srv.ListSpaces(context.Background(), &v1.ListSpacesRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestWithAuth_ResolvesBearerToken exercises the auth interceptor over
// real HTTP, the shape the running server sees.
//
// The interceptor once called FindAuthRecordByToken with its
// arguments swapped (FindAuthRecordByToken(token, validTypes...)), so
// PocketBase tried to parse the literal string "users" as a JWT. Every
// request then looked anonymous and the authenticated services
// answered 401 even with a valid token. A handler-level test cannot
// catch that, because the failure lives in the HTTP layer.
func TestWithAuth_ResolvesBearerToken(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)

	srv := connect.NewServer(WithAuth(app))
	apiv1connect.RegisterSpaceServiceHandler(srv, NewSpaceServer(app))

	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	call := func(authHeader string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(
			http.MethodPost,
			httpSrv.URL+"/memos.api.v1.SpaceService/ListSpaces",
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

	anon := call("")
	defer anon.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode, "no token must be rejected")

	authed := call("Bearer " + token)
	defer authed.Body.Close()
	assert.Equal(t, http.StatusOK, authed.StatusCode, "a valid user token must be accepted")

	// A non-Bearer header must not authenticate.
	bare := call(token)
	defer bare.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, bare.StatusCode, "a header without the Bearer prefix must be ignored")
}
