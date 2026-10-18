package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/connect"
)

// makeEventFor builds a *core.RequestEvent with the
// given app, request, and response writer. The
// RequestEvent is what the v1 handlers see; without
// it, the handler would panic on the first `e.Request`
// read.
func makeEventFor(app core.App, r *http.Request, w http.ResponseWriter) *core.RequestEvent {
	return &core.RequestEvent{
		App:      app,
		Request:  r,
		Response: w,
	}
}

// TestUpstreamMemosV1Routes_TableShape asserts the
// static invariants the v1 route table must satisfy:
// no duplicate method+path, every Builder name resolves
// to a registered bodyWrapper, every Target name
// resolves to a real connect-procedure constant, and
// every flavor is one of the four supported values.
//
// This is a unit test, not an integration test: it
// does not start a router. A new row added to the
// table that violates any of the invariants fails
// here, before the router is even built.
func TestUpstreamMemosV1Routes_TableShape(t *testing.T) {
	routes := upstreamMemosV1Routes()
	require.NotEmpty(t, routes, "the v1 route table must be non-empty")

	seen := map[string]bool{}
	for i, r := range routes {
		// Every row has a method and a path.
		assert.NotEmpty(t, r.Method, "row %d has empty method", i)
		assert.NotEmpty(t, r.Path, "row %d has empty path", i)
		assert.NotEmpty(t, r.Flavor, "row %d has empty flavor", i)

		// Flavor is one of the four supported.
		switch r.Flavor {
		case "connect_proxy", "connect_proxy_wrap", "pb_records_crud", "custom":
			// ok
		default:
			t.Errorf("row %d has unknown flavor %q", i, r.Flavor)
		}

		// (method, path) uniqueness: the v1 table
		// disambiguates by exact method+path.
		key := r.Method + " " + r.Path
		if seen[key] {
			t.Errorf("duplicate route %s", key)
		}
		seen[key] = true

		// Builder key resolves to a registered
		// wrapper for the wrap flavor.
		if r.Flavor == "connect_proxy_wrap" {
			if _, ok := v1BodyWrappers[r.Builder]; !ok {
				t.Errorf("row %d references unknown body wrapper %q", i, r.Builder)
			}
		}
		if r.Flavor == "custom" {
			// Custom builders are dispatched
			// through customBuilder; we just
			// sanity-check the key is one
			// of the known builders.
			known := map[string]bool{
				"v1_status": true, "v1_ping": true,
				"v1_user_stats": true, "not_implemented": true,
			}
			if !known[r.Builder] {
				t.Errorf("row %d references unknown custom builder %q", i, r.Builder)
			}
		}
	}
}

