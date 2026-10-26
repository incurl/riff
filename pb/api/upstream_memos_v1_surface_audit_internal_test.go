package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// newV1AuditApp boots a real PocketBase app, registers riff's
// hooks, mounts the V1 REST surface, and returns the mux plus a
// bearer-authenticated request helper.
func newV1AuditApp(t *testing.T, username string) (http.Handler, string, string) {
	t.Helper()

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
	rec.Set("username", username)
	rec.Set("email", username+"@example.invalid")
	// PocketBase hashes the `password` field itself on save, and
	// `AuthServer.verifyPassword` verifies against that same field
	// through `Record.ValidatePassword`, so setting the plain value
	// is all the harness has to do.
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
	patRec.Set("description", "v1 surface audit")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	return mux, pat, rec.Id
}

// v1Do issues one bearer-authenticated request against the mux.
func v1Do(t *testing.T, mux http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestV1SurfaceEveryClientOpAnswers is the completeness audit for
// the V1 (Memos 0.27-0.31) REST surface. The MoeMemos
// `MemosV1Service/openapi.yaml` declares 25 operations across 13
// paths; `TestV1PatPath_MoeMemosBoot` walks only three of them
// (instance/profile twice, auth/me), so a wrapper wired to the
// wrong Builder key — or a route that lost the ServeMux match to
// the SPA catch-all — stayed invisible until a real client hit it.
//
// This drives **every** declared client operation against a live
// PocketBase app with a real `memos_pat_*` PAT and asserts the
// answer is not one of the two shapes a client cannot recover
// from:
//
//   - 404 with an EMPTY body — the router found no matching
//     pattern (the SPA catch-all would have answered 200 + HTML,
//     so an empty 404 is PocketBase's "no such route");
//   - 501 Not Implemented — riff deliberately has no handler.
//
// A 4xx *with* a body is a handler's own NotFound/InvalidArgument
// answer, which is a correct result for a missing resource id and
// is deliberately allowed through. The test pins routing and
// handler presence, not the business policy.
func TestV1SurfaceEveryClientOpAnswers(t *testing.T) {
	mux, pat, _ := newV1AuditApp(t, "v1audit")

	do := func(method, target, body string) *httptest.ResponseRecorder {
		w := v1Do(t, mux, method, target, body)
		w.Header() // keep the recorder alive for the assertions below
		return w
	}
	doAuth := func(method, target, body string) *httptest.ResponseRecorder {
		// Re-issue with the PAT; v1Do leaves the header off so a
		// caller can also probe the unauthenticated shape.
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+pat)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Seed a memo so the sub-resource routes have something to
	// return. The V1 create body is the *inner* Memo — the proto
	// declares `body: "memo"`, so there is no `{"memo":{…}}`
	// envelope.
	wCreate := doAuth(http.MethodPost, "/api/v1/memos", `{"content":"v1 surface audit","visibility":"PRIVATE"}`)
	require.Equal(t, http.StatusOK, wCreate.Code,
		"POST /api/v1/memos must answer 200, got %d: %s", wCreate.Code, wCreate.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(wCreate.Body.Bytes(), &created),
		"create-memo body must be JSON: %s", wCreate.Body.String())
	memoName, _ := created["name"].(string)
	require.NotEmpty(t, memoName,
		"create-memo must return a resource name for the follow-up paths: %v", created)

	username := "v1audit"
	// The create response returns the full resource name
	// (`memos/<id>`); every follow-up path segment is the bare
	// id, which is what the client's `MemosV1Service.getId()`
	// extracts and what the route's `{memo}` wildcard matches.
	memoID := strings.TrimPrefix(memoName, "memos/")

	// `mustSucceed` marks the ops whose 2xx body is the *only*
	// acceptable answer on a freshly-migrated app with one user,
	// one memo and a valid PAT. Everything else in the table is
	// exercised for routing only and is allowed to 4xx (a missing
	// resource id, a deliberately-wrong password): asserting a 2xx
	// there would be asserting a business-policy answer, not the
	// wire-format contract this audit is about.
	type op struct {
		method, target, body, why string
		mustSucceed               bool
	}
	ops := []op{
		{http.MethodGet, "/api/v1/instance/profile", "", "version probe; unauthenticated", true},
		{http.MethodPost, "/api/v1/auth/signin", `{"passwordCredentials":{"username":"v1audit","password":"passw0rd!"}}`, "credential exchange", true},
		{http.MethodGet, "/api/v1/auth/me", "", "first call after sign-in", true},
		{http.MethodPost, "/api/v1/auth/refresh", `{"token":"` + pat + `"}`, "token refresh", false},
		{http.MethodGet, "/api/v1/memos?pageSize=10", "", "the feed", true},
		{http.MethodPost, "/api/v1/memos", `{"content":"second","visibility":"PRIVATE"}`, "create", true},
		{http.MethodGet, "/api/v1/memos/" + memoID, "", "single memo read", true},
		{http.MethodPatch, "/api/v1/memos/" + memoID + "?updateMask=content", `{"content":"patched"}`, "partial update", true},
		{http.MethodGet, "/api/v1/memos/" + memoID + "/comments", "", "comment list", true},
		{http.MethodPost, "/api/v1/memos/" + memoID + "/comments", `{"content":"a comment"}`, "comment create", true},
		{http.MethodGet, "/api/v1/memos/" + memoID + "/reactions", "", "reaction list", true},
		{http.MethodPost, "/api/v1/memos/" + memoID + "/reactions", `{"reaction":{"reactionType":"LIKE"}}`, "reaction upsert (nested: the client openapi makes reaction REQUIRED alongside name, and the proto reaction field is a nested message)", true},
		{http.MethodGet, "/api/v1/memos/" + memoID + "/relations", "", "relation list", true},
		{http.MethodPatch, "/api/v1/memos/" + memoID + "/relations", `{"relations":[]}`, "relation set", true},
		{http.MethodGet, "/api/v1/memos/" + memoID + "/attachments", "", "attachment list", true},
		{http.MethodPatch, "/api/v1/memos/" + memoID + "/attachments", `{"attachments":[]}`, "attachment set", true},
		{http.MethodGet, "/api/v1/attachments?pageSize=10", "", "global attachment list", true},
		{http.MethodPost, "/api/v1/attachments", `{"filename":"n.po33","type":"application/octet-stream"}`, "attachment create (inner Attachment body, re-wrapped by wrapV1CreateAttachmentBody)", true},
		{http.MethodGet, "/api/v1/attachments/attachments/missing", "", "single attachment read (404 from the handler is correct)", false},
		{http.MethodPatch, "/api/v1/attachments/attachments/missing?updateMask=filename", `{"filename":"n.po33"}`, "attachment update (inner body; 404 is correct)", false},
		{http.MethodDelete, "/api/v1/attachments/attachments/missing", "", "attachment delete (404 is correct)", false},
		{http.MethodGet, "/api/v1/users/" + username, "", "user read (the client's getId() strips to the bare username before the request)", true},
		{http.MethodGet, "/api/v1/users/" + username + "/settings", "", "user settings list", true},
		{http.MethodGet, "/api/v1/users/" + username + "/settings/GENERAL", "", "single setting read", true},
		{http.MethodPatch, "/api/v1/users/" + username + "/settings/GENERAL?updateMask=generalSetting", `{"locale":"en","memoVisibility":"PUBLIC"}`, "setting update", true},
		{http.MethodGet, "/api/v1/users/" + username + ":getStats", "", "user stats (bare username, per MemosV1Service.getId)", true},
		{http.MethodPost, "/api/v1/auth/signout", "", "sign out", false},
		{http.MethodDelete, "/api/v1/memos/" + memoID, "", "memo delete", false},
	}

	// v1BodyIsConnectError reports whether a 2xx body is a
	// connect-go error envelope rather than the procedure's real
	// response. A mis-wired wrapper does NOT 404 or 501: it
	// forwards a body the handler cannot unmarshal, the handler
	// returns `InvalidArgument`, and connect-go serialises that as
	// a **200** carrying `{"code":3,"message":"memo is required"}`
	// (or `"name is required"`, …). The client decodes that into a
	// missing field and renders an empty row, so the check has to
	// look at the body, not the status.
	// Only a **2xx** body can be a connect error envelope: the
	// connect protocol carries `InvalidArgument`/`Unauthenticated`
	// on a 2xx status and the codec decodes it into an empty
	// field. A real 4xx is an honest answer (`invalid credentials`
	// on a deliberately wrong password) and must not be flagged.
	v1BodyIsConnectError := func(w *httptest.ResponseRecorder) (bool, string) {
		if w.Code < 200 || w.Code >= 300 {
			return false, ""
		}
		var env struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(w.Body.Bytes(), &env) != nil {
			return false, ""
		}
		// A top-level "code"+"message" pair with no other payload
		// is the envelope. Real responses (Memo, User,
		// ListMemosResponse, …) never carry a bare `code`.
		if env.Code != "" && env.Message != "" {
			return true, env.Code + " " + env.Message
		}
		return false, ""
	}

	for _, o := range ops {
		w := doAuth(o.method, o.target, o.body)
		assert.NotEqual(t, http.StatusNotImplemented, w.Code,
			"%s %s (%s) must not answer 501 Not Implemented", o.method, o.target, o.why)
		if w.Code == http.StatusNotFound {
			assert.NotEmpty(t, strings.TrimSpace(w.Body.String()),
				"%s %s answered an EMPTY 404 — the router matched no pattern, so the route is unregistered or lost to the SPA catch-all",
				o.method, o.target)
		}
		if o.mustSucceed {
			assert.Less(t, w.Code, 300,
				"%s %s (%s) answered %d — this op must succeed on a fresh app with a valid PAT; body=%s",
				o.method, o.target, o.why, w.Code, w.Body.String())
		}
		if isErr, detail := v1BodyIsConnectError(w); isErr {
			assert.Fail(t, "connect error envelope on a 2xx",
				"%s %s (%s) answered 2xx carrying a connect error envelope %q — "+
					"the route is almost certainly a plain connect_proxy where the client "+
					"needs connect_proxy_wrap, so the handler got a body it cannot "+
					"unmarshal; body=%s", o.method, o.target, o.why, detail, w.Body.String())
		}
	}

	// The unauthenticated profile probe is the one op the client
	// issues before it has a token; it must still answer 200 JSON
	// (not the SPA's 200 + text/html, which is what breaks the
	// client's codec).
	wProbe := do(http.MethodGet, "/api/v1/instance/profile", "")
	assert.Equal(t, http.StatusOK, wProbe.Code,
		"the unauthenticated version probe must answer 200")
	assert.Equal(t, "application/json", wProbe.Header().Get("Content-Type"),
		"the unauthenticated version probe must answer JSON, not the SPA's text/html")
}
