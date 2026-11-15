package flarum

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/require"

	// The blank import registers PocketBase's own init migration, which
	// creates the base tables (including the `users` auth collection).
	_ "github.com/pocketbase/pocketbase/migrations"

	"github.com/incurl/riff/internal/testutil"
	"github.com/incurl/riff/pb/flarum/repository"
)

// newTestApp boots a PocketBase app for a forum test.
//
// The log persistence is disabled right after the bootstrap: the
// logger's flush goroutine would otherwise write into the temp
// directory while the test framework removes it, which shows up as an
// intermittent "directory not empty" failure in an unrelated test.
func newTestApp(t *testing.T) core.App {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_forum_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	testutil.DisableLogPersistence(app)
	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")

	return app
}

// newTestRouter builds a router over an app.
//
// It mirrors what the module does at OnServe time, so a test drives the
// real routes.
func newTestRouter(t *testing.T, app core.App) *router.Router[*core.RequestEvent] {
	t.Helper()

	r := router.NewRouter[*core.RequestEvent](func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: app, Response: w, Request: req}, nil
	})
	return r
}

// testUser creates a `users` record and returns it.
//
// The record lands in the SHARED users collection, which is what the
// forum reads as its account source.
func testUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	record := core.NewRecord(users)
	// The email is unique, so a second user with the same name needs a
	// distinct address.
	record.Set("email", username+"-"+randomSuffix(t)+"@example.com")
	record.Set("username", username)
	record.Set("password", "password12345")
	require.NoError(t, app.Save(record))

	return record
}

// randomSuffix returns a short random string.
//
// The tests create several accounts with the same name, and the shared
// users collection requires distinct email addresses.
func randomSuffix(t *testing.T) string {
	t.Helper()

	buf := make([]byte, 6)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}

// seedDiscussion creates a discussion with its first post.
//
// It writes through the forum's own repository, so the counters and
// the first/last post pointers are set the way a real creation sets
// them.
func seedDiscussion(t *testing.T, app core.App, title, content string) (string, string) {
	t.Helper()

	repo := repository.New(app, repository.NewRegistry(repository.DefaultPrefix))
	user := testUser(t, app, "author")

	discussionID, postID, err := repo.CreateDiscussionWithPost(user.Id, title, "slug-"+title, content, content)
	require.NoError(t, err, "the discussion should be created")

	return discussionID, postID
}

// attachTag attaches a tag to a discussion.
//
// The link table is written directly: the test needs the link, not the
// route that creates it.
func attachTag(t *testing.T, app core.App, discussionID, tagID string) {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_discussion_tags")
	require.NoError(t, err)

	record := core.NewRecord(collection)
	record.Set("discussion", discussionID)
	record.Set("tag", tagID)
	require.NoError(t, app.Save(record))
}

// sessionCookies returns the cookies a signed-in browser carries.
//
// The CSRF token is in a readable cookie AND has to be echoed in the
// X-CSRF-Token header: that is the double-submit contract the
// frontend's Session class implements.
func sessionCookies(token, csrf string) []*http.Cookie {
	return []*http.Cookie{
		{Name: "forum_access_token", Value: token},
		{Name: "forum_csrf", Value: csrf},
	}
}
