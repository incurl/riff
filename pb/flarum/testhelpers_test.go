package flarum

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/require"

	// The blank import registers PocketBase's own init migration, which
	// creates the base tables (including the `users` auth collection).
	_ "github.com/pocketbase/pocketbase/migrations"

	"github.com/incurl/riff/internal/testutil"
	"github.com/incurl/riff/pb/flarum/repository"
	_ "github.com/incurl/riff/pb/migrations"
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

// The forum's test app needs the HOST's migrations too, not just the
// forum module's.
//
// The forum reads the shared `users` collection as its account source,
// and the host's migrations are what add the `username` field to it
// (pb/migrations/1700000001_extend_users.go). Without them the collection
// has no `username`, and a login by username fails with "invalid filter
// expression: unknown field \"username\"" — a failure that has nothing
// to do with the code under test and that hid the real one.
//
// The host migrations are imported for their side effect: each file
// calls migrations.Register in its init(), exactly as main.go does.

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
//
// The username is unique (the host's migration adds the unique index on
// `username`), so the email always carries a random suffix while the
// username keeps the requested name — a test that logs in as "writer"
// needs that name to be exactly "writer". A test that needs a second
// account with the same name uses testUserNamed.
func testUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()

	return testUserNamed(t, app, username, username)
}

// testUserNamed creates a `users` record with a distinct username.
//
// It is for the tests that need several accounts: the username is
// unique, so each one carries a distinct value. The display name is not
// stored — the forum derives it from the username — so it is accepted
// only to document the intent at the call site.
func testUserNamed(t *testing.T, app core.App, username, _ string) *core.Record {
	t.Helper()

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	record := core.NewRecord(users)
	// The email is unique, so a second user with the same name needs a
	// distinct address.
	suffix := randomSuffix(t)
	record.Set("email", username+"-"+suffix+"@example.com")
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
	// A distinct username per discussion: the username is unique, and a
	// test may seed several discussions. The pattern is
	// `^[a-z0-9_]{3,30}$`, so the suffix is lowercased.
	user := testUserNamed(t, app, "author_"+strings.ToLower(randomSuffix(t)[:8]), "author")

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
