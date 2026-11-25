package flarum

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestForum_TheAppearanceUploadsAreRouted pins that every asset the
// Appearance page uploads has a route.
//
// The page's `UploadImageButton`s build their URL from a `routePath`
// (`logo`, `logo-dark-mode`, `email-logo`, `favicon`, `app-icon`), so a
// missing route is a 404 behind the button — and the settings they
// write (`logo_path` and friends) are what the forum document reads
// back into `logoUrl` and the shell's favicon. Without the routes the
// settings exist and nothing can ever set them.
func TestForum_TheAppearanceUploadsAreRouted(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	for _, asset := range []string{"logo", "logo-dark-mode", "email-logo", "favicon", "app-icon"} {
		recorder := do(t, handler, http.MethodPost, "/forum/api/"+asset, "", sessionCookies(admin[0].Value, admin[1].Value))
		require.NotEqual(t, http.StatusNotFound, recorder.Code,
			"the %s upload must be routed, or the Appearance page's button answers 404", asset)
	}
}

// TestForum_TheAppearanceUploadStoresTheAsset pins the upload
// round-trip.
//
// The bytes are stored, the SETTING carries the path, and the forum
// document's `logoUrl` is the URL the shell renders — so an
// administrator's upload is what the forum shows.
func TestForum_TheAppearanceUploadStoresTheAsset(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	body, contentType := pngUpload(t)

	request := httptest.NewRequest(http.MethodPost, "/forum/api/logo", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	for _, cookie := range sessionCookies(admin[0].Value, admin[1].Value) {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The setting carries the path the forum document builds its URL
	// from.
	repo := testRepo(t, app)
	require.Equal(t, "/logo", settingText(t, repo, "logo_path"))

	// And the forum document reports the URL.
	document := do(t, handler, http.MethodGet, "/forum/api", "", nil)
	require.Equal(t, http.StatusOK, document.Code, document.Body.String())

	require.Contains(t, document.Body.String(), "/forum/branding/logo",
		"the forum document should carry the uploaded logo's URL: %s", document.Body.String())
}

// TestForum_TheUploadedAssetIsServed pins that the uploaded bytes are
// served back.
func TestForum_TheUploadedAssetIsServed(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	body, contentType := pngUpload(t)

	request := httptest.NewRequest(http.MethodPost, "/forum/api/logo", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	for _, cookie := range sessionCookies(admin[0].Value, admin[1].Value) {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The upload is public: the shell's <img> has no credential.
	served := do(t, handler, http.MethodGet, "/forum/branding/logo", "", nil)
	require.Equal(t, http.StatusOK, served.Code, served.Body.String())

	require.Equal(t, testPNG, served.Body.Bytes(), "the served bytes should be the uploaded ones")
}

// TestForum_TheAppearanceUploadIsAdministratorOnly pins that a member
// cannot replace the forum's branding.
func TestForum_TheAppearanceUploadIsAdministratorOnly(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "logo_member")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/logo", "", sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestForum_AnUnknownAssetIs404 pins that only the known assets have
// routes.
func TestForum_AnUnknownAssetIs404(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/not-an-asset", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())

	// And the serve route refuses it too, so the filesystem is never
	// probed for a name the module does not store.
	served := do(t, handler, http.MethodGet, "/forum/branding/not-an-asset", "", nil)
	require.Equal(t, http.StatusNotFound, served.Code, served.Body.String())
}

// testPNG is a 1x1 PNG, the bytes an upload carries.
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// pngUpload builds a multipart body carrying a small PNG.
//
// The field name is `upload`, which is what the frontend's
// `UploadImageButton` sends.
func pngUpload(t *testing.T) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("upload", "logo.png")
	require.NoError(t, err)
	_, err = part.Write(testPNG)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return buf.Bytes(), writer.FormDataContentType()
}
