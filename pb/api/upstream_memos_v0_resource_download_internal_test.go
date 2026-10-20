package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	v1 "github.com/incurl/riff/proto/gen/api/v1"
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
	memoRec.Set("author", owner.Id)
	memoRec.Set("content", "private memo")
	memoRec.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(memoRec))

	const secretBytes = "PRIVATE-payload-do-not-leak"
	att := fileTestAttachment(t, app, memoRec.Id, "secret.txt", []byte(secretBytes))
	att.Set("creator", owner.Id)
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

// TestV0MemoWithResourceIdListBindsUpload reproduces MoeMemos'
// real attachment-linking wire format.
//
// Our V0 PATCH binding reads `resourceList` — an array of
// *resource objects* (`[{id, filename, type, externalLink}]`).
// MoeMemos never sends that. Its `CreateMemoRequest` and
// `PatchMemoRequest` schemas declare **`resourceIdList`**, a
// *flat array of integers*:
//
//	resourceIdList:
//	  type: array
//	  items:
//	    type: integer
//
// (Packages/Services/Sources/MemosV0Service/openapi.yaml, and
// the `resourceIdList:` argument at MemosV0Service.swift:79 and
// :119). `resourceList` is the *response* shape — it belongs to
// the `Memo` schema — not a request field.
//
// The consequence: `v0ResourceListIDs` found nothing, the PATCH
// bound nothing, and every uploaded file stayed unlinked. The
// existing regression tests could not catch this because they
// hand-wrote the `resourceList` object form into the PATCH body,
// a shape the client cannot produce — a green test asserting a
// fiction.
//
// The test below sends the client's exact payload, decoded with
// UseNumber because the client reads and writes 64-bit ids as
// Long.
func TestV0MemoWithResourceIdListBindsUpload(t *testing.T) {
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
	patRec.Set("description", "MoeMemos V0 resourceIdList regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mountFileRoutes(r, app)
	mountTestSpaFallback(r)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	doReq := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	decodeV0 := func(t *testing.T, body []byte) map[string]any {
		t.Helper()
		var out map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		require.NoError(t, dec.Decode(&out), "V0 body must be valid JSON: %s", body)
		return out
	}

	// 1. Create the memo.
	w1 := doReq(http.MethodPost, "/api/v1/memo",
		`{"content":"with a file","visibility":"PRIVATE"}`)
	require.Equal(t, http.StatusOK, w1.Code,
		"V0 memo create must answer 200, got %d: %s", w1.Code, w1.Body.String())
	created := decodeV0(t, w1.Body.Bytes())
	memoV0ID := fmt.Sprintf("%v", created["id"])

	// 2. Upload the file (lands unlinked by design).
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, ferr := mw.CreateFormFile("file", "photo.jpeg")
	require.NoError(t, ferr)
	_, _ = fw.Write([]byte("fake-jpeg-bytes"))
	require.NoError(t, mw.Close())
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/resource/blob", &b)
	req2.Header.Set("Authorization", "Bearer "+pat)
	req2.Header.Set("Content-Type", mw.FormDataContentType())
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 resource upload must answer 200, got %d: %s", w2.Code, w2.Body.String())
	uploaded := decodeV0(t, w2.Body.Bytes())
	resourceID := fmt.Sprintf("%v", uploaded["id"])

	// 3. PATCH with the client's REAL payload: `resourceIdList`
	//    is a flat array of integer ids, not resource objects.
	w3 := doReq(http.MethodPatch, "/api/v1/memo/"+memoV0ID,
		`{"content":"with a file","visibility":"PRIVATE","resourceIdList":[`+resourceID+`]}`)
	require.Equal(t, http.StatusOK, w3.Code,
		"V0 memo patch must answer 200, got %d: %s", w3.Code, w3.Body.String())
	patched := decodeV0(t, w3.Body.Bytes())

	// The binding must have happened: the PATCH response echoes
	// the memo's attachments so the client's next edit does not
	// rebuild an empty list and drop the file.
	res, _ := patched["resourceList"].([]any)
	require.Len(t, res, 1,
		"the PATCH response must echo the attachment bound via resourceIdList, got %v",
		patched["resourceList"])

	// And the store must agree — this is the actual bug that was
	// reported ("uploads from MoeMemos are still not linked").
	cols, aerr := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, aerr)
	rows, rerr := app.FindRecordsByFilter(cols, "", "-created", 0, 0)
	require.NoError(t, rerr)
	require.Len(t, rows, 1)
	assert.Equal(t, memoV0ID, fmt.Sprintf("%v", v0IDFromV1(rows[0].GetString("memo"))),
		"the uploaded attachment must be bound to the memo the client created")

	// The memo list must show the attachment too.
	w4 := doReq(http.MethodGet, "/api/v1/memo?pageSize=50", "")
	require.Equal(t, http.StatusOK, w4.Code)
	var listRows []any
	dl := json.NewDecoder(bytes.NewReader(w4.Body.Bytes()))
	dl.UseNumber()
	require.NoError(t, dl.Decode(&listRows))
	require.Len(t, listRows, 1)
	lm, _ := listRows[0].(map[string]any)
	lres, _ := lm["resourceList"].([]any)
	assert.Len(t, lres, 1,
		"the memo list must show the bound attachment, got %v", lm["resourceList"])
}

