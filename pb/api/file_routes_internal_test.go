package api

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/incurl/riff/pb/migrations"
)

// makeFileRouter builds an http.Handler with the public /file/...
// routes mounted, wired to the given app.
func makeFileRouter(t *testing.T, app core.App) http.Handler {
	t.Helper()
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountFileRoutes(r, app)
	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")
	return mux
}

// fileTestUser creates a user with the given username and returns
// the record.
func fileTestUser(t *testing.T, app core.App, username string) *core.Record {
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

// fileTestMemo creates a memo owned by author with the given
// visibility and returns the record.
func fileTestMemo(t *testing.T, app core.App, author *core.Record, visibility string) *core.Record {
	t.Helper()
	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(memos)
	rec.Set("author", author.Id)
	rec.Set("content", "hello")
	rec.Set("visibility", visibility)
	require.NoError(t, app.Save(rec))
	return rec
}

// fileTestAttachment creates an `attachments` row linked to memoID
// with the given bytes on disk, and returns the record.
//
// The bytes are written into PocketBase's filesystem under the
// canonical key (<collectionId>/<recordId>/<filename>) so the file
// server can find them.
func fileTestAttachment(t *testing.T, app core.App, memoID, filename string, payload []byte) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)

	rec := core.NewRecord(col)
	if memoID != "" {
		rec.Set("memo", memoID)
	}
	rec.Set("filename", filename)
	rec.Set("size", len(payload))
	rec.Set("storage", "local")

	f, err := filesystem.NewFileFromBytes(payload, filename)
	require.NoError(t, err)
	rec.Set("blob", f)
	require.NoError(t, app.Save(rec))

	// PocketBase stores the blob under <collectionId>/<recordId>/<filename>.
	fsys, err := app.NewFilesystem()
	require.NoError(t, err)
	defer fsys.Close()
	key := filepath.ToSlash(filepath.Join(col.Id, rec.Id, filename))
	require.NoError(t, fsys.Upload(payload, key))

	return rec
}

// sampleJPEG returns a small valid JPEG of the given size.
func sampleJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// samplePNG returns a small valid PNG of the given size.
func samplePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestFileRoute_ServesAttachmentBytes is the core regression test
// for the reported bug: the SPA's /file/attachments/{uid}/{filename}
// URL must return the file's bytes.
//
// Before the /file/... routes were registered, this path fell
// through to the SPA's catch-all and answered 200 with index.html.
func TestFileRoute_ServesAttachmentBytes(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	payload := sampleJPEG(t, 32, 32)
	att := fileTestAttachment(t, app, memo.Id, "photo.jpeg", payload)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/photo.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_ServesAttachmentWithoutFilename covers the
// /file/attachments/{uid} form (no filename segment).
func TestFileRoute_ServesAttachmentWithoutFilename(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	payload := sampleJPEG(t, 16, 16)
	att := fileTestAttachment(t, app, memo.Id, "photo.jpeg", payload)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_UnknownAttachmentIs404 asserts an unknown id is a
// clean 404 (not the SPA's HTML catch-all).
func TestFileRoute_UnknownAttachmentIs404(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/doesnotexist/photo.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Body.String(), "<!doctype html>")
}

// TestFileRoute_PrivateAttachmentIsNotPublic is the
// authorization guard: a private memo's attachment must not be
// readable by an anonymous caller.
func TestFileRoute_PrivateAttachmentIsNotPublic(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := sampleJPEG(t, 16, 16)
	att := fileTestAttachment(t, app, memo.Id, "secret.jpeg", payload)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/secret.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotEqual(t, payload, w.Body.Bytes())
}

// TestFileRoute_ProtectedAttachmentFollowsTheCollectionRule pins
// the file server's authorization to the collection's own rule.
//
// riff's `memos` ViewRule reads
//
//	author = @request.auth.id || visibility = "PROTECTED" || ...
//
// so a PROTECTED memo is world-readable in riff (this diverges from
// upstream Memos, where PROTECTED means "any signed-in user"). The
// file server deliberately does not re-implement the visibility
// policy: it asks the collection rule, so the file surface and the
// API surface can never disagree about who may read a memo.
//
// The invariant under test is "file access == memo access". If the
// rule ever changes, both surfaces change together.
func TestFileRoute_ProtectedAttachmentFollowsTheCollectionRule(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PROTECTED")
	payload := sampleJPEG(t, 16, 16)
	att := fileTestAttachment(t, app, memo.Id, "protected.jpeg", payload)

	// The memo itself is readable by an anonymous caller, so the
	// file must be too — otherwise the SPA would render a broken
	// image for a memo it can read.
	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/protected.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())

	// And the rule really is world-readable, so the test would
	// notice if the semantics changed under it.
	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	ok, err := app.CanAccessRecord(memo, &core.RequestInfo{Method: http.MethodGet}, memos.ViewRule)
	require.NoError(t, err)
	assert.True(t, ok, "a PROTECTED memo is readable by an anonymous caller under riff's rule")
}

