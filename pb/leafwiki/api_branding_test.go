package leafwiki

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The branding surface, at upstream LeafWiki's paths.
//
// It exists because a SAVE THAT DOES NOTHING IS INDISTINGUISHABLE FROM ONE
// THAT WORKED, and the two failures the operator saw were the consequences:
// the site name never applied, and the SPA threw
// "Cannot read properties of null (reading 'faviconFile')" — its
// `updateBranding` reads that field off a response that carried no JSON.

// testSuperuser creates a PocketBase superuser, which is the wiki's `admin`.
func testSuperuser(t *testing.T, app core.App) *core.Record {
	t.Helper()

	superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	require.NoError(t, err)

	admin := core.NewRecord(superusers)
	admin.Set("email", "wiki-admin@example.com")
	admin.Set("password", "passw0rd!")
	require.NoError(t, app.Save(admin))

	return admin
}

// uploadFile posts a multipart body with one file part.
func uploadFile(t *testing.T, handler http.Handler, target, field, filename string, content []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, target, body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

// pngBytes is a minimal PNG header, enough for a content-type assertion.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// TestBranding_TheReadAnswersUpstreamsContract pins `GET /api/branding`.
//
// It is PUBLIC — the SPA loads it on every mount and a guest sees the name —
// and `brandingConstraints` is NESTED with real values, because the SPA
// validates an upload against them before sending it: an empty extension list
// is an uploader that refuses every file.
func TestBranding_TheReadAnswersUpstreamsContract(t *testing.T) {
	_, _, handler := newTestAPI(t)

	recorder := doJSON(t, handler, http.MethodGet, "/wiki/api/branding", nil, nil)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	branding := decodeJSON[brandingResponse](t, recorder)

	assert.Equal(t, defaultBrandingSiteName, branding.SiteName)
	assert.Empty(t, branding.LogoFile)
	assert.Empty(t, branding.FaviconFile)

	assert.NotEmpty(t, branding.BrandingConstraints.LogoExts, "the uploader validates the logo extension against this list")
	assert.NotEmpty(t, branding.BrandingConstraints.FaviconExts)
	assert.Positive(t, branding.BrandingConstraints.MaxLogoSize)
	assert.Positive(t, branding.BrandingConstraints.MaxFaviconSize)
	assert.Equal(t, maxSiteNameLength, branding.BrandingConstraints.MaxSiteNameLength)
}

// TestBranding_TheSiteNameAppliesEverywhere pins the whole round trip.
//
// The operator's complaint was that the name did not apply, so the test walks
// every reader of it: the response, the public read, the SPA shell the
// browser gets on a cold load, and the file on disk that a RESTART re-reads.
func TestBranding_TheSiteNameAppliesEverywhere(t *testing.T) {
	server, app, handler := newTestAPI(t)

	admin := testSuperuser(t, app)

	recorder := doJSON(t, handler, http.MethodPut, "/wiki/api/branding", map[string]any{
		"siteName": "  The Renamed Wiki  ",
	}, bearer(t, admin))
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	updated := decodeJSON[brandingResponse](t, recorder)
	assert.Equal(t, "The Renamed Wiki", updated.SiteName, "the name is trimmed, and the ANSWER carries it")

	read := doJSON(t, handler, http.MethodGet, "/wiki/api/branding", nil, nil)
	require.Equal(t, http.StatusOK, read.Code)
	assert.Equal(t, "The Renamed Wiki", decodeJSON[brandingResponse](t, read).SiteName)

	// The SPA shell is rendered BEFORE the app boots: the tab title and the
	// first frame come from it, so a name that only reached the SPA would
	// leave the tab showing the old one until React mounted.
	shell := doJSON(t, handler, http.MethodGet, "/wiki/", nil, nil)
	require.Equal(t, http.StatusOK, shell.Code, "body: %s", shell.Body.String())
	assert.Contains(t, shell.Body.String(), "The Renamed Wiki")

	// The file is the source a restart reads.
	payload, err := os.ReadFile(filepath.Join(server.Tree().StorageDir(), brandingFilename))
	require.NoError(t, err)
	assert.Contains(t, string(payload), "The Renamed Wiki")

	reloaded := newBrandingStore(server.Tree().StorageDir())
	assert.Equal(t, "The Renamed Wiki", reloaded.siteName(), "a restart must not lose the name")
}

// TestBranding_ANonAdminCannotRenameTheWiki pins the gate.
//
// Upstream's routes are `RequireAdmin`, and a wiki EDITOR writes pages — the
// name is the site's identity and it is shown to every reader.
func TestBranding_ANonAdminCannotRenameTheWiki(t *testing.T) {
	_, app, handler := newTestAPI(t)

	editor := testUser(t, app, "wikieditor")

	recorder := doJSON(t, handler, http.MethodPut, "/wiki/api/branding", map[string]any{
		"siteName": "Editor's Wiki",
	}, bearer(t, editor))
	assert.Equal(t, http.StatusForbidden, recorder.Code, "body: %s", recorder.Body.String())

	// An anonymous caller is not even authenticated.
	recorder = doJSON(t, handler, http.MethodPut, "/wiki/api/branding", map[string]any{
		"siteName": "Anonymous Wiki",
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, "body: %s", recorder.Body.String())
}

// TestBranding_TheSiteNameIsValidated pins upstream's rules.
//
// Each is a 400 with the server's own message: the SPA shows it against the
// field, so a silent 200 would be the operator's next surprise.
func TestBranding_TheSiteNameIsValidated(t *testing.T) {
	_, app, handler := newTestAPI(t)

	admin := testSuperuser(t, app)

	for name, body := range map[string]map[string]any{
		"empty":        {"siteName": "   "},
		"too long":     {"siteName": strings.Repeat("a", maxSiteNameLength+1)},
		"control char": {"siteName": "Wiki\u0007"},
	} {
		recorder := doJSON(t, handler, http.MethodPut, "/wiki/api/branding", body, bearer(t, admin))
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "%s: %s", name, recorder.Body.String())
	}

	// The stored name is untouched by a refused save.
	read := doJSON(t, handler, http.MethodGet, "/wiki/api/branding", nil, nil)
	require.Equal(t, http.StatusOK, read.Code)
	assert.Equal(t, defaultBrandingSiteName, decodeJSON[brandingResponse](t, read).SiteName)
}

// TestBranding_TheFaviconUploadsServesAndDeletes pins the image round trip.
//
// The SPA builds `<base>/branding/<file>` for the favicon and appends a
// version, and the shell advertises the same URL — so the upload has to be
// readable at that path, and deleting it has to put the default icon back.
func TestBranding_TheFaviconUploadsServesAndDeletes(t *testing.T) {
	_, app, handler := newTestAPI(t)

	admin := testSuperuser(t, app)

	uploaded := uploadFile(t, handler, "/wiki/api/branding/favicon", "file", "favicon.png", pngBytes, bearer(t, admin))
	require.Equal(t, http.StatusOK, uploaded.Code, "body: %s", uploaded.Body.String())

	var answer struct {
		Path     string           `json:"path"`
		Branding brandingResponse `json:"branding"`
	}
	require.NoError(t, json.Unmarshal(uploaded.Body.Bytes(), &answer))
	assert.Equal(t, "favicon.png", answer.Branding.FaviconFile, "the answer carries the whole configuration")
	assert.Equal(t, "/wiki/branding/favicon.png", answer.Path)

	image := doJSON(t, handler, http.MethodGet, "/wiki/branding/favicon.png", nil, nil)
	require.Equal(t, http.StatusOK, image.Code, "body: %s", image.Body.String())
	assert.Equal(t, "image/png", image.Header().Get("Content-Type"))
	assert.Equal(t, pngBytes, image.Body.Bytes())

	// The shell points the browser at the uploaded file, and the browser's
	// own `/favicon.ico` request answers with it.
	shell := doJSON(t, handler, http.MethodGet, "/wiki/", nil, nil)
	require.Equal(t, http.StatusOK, shell.Code)
	assert.Contains(t, shell.Body.String(), "/wiki/branding/favicon.png")

	ico := doJSON(t, handler, http.MethodGet, "/wiki/favicon.ico", nil, nil)
	require.Equal(t, http.StatusOK, ico.Code, "body: %s", ico.Body.String())
	assert.Equal(t, pngBytes, ico.Body.Bytes())

	// DELETE puts everything back.
	deleted := doJSON(t, handler, http.MethodDelete, "/wiki/api/branding/favicon", nil, bearer(t, admin))
	require.Equal(t, http.StatusOK, deleted.Code, "body: %s", deleted.Body.String())
	assert.Empty(t, decodeJSON[brandingResponse](t, deleted).FaviconFile)

	image = doJSON(t, handler, http.MethodGet, "/wiki/branding/favicon.png", nil, nil)
	assert.Equal(t, http.StatusNotFound, image.Code, "the file is gone")

	shell = doJSON(t, handler, http.MethodGet, "/wiki/", nil, nil)
	require.Equal(t, http.StatusOK, shell.Code)
	assert.Contains(t, shell.Body.String(), "/wiki/favicon.svg", "the default icon is back")

	ico = doJSON(t, handler, http.MethodGet, "/wiki/favicon.ico", nil, nil)
	assert.Equal(t, http.StatusNotFound, ico.Code)
}

// TestBranding_ABadImageIsRefused pins the server-side half of the uploader's
// validation: an extension the advertised list does not carry is a 400, and an
// editor may not upload at all.
func TestBranding_ABadImageIsRefused(t *testing.T) {
	_, app, handler := newTestAPI(t)

	admin := testSuperuser(t, app)

	// `.txt` is not on the favicon list the response advertises.
	recorder := uploadFile(t, handler, "/wiki/api/branding/favicon", "file", "notes.txt", []byte("hello"), bearer(t, admin))
	assert.Equal(t, http.StatusBadRequest, recorder.Code, "body: %s", recorder.Body.String())

	// A logo must be an image too.
	recorder = uploadFile(t, handler, "/wiki/api/branding/logo", "file", "logo.gif", []byte("GIF89a"), bearer(t, admin))
	assert.Equal(t, http.StatusBadRequest, recorder.Code, "a .gif is a favicon format, not a logo one")

	// An editor may not write the site's identity.
	editor := testUser(t, app, "wikiuploader")
	recorder = uploadFile(t, handler, "/wiki/api/branding/favicon", "file", "favicon.png", pngBytes, bearer(t, editor))
	assert.Equal(t, http.StatusForbidden, recorder.Code, "body: %s", recorder.Body.String())
}

// TestBranding_AnOversizedImageIsRefused pins the size cap.
//
// It is the SAME number `GET /api/branding` advertises, so the SPA refuses the
// file before the upload and the server refuses it again if the client does
// not — one limit, two enforcement points.
func TestBranding_AnOversizedImageIsRefused(t *testing.T) {
	_, app, handler := newTestAPI(t)

	admin := testSuperuser(t, app)

	oversized := bytes.Repeat([]byte("a"), maxBrandingImageBytes+1)

	recorder := uploadFile(t, handler, "/wiki/api/branding/favicon", "file", "favicon.png", oversized, bearer(t, admin))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, "body: %s", recorder.Body.String())

	// Nothing was stored: the configuration still reports no favicon.
	read := doJSON(t, handler, http.MethodGet, "/wiki/api/branding", nil, nil)
	require.Equal(t, http.StatusOK, read.Code)
	assert.Empty(t, decodeJSON[brandingResponse](t, read).FaviconFile)
}

// TestBranding_AnUnknownImageIsNotFound pins the asset route's allow-list.
//
// Only the names the configuration holds are served, so no path a caller
// invents — including a traversal — can reach the file system.
func TestBranding_AnUnknownImageIsNotFound(t *testing.T) {
	_, _, handler := newTestAPI(t)

	for _, name := range []string{"logo.png", "favicon.png", "..%2F..%2Fpb_data", "branding.json"} {
		recorder := doJSON(t, handler, http.MethodGet, "/wiki/branding/"+name, nil, nil)
		assert.Equal(t, http.StatusNotFound, recorder.Code, "%s must not be served: %s", name, recorder.Body.String())
	}
}
