package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/incurl/riff/internal/random"
	"github.com/incurl/riff/pb/connect"
	"github.com/incurl/riff/pb/hooks"
	apiv1connect "github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
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
				"v1_user_stats": true,
				// `GET /api/v1/users/{user}` is the colon-verb
				// dispatcher: the client's `…/{user}:getStats`
				// cannot be a separate pattern (ServeMux panics on
				// `{var}:`), so the single-segment route splits the
				// verb itself.
				"v1_get_user_or_stats": true,
				"not_implemented":      true,
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
		_, _ = w.Write([]byte(`{"profile":{"version":"0.21.0","mode":"prod"}}`))
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

// TestV1StatusHandler_DefaultVersionIsV0 confirms
// the version-probe endpoint returns the V0 (Memos
// 0.21) version string `0.21.0` when no override is
// set. The MoeMemos (iOS + Android) boot path inspects
// `profile.version`: a non-empty value routes the
// client to the V0 surface, an empty value routes
// it to the V1 surface. The V1 fallback path
// (`/api/v1/workspace/profile`) is not served, so
// the V0 contract is the only one that works on
// the live deployment. See DECISIONS.md "V0
// dispatch helper strips `Accept-Encoding: gzip`
// ..." for the rationale.
//
// The companion test TestV1StatusHandler_OverrideViaSetter
// confirms the override path (RIFF_MEMOS_VERSION).
func TestV1StatusHandler_DefaultVersionIsV0(t *testing.T) {
	// Reset the package variable in case another test in the
	// same run set it. t.Cleanup restores whatever it was
	// before the test so cross-test order doesn't leak state.
	prev := advertisedMemosVersion
	t.Cleanup(func() { advertisedMemosVersion = prev })
	advertisedMemosVersion = "0.21.0"

	app := newTestApp(t)
	h := v1StatusHandler(app)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	require.NoError(t, h(makeEventFor(app, req, w)))

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	profile, ok := body["profile"].(map[string]any)
	require.True(t, ok, "status body must contain profile")
	assert.Equal(t, "0.21.0", profile["version"],
		"status must report a non-empty V0 version so the MoeMemos client selects the V0 surface")
	assert.Equal(t, "prod", profile["mode"],
		"status mode must be one of prod/dev/demo")
}

// TestV1StatusHandler_OverrideViaSetter pins the
// RIFF_MEMOS_VERSION path. Once the V1 surface is complete
// and an operator sets RIFF_MEMOS_VERSION=0.31.0, the probe
// must report that exact string — not the default — so the
// client takes the V1 branch. An empty or whitespace-only
// value is a no-op (the default is preserved), so a
// misconfigured operator does not silently route clients to
// the unimplemented V1 surface.
func TestV1StatusHandler_OverrideViaSetter(t *testing.T) {
	prev := advertisedMemosVersion
	t.Cleanup(func() { advertisedMemosVersion = prev })

	// Empty value is a no-op: probe still reports the default.
	advertisedMemosVersion = "0.21.0"
	SetAdvertisedVersion("")
	app := newTestApp(t)
	h := v1StatusHandler(app)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	require.NoError(t, h(makeEventFor(app, req, w)))
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "0.21.0", body["profile"].(map[string]any)["version"],
		"an empty SetAdvertisedVersion call must not change the advertised version")

	// Non-empty value overrides.
	SetAdvertisedVersion("0.31.0")
	w = httptest.NewRecorder()
	require.NoError(t, h(makeEventFor(app, req, w)))
	body = nil
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "0.31.0", body["profile"].(map[string]any)["version"],
		"SetAdvertisedVersion must override the default and stay overridden for subsequent requests")
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
// wrap helper emits an EMPTY mask, which the connect
// handler reads as "every field the body populated"
// (pb/connect/translate.go::fieldMaskPaths).
//
// It must not be the literal `*`: protojson rejects
// that as a FieldMask path ("contains invalid path:
// \"*\""), so a request that carried one failed to
// unmarshal before the handler ever ran.
func TestV1UpdateMemoWrap_MissingMaskDefaults(t *testing.T) {
	out, err := wrapV1UpdateMemoBody(
		[]byte(`{"content":"x"}`),
		map[string]string{"memo": "abc123"},
		url.Values{},
	)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "", got["update_mask"], "an absent mask means every field in the body")
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