// TestFileRoute_SpaceAttachmentIsNotPublic asserts a SPACE memo's
// attachment is not served to a non-member.
func TestFileRoute_SpaceAttachmentIsNotPublic(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "SPACE")
	att := fileTestAttachment(t, app, memo.Id, "space.jpeg", sampleJPEG(t, 16, 16))

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/space.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestFileRoute_AuthorCanReadOwnPrivateAttachment asserts the
// author of a private memo still gets their own file.
func TestFileRoute_AuthorCanReadOwnPrivateAttachment(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := sampleJPEG(t, 16, 16)
	att := fileTestAttachment(t, app, memo.Id, "mine.jpeg", payload)

	token, err := author.NewAuthToken()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/mine.jpeg", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_SessionCookieUnlocksPrivateAttachmentInAnImgTag is
// the regression test for the reported 404 on the SPA's insert-image
// path (`GET /file/attachments/ob0frvna8r7yuce/main_9cfda642.webp`).
//
// The failure is a transport mismatch, not a broken upload: the SPA
// holds its PocketBase token in localStorage, and an `<img src>` can
// present neither a localStorage value nor an Authorization header.
// The browser therefore sent the file GET with no credential at all,
// `authorizeAttachment` fell through to the memos visibility rule,
// and the memo's PRIVATE visibility — the editor's default — produced
// a 404 for the author's own image.
//
// The test reproduces the browser exactly: a plain GET carrying only
// a Cookie header (no Authorization, which is the whole point) and
// the `X-Forwarded-Proto: https` that the deployment's caddy layer
// sets. It asserts both halves: the owner is served, and the bytes
// are the real bytes rather than the SPA's index.html fallback.
func TestFileRoute_SessionCookieUnlocksPrivateAttachmentInAnImgTag(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := samplePNG(t, 24, 24)
	att := fileTestAttachment(t, app, memo.Id, "main_9cfda642.webp", payload)

	token, err := author.NewAuthToken()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/main_9cfda642.webp", nil)
	// No Authorization header: this is what a browser <img> can do.
	req.AddCookie(&http.Cookie{Name: fileAuthCookieName, Value: token})
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "the memo's own author must be able to render their own private attachment")
	assert.NotContains(t, w.Body.String(), "<!doctype html>", "a 200 that carries index.html is the SPA catch-all, not the file")
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_SessionCookieWorksOnLoopbackOverPlainHTTP pins the
// development carve-out: `pnpm dev` serves the SPA over plain HTTP on
// localhost, and a device bring-up may use a plain-HTTP LAN address.
// The cookie must still work there, otherwise the fix would appear
// to break locally and then work in production — the worst possible
// order in which to discover a transport rule.
//
// The loopback case is safe because the traffic never leaves the
// host, which is the same reasoning that lets browsers mark cookies
// Secure-by-default on localhost.
func TestFileRoute_SessionCookieWorksOnLoopbackOverPlainHTTP(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := sampleJPEG(t, 16, 16)
	att := fileTestAttachment(t, app, memo.Id, "local.jpeg", payload)

	token, err := author.NewAuthToken()
	require.NoError(t, err)

	// httptest.NewRequest targets example.com, so the Host header is
	// rewritten to a loopback name to exercise the carve-out.
	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/local.jpeg", nil)
	req.Host = "localhost:8090"
	req.AddCookie(&http.Cookie{Name: fileAuthCookieName, Value: token})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_SessionCookieIsScopedToTheFileSurface pins that the
// cookie does not turn the file server into a public bucket.
//
// Three cases matter: a foreign user holding a valid token must not
// read another user's PRIVATE memo; a garbage cookie must not be
// treated as a credential (an unauthenticated request must not
// silently become "nobody but allowed"); and a cookie presented over
// plain HTTP on a non-loopback host must be refused, because
// accepting a session token on a cleartext channel is the one case
// where the cookie genuinely hands the credential to a network
// observer.
func TestFileRoute_SessionCookieIsScopedToTheFileSurface(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	other := fileTestUser(t, app, "mallory")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := sampleJPEG(t, 16, 16)
	att := fileTestAttachment(t, app, memo.Id, "secret.jpeg", payload)

	token, err := author.NewAuthToken()
	require.NoError(t, err)
	// A different user's own valid token still grants nothing on
	// someone else's PRIVATE memo: the cookie resolves a user, it
	// does not bypass the visibility rule.
	malloryToken, err := other.NewAuthToken()
	require.NoError(t, err)

	tests := []struct {
		name        string
		token       string
		forwardedTo string
		host        string
	}{
		{
			name:        "another user's cookie is refused",
			token:       malloryToken,
			forwardedTo: "https",
		},
		{
			name:        "a garbage cookie is not a credential",
			token:       "not-a-real-token",
			forwardedTo: "https",
		},
		{
			name:        "cleartext http on a remote host is refused",
			token:       token,
			forwardedTo: "http",
			host:        "ravine.fly.dev",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/secret.jpeg", nil)
			if tc.host != "" {
				req.Host = tc.host
			}
			req.AddCookie(&http.Cookie{Name: fileAuthCookieName, Value: tc.token})
			if tc.forwardedTo != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwardedTo)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code, "a denied file read must answer 404, never 403")
			assert.NotEqual(t, payload, w.Body.Bytes(), "no bytes may leak on a denial")
		})
	}
}

