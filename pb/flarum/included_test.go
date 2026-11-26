package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// A RESPONSE'S `included` CARRIES THE RELATED RESOURCES.
//
// The frontend resolves a relationship through the STORE, not through
// the identifier: `Model.hasOne` is
// `this.store.getById(relationshipData.type, relationshipData.id)`, and
// the store is filled from `payload.included`:
//
//	pushPayload(payload) {
//	  if (payload.included) payload.included.map(this.pushObject.bind(this));
//	  ...
//	}
//
// So a relationship identifier alone is NOT enough: the related
// RESOURCE has to be in the response, or `getById` returns undefined
// and the relation reads as absent.
//
// Upstream sends those resources. The discussion index has
// `defaultInclude(['user', 'user.groups', 'lastPostedUser', ...])`
// (framework/core/src/Api/Resource/DiscussionResource.php:101), the
// discussion show has its own set (:87), and the post index has
// `['user', 'discussion', 'discussion.lastPostedUser']`
// (PostResource.php:147).
//
// The frontend asks for the same thing explicitly — the discussion
// list sends `include: ['user', 'lastPostedUser']`
// (framework/core/js/src/forum/states/DiscussionListState.ts:32).

// TestForum_TheDiscussionListCarriesItsAuthor pins that a discussion's
// author comes with it.
//
// Without the author in the response, `discussion.user()` reads as
// undefined and the list item renders a blank author: no name, no
// avatar.
func TestForum_TheDiscussionListCarriesItsAuthor(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "users",
		"the discussion list must carry the author: %s", recorder.Body.String())

	// And the author is the RIGHT one.
	users := includedResourcesOf(t, recorder.Body.String(), "users")
	require.Len(t, users, 1)
	require.Equal(t, author.Id, users[0]["id"])
}

// TestForum_TheDiscussionShowCarriesItsAuthor pins the same for the
// single discussion.
//
// The discussion page renders the author of the first post, and the
// post's own author.
func TestForum_TheDiscussionShowCarriesItsAuthor(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	discussionID := resourceIDOf(t, created)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "users",
		"the discussion show must carry the author: %s", recorder.Body.String())
}

// TestForum_ThePostListCarriesItsAuthor pins that a post's author
// comes with it.
//
// A post is rendered with its author's name and avatar, so the post
// list has to carry the user too (upstream's
// `PostResource.php:147`).
func TestForum_ThePostListCarriesItsAuthor(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	discussion := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, discussion.Code, discussion.Body.String())
	discussionID := resourceIDOf(t, discussion)

	post := do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","relationships":{"discussion":{"data":{"type":"discussions","id":"`+discussionID+`"}}},"attributes":{"content":"A reply"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, post.Code, post.Body.String())
	postID := resourceIDOf(t, post)

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts?filter[discussion]="+discussionID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "users",
		"the post list must carry the author: %s", recorder.Body.String())

	// And the post itself is in the response's data.
	var document struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	found := false
	for _, p := range document.Data {
		if p["id"] == postID {
			found = true
		}
	}
	require.True(t, found, "the post list should carry the posted post")
}

// includedTypesOf lists the resource types a response carries in its
// `included` array.
func includedTypesOf(t *testing.T, body string) []string {
	t.Helper()

	var document struct {
		Included []map[string]any `json:"included"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))

	types := make([]string, 0, len(document.Included))
	for _, resource := range document.Included {
		if resourceType, ok := resource["type"].(string); ok {
			types = append(types, resourceType)
		}
	}
	return types
}

// includedResourcesOf returns the `included` resources of one type.
func includedResourcesOf(t *testing.T, body, resourceType string) []map[string]any {
	t.Helper()

	var document struct {
		Included []map[string]any `json:"included"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))

	out := make([]map[string]any, 0, len(document.Included))
	for _, resource := range document.Included {
		if resource["type"] == resourceType {
			out = append(out, resource)
		}
	}
	return out
}

// TestForum_ThePostStreamCanLoadItsPostsByID pins the post stream's
// lookup.
//
// The stream shows a WINDOW of posts: the discussion's `posts`
// relationship names them all but carries no attributes, so the stream
// loads the ones it is about to show. It asks for them BY ID
// (framework/core/js/src/forum/states/PostStreamState.ts:308):
//
//	return app.store.find<Post[]>('posts', loadIds).then((newPosts) => {
//
// and `Store.find` with an array builds `?filter[id]=a,b,c`
// (framework/core/js/src/common/Store.ts:193).
//
// Without that filter the endpoint answered 422 ("A discussion or
// author filter is required"), and the discussion page could not show
// its replies.
func TestForum_ThePostStreamCanLoadItsPostsByID(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	discussion := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, discussion.Code, discussion.Body.String())
	discussionID := resourceIDOf(t, discussion)

	post := do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","relationships":{"discussion":{"data":{"type":"discussions","id":"`+discussionID+`"}}},"attributes":{"content":"A reply"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, post.Code, post.Body.String())
	postID := resourceIDOf(t, post)

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts?filter[id]="+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code,
		"the post stream must be able to load its posts by id: %s", recorder.Body.String())

	var document struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
	require.Len(t, document.Data, 1, "the post should be in the response")
	require.Equal(t, postID, document.Data[0]["id"])
}