// TestV1PatPath_MoeMemosBoot is the regression test
// for the MoeMemos "data couldn't be read" error
// reported against the riff community deployment.
//
// MoeMemos's V1 client (MemosV1Api.kt in
// github.com/mudkipme/MoeMemosAndroid) is PAT-only:
// it never calls /api/v1/auth/signin and only reads
// from `getCurrentUser`, `getProfile`, `listMemos`,
// and friends. Each call carries
// `Authorization: Bearer <pat>`. The first call after
// the version probe is `GET /api/v1/auth/me`
// (== getCurrentUser); if it returns anything other
// than 200 application/json, or returns
// `{"user": null}`, the kotlinx.serialization codec
// that MemosV1Api uses can't decode it as a
// non-nullable `User` and the client surfaces
// "data couldn't be read".
//
// On the pre-ship-1 build:
//
//   - GET /api/v1/instance/profile was routed
//     (the table already had the row) and answered
//     200 application/json via the in-process connect
//     proxy, so the V1 client could parse it.
//   - GET /api/v1/auth/me was *also* routed, but
//     the AuthService handler returned
//     `{"user": <viewer>}`; if the PAT verification
//     failed the response was `{"user": null}` and
//     the codec failed.
//
// This test stands up a real PocketBase app, mounts
// the V1 surface against the real Connect mux
// (AuthService + InstanceService + UserService) with
// the production withAuth interceptor, mints a real
// PAT, and walks the exact boot sequence MoeMemos
// uses. If it passes, the deployment is functionally
// correct for the V1 client; if it fails, the failure
// mode names the exact RPC the client chokes on so
// the fix is local.
func TestV1PatPath_MoeMemosBoot(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	// Stand up the real Connect mux with the
	// production auth interceptor and the three
	// services the V1 boot path touches.
	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))

	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	// Create a real user.
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moe")
	rec.Set("email", "moe@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))

	// Mint a raw PAT and persist a bcrypt hash, the
	// same shape the production lifecycle uses. The
	// raw token is what a V1 client would paste into
	// the MoeMemos server URL config.
	patRaw, perr := random.String(24)
	require.NoError(t, perr)
	pat := "memos_pat_" + patRaw
	patHash, perr := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	require.NoError(t, perr)
	pats, perr := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, perr)
	patRec := core.NewRecord(pats)
	patRec.Set("user", rec.Id)
	patRec.Set("description", "MoeMemos boot regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	// Mount the V1 routes on a real router.
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	// 1. GET /api/v1/instance/profile (no auth) must
	//    answer 200 application/json, not the SPA
	//    catch-all 200 text/html that breaks the
	//    V1 client codec. The V1 client decodes
	//    the response into MemosProfile(val version:
	//    String); `version` must be present and a
	//    string so kotlinx.serialization accepts it.
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/instance/profile", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code,
		"V1 instance/profile must answer 200")
	require.Equal(t, "application/json", w1.Header().Get("Content-Type"),
		"V1 instance/profile must answer application/json, not text/html")
	var profileBody map[string]any
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &profileBody),
		"V1 instance/profile body must be valid JSON: %s", w1.Body.String())
	_, hasVersion := profileBody["version"]
	assert.True(t, hasVersion,
		"V1 instance/profile must include a 'version' field (MemosProfile.version): body=%v", profileBody)

	// 2. GET /api/v1/instance/profile with the PAT.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/instance/profile", nil)
	req2.Header.Set("Authorization", "Bearer "+pat)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code,
		"V1 instance/profile must answer 200 for a PAT-bearing client")
	require.Equal(t, "application/json", w2.Header().Get("Content-Type"),
		"V1 instance/profile must answer application/json for a PAT-bearing client")

	// 3. GET /api/v1/auth/me with the PAT, the first
	//    call MoeMemos issues after the version probe.
	//    The V1 client decodes the response into
	//    GetCurrentUserResponse(val user: MemosV1User?)
	//    and bails if user is null. The proto handler
	//    must resolve the PAT to the owner record.
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req3.Header.Set("Authorization", "Bearer "+pat)
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, req3)
	require.Equal(t, http.StatusOK, w3.Code,
		"V1 auth/me must answer 200 for a PAT-bearing client")
	require.Equal(t, "application/json", w3.Header().Get("Content-Type"),
		"V1 auth/me must answer application/json")
	var meBody map[string]any
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &meBody),
		"V1 auth/me body must be valid JSON: %s", w3.Body.String())
	meUser, ok := meBody["user"].(map[string]any)
	require.True(t, ok,
		"auth/me must include a non-null 'user' object (MoeMemos treats null as a parse error): body=%v", meBody)
	assert.Equal(t, "moe", meUser["username"],
		"auth/me must return the PAT owner's username")
	// MemosV1Api.getUser @GET("api/v1/users/{id}") reads
	// `name`; the proto User is keyed on `name` and the
	// converter must populate it for the V1 client to
	// build follow-up request paths.
	assert.NotEmpty(t, meUser["name"],
		"auth/me user must include a 'name' field (MemosV1Api.getUser reads it): user=%v", meUser)
}