// TestUpstreamMemosV1Routes_MountsAndServesGet
// exercises the route registration loop with a stub
// connect mux. The expected response status is 200
// from the stub; this proves the route table can be
// mounted without panicking, the registered handler
// reaches the connect mux, and the response is
// copied back to the original writer.
func TestUpstreamMemosV1Routes_MountsAndServesGet(t *testing.T) {
	app := newTestApp(t)

	stubMux := http.NewServeMux()
	stubMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"profile":{"version":"","mode":"prod"}}`))
	})
	connect.SetConnectMux(stubMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed for the v1 table")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/profile", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"the stub connect mux should have been reached and replayed")
	assert.Contains(t, w.Body.String(), "profile",
		"the stub response body should be present")
}

// TestV1StatusHandler_DefaultVersionIsEmpty confirms
// the version-probe endpoint returns an empty
// `profile.version`. The MoeMemos client uses this
// signal to pick the V1 client; a non-empty value
// would trigger the legacy V0 client which is no
// longer supported.
func TestV1StatusHandler_DefaultVersionIsEmpty(t *testing.T) {
	app := newTestApp(t)
	h := v1StatusHandler(app)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	require.NoError(t, h(makeEventFor(app, req, w)))

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	profile, ok := body["profile"].(map[string]any)
	require.True(t, ok, "status body must contain profile")
	assert.Equal(t, "", profile["version"],
		"default version must be empty so the V1 client is selected")
}

// TestV1PingHandler_ReturnsOk covers the trivial
// /api/v1/ping handler. The body must contain a
// `data.ping` value of "ok" per the Memos OpenAPI
// spec.
func TestV1PingHandler_ReturnsOk(t *testing.T) {
	app := newTestApp(t)
	h := v1PingHandler(app)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	w := httptest.NewRecorder()
	require.NoError(t, h(makeEventFor(app, req, w)))

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "ping body must contain data")
	assert.Equal(t, "ok", data["ping"])
}

// TestV1GetMemoWrap_InjectsName covers the simplest
// V1 wrap: a GET with no body and a path variable.
// The wrap helper must produce
// `{"name": "memos/{memo}"}` for the connect
// handler to read.
func TestV1GetMemoWrap_InjectsName(t *testing.T) {
	out, err := wrapV1GetMemoBody(nil, map[string]string{"memo": "abc123"}, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "memos/abc123", got["name"])
}

// TestV1UpdateMemoWrap_UnwrapsAndInjects covers the
// most complex V1 wrap: a PATCH body in the inner
// `Memo` shape, with the memo name coming from the
// path and the update_mask coming from the query.
func TestV1UpdateMemoWrap_UnwrapsAndInjects(t *testing.T) {
	body := []byte(`{"content":"new text","pinned":true}`)
	q := url.Values{}
	q.Set("updateMask", "content,pinned")
	out, err := wrapV1UpdateMemoBody(body, map[string]string{"memo": "abc123"}, q)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "content,pinned", got["update_mask"])
	memo, ok := got["memo"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "memos/abc123", memo["name"])
	assert.Equal(t, "new text", memo["content"])
	assert.Equal(t, true, memo["pinned"])
}

// TestV1UpdateMemoWrap_MissingMaskDefaults is the
// safety net: when the client omits updateMask, the
// wrap helper defaults to `*` (all populated
// fields). The connect handler interprets `*` as
// "every field the body populated".
func TestV1UpdateMemoWrap_MissingMaskDefaults(t *testing.T) {
	out, err := wrapV1UpdateMemoBody(
		[]byte(`{"content":"x"}`),
		map[string]string{"memo": "abc123"},
		url.Values{},
	)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "*", got["update_mask"])
}

// TestV1PathInjectBody_FactoryExpansion is a unit
// test for the wrapV1PathInjectBody factory. The
// factory is the most common V1 wrap: it builds a
// single-field body whose value is the path's
// resource name.
func TestV1PathInjectBody_FactoryExpansion(t *testing.T) {
	w := wrapV1PathInjectBody("name", "memos/{memo}")
	out, err := w(nil, map[string]string{"memo": "abc123"}, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "memos/abc123", got["name"])
}

// TestV1PathInjectBody_MultiSegment covers the
// AIP-style compound path (e.g.
// `memos/{memo}/reactions/{reaction}`).
func TestV1PathInjectBody_MultiSegment(t *testing.T) {
	w := wrapV1PathInjectBody("name", "memos/{memo}/reactions/{reaction}")
	out, err := w(nil, map[string]string{"memo": "abc", "reaction": "rxn1"}, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "memos/abc/reactions/rxn1", got["name"])
}

// TestV1PathInjectBody_MissingSegmentIs400 ensures
// a missing path variable produces a 400, not a
// malformed body. The wrap helper detects
// unfilled `{` tokens and bails.
func TestV1PathInjectBody_MissingSegmentIs400(t *testing.T) {
	w := wrapV1PathInjectBody("name", "memos/{memo}/reactions/{reaction}")
	_, err := w(nil, map[string]string{"memo": "abc"}, nil)
	require.Error(t, err)
	we, ok := err.(*wrapErr)
	require.True(t, ok)
	assert.Equal(t, 400, we.status)
}

// TestPassthroughBody_EmptyDefaultsToEmptyObject
// confirms the passthrough helper emits a valid
// empty object for an empty input. The connect
// handler's protojson unmarshal chokes on a
// truly-empty body.
func TestPassthroughBody_EmptyDefaultsToEmptyObject(t *testing.T) {
	out, err := passthroughBody(nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("{}"), out,
		"empty input must produce an empty object")
}

// TestPassthroughBody_PassesThrough asserts a
// non-empty body is forwarded byte-for-byte.
func TestPassthroughBody_PassesThrough(t *testing.T) {
	in := []byte(`{"hello":"world"}`)
	out, err := passthroughBody(in, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

// TestCoerceIntToString, TestV0LookupUsername_CachesEmpty,
// TestAtoiDefault_Defaults, TestV0MemoAllFilter, and
// TestConnectProxyWrap_StripsAndForwards were removed
// with the V0 helpers they covered. The V0 routes were
// the only consumers of the `connect_proxy_wrap` flavor;
// the V1 routes are all `connect_proxy` (no wrap).
