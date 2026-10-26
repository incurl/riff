package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/testutil"
	"github.com/incurl/riff/pb/connect"
	_ "github.com/incurl/riff/pb/migrations"
)

// makeTestRouter builds an http.Handler whose routes
// match the upstream-memos REST surface and whose
// event factory is wired to the given app. The
// connect-proxy stub is installed here so the
// connect_proxy flavor's handler does not 503;
// tests that want a real connect dispatch install
// their own stub via connect.SetConnectMux.
func makeTestRouter(t *testing.T, app core.App) http.Handler {
	t.Helper()

	stubMux := http.NewServeMux()
	stubMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"stub":true}`))
	})
	connect.SetConnectMux(stubMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosRoutes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")
	return mux
}

// eventFactoryFor returns a router.EventFactoryFunc
// that hands every *core.RequestEvent the same App
// and a non-nil Request. The Request is needed
// because the upstream-memos handlers read e.Request
// (path, method, body) and a nil Request would
// panic the moment a handler tries to read a
// header. The Response writer is also set so the
// handler chain can write to the recorder.
func eventFactoryFor(app core.App) router.EventFactoryFunc[*core.RequestEvent] {
	return func(w http.ResponseWriter, r *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		// Populate e.Auth from the context viewer, the role
		// PocketBase's global loadAuthToken middleware plays on the
		// real router. The rule engine reads e.Auth, so a test that
		// wants an authenticated caller has to set it the same way.
		viewer, _ := r.Context().Value(ctxKeyConnectViewer{}).(*core.Record)
		return &core.RequestEvent{
			App:      app,
			Response: w,
			Request:  r,
			Auth:     viewer,
		}, nil
	}
}

// TestUpstreamMemosRouteTable is a table-driven smoke
// test: for every row in upstreamMemosRoutes we
// dispatch a request through the test router and
// assert that the route is matched by checking the
// response is a Memos-shape envelope (a JSON body
// with a `code` field). A miss from the router
// returns a plain HTML 404 page (no JSON body),
// which is the signal we assert against.
//
// The test's purpose is to catch typos in the route
// table — a path that does not compile in PocketBase's
// router format would 404 from the framework, and
// the JSON-shape check below catches that.
//
// The SSE row is excluded because the handler is a
// long-lived EventSource stream that does not return
// on its own; the upstream-memos rest sse test
// covers it separately.
//
// Path parameters ({name}, {uid}, ...) are
// substituted with concrete test values so the
// router can match a real URL pattern.
func TestUpstreamMemosRouteTable(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	for _, entry := range upstreamMemosRoutes() {
		entry := entry
		if entry.Flavor == "sse" {
			continue
		}
		name := entry.Method + " " + entry.Path
		t.Run(name, func(t *testing.T) {
			concrete := concretePathForTest(entry.Path)
			req := httptest.NewRequest(entry.Method, concrete, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			// A 200 means the route matched and
			// the handler returned success. A
			// 4xx is fine if it's a Memos-shape
			// envelope (handler ran, returned
			// a deliberate error). A plain
			// "404 page not found" HTML is a
			// router miss.
			body := w.Body.String()
			if !strings.HasPrefix(body, "{") {
				t.Fatalf("route %s (concrete %s) was not matched by the router; body=%s",
					name, concrete, body)
			}
		})
	}
}

// concretePathForTest replaces every {name}, {parent},
// {uid}, {filename}, {identifier}, {member_name},
// {invitation_name}, {reaction_name}, {code} with a
// test value so the router can match the pattern. The
// values are deterministic so a regression that
// affects a specific path is reproducible.
func concretePathForTest(p string) string {
	repl := []struct{ old, new string }{
		{"{name}", "test-id"},
		{"{parent}", "test-parent"},
		{"{uid}", "test-uid"},
		{"{filename}", "test-file.txt"},
		{"{identifier}", "test-identifier"},
		{"{member_name}", "test-member"},
		{"{invitation_name}", "test-invitation"},
		{"{reaction_name}", "test-reaction"},
		{"{code}", "test-code"},
		{"{rest...}", "test-rest"},
	}
	out := p
	for _, r := range repl {
		out = strings.ReplaceAll(out, r.old, r.new)
	}
	return out
}

// TestUpstreamMemosRouteTableIncludesSSE is a
// single-row companion to TestUpstreamMemosRouteTable:
// it asserts the SSE route is registered by
// exercising the path and then cancelling the
// request context to release the handler. The
// 1-second deadline keeps the test fast even on
// a busy CI machine.
func TestUpstreamMemosRouteTableIncludesSSE(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/memos/api/sse", nil).WithContext(ctx)
	req = req.WithContext(contextWithViewer(req.Context(), seedAuditUser(t, app, "sseprobe")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// The SSE handler writes 200 OK and a
	// `: connected\n\n` frame before blocking.
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestUpstreamMemosRouteCount guards against silent
// truncation of the route table. 93 proto-annotated
// + 3 file + 1 SSE = 97.
func TestUpstreamMemosRouteCount(t *testing.T) {
	t.Parallel()
	routes := upstreamMemosRoutes()
	assert.Equal(t, 97, len(routes), "upstreamMemosRoutes must have 97 rows (93 proto + 3 file + 1 SSE)")
	for i, r := range routes {
		assert.NotEmpty(t, r.Method, "row %d has empty Method", i)
		assert.NotEmpty(t, r.Path, "row %d has empty Path", i)
	}
}

// TestUpstreamMemosPathCollisions ensures no two
// routes in the table share a (method, path) pair.
func TestUpstreamMemosPathCollisions(t *testing.T) {
	t.Parallel()
	routes := upstreamMemosRoutes()
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		key := r.Method + " " + r.Path
		require.False(t, seen[key], "duplicate route: %s", key)
		seen[key] = true
	}
}

// TestUpstreamMemosPathParametersAreConsistent
// asserts the path parameters declared in each
// route's Path use the {name} syntax that
// PocketBase's router supports.
func TestUpstreamMemosPathParametersAreConsistent(t *testing.T) {
	t.Parallel()
	for _, r := range upstreamMemosRoutes() {
		assert.NotContains(t, r.Path, "=*", "route %s uses the proto {name=foo/*} syntax; use {name}", r.Path)
	}
}

// TestUpstreamMemosFlavorsAreKnown asserts every
// row's Flavor is one of the strings the
// upstreamMemosHandlerFactory switch understands.
func TestUpstreamMemosFlavorsAreKnown(t *testing.T) {
	t.Parallel()
	known := map[string]bool{
		"connect_proxy": true,
		// connect_proxy_wrap injects the route's path variables
		// into the request body before dispatching, so a Connect
		// handler that reads `name`/`parent` from the body sees
		// the resource the Memos path addressed.
		"connect_proxy_wrap": true,
		"pb_records_crud":    true,
		"custom":             true,
		"file_server":        true,
		"sse":                true,
	}
	for _, r := range upstreamMemosRoutes() {
		assert.True(t, known[r.Flavor], "unknown flavor %q on %s %s", r.Flavor, r.Method, r.Path)
	}
}

// newTestApp boots a migrated PocketBase app in a
// per-test temp dir.
func newTestApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_api_upstream_memos_rest_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	testutil.DisableLogPersistence(app)
	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")
	return app
}