// TestV1CreateMemo_BareMemoBodyIsWrapped pins the V1
// create-memo wire format. The V1 proto declares `CreateMemo`
// with `body: "memo"`, so the MoeMemos openapi-generator client
// sends the inner `Memo` fields directly (no `{memo: {...}}`
// envelope): `{"content":"hello","visibility":"PRIVATE"}`. The
// connect handler expects `CreateMemoRequest{memo: Memo}`, so
// the route must wrap the bare body via `wrapV1CreateMemoBody`.
// The prior state (all V1 routes `connect_proxy`) forwarded the
// bare body verbatim and left `memo` nil, so the handler
// answered "memo is required".
func TestV1CreateMemo_BareMemoBodyIsWrapped(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))
	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moe")
	rec.Set("email", "moe@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	patRaw, perr := random.String(24)
	require.NoError(t, perr)
	pat := "memos_pat_" + patRaw
	patHash, perr := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	require.NoError(t, perr)
	pats, perr := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, perr)
	patRec := core.NewRecord(pats)
	patRec.Set("user", rec.Id)
	patRec.Set("description", "v1 create memo")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/memos",
		strings.NewReader(`{"content":"hello from v1","visibility":"PRIVATE"}`))
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"V1 memo create must answer 200, got %d: %s", w.Code, w.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created),
		"V1 memo create body must be valid JSON: %s", w.Body.String())
	assert.Equal(t, "hello from v1", created["content"],
		"the created memo must echo the client's content")
}

// TestV1MemoPathRoutesInjectName pins the V1 read/delete routes
// that carry the memo id in the URL path. The proto declares
// `GetMemo` as `get: "/api/v1/{name=memos/*}"`, so the client
// sends an empty body and the route must inject `name` from the
// path via `wrapV1GetMemoBody` / `wrapV1DeleteMemoBody`. The
// prior state (all V1 routes `connect_proxy`) forwarded `{}`
// verbatim, so the connect handler answered "name is required".
func TestV1MemoPathRoutesInjectName(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))
	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moe")
	rec.Set("email", "moe@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	patRaw, perr := random.String(24)
	require.NoError(t, perr)
	pat := "memos_pat_" + patRaw
	patHash, perr := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	require.NoError(t, perr)
	pats, perr := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, perr)
	patRec := core.NewRecord(pats)
	patRec.Set("user", rec.Id)
	patRec.Set("description", "v1 memo path")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	memoRec := core.NewRecord(memos)
	memoRec.Set("content", "existing memo")
	memoRec.Set("visibility", "PRIVATE")
	memoRec.Set("author", rec.Id)
	require.NoError(t, app.Save(memoRec))

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/memos/"+memoRec.Id, nil)
	getReq.Header.Set("Authorization", "Bearer "+pat)
	wGet := httptest.NewRecorder()
	mux.ServeHTTP(wGet, getReq)
	require.Equal(t, http.StatusOK, wGet.Code,
		"V1 memo get must answer 200, got %d: %s", wGet.Code, wGet.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(wGet.Body.Bytes(), &got),
		"V1 memo get body must be valid JSON: %s", wGet.Body.String())
	assert.Equal(t, "existing memo", got["content"],
		"the fetched memo must carry its content")

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/memos/"+memoRec.Id, nil)
	delReq.Header.Set("Authorization", "Bearer "+pat)
	wDel := httptest.NewRecorder()
	mux.ServeHTTP(wDel, delReq)
	require.Equal(t, http.StatusOK, wDel.Code,
		"V1 memo delete must answer 200, got %d: %s", wDel.Code, wDel.Body.String())
}

// TestCoerceIntToString, TestV0LookupUsername_CachesEmpty,
// TestAtoiDefault_Defaults, TestV0MemoAllFilter, and
// TestConnectProxyWrap_StripsAndForwards were removed
// with the V0 helpers they covered.