// TestForum_TheDiscussionListCarriesItsPagination pins the pagination
// metadata and the links.
//
// The frontend reads FOUR of them (framework/core/js/src/common/states/
// PaginatedListState.ts:197,227,147):
//
//	const usedPerPage = results.payload?.meta?.perPage;
//	const usedTotal = results.payload?.meta?.page?.total;
//	...
//	page.items.payload.meta?.page?.offset
//	...
//	hasNext: !!links?.next,
//
// and the LAST ONE IS THE "Load more" BUTTON: the button is only
// rendered when `state.hasNext()` is true
// (forum/components/PostList.tsx:25). Without `links.next` the
// infinite scroll never loads a second page.
func TestForum_TheDiscussionListCarriesItsPagination(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	cookies := sessionCookies(token, "csrf-token")

	// Three discussions, so there is a second page.
	for i := 0; i < 3; i++ {
		created := do(t, handler, http.MethodPost, "/forum/api/discussions",
			`{"data":{"type":"discussions","attributes":{"title":"Test `+string(rune('a'+i))+`","content":"First message"}}}`,
			cookies)
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	}

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions?page[limit]=2", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var document struct {
		Meta struct {
			PerPage int `json:"perPage"`
			Page    struct {
				Total  int `json:"total"`
				Offset int `json:"offset"`
				Limit  int `json:"limit"`
			} `json:"page"`
		} `json:"meta"`
		Links struct {
			Next string `json:"next"`
			Prev string `json:"prev"`
		} `json:"links"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	require.Equal(t, 3, document.Meta.Page.Total, "the total must count every match")
	require.Equal(t, 2, document.Meta.PerPage, "the page size must be reported")
	require.Equal(t, 0, document.Meta.Page.Offset, "the first page starts at zero")
	require.Equal(t, 2, document.Meta.Page.Limit, "the page size must be reported")

	require.NotEmpty(t, document.Links.Next,
		"there is a second page, so the next link must be there")
	require.Empty(t, document.Links.Prev,
		"the first page has no previous one")

	// The next link is a usable URL: it points at the next page.
	require.Contains(t, document.Links.Next, "page%5Boffset%5D=2")
}

// TestForum_TheLastPageHasNoNextLink pins that the link is ABSENT on
// the last page.
//
// The frontend reads the link's ABSENCE ("hasNext: !!links?.next"), so
// an empty string would read as "there is a next page" and the "Load
// more" button would never go away.
func TestForum_TheLastPageHasNoNextLink(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Only","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var document struct {
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
	require.Empty(t, document.Links.Next,
		"the last page has no next page, so the link must be absent")
}

// TestForum_TheDiscussionListHonoursTheSort pins the sort.
//
// The frontend's index page sends upstream's sort names
// (framework/core/js/src/forum/states/DiscussionListState.ts:73):
//
//	map.latest = '-lastPostedAt';
//	map.top = '-commentCount';
//	map.newest = '-createdAt';
//	map.oldest = 'createdAt';
//	map.az = 'title';
//	map.za = '-title';
//
// Without the sort the dropdown did nothing: every listing came back
// in the repository's own order.
func TestForum_TheDiscussionListHonoursTheSort(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	cookies := sessionCookies(token, "csrf-token")

	// Two discussions whose titles sort the other way round from their
	// creation order.
	for _, title := range []string{"Bravo", "Alpha"} {
		created := do(t, handler, http.MethodPost, "/forum/api/discussions",
			`{"data":{"type":"discussions","attributes":{"title":"`+title+`","content":"First message"}}}`,
			cookies)
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	}

	// By title, ascending.
	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions?sort=title", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles := discussionTitlesOf(t, recorder.Body.String())
	require.Equal(t, []string{"Alpha", "Bravo"}, titles,
		"sort=title must order by the title")

	// By title, descending.
	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions?sort=-title", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles = discussionTitlesOf(t, recorder.Body.String())
	require.Equal(t, []string{"Bravo", "Alpha"}, titles,
		"sort=-title must reverse the order")
}

// discussionTitlesOf reads the titles of the discussions in a listing,
// in the order they came.
func discussionTitlesOf(t *testing.T, body string) []string {
	t.Helper()

	var document struct {
		Data []struct {
			Attributes struct {
				Title string `json:"title"`
			} `json:"attributes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))

	titles := make([]string, 0, len(document.Data))
	for _, discussion := range document.Data {
		titles = append(titles, discussion.Attributes.Title)
	}
	return titles
}

