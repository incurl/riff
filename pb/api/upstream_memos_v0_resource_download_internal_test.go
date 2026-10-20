package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
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

// TestV0ResourceIsDownloadable pins the file-read half of the
// MoeMemos V0 surface, which the metadata regression
// (TestV0BootServesRealCreationMetadata) cannot see: that test
// only asserts on the *shaped JSON*, never on the bytes the
// client subsequently downloads from the URL inside that JSON.
//
// The MoeMemos iOS client resolves a resource's URL in
// `MemosV0Resource.url(for:)` (Packages/Services/Sources/
// MemosV0Service/Ext.swift in the MoeMemos repo):
//
//	func path() -> String {
//	    if let uid = uid, !uid.isEmpty { return "/o/r/\(uid)" }
//	    return "/o/r/\(name ?? "")"
//	}
//	func url(for hostURL: URL) -> URL {
//	    if let externalLink = externalLink, ... { return url }
//	    return hostURL.appending(path: path())
//	}
//
// So the client downloads from `<host>/o/r/<uid>`, not from
// riff's `/file/attachments/<id>`. Two things must therefore
// hold, and neither was true:
//
//  1. The V0 resource shape must carry `uid`. Upstream Memos
//     populates it from `store.Resource.UID` (a shortuuid); riff
//     has no such column and omitted the field entirely, so the
//     Swift `uid` optional was always nil and the client fell
//     through to the `name` branch, requesting `/o/r/` with an
//     empty segment.
//  2. `GET /o/r/{uid}` must be served. It was not registered at
//     all, so the request fell through to the SPA catch-all
//     (server/frontend/embed.go) and answered **200 with
//     index.html**. The client's `download` wrote HTML into the
//     attachment cache and every image render failed — the
//     "MoeMemos is locked out of file GETs" symptom.
//
// The `externalLink` fallback matters too: when riff emits one
// (it always does, see v0ShapeResource) the client uses that
// absolute/relative URL instead of `/o/r/{uid}`, so this test
// asserts both routes work.
func TestV0ResourceIsDownloadable(t *testing.T) {
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
	patRec.Set("description", "MoeMemos V0 resource download regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	// The full surface, not just the V0 routes: the file routes
	// live on the same router as the V0 API, and the SPA
	// catch-all has to be mounted too so the test reproduces the
	// "fell through to index.html" failure rather than a bare
	// mux 404 (which would pass for the wrong reason).
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mountUpstreamMemosV1Routes(r, app, nil)
	mountFileRoutes(r, app)
	mountTestSpaFallback(r)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	doReq := func(method, target string, body []byte, contentType string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == nil {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, bytes.NewReader(body))
		}
		req.Header.Set("Authorization", "Bearer "+pat)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// The V0 client decodes 64-bit ids as `Long`, so every
	// numeric field is read with UseNumber.
	decodeV0 := func(t *testing.T, body []byte) map[string]any {
		t.Helper()
		var out map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		require.NoError(t, dec.Decode(&out), "V0 body must be valid JSON: %s", body)
		return out
	}

	// Upload a file with known, non-HTML bytes.
	const fileBytes = "RIFF\x00\x00fake-wav-payload"
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, ferr := mw.CreateFormFile("file", "sketch.wav")
	require.NoError(t, ferr)
	_, _ = fw.Write([]byte(fileBytes))
	require.NoError(t, mw.Close())

	w2 := doReq(http.MethodPost, "/api/v1/resource/blob", b.Bytes(), mw.FormDataContentType())
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 resource upload must answer 200, got %d: %s", w2.Code, w2.Body.String())
	uploaded := decodeV0(t, w2.Body.Bytes())

	// (1) The shape must carry `uid`, because the client's URL
	// builder reads it and falls back to an *empty* path segment
	// without it. Upstream Memos populates this from
	// `store.Resource.UID`; riff has no UID column, so the id the
	// client can actually resolve is the PocketBase record id —
	// which is exactly what the `/o/r/{uid}` route below accepts.
	uid, _ := uploaded["uid"].(string)
	assert.NotEmpty(t, uid,
		"V0 resource upload must return a `uid`; without it the MoeMemos "+
			"client builds `/o/r/` and downloads nothing, got body: %s", w2.Body.String())

	// (2) The client's primary download path. It is the one
	// taken whenever `uid` is present.
	if uid != "" {
		wget := doReq(http.MethodGet, "/o/r/"+uid, nil, "")
		require.Equal(t, http.StatusOK, wget.Code,
			"GET /o/r/{uid} must answer 200, got %d: %s", wget.Code, wget.Body.String())
		assert.Equal(t, fileBytes, wget.Body.String(),
			"GET /o/r/{uid} must stream the attachment bytes, not the SPA's index.html")
		assert.Contains(t, wget.Header().Get("Content-Type"), "audio",
			"GET /o/r/{uid} must serve the stored MIME type, got %q",
			wget.Header().Get("Content-Type"))
	}

	// (3) The `externalLink` fallback riff emits. The client
	// prefers it over `/o/r/{uid}`, so it is the path most
	// downloads actually take. It is a *relative* URL in our
	// response, which the client resolves against its configured
	// host — so the same bytes must be reachable there.
	externalLink, _ := uploaded["externalLink"].(string)
	require.NotEmpty(t, externalLink,
		"V0 resource upload must return an externalLink, got body: %s", w2.Body.String())
	linkPath := externalLink
	if i := strings.Index(linkPath, "://"); i >= 0 {
		// Absolute form: reduce to its path + query.
		rest := linkPath[i+3:]
		slash := strings.Index(rest, "/")
		require.GreaterOrEqual(t, slash, 0, "absolute externalLink has no path: %s", externalLink)
		linkPath = rest[slash:]
	}
	wlink := doReq(http.MethodGet, linkPath, nil, "")
	require.Equal(t, http.StatusOK, wlink.Code,
		"the resource externalLink %q must answer 200, got %d: %s",
		externalLink, wlink.Code, wlink.Body.String())
	assert.Equal(t, fileBytes, wlink.Body.String(),
		"the resource externalLink must stream the attachment bytes, not index.html")
}

// TestV0ResourceDownloadDeniesOtherUsers is the authorization
// half: `/o/r/{uid}` must not become a way to read an arbitrary
// attachment by id. A denial answers 404 (not 403) so the
// response never confirms that the attachment exists, matching
// the `/file/attachments/...` surface.
func TestV0ResourceDownloadDeniesOtherUsers(t *testing.T) {
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

	makeUser := func(username, email string) *core.Record {
		r := core.NewRecord(users)
		r.Set("username", username)
		r.Set("email", email)
		r.Set("password", "passw0rd!")
		require.NoError(t, app.Save(r))
		return r
	}
	owner := makeUser("moe", "moe@example.com")
	other := makeUser("mallory", "mallory@example.com")

	makePAT := func(t *testing.T, userID, desc string) string {
		t.Helper()
		raw, rerr := random.String(24)
		require.NoError(t, rerr)
		tok := "memos_pat_" + raw
		hash, herr := bcrypt.GenerateFromPassword([]byte(tok), bcrypt.MinCost)
		require.NoError(t, herr)
		col, cerr := app.FindCollectionByNameOrId("personal_access_tokens")
		require.NoError(t, cerr)
		rec := core.NewRecord(col)
		rec.Set("user", userID)
		rec.Set("description", desc)
		rec.Set("token_hash", string(hash))
		require.NoError(t, app.Save(rec))
		return tok
	}
	ownerPAT := makePAT(t, owner.Id, "owner")
	otherPAT := makePAT(t, other.Id, "other user")

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mountFileRoutes(r, app)
	mountTestSpaFallback(r)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	doReq := func(target, pat string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if pat != "" {
			req.Header.Set("Authorization", "Bearer "+pat)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// moe owns a PRIVATE memo carrying an attachment, so only
	// moe may read the blob.
	memosCol, cerr := app.FindCollectionByNameOrId("memos")
	require.NoError(t, cerr)
	memoRec := core.NewRecord(memosCol)
	memoRec.Set("creator", owner.Id)
	memoRec.Set("content", "private memo")
	memoRec.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(memoRec))

	const secretBytes = "PRIVATE-payload-do-not-leak"
	attCol, aerr := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, aerr)
	att := core.NewRecord(attCol)
	att.Set("memo", memoRec.Id)
	att.Set("creator", owner.Id)
	att.Set("filename", "secret.txt")
	att.Set("mime_type", "text/plain")
	att.Set("size", len(secretBytes))
	require.NoError(t, app.Save(att))

	wOwner := doReq("/o/r/"+att.Id, ownerPAT)
	wAnon := doReq("/o/r/"+att.Id, "")
	wOther := doReq("/o/r/"+att.Id, otherPAT)

	assert.Equal(t, http.StatusOK, wOwner.Code,
		"the owner must be able to read their own attachment via /o/r/{uid}, got %d: %s",
		wOwner.Code, wOwner.Body.String())
	assert.Equal(t, secretBytes, wOwner.Body.String(),
		"the owner must receive the real bytes")

	assert.Equal(t, http.StatusNotFound, wAnon.Code,
		"an anonymous /o/r/{uid} read of a private attachment must answer 404, got %d: %s",
		wAnon.Code, wAnon.Body.String())
	assert.NotContains(t, wAnon.Body.String(), secretBytes,
		"a denied /o/r/{uid} read must not leak the bytes")

	assert.Equal(t, http.StatusNotFound, wOther.Code,
		"another user's /o/r/{uid} read must answer 404, got %d: %s",
		wOther.Code, wOther.Body.String())
	assert.NotContains(t, wOther.Body.String(), secretBytes,
		"a denied /o/r/{uid} read must not leak the bytes")
}

// mountTestSpaFallback mounts a stand-in for the catch-all the
// real binary serves (server/frontend/embed.go) so an unrouted
// path answers 200 index.html rather than a mux 404. Without it,
// a missing `/o/r/{uid}` route would 404 and the test could not
// tell the two failure modes apart — which is precisely the bug:
// the route *is* missing and the SPA *does* swallow it.
func mountTestSpaFallback(r *router.Router[*core.RequestEvent]) {
	r.GET("/{path...}", func(e *core.RequestEvent) error {
		return e.String(http.StatusOK, "<!doctype html><html><body>SPA index</body></html>")
	})
}