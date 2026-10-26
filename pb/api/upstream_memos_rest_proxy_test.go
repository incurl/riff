package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/connect"
)

// TestConnectProxy_ForwardsRequestBody is a
// focused test for the connect_proxy flavor: a
// stub connect mux is installed, a request to a
// registered upstream-memos REST route is
// dispatched, and the stub verifies it received
// the same body, the same Authorization header,
// and the procedure path in `r.URL.Path`.
//
// Note: this test is NOT t.Parallel() because
// the connect mux is a process-global variable.
// Running these tests in parallel would race on
// the mux; each test installs and tears down its
// own stub, but two concurrent installs can leave
// the global pointing at the wrong stub. The
// t.Cleanup hooks run synchronously at the end
// of each test, which keeps the global state
// consistent within a sequential test run.
func TestConnectProxy_ForwardsRequestBody(t *testing.T) {
	app := newTestApp(t)

	var capturedBody []byte
	var capturedPath string
	var capturedAuth string
	stubMux := http.NewServeMux()
	stubMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		capturedBody = buf.Bytes()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"stub":true}`))
	})
	connect.SetConnectMux(stubMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosRoutes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"username":"alice","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/memos/api/auth/signin", body)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedPath, "AuthService",
		"the procedure path should include the service name")
	assert.Contains(t, capturedPath, "SignIn",
		"the procedure path should include the method name")
	assert.Equal(t, "Bearer test-token", capturedAuth,
		"the Authorization header must be forwarded verbatim")
	assert.Equal(t, []byte(`{"username":"alice","password":"hunter2"}`), capturedBody,
		"the request body must be forwarded verbatim")
}

// TestConnectProxy_CopiesResponseHeaders verifies
// the bridge copies Content-Type and other
// response headers from the upstream handler to
// the original writer.
func TestConnectProxy_CopiesResponseHeaders(t *testing.T) {
	app := newTestApp(t)

	stubMux := http.NewServeMux()
	stubMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Stub-Header", "stub-value")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"stub":true}`))
	})
	connect.SetConnectMux(stubMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosRoutes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/instance/profile", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTeapot, w.Code,
		"the upstream status code must be replayed")
	assert.Equal(t, "stub-value", w.Header().Get("X-Stub-Header"),
		"upstream response headers must be copied")
}

// TestConnectProxy_NoMuxReturns503 covers the
// fail-soft path: when the connect package's
// mux is nil (i.e. Register has not run), the
// handler returns 503 with a Memos-shape
// envelope rather than panicking.
func TestConnectProxy_NoMuxReturns503(t *testing.T) {
	app := newTestApp(t)
	connect.SetConnectMux(nil)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosRoutes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/instance/profile", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.EqualValues(t, 13, body["code"])
}

// TestConnectProxy_PassesQueryString verifies the
// bridge forwards the URL query string verbatim
// (e.g. ListMemos's `?filter=...`).
func TestConnectProxy_PassesQueryString(t *testing.T) {
	app := newTestApp(t)

	var capturedQuery string
	stubMux := http.NewServeMux()
	stubMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	connect.SetConnectMux(stubMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosRoutes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet,
		"/memos/api/memos?filter=visibility%3D%27PUBLIC%27&page=2&page_size=10", nil)
	req = req.WithContext(contextWithViewer(req.Context(), seedAuditUser(t, app, "proxyquery")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "filter=visibility%3D%27PUBLIC%27&page=2&page_size=10", capturedQuery,
		"the query string must be forwarded verbatim")
}

// TestConnectProxy_UnknownFlavorReturns500 covers
// the fail-loud path: when a row in the route
// table references an unknown Flavor, the
// factory emits a 500 with a Memos-shape envelope.
// This test does not exercise the factory
// directly; it asserts the route table has no
// typos, which is the precondition for the
// factory to never need to fail-loud.
func TestConnectProxy_UnknownFlavorReturns500(t *testing.T) {
	// Build a fake row referencing a flavor
	// the factory does not know.
	handler := upstreamMemosHandlerFactory(nil, upstreamRouteEntry{
		Method: http.MethodGet,
		Path:   "/memos/api/_test/unknown_flavor",
		Flavor: "definitely_not_a_real_flavor",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/memos/api/_test/unknown_flavor", nil)
	// Build a RequestEvent manually because
	// the factory returns a closure that takes
	// one, not the request/recorder.
	e := &core.RequestEvent{App: nil, Response: rec, Request: req}
	_ = handler(e)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "definitely_not_a_real_flavor"),
		"the error body must mention the unknown flavor")
}
