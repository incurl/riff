package leafwiki

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uploadAsset posts a multipart upload for a page.
func uploadAsset(t *testing.T, handler http.Handler, pageID, filename string, content []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/wiki/api/pages/"+pageID+"/assets", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAPI_AssetUploadListAndServe(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "alice")

	page, err := server.Tree().CreatePage(Actor{ID: user.Id}, "root", "Media", "", NodeKindPage)
	require.NoError(t, err)

	uploaded := uploadAsset(t, handler, page.ID, "Screenshot One.png", []byte("png-bytes"), bearer(t, user))
	require.Equal(t, http.StatusCreated, uploaded.Code, "body: %s", uploaded.Body.String())

	stored := decodeJSON[assetWriteResponse](t, uploaded)
	assert.Equal(t, "/assets/"+page.ID+"/screenshot-one.png", stored.File)

	listed := doJSON(t, handler, http.MethodGet, "/wiki/api/pages/"+page.ID+"/assets", nil, nil)
	require.Equal(t, http.StatusOK, listed.Code)
	assert.Equal(t, []string{stored.File}, decodeJSON[assetListResponse](t, listed).Files)

	served := httptest.NewRecorder()
	handler.ServeHTTP(served, httptest.NewRequest(http.MethodGet, "/wiki/assets/"+page.ID+"/screenshot-one.png", nil))
	require.Equal(t, http.StatusOK, served.Code)
	assert.Equal(t, "png-bytes", served.Body.String())

	// A path traversal attempt is refused, not resolved.
	escape := httptest.NewRecorder()
	handler.ServeHTTP(escape, httptest.NewRequest(http.MethodGet, "/wiki/assets/"+page.ID+"/..%2f..%2fsecret", nil))
	assert.NotEqual(t, http.StatusOK, escape.Code)

	renamed := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID+"/assets/rename", map[string]any{
		"old_filename": "screenshot-one.png",
		"new_filename": "renamed.png",
	}, bearer(t, user))
	require.Equal(t, http.StatusOK, renamed.Code, "body: %s", renamed.Body.String())
	assert.Equal(t, "/assets/"+page.ID+"/renamed.png", decodeJSON[assetWriteResponse](t, renamed).File)

	deleted := doJSON(t, handler, http.MethodDelete, "/wiki/api/pages/"+page.ID+"/assets/renamed.png", nil, bearer(t, user))
	require.Equal(t, http.StatusOK, deleted.Code)
}

func TestAPI_ServesTheSPAWithPrefixSubstitution(t *testing.T) {
	server, _, handler := newTestAPI(t)

	for _, target := range []string{"/wiki/", "/wiki/some/deep/link"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, recorder.Code, "target %s", target)

		body, err := io.ReadAll(recorder.Result().Body)
		require.NoError(t, err)
		html := string(body)
		assert.Contains(t, strings.ToLower(html), "<!doctype html>")
		assert.NotContains(t, html, tokenBasePath, "the mount prefix must be substituted")
		assert.Contains(t, html, `content="/wiki"`, "the SPA must see the mount prefix")

		// The served document must be the LeafWiki bundle, not riff's SPA
		// catch-all. The bundle's entry chunk is referenced through the
		// rewritten, absolute "/wiki/static/…" path; the fallback reader
		// (used when no build was copied in) has a "#tree" element instead.
		//
		// The embed is baked at COMPILE time, so the assertion follows the
		// embedded tree rather than the working directory: a checkout
		// without a built bundle serves the fallback reader, and one with
		// it serves the real SPA.
		if server.ui.dist != nil {
			assert.Contains(t, html, `src="/wiki/static/`,
				"the wiki bundle must be served with absolute asset paths")
		} else {
			assert.Contains(t, html, `id="tree"`, "the fallback reader must be served when no bundle is embedded")
		}
	}

	// The bare prefix redirects to the trailing-slash form.
	bare := httptest.NewRecorder()
	handler.ServeHTTP(bare, httptest.NewRequest(http.MethodGet, "/wiki", nil))
	assert.Equal(t, http.StatusMovedPermanently, bare.Code)
	assert.Equal(t, "/wiki/", bare.Header().Get("Location"))
}

func TestAPI_ConfigAndHealth(t *testing.T) {
	server, _, handler := newTestAPI(t)

	config := decodeJSON[configResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/config", nil, nil))
	assert.True(t, config.PublicAccess)
	assert.False(t, config.AuthDisabled)
	assert.True(t, config.EnableLinkRefactor)
	// LoginURL/LogoutURL are upstream's OPTIONAL EXTERNAL-URL escape hatches,
	// not the wiki's own routes. Advertising this wiki's own /login page here
	// makes the SPA render `<ExternalRedirect to="/wiki/login">` on /login —
	// an endless redirect loop. Empty means "use the built-in login form",
	// which posts to /wiki/api/auth/login (implemented in api_auth.go).
	assert.Empty(t, config.LoginURL)
	assert.Empty(t, config.LogoutURL)

	health := decodeJSON[healthResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/health", nil, nil))
	assert.Equal(t, "ok", health.Status)
	assert.Equal(t, "test", health.Version)
	assert.True(t, health.IndexAvailable)
	assert.Equal(t, server.Tree().StorageDir(), health.StorageDir)
}

// TestAPI_ConfigLoginURLIsNotTheLoginPage pins the /wiki/login redirect loop.
//
// The SPA renders its /login route as `loginUrl ? <ExternalRedirect
// to={loginUrl} /> : <LoginForm />`, so any value that points back at the
// wiki's own /login page makes that page redirect to itself forever. The
// field must stay empty (or name a genuinely external IdP URL) exactly as it
// does in upstream LeafWiki, where it defaults to "".
func TestAPI_ConfigLoginURLIsNotTheLoginPage(t *testing.T) {
	_, _, handler := newTestAPI(t)

	config := decodeJSON[configResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/config", nil, nil))

	for name, value := range map[string]string{"loginUrl": config.LoginURL, "logoutUrl": config.LogoutURL} {
		if value == "" {
			continue
		}
		// A non-empty value is only legitimate when it leaves this wiki
		// entirely; a path inside it would bounce the SPA back to itself.
		assert.NotContains(t, value, "/wiki", "%s must not point back at the wiki's own routes", name)
		assert.NotContains(t, value, "/login", "%s must not point at the wiki's own login page", name)
	}
}
