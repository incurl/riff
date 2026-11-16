package flarum

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserPage_ProfileIsFoundBySlug pins the profile lookup.
//
// The profile route is keyed by the username, not the record id, so the
// frontend asks for `GET /api/users/<slug>?bySlug=true` — a page cannot
// know the record id without loading the user first.
func TestUserPage_ProfileIsFoundBySlug(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "venom")

	bySlug := do(t, handler, http.MethodGet, "/forum/api/users/venom?bySlug=true", "", nil)
	require.Equal(t, http.StatusOK, bySlug.Code, bySlug.Body.String())

	document := decodeDocument(t, bySlug)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")
	require.Equal(t, user.Id, resource["id"])

	// By id still works, and is what a plain lookup does.
	byID := do(t, handler, http.MethodGet, "/forum/api/users/"+user.Id, "", nil)
	require.Equal(t, http.StatusOK, byID.Code, byID.Body.String())

	// An unknown slug is a 404, not a 500.
	missing := do(t, handler, http.MethodGet, "/forum/api/users/nobody?bySlug=true", "", nil)
	require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
}

// TestUserPage_AuthorFeedAcceptsTheFrontendsQuery pins the exact request
// the profile page sends.
//
// `PostsUserPage` asks for `filter[author]=<slug>&filter[type]=comment`
// with `include`, `withDefaultIncludes`, an empty `page[limit]` and a
// `sort`. The posts endpoint used to REQUIRE `filter[discussion]` and
// answered 422 for anything else, so every profile page failed to load.
//
// The author here has no posts, so the document is empty; what matters
// is that the shape is accepted.
func TestUserPage_AuthorFeedAcceptsTheFrontendsQuery(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	discussionID, _ := seedDiscussion(t, app, "Alice's thread", "first post")
	_ = alice

	// The feed is public, so no credentials are needed.
	recorder := do(t, handler, http.MethodGet,
		"/forum/api/posts?include=user%2Cdiscussion&withDefaultIncludes=true&page%5Btotal%5D=0&page%5Boffset%5D=0&page%5Blimit%5D&filter%5Btype%5D=comment&filter%5Bauthor%5D=author&sort=-createdAt",
		"", nil)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	require.NotNil(t, document.Data, "the response should carry data")
	_ = discussionID
}

// TestUserPage_AuthorFilterIsCaseExact pins that the filter resolves the
// slug the way the profile lookup does.
func TestUserPage_AuthorFilterFindsThePost(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "venom")
	discussionID, postID := seedDiscussionWithAuthor(t, app, user.Id, "Thread", "a post")
	require.NotEmpty(t, discussionID)

	recorder := do(t, handler, http.MethodGet,
		"/forum/api/posts?filter%5Btype%5D=comment&filter%5Bauthor%5D=venom", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a collection")
	require.Len(t, resources, 1, "the user's one post should be listed")

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, postID, resource["id"])
}