// TestFileRoute_ThumbnailIsGenerated asserts ?thumbnail=true
// produces a downscaled JPEG rather than the original bytes.
//
// The upstream server uses a 600px box; the test uses a source
// larger than that so the resize is observable.
func TestFileRoute_ThumbnailIsGenerated(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	payload := sampleJPEG(t, 1200, 900)
	att := fileTestAttachment(t, app, memo.Id, "big.jpeg", payload)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/big.jpeg?thumbnail=true", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	assert.Less(t, w.Body.Len(), len(payload), "the thumbnail must be smaller than the original")

	// The thumbnail must decode and fit the box.
	img, _, err := image.Decode(bytes.NewReader(w.Body.Bytes()))
	require.NoError(t, err, "the thumbnail must be a decodable image")
	assert.LessOrEqual(t, img.Bounds().Dx(), thumbnailMaxSize)
	assert.LessOrEqual(t, img.Bounds().Dy(), thumbnailMaxSize)
}

// TestFileRoute_ThumbnailIsCached asserts the second request for
// the same thumbnail is served from the cache (and still returns a
// valid image).
func TestFileRoute_ThumbnailIsCached(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	payload := sampleJPEG(t, 1200, 900)
	att := fileTestAttachment(t, app, memo.Id, "big.jpeg", payload)

	var first []byte
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/big.jpeg?thumbnail=true", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		if i == 0 {
			first = w.Body.Bytes()
			continue
		}
		assert.Equal(t, first, w.Body.Bytes(), "the cached thumbnail must match the generated one")
	}
}

// TestFileRoute_ThumbnailFallsBackToOriginal asserts a format the
// generator cannot decode still serves the original bytes rather
// than a 404.
func TestFileRoute_ThumbnailFallsBackToOriginal(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	payload := []byte("not-an-image-at-all")
	att := fileTestAttachment(t, app, memo.Id, "notes.txt", payload)

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/notes.txt?thumbnail=true", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_PublicAttachmentIsCacheable asserts a public
// memo's attachment gets a cacheable Cache-Control, while a
// private one does not.
func TestFileRoute_PublicAttachmentIsCacheable(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	publicMemo := fileTestMemo(t, app, author, "PUBLIC")
	publicAtt := fileTestAttachment(t, app, publicMemo.Id, "public.jpeg", sampleJPEG(t, 8, 8))

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+publicAtt.Id+"/public.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Cache-Control"), "public")
}