// mountTestSpaFallback mounts a stand-in for the catch-all the
// TestV0MemoImageIsDownloadableAfterLink walks the exact
// sequence a MoeMemos user performs when they attach an image to
// a memo and the app renders it: create memo, upload the image,
// PATCH to bind, then download the bytes from the `uid` URL the
// client builds.
//
// It runs for both PRIVATE and PUBLIC memos because the two take
// different authorization paths, and the reported symptom was
// that *both* failed:
//
//   - Before the binding lands the row is an orphan, so
//     `authorizeAttachment` takes the `creator` branch.
//   - After the binding lands the row has a memo, so the branch
//     becomes `app.CanAccessRecord(memo, …, memos.ViewRule)`.
//
// Either branch failing means every image in the feed is a broken
// tile, so a test that only covers the orphan window proves
// nothing about the steady state.
func TestV0MemoImageIsDownloadableAfterLink(t *testing.T) {
	for _, visibility := range []string{"PRIVATE", "PUBLIC"} {
		t.Run(visibility, func(t *testing.T) {
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
			patRec.Set("description", "download after link")
			patRec.Set("token_hash", string(patHash))
			require.NoError(t, app.Save(patRec))

			r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
			mountUpstreamMemosV0Routes(r, app, nil)
			mountFileRoutes(r, app)
			mountTestSpaFallback(r)
			mux, err := r.BuildMux()
			require.NoError(t, err)

			doJSON := func(method, target, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, target, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+pat)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				return w
			}
			getBlob := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+pat)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				return w
			}
			decodeV0 := func(t *testing.T, body []byte) map[string]any {
				t.Helper()
				var out map[string]any
				dec := json.NewDecoder(bytes.NewReader(body))
				dec.UseNumber()
				require.NoError(t, dec.Decode(&out), "V0 body must be valid JSON: %s", body)
				return out
			}
			// Create the memo at the visibility under test.
			w1 := doJSON(http.MethodPost, "/api/v1/memo",
				`{"content":"photo memo","visibility":"`+visibility+`"}`)
			require.Equal(t, http.StatusOK, w1.Code,
				"V0 memo create must answer 200, got %d: %s", w1.Code, w1.Body.String())
			memoV0ID := fmt.Sprintf("%v", decodeV0(t, w1.Body.Bytes())["id"])

			// Upload a real (tiny) JPEG.
			imgBytes := sampleJPEG(t, 4, 4)
			var b bytes.Buffer
			mw := multipart.NewWriter(&b)
			fw, ferr := mw.CreateFormFile("file", "photo.jpeg")
			require.NoError(t, ferr)
			_, _ = fw.Write(imgBytes)
			require.NoError(t, mw.Close())
			req2 := httptest.NewRequest(http.MethodPost, "/api/v1/resource/blob", &b)
			req2.Header.Set("Authorization", "Bearer "+pat)
			req2.Header.Set("Content-Type", mw.FormDataContentType())
			w2 := httptest.NewRecorder()
			mux.ServeHTTP(w2, req2)
			require.Equal(t, http.StatusOK, w2.Code,
				"V0 resource upload must answer 200, got %d: %s", w2.Code, w2.Body.String())
			uploaded := decodeV0(t, w2.Body.Bytes())
			resourceID := fmt.Sprintf("%v", uploaded["id"])
			uid, _ := uploaded["uid"].(string)
			require.NotEmpty(t, uid, "the V0 resource must carry a uid: %s", w2.Body.String())

			// BEFORE the binding: the row is an orphan and only the
			// uploader may read it.
			wPre := getBlob("/o/r/" + uid)
			assert.Equal(t, http.StatusOK, wPre.Code,
				"[%s] the uploader must be able to read the unlinked upload, got %d: %s",
				visibility, wPre.Code, wPre.Body.String())

			// Bind it with the client's real `resourceIdList`.
			w3 := doJSON(http.MethodPatch, "/api/v1/memo/"+memoV0ID,
				`{"content":"photo memo","visibility":"`+visibility+`","resourceIdList":[`+resourceID+`]}`)
			require.Equal(t, http.StatusOK, w3.Code,
				"V0 memo patch must answer 200, got %d: %s", w3.Code, w3.Body.String())

			// AFTER the binding: the row has a memo, so authorization
			// goes through the memo's ViewRule instead of the
			// orphan branch. This is the steady state the app
			// renders, and the one that was reported broken.
			wPost := getBlob("/o/r/" + uid)
			assert.Equal(t, http.StatusOK, wPost.Code,
				"[%s] the uploader must read the BOUND attachment from /o/r/{uid}, got %d: %s",
				visibility, wPost.Code, wPost.Body.String())
			assert.Equal(t, imgBytes, wPost.Body.Bytes(),
				"[%s] the bound attachment must serve the real image bytes, not index.html", visibility)
			assert.Equal(t, "image/jpeg", wPost.Header().Get("Content-Type"),
				"[%s] the bound attachment must report its stored MIME type", visibility)

			// The same bytes must also be reachable through the
			// `externalLink` the client falls back to.
			externalLink, _ := uploaded["externalLink"].(string)
			require.NotEmpty(t, externalLink, "the resource must carry an externalLink")
			wLink := getBlob(externalLink)
			assert.Equal(t, http.StatusOK, wLink.Code,
				"[%s] the externalLink %q must answer 200, got %d: %s",
				visibility, externalLink, wLink.Code, wLink.Body.String())
			assert.Equal(t, imgBytes, wLink.Body.Bytes(),
				"[%s] the externalLink must serve the real image bytes", visibility)
		})
	}
}

