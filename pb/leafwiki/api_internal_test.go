package leafwiki

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/testutil"

	// Registers riff's Go migrations, which include the leafwiki
	// collections this package's index writes to.
	_ "github.com/incurl/riff/pb/migrations"
)

// newTestAPI boots a migrated PocketBase app, builds the wiki server and
// returns it behind an http.Handler.
func newTestAPI(t *testing.T) (*Server, core.App, http.Handler) {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_leafwiki_api_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	testutil.DisableLogPersistence(app)
	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")

	server, err := newServer(app, Options{
		StorageDir: t.TempDir(),
		Prefix:     "/wiki",
		Version:    "test",
	})
	require.NoError(t, err)

	r := router.NewRouter[*core.RequestEvent](func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: app, Response: w, Request: req}, nil
	})
	server.mount(r)
	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")

	return server, app, mux
}

// testUser creates a `users` record and returns it.
func testUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	record := core.NewRecord(users)
	record.Set("username", username)
	record.Set("email", username+"@example.com")
	record.Set("password", "passw0rd!")
	require.NoError(t, app.Save(record))
	return record
}

// doJSON issues a JSON request.
func doJSON(t *testing.T, handler http.Handler, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}

	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeBody unmarshals a JSON response.
func decodeJSON[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var decoded T
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &decoded), "body: %s", recorder.Body.String())
	return decoded
}

// bearer returns the Authorization header for a record.
func bearer(t *testing.T, record *core.Record) map[string]string {
	t.Helper()
	token, err := record.NewAuthToken()
	require.NoError(t, err)
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestAPI_ReadsArePublicAndWritesNeedAnAccount(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "alice")

	treeResponse := doJSON(t, handler, http.MethodGet, "/wiki/api/tree", nil, nil)
	require.Equal(t, http.StatusOK, treeResponse.Code)
	assert.Empty(t, decodeJSON[PageNode](t, treeResponse).Children)

	unauthorised := doJSON(t, handler, http.MethodPost, "/wiki/api/pages", map[string]any{
		"title": "Nope",
		"slug":  "nope",
		"kind":  "page",
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, unauthorised.Code)

	created := doJSON(t, handler, http.MethodPost, "/wiki/api/pages", map[string]any{
		"title": "Getting Started",
		"slug":  "getting-started",
		"kind":  "page",
	}, bearer(t, user))
	require.Equal(t, http.StatusCreated, created.Code, "body: %s", created.Body.String())
	page := decodeJSON[Page](t, created)
	assert.Equal(t, "getting-started", page.Path)
	assert.Equal(t, user.Id, page.Metadata.CreatorID)
	require.NotNil(t, page.Metadata.LastAuthor)
	assert.Equal(t, "alice", page.Metadata.LastAuthor.Username)

	// The page is on disk, in LeafWiki's layout.
	onDisk, err := os.ReadFile(filepath.Join(server.Tree().StorageDir(), "root", "getting-started.md"))
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "leafwiki_id: "+page.ID)

	byPath := decodeJSON[Page](t, doJSON(t, handler, http.MethodGet, "/wiki/api/pages/by-path?path=getting-started", nil, nil))
	assert.Equal(t, page.ID, byPath.ID)

	content := "# Getting Started\n\nhello world\n"
	updated := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID, map[string]any{
		"version": page.Version,
		"title":   "Getting Started",
		"slug":    "getting-started",
		"content": content,
	}, bearer(t, user))
	require.Equal(t, http.StatusOK, updated.Code, "body: %s", updated.Body.String())
	assert.Equal(t, content, decodeJSON[Page](t, updated).Content)

	// A stale version is rejected, so two editors cannot silently overwrite
	// each other.
	stale := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID, map[string]any{
		"version": page.Version,
		"title":   "Getting Started",
		"slug":    "getting-started",
		"content": "overwritten",
	}, bearer(t, user))
	assert.Equal(t, http.StatusConflict, stale.Code)
}

func TestAPI_SearchTagsAndLinksComeFromTheIndex(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "alice")

	tree := server.Tree()
	target, err := tree.CreatePage(Actor{ID: user.Id}, "root", "Install", "", NodeKindPage)
	require.NoError(t, err)

	tags := []string{"Setup"}
	targetContent := "how to install\n"
	_, err = tree.UpdatePage(Actor{ID: user.Id}, target.ID, target.Version, "Install", "", &targetContent, &tags, nil)
	require.NoError(t, err)

	sourceContent := "see [the install guide](/install) and [[Install]]\n"
	_, err = tree.CreatePage(Actor{ID: user.Id}, "root", "Readme", "", NodeKindPage)
	require.NoError(t, err)
	source, err := tree.PageByPath("readme")
	require.NoError(t, err)
	_, err = tree.UpdatePage(Actor{ID: user.Id}, source.ID, source.Version, "Readme", "", &sourceContent, nil, nil)
	require.NoError(t, err)

	// --- search -------------------------------------------------------
	// Both pages mention "install"; the title match must rank first.
	result := decodeJSON[searchResult](t, doJSON(t, handler, http.MethodGet, "/wiki/api/search?q=install", nil, nil))
	require.Equal(t, 2, result.Count)
	require.Len(t, result.Items, 2)
	assert.Equal(t, "install", result.Items[0].Path)
	assert.Equal(t, 2, result.Items[0].Rank, "a title match outranks a body match")
	assert.NotEmpty(t, result.Items[0].Excerpt)

	// --- tag facets ---------------------------------------------------
	facets := decodeJSON[[]tagCount](t, doJSON(t, handler, http.MethodGet, "/wiki/api/tags", nil, nil))
	require.Len(t, facets, 1)
	assert.Equal(t, tagCount{Tag: "setup", Count: 1}, facets[0])

	byTag := decodeJSON[[]taggedPage](t, doJSON(t, handler, http.MethodGet, "/wiki/api/tags/pages?tags=setup", nil, nil))
	require.Len(t, byTag, 1)
	assert.Equal(t, "Install", byTag[0].Title)

	// --- backlinks ----------------------------------------------------
	status := decodeJSON[linkStatusResult](t, doJSON(t, handler, http.MethodGet, "/wiki/api/pages/"+target.ID+"/links", nil, nil))
	assert.Equal(t, 1, status.Counts.Backlinks, "the markdown link and the wikilink both resolve to Install")
	assert.Equal(t, 0, status.Counts.BrokenIncoming)

	broken := decodeJSON[map[string][]brokenLink](t, doJSON(t, handler, http.MethodGet, "/wiki/api/links/broken", nil, nil))
	assert.Empty(t, broken["links"])

	// --- index of an empty instance is still answerable ---------------
	status2 := decodeJSON[searchStatus](t, doJSON(t, handler, http.MethodGet, "/wiki/api/search/status", nil, nil))
	assert.True(t, status2.Active)
	assert.Equal(t, 2, status2.Indexed)
}
