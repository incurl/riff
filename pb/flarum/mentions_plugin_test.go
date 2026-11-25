package flarum

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/plugins/mentions"
	"github.com/incurl/riff/pb/flarum/repository"
)

// mentionsCollectionName is the plugin's collection.
const mentionsCollectionName = "forum_post_mentions_user"

// mentionRows returns the mention rows of a post.
func mentionRows(t *testing.T, app core.App, postID string) []*core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId(mentionsCollectionName)
	require.NoError(t, err, "the mentions collection should exist")

	rows, err := app.FindRecordsByFilter(collection, "post = {:post}", "", 0, 0,
		map[string]any{"post": postID})
	require.NoError(t, err)

	return rows
}

// notificationRows returns a user's notifications.
func notificationRows(t *testing.T, app core.App, userID string) []*core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_notifications")
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter(collection, "user = {:user}", "", 0, 0,
		map[string]any{"user": userID})
	require.NoError(t, err)

	return rows
}

// postBody builds the JSON:API body that creates a reply.
//
// It is the shape the create-post handler reads: the content is an
// attribute and the discussion is a relationship.
func postBody(t *testing.T, app core.App, authorID, content string) string {
	t.Helper()

	return `{"data":{"type":"posts","attributes":{"content":"` + content + `"},"relationships":{"discussion":{"data":{"type":"discussions","id":"` +
		seedDiscussionID(t, app, authorID) + `"}}}}}}`
}

// seedDiscussionID creates a discussion for an author and returns its id.
//
// It is the form a mention test needs: the test creates its OWN posts
// in a discussion it controls.
func seedDiscussionID(t *testing.T, app core.App, authorID string) string {
	t.Helper()

	repo := repository.New(app, repository.NewRegistry(repository.DefaultPrefix))
	discussionID, _, err := repo.CreateDiscussionWithPost(authorID, "Thread", "slug-"+randomSuffix(t)[:8], "first post", "first post")
	require.NoError(t, err, "the discussion should be created")

	return discussionID
}

// postWithContent returns the id of the author's post whose content
// matches.
//
// The helper creates the discussion's OWN first post, so the author has
// two posts and the reply is found by its CONTENT rather than by its
// number (which restarts in every discussion) or by its creation time
// (the two are written in the same millisecond).
func postWithContent(t *testing.T, app core.App, authorID, content string) string {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_posts")
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter(collection, "user = {:user}", "", 0, 0,
		map[string]any{"user": authorID})
	require.NoError(t, err)

	for _, row := range rows {
		if row.GetString("content") == content {
			return row.Id
		}
	}

	t.Fatalf("the author should have a post with the content %q", content)
	return ""
}

// TestMentions_TheCollectionExistsAndRefusesADuplicate pins Step 3.
//
// The unique index on (post, user) is what makes a mention idempotent:
// a post that mentions the same account twice mentions it once.
func TestMentions_TheCollectionExistsAndRefusesADuplicate(t *testing.T) {
	app, _ := newTestForum(t)

	collection, err := app.FindCollectionByNameOrId(mentionsCollectionName)
	require.NoError(t, err, "the mentions collection should exist")

	alice := testUser(t, app, "alice")
	_, postID := seedDiscussionWithAuthor(t, app, alice.Id, "Thread", "hello")

	first := core.NewRecord(collection)
	first.Set("post", postID)
	first.Set("user", alice.Id)
	require.NoError(t, app.Save(first))

	// The second row for the same pair must be refused.
	second := core.NewRecord(collection)
	second.Set("post", postID)
	second.Set("user", alice.Id)
	require.Error(t, app.Save(second), "the unique index should refuse a duplicate")
}

// TestMentions_APostStoresItsMentions pins Step 4.
//
// The mention is extracted from the post's own content, resolved to an
// account, and stored.
func TestMentions_APostStoresItsMentions(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @bob, look at this")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @bob, look at this")

	rows := mentionRows(t, app, postID)
	require.Len(t, rows, 1, "the mention should be stored")
	require.Equal(t, bob.Id, rows[0].GetString("user"), "the mention should name the mentioned account")
}

// TestMentions_TheUsernameFormatCanBeTurnedOff pins upstream's
// `flarum-mentions.allow_username_format`.
//
// Upstream's `ConfigureMentions::addUserId` reads the setting and,
// when it is off, refuses to resolve a `@username` to an account — the
// quoted `@"Display Name"#id` form still resolves. This port
// recognises the bare form only, so turning the setting off stops a
// bare mention from resolving at all.
//
// The default is ON: upstream's migration writes 1, and a mention that
// resolves to nobody notifies nobody, so the default has to be the
// permissive one.
func TestMentions_TheUsernameFormatCanBeTurnedOff(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// The setting is off.
	setForumSetting(t, app, mentions.SettingAllowUsernameFormat, "0")

	body := postBody(t, app, alice.Id, "hey @bob, look at this")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @bob, look at this")
	require.Empty(t, mentionRows(t, app, postID),
		"a bare mention must not resolve while the username format is off")
}

