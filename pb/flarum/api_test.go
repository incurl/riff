package flarum

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/repository"
)

// slogDiscard returns a logger that drops every record.
//
// The forum logs a lot at INFO; a test does not need the noise.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestForum boots a forum module over a fresh PocketBase app and
// returns the app and the request handler.
//
// The module is built the way the host builds it: the migrations run,
// the plugins load, and the routes are mounted.
func newTestForum(t *testing.T) (core.App, http.Handler) {
	t.Helper()

	app, handler, _ := newTestForumModule(t)
	return app, handler
}

// newTestForumModule boots a forum module and returns the app, the
// request handler and the module itself.
//
// The module is returned so a test can ask about the front-end build,
// which is optional.
func newTestForumModule(t *testing.T) (core.App, http.Handler, *Module) {
	t.Helper()

	app := newTestApp(t)

	module, err := newModule(app, Options{
		BasePath: DefaultBasePath,
		Logger:   slogDiscard(),
	})
	require.NoError(t, err, "the module should build")
	require.NoError(t, module.boot(context.Background()), "the plugins should load")

	r := newTestRouter(t, app)
	module.mount(r)
	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")

	return app, mux, module
}

// do performs a request against the forum and returns the response.
func do(t *testing.T, handler http.Handler, method, path string, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/vnd.api+json")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
		// The double-submit CSRF contract: the header must carry the
		// same value as the readable cookie.
		if cookie.Name == "forum_csrf" {
			request.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeDocument decodes a JSON:API document from a response.
func decodeDocument(t *testing.T, recorder *httptest.ResponseRecorder) api.Document {
	t.Helper()

	var document api.Document
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document),
		"the body should be a JSON:API document: %s", recorder.Body.String())
	return document
}

// TestForum_ForumDocumentIsServed pins the boot document: the frontend
// reads the API URL and the base path out of it, so both must be right.
func TestForum_ForumDocumentIsServed(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, api.ContentType, recorder.Header().Get("Content-Type"))

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	require.Equal(t, "forums", resource["type"])
	require.Equal(t, "1", resource["id"])

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "/forum/api", attributes["apiUrl"])
	require.Equal(t, "/forum", attributes["basePath"])
}

// TestForum_DiscussionsAreListed pins the discussion listing's shape.
func TestForum_DiscussionsAreListed(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Empty(t, resources)

	require.NotNil(t, document.Meta)
	require.NotNil(t, document.Meta.Page)
	require.Equal(t, 0, document.Meta.Page.Total)
}

// TestForum_UnknownResourceIsAJSONAPIError pins the error envelope.
//
// The frontend's RequestError reads the body, so the shape matters: a
// bare 404 with no body would break the error display.
func TestForum_UnknownResourceIsAJSONAPIError(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/nope", "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Equal(t, api.ContentType, recorder.Header().Get("Content-Type"))

	var document api.ErrorDocument
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
	require.Len(t, document.Errors, 1)
	require.Equal(t, "404", document.Errors[0].Status)
	require.Equal(t, api.CodeNotFound, document.Errors[0].Code)
	require.NotEmpty(t, document.Errors[0].Detail)
}

// TestForum_AnonymousCannotPost pins the auth gate on a mutation.
func TestForum_AnonymousCannotPost(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Hi","content":"Hello"}}}`, nil)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())

	var document api.ErrorDocument
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
	require.Equal(t, api.CodeNotAuthenticated, document.Errors[0].Code)
}

// TestForum_SpaShellIsServed pins the boot shell.
//
// The shell is what makes the reused frontend work at all: it carries
// the payload, the bundle scripts and the boot call.
//
// Without a front-end build the tracked fallback shell is served
// instead. Both states answer 200 with an HTML document; only the
// built one boots the SPA.
func TestForum_SpaShellIsServed(t *testing.T) {
	_, handler, module := newTestForumModule(t)

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/html")

	body := recorder.Body.String()
	if module.HasFrontendBuild() {
		require.Contains(t, body, "flarum-json-payload")
		require.Contains(t, body, "app.boot()")
		require.Contains(t, body, "/forum/assets/forum.js")
		return
	}

	// The fallback explains what to build instead of showing a blank
	// page.
	require.Contains(t, body, "not built")
	require.Contains(t, body, "npm run build")
}

// TestForum_AssetIsServed pins that the built bundle is reachable under
// the forum's asset prefix.
//
// It only applies when a build was staged: a fresh clone has no
// bundles, and the asset route answers 404 for them.
func TestForum_AssetIsServed(t *testing.T) {
	_, handler, module := newTestForumModule(t)

	recorder := do(t, handler, http.MethodGet, "/forum/assets/forum.js", "", nil)
	if !module.HasFrontendBuild() {
		require.Equal(t, http.StatusNotFound, recorder.Code)
		return
	}

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "flarum")
}

// TestForum_ExtensionManifestIsServed pins the plugin bundle manifest
// the SPA boots from.
func TestForum_ExtensionManifestIsServed(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/extensions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var manifest struct {
		Extensions []map[string]any `json:"extensions"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &manifest))
	// Every first-party plugin that ships a frontend bundle is listed. The
	// language pack is not: it declares a locale, not an asset bundle.
	require.Len(t, manifest.Extensions, 8, "every bundled plugin must be listed")

	ids := []string{}
	for _, extension := range manifest.Extensions {
		ids = append(ids, extension["id"].(string))
	}

	for _, want := range []string{"riff/tags", "riff/likes", "riff/uploads", "flarum/lock", "flarum/sticky", "flarum/suspend", "flarum/markdown", "fof/gamification"} {
		require.Contains(t, ids, want)
	}
}

// TestForum_PluginCollectionsAreCreated pins that the plugins' own
// migrations ran.
func TestForum_PluginCollectionsAreCreated(t *testing.T) {
	app, _ := newTestForum(t)

	for _, name := range []string{"forum_tags", "forum_likes", "forum_files"} {
		_, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "the %q collection should exist", name)
	}
}

// TestForum_SharedUsersIsNotRecreated pins the shared-users boundary:
// the forum never creates its own account store.
func TestForum_SharedUsersIsNotRecreated(t *testing.T) {
	app, _ := newTestForum(t)

	_, err := app.FindCollectionByNameOrId("forum_users")
	require.Error(t, err, "the forum must not create its own account store")

	registry := repository.NewRegistry(repository.DefaultPrefix)
	require.NotContains(t, registry.PhysicalNames(), "forum_users")
}