// TestForum_TheNotificationsCanBeDeletedAll pins the delete-all
// endpoint.
//
// The frontend's notification page has a "delete all" button, and it
// calls `DELETE /api/notifications`
// (framework/core/js/src/forum/states/NotificationListState.ts:60).
// Upstream serves it (`Api/routes.php`: `$map->delete('/notifications',
// 'notifications.deleteAll', …)`), and riff did not — so the button
// answered 404.
//
// The frontend does not send a DELETE though: it rewrites every method
// other than GET and POST into a POST with an
// `X-HTTP-Method-Override` header, so the route has to answer both.
func TestForum_TheNotificationsCanBeDeletedAll(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "reader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	cookies := sessionCookies(token, "csrf-token")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/notifications", "",
		cookies, map[string]string{"X-HTTP-Method-Override": "DELETE"})
	require.Equal(t, http.StatusNoContent, recorder.Code,
		"the notifications must be deletable: %s", recorder.Body.String())

	// A plain DELETE works too, for a client that can send one.
	recorder = do(t, handler, http.MethodDelete, "/forum/api/notifications", "", cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code,
		"the notifications must be deletable: %s", recorder.Body.String())
}

// TestForum_TheNotificationsDeleteIsOptIn pins that the override
// decides.
//
// A POST that names no method is a client that does not speak the
// protocol, so it is refused with a 405 rather than silently treated
// as a delete.
func TestForum_TheNotificationsDeleteIsOptIn(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "reader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/notifications", "",
		sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code,
		"a POST without the override header must be refused: %s", recorder.Body.String())
}

// TestForum_ACombinedDiscussionFilterDoesNotFail pins that a listing can
// carry more than one filter.
//
// The repository joined its clauses with PocketBase's filter `&&`, but the
// count query and the page query take dbx expressions, which reach the
// driver as written — so any listing with TWO clauses answered 500 with
// `near "&": syntax error`: a search term (`filter[q]` adds the state
// clause and the search clause) and "started by me" (`filter[user]`) both
// did. A single clause happened to be valid SQL, which is why the fault was
// invisible until two were combined.
func TestForum_ACombinedDiscussionFilterDoesNotFail(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "filtered_writer")

	matchedID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Release notes", "body")
	seedDiscussionWithAuthor(t, app, author.Id, "Something else", "body")

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions?filter[q]=Release", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 1, "the search must select the matching discussion only")

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok, "the resource should be an object")
	require.Equal(t, matchedID, resource["id"])

	// The author's own listing is the other two-clause case.
	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions?filter[user]="+author.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// TestForum_TheUserShowCarriesItsGroups pins that a TO-MANY relation is
// followed.
//
// The walk read a SINGLE identifier out of a relation's `data`. A
// to-many relation's data is an ARRAY, so the read never matched and
// the relation was never followed: `GET /api/users/{id}` for an account
// in a group answered with `included types = []`.
//
// The consequence is quiet: the frontend reads the absence as "no
// groups", so the account renders without the badge that names its
// group — and the frontend KEEPS that record, so the profile stays
// badge-less until the page is reloaded.
//
// It is a separate defect from the polls work, and it is pinned by its
// own test so the two are not conflated.
func TestForum_TheUserShowCarriesItsGroups(t *testing.T) {
	app, handler := newTestForum(t)

	member := testUser(t, app, "grouped")
	require.NoError(t, testRepo(t, app).AddUserToGroup(member.Id, domain.GroupAdminID))

	recorder := do(t, handler, http.MethodGet, "/forum/api/users/"+member.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "groups",
		"a user's groups must travel with the response: %s", recorder.Body.String())

	groups := includedResourcesOf(t, recorder.Body.String(), "groups")
	require.Len(t, groups, 1, "the account holds one group")
	require.Equal(t, domain.GroupAdminID, groups[0]["id"])
}

// TestForum_TheNestedIncludeIsFollowed pins that a DOTTED include path
// reaches the end of its chain.
//
// Upstream's include paths are dotted: `firstPost.user.groups` names
// the discussion's first post, that post's author, and that author's
// groups. The frontend needs every link of the chain in the response,
// because it resolves a relation through the STORE and the store is
// filled from `included`.
//
// A walk that only follows the FIRST hop answers with the post and no
// author — and the post renders with a blank author.
func TestForum_TheNestedIncludeIsFollowed(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "nested_author")
	require.NoError(t, testRepo(t, app).AddUserToGroup(author.Id, domain.GroupAdminID))

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Nested","content":"First message"}}}`,
		sessionCookies(mustAuthToken(t, author), "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	discussionID := resourceIDOf(t, created)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID+"?include=firstPost.user.groups", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "posts",
		"the discussion's first post must travel with the response: %s", recorder.Body.String())
	require.Contains(t, included, "users",
		"the post's author must travel with the response: %s", recorder.Body.String())
	require.Contains(t, included, "groups",
		"the author's groups must travel with the response: %s", recorder.Body.String())
}

// mustAuthToken mints an auth token for an account.
func mustAuthToken(t *testing.T, record *core.Record) string {
	t.Helper()

	token, err := record.NewAuthToken()
	require.NoError(t, err)

	return token
}
