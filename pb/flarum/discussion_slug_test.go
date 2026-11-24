package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
)

// forumSlugOf reads a resource's `slug` attribute out of a JSON:API
// document.
func forumSlugOf(t *testing.T, body string) string {
	t.Helper()

	var document struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))

	slug, ok := document.Data.Attributes["slug"].(string)
	require.True(t, ok, "the resource should carry a slug")
	return slug
}

// forumIDOf reads the primary resource's id out of a JSON:API document.
func forumIDOf(t *testing.T, body string) string {
	t.Helper()

	var document struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))

	require.NotEmpty(t, document.Data.ID, "the document should carry an id")
	return document.Data.ID
}

// The discussion URL is built from the discussion's SLUG.
//
// The frontend's route helper uses `discussion.slug()`
// (framework/core/js/src/forum/routes.ts:58):
//
//	discussion: (discussion: Discussion, near?: number) => {
//	  return app.route(near && near !== 1 ? 'discussion.near' : 'discussion', {
//	    id: discussion.slug(),
//	    near: near && near !== 1 ? near : undefined,
//	  });
//	},
//
// and the discussion page then asks the API for it with `bySlug=true`
// (framework/core/js/src/forum/components/DiscussionPage.tsx:176,257):
//
//	discussionPromise = app.store.find<Discussion>('discussions', m.route.param('id'), this.requestParams());
//	...
//	requestParams(): Record<string, unknown> {
//	  return { bySlug: true, page: { near: this.near } };
//	}
//
// So the request is `GET /api/discussions/<slug>?bySlug=true`, and the
// server has to resolve a SLUG there.
//
// Upstream resolves it the same way (`DiscussionResource::find`):
//
//	if (Arr::get($context->request->getQueryParams(), 'bySlug', false)) {
//	    $discussion = $this->slugManager->forResource(Discussion::class)->fromSlug($id, $actor);
//	} else {
//	    $discussion = $this->query($context)->findOrFail($id);
//	}

// TestForum_TheDiscussionIsFoundByItsSlug pins the reported symptom: a
// discussion that was just posted could not be opened.
//
// The slug is what the frontend navigates to, and it asks for it with
// `bySlug=true`.
func TestForum_TheDiscussionIsFoundByItsSlug(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	// The slug the API published.
	slug := forumSlugOf(t, created.Body.String())
	require.NotEmpty(t, slug, "the discussion should carry a slug")

	// The frontend asks for it by slug.
	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+slug+"?bySlug=true", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code,
		"the discussion must be found by its slug: %s", recorder.Body.String())
}

// TestForum_TheSlugCarriesTheID pins upstream's slug format.
//
// Upstream's slug is the id, then the title's slug
// (`IdWithTransliteratedSlugDriver::toSlug` is
// `$instance->id . (trim($instance->slug) ? '-' . $instance->slug : ”)`).
//
// **THE ID IS WHAT MAKES THE SLUG UNIQUE.** The title's slug alone is
// not: two discussions may share a title, and the slug column is not
// unique. Upstream resolves the ambiguity by leading with the id and
// reading it back by taking the part before the first dash.
func TestForum_TheSlugCarriesTheID(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	id := resourceIDOf(t, created)
	slug := forumSlugOf(t, created.Body.String())

	require.Equal(t, id+"-"+"test", slug,
		"the slug must lead with the id, as upstream's does")
}

// TestForum_TwoDiscussionsWithTheSameTitleAreDistinct pins that the
// id in the slug is what keeps them apart.
//
// Two discussions may share a title — the title's slug is the same for
// both — so a slug built from the title alone would name the wrong
// discussion, or an arbitrary one.
func TestForum_TwoDiscussionsWithTheSameTitleAreDistinct(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	cookies := sessionCookies(token, "csrf-token")

	first := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Same Title","content":"First"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	second := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Same Title","content":"Second"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())

	firstSlug := forumSlugOf(t, first.Body.String())
	secondSlug := forumSlugOf(t, second.Body.String())

	require.NotEqual(t, firstSlug, secondSlug,
		"two discussions with the same title must have different slugs")

	// Each slug resolves to its OWN discussion: the id in the slug is
	// what tells them apart, since the title's slug is the same for
	// both.
	for slug, want := range map[string]string{
		firstSlug:  resourceIDOf(t, first),
		secondSlug: resourceIDOf(t, second),
	} {
		recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+slug+"?bySlug=true", "", nil)
		require.Equal(t, http.StatusOK, recorder.Code,
			"the slug %q must resolve: %s", slug, recorder.Body.String())

		got := forumIDOf(t, recorder.Body.String())
		require.Equal(t, want, got,
			"the slug %q must resolve to its own discussion", slug)
	}
}

// TestForum_TheDiscussionIsStillFoundByItsID pins that the slug lookup
// did not break the plain id lookup.
//
// The two share one route, so the `bySlug` flag is what tells them
// apart.
func TestForum_TheDiscussionIsStillFoundByItsID(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	id := resourceIDOf(t, created)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code,
		"the discussion must still be found by its id: %s", recorder.Body.String())
}

// TestForum_TheSlugHelpersAreInverses pins the helper pair.
func TestForum_TheSlugHelpersAreInverses(t *testing.T) {
	for _, tc := range []struct {
		id   string
		slug string
	}{
		{"abc123", "test"},
		{"abc123", ""},
		{"abc123", "a-longer-title"},
	} {
		slug := api.DiscussionSlug(tc.id, tc.slug)
		require.Equal(t, tc.id, api.DiscussionIDFromSlug(slug),
			"the id must survive the round trip: %q", slug)
	}
}

// TestForum_TheSlugLookupIsOptIn pins that the flag decides.
//
// A request without `bySlug` is an id lookup, so a slug in the path is
// not found — which is upstream's behaviour, and what keeps the two
// apart on one route.
func TestForum_TheSlugLookupIsOptIn(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Test","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	slug := forumSlugOf(t, created.Body.String())

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+slug, "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code,
		"without the flag the path segment is an id, and the slug is not one: %s", recorder.Body.String())
}