// TestMentions_TheUsernameFormatIsOnByDefault pins that the bare
// mention resolves without the operator touching the setting.
//
// The settings table starts EMPTY on a fresh instance, so the reader
// falls back to the declared default. Upstream's installed default is
// 1, and a mention that resolves to nobody notifies nobody — so a
// default of "off" would silently break every mention on a fresh
// instance.
func TestMentions_TheUsernameFormatIsOnByDefault(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @bob, look at this")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @bob, look at this")

	rows := mentionRows(t, app, postID)
	require.Len(t, rows, 1, "a bare mention should resolve by default")
	require.Equal(t, bob.Id, rows[0].GetString("user"), "the mention should name the mentioned account")
}

// TestMentions_AnUnknownUsernameIsInert pins Step 4.
//
// A mention of an account that does not exist is dropped: it does not
// fail the save, and it notifies nobody.
func TestMentions_AnUnknownUsernameIsInert(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @nobody at all")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @nobody at all")
	require.Empty(t, mentionRows(t, app, postID), "an unknown username should store nothing")
}

// TestMentions_TheMentionedAccountIsNotified pins Step 5.
//
// The mentioned account is notified, with the kind the SPA looks up.
func TestMentions_TheMentionedAccountIsNotified(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @bob")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	rows := notificationRows(t, app, bob.Id)
	require.Len(t, rows, 1, "the mentioned account should be notified")
	require.Equal(t, mentions.NotificationType, rows[0].GetString("type"),
		"the notification should carry the mention kind")
	require.Equal(t, alice.Id, rows[0].GetString("from_user"),
		"the notification should name the author")

	// The author is not notified about its own mention.
	require.Empty(t, notificationRows(t, app, alice.Id),
		"the author should not be notified about its own mention")
}

// TestMentions_ASelfMentionIsStoredButNotNotified pins Step 4 and 5.
//
// A mention of oneself is a real reference — it is stored — but it does
// not notify: upstream builds the recipient list from the post's
// mentions MINUS its author.
func TestMentions_ASelfMentionIsStoredButNotNotified(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "note to @alice")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "note to @alice")
	require.Len(t, mentionRows(t, app, postID), 1, "the self-mention should be stored")
	require.Empty(t, notificationRows(t, app, alice.Id),
		"a self-mention should not notify")
}

// TestMentions_AnEditReplacesTheMentionSet pins Step 4.
//
// The mention set is REPLACED, not merged: a mention the edit removed
// is gone, and a mention the edit added is there.
func TestMentions_AnEditReplacesTheMentionSet(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")
	carol := testUser(t, app, "carol")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @bob")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @bob")
	require.Len(t, mentionRows(t, app, postID), 1)
	require.Equal(t, bob.Id, mentionRows(t, app, postID)[0].GetString("user"),
		"the mention should name the mentioned account")

	// The edit drops @bob and adds @carol.
	patch := `{"data":{"type":"posts","attributes":{"content":"hey @carol"}}}`
	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID, patch, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	rows := mentionRows(t, app, postID)
	require.Len(t, rows, 1, "the mention set should be replaced")
	require.Equal(t, carol.Id, rows[0].GetString("user"),
		"the removed mention should be gone and the added one should be there")
	require.Empty(t, mentionRows(t, app, postID+"-none"), "the helper should be sane")
}

// TestMentions_ThePostResourceCarriesTheMentionFields pins Step 6.
//
// The post resource carries `mentionedByCount` and the `mentionedBy`
// relationship, and the two agree.
func TestMentions_ThePostResourceCarriesTheMentionFields(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	// Bob's post mentions Alice.
	token, err := bob.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, bob.Id, "hey @alice")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	mentioningPostID := postWithContent(t, app, bob.Id, "hey @alice")

	// Alice's own post is the one that was mentioned.
	_, alicePostID := seedDiscussionWithAuthor(t, app, alice.Id, "Alice's thread", "hello")

	recorder = do(t, handler, http.MethodGet, "/forum/api/posts/"+alicePostID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	count, ok := attributes["mentionedByCount"].(float64)
	require.True(t, ok, "the post should carry mentionedByCount")
	require.Equal(t, float64(1), count, "one post mentions Alice")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource should carry relationships")

	mentionedBy, ok := relationships["mentionedBy"].(map[string]any)
	require.True(t, ok, "the post should carry the mentionedBy relationship")

	data, ok := mentionedBy["data"].([]any)
	require.True(t, ok, "the relationship should carry a list")
	require.Len(t, data, 1, "the mentioning post should be listed")

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, mentioningPostID, identifier["id"], "the mentioning post should be named")
}