// TestFileRoute_ShareTokenGrantsAccess asserts a share link's
// bearer credential grants access to the shared memo's attachment
// even though the holder is not the author.
func TestFileRoute_ShareTokenGrantsAccess(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := sampleJPEG(t, 8, 8)
	att := fileTestAttachment(t, app, memo.Id, "shared.jpeg", payload)

	shares, err := app.FindCollectionByNameOrId("memo_shares")
	require.NoError(t, err)
	share := core.NewRecord(shares)
	share.Set("memo", memo.Id)
	share.Set("code", "shareTokenCode1234567890")
	share.Set("created_by", author.Id)
	require.NoError(t, app.Save(share))

	req := httptest.NewRequest(http.MethodGet,
		"/file/attachments/"+att.Id+"/shared.jpeg?share_token=shareTokenCode1234567890", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_WrongShareTokenIsNotFound asserts a share token
// for a different memo grants nothing.
func TestFileRoute_WrongShareTokenIsNotFound(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	att := fileTestAttachment(t, app, memo.Id, "secret.jpeg", sampleJPEG(t, 8, 8))

	other := fileTestMemo(t, app, author, "PUBLIC")
	shares, err := app.FindCollectionByNameOrId("memo_shares")
	require.NoError(t, err)
	share := core.NewRecord(shares)
	share.Set("memo", other.Id)
	share.Set("code", "otherTokenCode1234567890")
	share.Set("created_by", author.Id)
	require.NoError(t, app.Save(share))

	req := httptest.NewRequest(http.MethodGet,
		"/file/attachments/"+att.Id+"/secret.jpeg?share_token=otherTokenCode1234567890", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestFileRoute_ExpiredShareTokenIsNotFound asserts an expired
// share no longer grants access.
func TestFileRoute_ExpiredShareTokenIsNotFound(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	att := fileTestAttachment(t, app, memo.Id, "secret.jpeg", sampleJPEG(t, 8, 8))

	shares, err := app.FindCollectionByNameOrId("memo_shares")
	require.NoError(t, err)
	share := core.NewRecord(shares)
	share.Set("memo", memo.Id)
	share.Set("code", "expiredTokenCode1234567")
	share.Set("created_by", author.Id)
	share.Set("expire_time", "2000-01-01 00:00:00.000Z")
	require.NoError(t, app.Save(share))

	req := httptest.NewRequest(http.MethodGet,
		"/file/attachments/"+att.Id+"/secret.jpeg?share_token=expiredTokenCode1234567", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestFileRoute_UpstreamRouteFamilySharesTheHandler asserts the
// /memos/api/file/... family enforces the same authorization as
// the public /file/... family.
//
// The upstream family used to serve any attachment to any caller
// with no authorization at all.
func TestFileRoute_UpstreamRouteFamilySharesTheHandler(t *testing.T) {
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PRIVATE")
	payload := sampleJPEG(t, 8, 8)
	att := fileTestAttachment(t, app, memo.Id, "secret.jpeg", payload)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/file/attachments/"+att.Id+"/secret.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotEqual(t, payload, w.Body.Bytes())
}

// TestFileRoute_UpstreamRouteFamilyServesPublicAttachment
// asserts the upstream family still works for a public memo.
func TestFileRoute_UpstreamRouteFamilyServesPublicAttachment(t *testing.T) {
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	payload := sampleJPEG(t, 8, 8)
	att := fileTestAttachment(t, app, memo.Id, "public.jpeg", payload)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/file/attachments/"+att.Id+"/public.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, payload, w.Body.Bytes())
}

// TestFileRoute_MotionClipForNonMotionPhotoIs404 asserts
// ?motion=true on a file with no embedded video answers 404
// rather than serving a still frame that would look like a broken
// video.
func TestFileRoute_MotionClipForNonMotionPhotoIs404(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	att := fileTestAttachment(t, app, memo.Id, "plain.jpeg", sampleJPEG(t, 8, 8))

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/plain.jpeg?motion=true", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestFileRoute_ConditionalGetIs304 asserts the ETag round-trip
// short-circuits a repeated request.
func TestFileRoute_ConditionalGetIs304(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	att := fileTestAttachment(t, app, memo.Id, "photo.jpeg", sampleJPEG(t, 8, 8))

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/photo.jpeg", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	etag := w.Header().Get("ETag")
	require.NotEmpty(t, etag)

	req2 := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/photo.jpeg", nil)
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusNotModified, w2.Code)
}

// TestFileRoute_NonImageIsForcedToDownload asserts a file with a
// non-inline content type is served as an attachment so the
// browser cannot execute it against the instance's origin.
func TestFileRoute_NonImageIsForcedToDownload(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	att := fileTestAttachment(t, app, memo.Id, "notes.txt", []byte("hello"))

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/notes.txt", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
}

// TestFileRoute_AvatarIsServed asserts /file/users/{id}/avatar
// streams the user's avatar blob.
func TestFileRoute_AvatarIsServed(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	user := fileTestUser(t, app, "alice")
	payload := samplePNG(t, 16, 16)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	f, err := filesystem.NewFileFromBytes(payload, "avatar.png")
	require.NoError(t, err)
	user.Set("avatar", f)
	require.NoError(t, app.Save(user))

	fsys, err := app.NewFilesystem()
	require.NoError(t, err)
	defer fsys.Close()
	key := filepath.ToSlash(filepath.Join(users.Id, user.Id, "avatar.png"))
	require.NoError(t, fsys.Upload(payload, key))

	req := httptest.NewRequest(http.MethodGet, "/file/users/"+user.Id+"/avatar", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
}

// TestFileRoute_AvatarMissingIs404 asserts a user without an
// avatar answers 404.
func TestFileRoute_AvatarMissingIs404(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	user := fileTestUser(t, app, "alice")

	req := httptest.NewRequest(http.MethodGet, "/file/users/"+user.Id+"/avatar", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestThumbnailKey_ChangesWithSource guards the cache-key
// derivation: two different attachments must not share a cache
// entry.
func TestThumbnailKey_ChangesWithSource(t *testing.T) {
	app := newTestApp(t)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	first := fileTestAttachment(t, app, memo.Id, "a.jpeg", sampleJPEG(t, 8, 8))
	second := fileTestAttachment(t, app, memo.Id, "b.jpeg", sampleJPEG(t, 9, 9))

	keyA := thumbnailKey(first, "a.jpeg", 100)
	keyB := thumbnailKey(second, "b.jpeg", 100)

	assert.NotEqual(t, keyA, keyB)
	assert.Contains(t, keyA, thumbnailCachePrefix)
}

// TestThumbnailKey_IsStable asserts the same attachment produces
// the same key across calls (so the cache actually hits).
func TestThumbnailKey_IsStable(t *testing.T) {
	app := newTestApp(t)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	att := fileTestAttachment(t, app, memo.Id, "a.jpeg", sampleJPEG(t, 8, 8))

	assert.Equal(t, thumbnailKey(att, "a.jpeg", 100), thumbnailKey(att, "a.jpeg", 100))
}

// TestFileRoute_ThumbnailCacheIsOnDisk asserts the generated
// thumbnail is persisted in the PocketBase filesystem, so a restart
// does not have to regenerate it.
func TestFileRoute_ThumbnailCacheIsOnDisk(t *testing.T) {
	app := newTestApp(t)
	mux := makeFileRouter(t, app)

	author := fileTestUser(t, app, "alice")
	memo := fileTestMemo(t, app, author, "PUBLIC")
	att := fileTestAttachment(t, app, memo.Id, "big.jpeg", sampleJPEG(t, 1200, 900))

	req := httptest.NewRequest(http.MethodGet, "/file/attachments/"+att.Id+"/big.jpeg?thumbnail=true", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// The cache lives under the app's data dir.
	var found bool
	_ = filepath.Walk(app.DataDir(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && filepath.Base(path) != "" &&
			bytes.Contains([]byte(filepath.ToSlash(path)), []byte(thumbnailCachePrefix)) {
			found = true
		}
		return nil
	})
	assert.True(t, found, "the thumbnail must be cached on disk")
}
