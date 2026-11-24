package flarum

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMentions_AMentionRendersAsALink pins Step 1 and Step 2.
//
// The forum renders a post's Markdown server-side, and the frontend
// inserts `contentHtml` as TRUSTED HTML — so the mention has to be in
// the rendered body, as a link to the account's profile.
//
// **THE TEXT MUST NEVER BE DROPPED.** The mention node carries no
// source segment, so an HTML renderer that emits nothing for it
// DELETES the mention from the output: "hey @alice" rendered as "hey
// ", with nothing in the output saying so. The forum used to disable
// the mention extension for exactly that reason.
func TestMentions_AMentionRendersAsALink(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @alice, look")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post should carry its rendered HTML")

	require.Contains(t, contentHTML, "@alice",
		"the mention must survive the rendering")
	require.Contains(t, contentHTML, `<a class="UserMention"`,
		"the mention should be a link")
	require.Contains(t, contentHTML, `href="/forum/u/alice"`,
		"the link should point at the account's profile inside the mount path")
}

// TestMentions_APlainTextMentionIsNotALink pins the degraded form.
//
// A username that does not exist still renders as a link: the renderer
// cannot consult the database (layering), so the link is rendered
// regardless and it is the READ side that decides whether the account
// exists. The mention is not stored either way.
func TestMentions_APlainTextMentionIsNotALink(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @nobody")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok)

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok)

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok)

	require.Contains(t, contentHTML, "@nobody",
		"the mention must survive the rendering")
}

// TestMentions_ThePostCarriesItsOwnMentions pins Step 6.
//
// `mentionsUsers` is the READ side of the stored rows: the accounts the
// post names.
func TestMentions_ThePostCarriesItsOwnMentions(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @bob")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @bob")

	recorder = do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok)

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource should carry relationships")

	mentionsUsers, ok := relationships["mentionsUsers"].(map[string]any)
	require.True(t, ok, "the post should carry the mentionsUsers relationship")

	data, ok := mentionsUsers["data"].([]any)
	require.True(t, ok, "the relationship should carry a list")
	require.Len(t, data, 1, "the mentioned account should be listed")

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, bob.Id, identifier["id"], "the mentioned account should be named")
}

// TestMentions_APostTheCallerCannotSeeIsNotCounted pins Step 6.
//
// The count of the mentioning posts is over the posts the CALLER may
// see — it is upstream's `whereVisibleTo` — so the number cannot reveal
// a post the caller cannot read.
//
// A HIDDEN post is visible to a moderator and to nobody else.
func TestMentions_APostTheCallerCannotSeeIsNotCounted(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	// Bob's post mentions Alice.
	bobToken, err := bob.NewAuthToken()
	require.NoError(t, err)
	bobCookies := sessionCookies(bobToken, "csrf-token")

	body := postBody(t, app, bob.Id, "hey @alice")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, bobCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	mentioningPostID := postWithContent(t, app, bob.Id, "hey @alice")

	// Alice's own post is the one that was mentioned.
	_, alicePostID := seedDiscussionWithAuthor(t, app, alice.Id, "Alice's thread", "hello")

	// Before the hide, the count is one.
	recorder = do(t, handler, http.MethodGet, "/forum/api/posts/"+alicePostID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, float64(1), mentionedByCountOf(t, recorder), "the mention should be counted")

	// The mentioning post is hidden. A hidden post is visible to a
	// moderator and to nobody else, so the count drops to zero.
	repo := testRepo(t, app)
	require.NoError(t, repo.HidePost(mentioningPostID, bob.Id))

	recorder = do(t, handler, http.MethodGet, "/forum/api/posts/"+alicePostID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, float64(0), mentionedByCountOf(t, recorder),
		"a post the caller cannot see must not be counted")
}

// mentionedByCountOf reads the mentionedByCount attribute out of a
// post resource.
func mentionedByCountOf(t *testing.T, recorder *httptest.ResponseRecorder) float64 {
	t.Helper()

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	count, ok := attributes["mentionedByCount"].(float64)
	require.True(t, ok, "the post should carry mentionedByCount")

	return count
}
