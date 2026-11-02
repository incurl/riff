package leafwiki

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// avatarTestPNG is a 1x1 PNG: a valid image the users collection's avatar
// FileField accepts (its MIME allow-list is the image types).
var avatarTestPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x60, 0x60, 0x60, 0x60,
	0x00, 0x00, 0x00, 0x05, 0x00, 0x01, 0x5D, 0x7A,
	0x1A, 0x0F, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

// setAvatar stores an avatar on a user record, the way an upload would.
func setAvatar(t *testing.T, app core.App, record *core.Record) {
	t.Helper()
	file, err := filesystem.NewFileFromBytes(avatarTestPNG, "avatar.png")
	require.NoError(t, err)
	record.Set(avatarFieldName, file)
	require.NoError(t, app.Save(record))
}

func TestAPI_AvatarServesThePocketBaseUserAvatar(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "alice")
	setAvatar(t, app, alice)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/wiki/avatars/"+alice.Id, nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "image/png", recorder.Header().Get("Content-Type"))
	assert.Equal(t, avatarTestPNG, recorder.Body.Bytes(), "the bytes must be the PocketBase record's file")
}

// TestAPI_AvatarIsPublicAndAbsentByDefault covers the two states the SPA relies
// on: an avatar is readable without a session (the URL is built for <img src>),
// and a user with no avatar answers 404 so AvatarFallback renders the initials.
func TestAPI_AvatarIsPublicAndAbsentByDefault(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "alice")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/wiki/avatars/"+alice.Id, nil))
	assert.Equal(t, http.StatusNotFound, recorder.Code, "a user without an avatar must 404, not fall through to the SPA")

	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/wiki/avatars/doesnotexist", nil))
	assert.Equal(t, http.StatusNotFound, unknown.Code)
}

// TestAPI_AvatarVersionedURLIsCacheable pins the cache contract the SPA's
// stores/avatar.ts depends on: ?v= is bumped after every upload/delete, so a
// versioned URL may be cached hard while an unversioned one may not.
func TestAPI_AvatarVersionedURLIsCacheable(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "alice")
	setAvatar(t, app, alice)

	versioned := httptest.NewRecorder()
	handler.ServeHTTP(versioned, httptest.NewRequest(http.MethodGet, "/wiki/avatars/"+alice.Id+"?v=1699999999", nil))
	require.Equal(t, http.StatusOK, versioned.Code)
	assert.Contains(t, versioned.Header().Get("Cache-Control"), "immutable")

	plain := httptest.NewRecorder()
	handler.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/wiki/avatars/"+alice.Id, nil))
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Equal(t, "no-cache", plain.Header().Get("Cache-Control"))
}

// TestAPI_AvatarUploadWritesThePocketBaseRecord is the core of the feature: the
// wiki's settings panel writes through to the account's PocketBase avatar, so
// the Memos SPA and the admin UI see the same picture.
func TestAPI_AvatarUploadWritesThePocketBaseRecord(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "alice")
	session, csrf := signIn(t, handler, "alice@example.com")

	body, contentType := avatarMultipart(t, "file", "me.png", avatarTestPNG)
	request := httptest.NewRequest(http.MethodPost, "/wiki/api/user/avatar", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(csrfHeaderName, csrf.Value)
	request.AddCookie(session)
	request.AddCookie(csrf)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	// The upload landed on the users record, not in a wiki-side store.
	reloaded, err := app.FindRecordById("users", alice.Id)
	require.NoError(t, err)
	stored := reloaded.GetString(avatarFieldName)
	require.NotEmpty(t, stored, "the upload must be written to users.avatar")

	// And the public avatar route now serves it.
	served := httptest.NewRecorder()
	handler.ServeHTTP(served, httptest.NewRequest(http.MethodGet, "/wiki/avatars/"+alice.Id, nil))
	require.Equal(t, http.StatusOK, served.Code)
	assert.Equal(t, avatarTestPNG, served.Body.Bytes())
}