// TestV1AttachmentFromSpaUploadIsDownloadableFromMoeMemos
// reproduces the user-reported failure: an attachment posted
// from the SPA (i.e. via the chunked `UploadAttachment` connect
// RPC) is visible in the feed but the image URL MoeMemos builds
// answers 404.
//
// The SPA-upload path is the path that lands a row with
// `memo = <id>`, `creator = <uploader>`, `mime_type`,
// `filename`, and `size` all populated (see
// `pb/connect/attachment_upload.go::finalizeUpload`). The MoeMemos
// V0 client reads the resulting `MemosV0Resource.externalLink`
// (a `/file/attachments/<uid>/<filename>` URL pointing at the
// public file surface) and downloads from it. The V1 client
// reads `MemosV1Attachment.externalLink` and falls back to
// `/file/<name>/<filename>` when it is empty.
//
// This test exercises the SPA upload end-to-end and then
// downloads the resulting blob from both URL forms MoeMemos uses,
// asserting on the actual bytes the client receives (not just the
// status code). A test that only asserts the status would pass
// for a 200 that returned `index.html`.
func TestV1AttachmentFromSpaUploadIsDownloadableFromMoeMemos(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	// The connect dispatcher the production handler reaches
	// through. The V0/V1 REST shapers look up the registered
	// `connect.Server` via `connect.ConnectMux()`, so the test
	// has to install one before any connect call lands.
	require.NoError(t, connect.Register(app, nil))
	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))
	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	// The chunked `UploadAttachment` is a connect-only RPC; the
	// SPA reaches it through the typed client. The connect.Mux
	// above is in-process, so we serve it on a test server to
	// give the typed client an HTTP target to dial.
	connectSrv := httptest.NewServer(connectMux)
	t.Cleanup(connectSrv.Close)

	// Real SPA user with a memos_pat_* Bearer token (the same
	// credential form MoeMemos uses; see the V0 boot test).
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	alice := core.NewRecord(users)
	alice.Set("username", "alice")
	alice.Set("email", "alice@example.com")
	alice.Set("password", "passw0rd!")
	require.NoError(t, app.Save(alice))
	patRaw, perr := random.String(24)
	require.NoError(t, perr)
	pat := "memos_pat_" + patRaw
	patHash, perr := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	require.NoError(t, perr)
	pats, perr := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, perr)
	patRec := core.NewRecord(pats)
	patRec.Set("user", alice.Id)
	patRec.Set("description", "alice")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	// The SPA carries the Bearer token on every connect call, so
	// the connect client must add it too. The connect client is
	// built from a `connecthttp.NewTransport(httpClient, baseURL)`
	// pair so the URL and the Bearer transport are both wired.
	httpClient := newBearerClient(pat)
	transport := connecthttp.NewTransport(httpClient, connectSrv.URL)
	connClient := connectrpc.NewClient(transport)
	memoClient := apiv1connect.NewMemoServiceClient(connClient)
	attachClient := apiv1connect.NewAttachmentServiceClient(connClient)

	// Create a memo so the chunked upload has somewhere to land.
	createResp, err := memoClient.CreateMemo(context.Background(), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "photo memo", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoName := createResp.GetName()
	require.NotEmpty(t, memoName)

	// SPA chunked upload through the *real* HTTP connect surface —
	// the same code path the production handler takes.
	imgBytes := sampleJPEG(t, 4, 4)
	specResp, err := attachClient.UploadAttachment(context.Background(), &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename: "from-spa.jpeg",
					Type:     "image/jpeg",
					Memo:     &memoName,
				},
				TotalSize: int64(len(imgBytes)),
			},
		},
	})
	require.NoError(t, err)
	uploadID := specResp.GetUploadId()
	require.NotEmpty(t, uploadID)

	finResp, err := attachClient.UploadAttachment(context.Background(), &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_UploadId{
			UploadId: uploadID,
		},
		WriteOffset: 0,
		Data:        imgBytes,
		FinishWrite: true,
	})
	require.NoError(t, err)
	finAttachment := finResp.GetAttachment()
	require.NotNil(t, finAttachment, "the SPA upload must return the final attachment proto")

	attachmentName := finAttachment.GetName()
	externalLink := finAttachment.GetExternalLink()
	require.NotEmpty(t, attachmentName, "the SPA-uploaded attachment must have a name")
	require.NotEmpty(t, externalLink, "the V1 attachment shaper must emit an externalLink")

	// Build the V1 client view: the proto's `externalLink` is what
	// the client prefers when present, but it is relative and the
	// client resolves it against the configured host. The
	// fallback URL the client builds (`/file/<name>/<filename>`)
	// is a different path on our router and a known-failing
	// surface — assert it too, so the test pins both the SPA
	// contract and the V1 client contract.
	filename := finAttachment.GetFilename()

	// Mount the file routes alongside the V1 REST surface (the
	// SPA catch-all stands in for the real binary so a missing
	// route answers 200 index.html rather than 404).
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV1Routes(r, app, nil)
	mountFileRoutes(r, app)
	mountTestSpaFallback(r)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+pat)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// (a) The URL the V1/V0 connect shaper emits. The SPA catch-all
	// below is set up precisely so a 200-with-index.html here is
	// visible: a missing /file/... surface was the original bug.
	wLink := get(externalLink)
	assert.Equal(t, http.StatusOK, wLink.Code,
		"the V1 externalLink %q must answer 200, got %d: %s",
		externalLink, wLink.Code, wLink.Body.String())
	assert.Equal(t, imgBytes, wLink.Body.Bytes(),
		"the V1 externalLink must stream the actual JPEG bytes, not index.html")
	assert.Equal(t, "image/jpeg", wLink.Header().Get("Content-Type"),
		"the V1 externalLink must serve the stored MIME type, got %q",
		wLink.Header().Get("Content-Type"))

	// (b) The V1 client fallback URL: the Swift client builds
	// `<host>/file/<name>/<filename>` (note: `name` is the
	// resource name like `attachments/<id>`, NOT just `<id>`).
	// This is the same path as the `externalLink` for our
	// setup, so it answers 200 here. The probe exists so a
	// future change that diverges the two surfaces fails the
	// test rather than silently breaking one of them.
	wFB := get("/file/" + attachmentName + "/" + filename)
	require.Equal(t, http.StatusOK, wFB.Code,
		"the V1 client fallback URL /file/{name}/{filename} must answer 200, got %d: %s",
		wFB.Code, wFB.Body.String())
	assert.Equal(t, imgBytes, wFB.Body.Bytes(),
		"the V1 client fallback URL must stream the actual JPEG bytes")
	assert.Equal(t, "image/jpeg", wFB.Header().Get("Content-Type"),
		"the V1 client fallback URL must serve the stored MIME type")

	// (c) The V0 surface MoeMemos actually consumes. The V0
	// client reads the feed from `GET /api/v1/memo` and walks
	// each memo's `resourceList` to fetch the image. The V0
	// resource shape carries a *different* URL form (`uid` and
	// an absolute externalLink), so the test exercises the
	// whole feed-publish path end-to-end. A 404 here would mean
	// MoeMemos renders the memo but every image preview is
	// broken — the exact symptom the user reported.
	r2 := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r2, app, nil)
	mountFileRoutes(r2, app)
	mountTestSpaFallback(r2)
	muxV0, err := r2.BuildMux()
	require.NoError(t, err)
	wList := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+pat)
		w := httptest.NewRecorder()
		muxV0.ServeHTTP(w, req)
		return w
	}
	wFeed := wList("/api/v1/memo?pageSize=50")
	require.Equal(t, http.StatusOK, wFeed.Code,
		"V0 list must answer 200, got %d: %s", wFeed.Code, wFeed.Body.String())
	var feedRows []any
	dl := json.NewDecoder(bytes.NewReader(wFeed.Body.Bytes()))
	dl.UseNumber()
	require.NoError(t, dl.Decode(&feedRows), "V0 list must be a JSON array: %s", wFeed.Body.String())
	require.Len(t, feedRows, 1, "the SPA upload must be bound to a memo")
	feedMemo, _ := feedRows[0].(map[string]any)
	feedRes, _ := feedMemo["resourceList"].([]any)
	require.Len(t, feedRes, 1, "the V0 list must show the attachment: %v", feedMemo["resourceList"])
	feedAtt, _ := feedRes[0].(map[string]any)
	feedExtLink, _ := feedAtt["externalLink"].(string)
	feedV0ID, _ := feedAtt["uid"].(string)
	require.NotEmpty(t, feedV0ID, "the V0 resource must carry a uid")
	require.NotEmpty(t, feedExtLink, "the V0 resource must carry an externalLink")

	wDL := wList(feedExtLink)
	require.Equal(t, http.StatusOK, wDL.Code,
		"V0 externalLink %q must answer 200, got %d: %s",
		feedExtLink, wDL.Code, wDL.Body.String())
	assert.Equal(t, imgBytes, wDL.Body.Bytes(),
		"V0 externalLink must stream the actual JPEG bytes, not index.html")

	// (d) Anonymous fetch of the PRIVATE attachment must answer
	// 404 (a denial must not confirm the attachment exists, and
	// must not leak the bytes).
	req := httptest.NewRequest(http.MethodGet, feedExtLink, nil)
	wAnon := httptest.NewRecorder()
	muxV0.ServeHTTP(wAnon, req)
	assert.Equal(t, http.StatusNotFound, wAnon.Code,
		"an anonymous read of a PRIVATE attachment must answer 404, got %d: %s",
		wAnon.Code, wAnon.Body.String())
	assert.NotContains(t, wAnon.Body.String(), string(imgBytes),
		"a denied read must not leak the attachment bytes")

	// (e) The separate resource list endpoint. MoeMemos V0
	// occasionally calls `GET /api/v1/resource?creatorId=…` (a
	// top-level array) in addition to walking each memo's
	// `resourceList`. The SPA-uploaded attachment must appear
	// here too, with a downloadable `externalLink` and a `uid`.
	wRes := wList("/api/v1/resource?limit=50")
	require.Equal(t, http.StatusOK, wRes.Code,
		"V0 resource list must answer 200, got %d: %s", wRes.Code, wRes.Body.String())
	var resRows []any
	dres := json.NewDecoder(bytes.NewReader(wRes.Body.Bytes()))
	dres.UseNumber()
	require.NoError(t, dres.Decode(&resRows),
		"V0 resource list must be a JSON array: %s", wRes.Body.String())
	require.GreaterOrEqual(t, len(resRows), 1,
		"V0 resource list must contain the SPA-uploaded attachment")
	var resAtt map[string]any
	for _, r := range resRows {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		if ext, _ := m["externalLink"].(string); strings.HasSuffix(ext, "/from-spa.jpeg") {
			resAtt = m
			break
		}
	}
	require.NotNil(t, resAtt,
		"V0 resource list must include the SPA-uploaded attachment by filename: %s",
		wRes.Body.String())
	resUID, _ := resAtt["uid"].(string)
	assert.NotEmpty(t, resUID,
		"a SPA-uploaded attachment must carry a uid in the resource list")
	resExtLink, _ := resAtt["externalLink"].(string)
	require.NotEmpty(t, resExtLink)
	wResDL := wList(resExtLink)
	require.Equal(t, http.StatusOK, wResDL.Code,
		"V0 resource list externalLink %q must answer 200, got %d: %s",
		resExtLink, wResDL.Code, wResDL.Body.String())
	assert.Equal(t, imgBytes, wResDL.Body.Bytes(),
		"V0 resource list externalLink must stream the actual JPEG bytes")
}

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

// bearerTransport attaches a fixed Authorization header to every
// outgoing request. It is enough for the SPA upload path, which
// reads the user from the Bearer token only — no cookies, no
// signed URLs.
type bearerTransport struct {
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.Header.Get("Authorization") == "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(clone)
}

func newBearerClient(token string) *http.Client {
	return &http.Client{Transport: bearerTransport{token: token}}
}