func TestAPI_AvatarDeleteClearsThePocketBaseRecord(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "alice")
	setAvatar(t, app, alice)
	session, csrf := signIn(t, handler, "alice@example.com")

	request := httptest.NewRequest(http.MethodDelete, "/wiki/api/user/avatar", nil)
	request.Header.Set(csrfHeaderName, csrf.Value)
	request.AddCookie(session)
	request.AddCookie(csrf)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	reloaded, err := app.FindRecordById("users", alice.Id)
	require.NoError(t, err)
	assert.Empty(t, reloaded.GetString(avatarFieldName), "delete must clear users.avatar")

	served := httptest.NewRecorder()
	handler.ServeHTTP(served, httptest.NewRequest(http.MethodGet, "/wiki/avatars/"+alice.Id, nil))
	assert.Equal(t, http.StatusNotFound, served.Code, "the avatar route must 404 once the file is gone")
}

// TestAPI_AvatarMutationsNeedASession pins that an avatar is an account
// property: the double-submit CSRF token is required, and an anonymous caller
// cannot set or clear one even when RIFF_WIKI_PUBLIC_WRITE opens page writes.
func TestAPI_AvatarMutationsNeedASession(t *testing.T) {
	_, app, handler := newTestAPI(t)
	testUser(t, app, "alice")

	body, contentType := avatarMultipart(t, "file", "me.png", avatarTestPNG)
	anonymous := httptest.NewRequest(http.MethodPost, "/wiki/api/user/avatar", body)
	anonymous.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, anonymous)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)

	deleteRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deleteRecorder, httptest.NewRequest(http.MethodDelete, "/wiki/api/user/avatar", nil))
	assert.Equal(t, http.StatusUnauthorized, deleteRecorder.Code)

	// A session without the CSRF header is refused: the cookie alone is not
	// enough to mutate an account.
	session, _ := signIn(t, handler, "alice@example.com")
	body, contentType = avatarMultipart(t, "file", "me.png", avatarTestPNG)
	withoutCSRF := httptest.NewRequest(http.MethodPost, "/wiki/api/user/avatar", body)
	withoutCSRF.Header.Set("Content-Type", contentType)
	withoutCSRF.AddCookie(session)
	csrfRecorder := httptest.NewRecorder()
	handler.ServeHTTP(csrfRecorder, withoutCSRF)
	assert.Equal(t, http.StatusForbidden, csrfRecorder.Code)
}

// TestAPI_AvatarUploadRejectsANonImage pins that validation is PocketBase's:
// the FileField's MIME allow-list is the single source of truth, so the wiki
// does not carry a second, drifting copy of it.
func TestAPI_AvatarUploadRejectsANonImage(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "alice")
	session, csrf := signIn(t, handler, "alice@example.com")

	body, contentType := avatarMultipart(t, "file", "notes.txt", []byte("not an image at all"))
	request := httptest.NewRequest(http.MethodPost, "/wiki/api/user/avatar", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(csrfHeaderName, csrf.Value)
	request.AddCookie(session)
	request.AddCookie(csrf)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusBadRequest, recorder.Code, "body: %s", recorder.Body.String())

	reloaded, err := app.FindRecordById("users", alice.Id)
	require.NoError(t, err)
	assert.Empty(t, reloaded.GetString(avatarFieldName), "a rejected upload must not touch the record")
}

// avatarMultipart builds a one-file multipart body.
func avatarMultipart(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return &body, writer.FormDataContentType()
}

// signIn logs a test user in and returns the session cookie and the CSRF
// cookie. The wiki's double-submit check wants the token in *both* places —
// the X-CSRF-Token header and the leafwiki_csrf cookie — which is exactly what
// the SPA does (it echoes the cookie back as the header).
func signIn(t *testing.T, handler http.Handler, identifier string) (*http.Cookie, *http.Cookie) {
	t.Helper()

	login := doJSON(t, handler, http.MethodPost, "/wiki/api/auth/login", map[string]any{
		"identifier": identifier,
		"password":   "passw0rd!",
	}, nil)
	require.Equal(t, http.StatusOK, login.Code, "body: %s", login.Body.String())

	var session, csrf *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		switch cookie.Name {
		case accessCookieName:
			session = cookie
		case csrfCookieName:
			csrf = cookie
		default:
			// Any other cookie is outside this contract.
		}
	}
	require.NotNil(t, session, "login must set the session cookie")
	require.NotNil(t, csrf, "login must set the CSRF cookie")
	return session, csrf
}
