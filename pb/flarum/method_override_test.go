package flarum

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// The frontend rewrites every method it cannot send into a POST with
// an override header.
//
// The vendored `Application.request` (framework/core/js/src/common/
// Application.tsx:673-684) is byte-identical to upstream's:
//
//	if (options.method && !['GET', 'POST'].includes(options.method)) {
//	  const method = options.method;
//
//	  extend(configurable, 'config', (_: unknown, xhr: XMLHttpRequest) => {
//	    xhr.setRequestHeader('X-HTTP-Method-Override', method);
//	  });
//
//	  options.method = 'POST';
//	}
//
// Upstream's API carries a `FakeHttpMethods` middleware that reads the
// header back. PocketBase's router matches on the REAL method before any
// handler runs, so a route registered only for PATCH never sees the
// request — it answers 404, and the frontend reports "That resource
// does not exist" for an edit that is perfectly ordinary.
//
// Every write route therefore needs a POST twin that resolves the
// effective method itself.

// TestForum_TheUserPreferencesSaveReachesTheServer pins the reported
// symptom: the settings page's save.
//
// `User.savePreferences` calls `Model.save()`, which sends a PATCH to
// `/api/users/{id}`. The frontend rewrites that to a POST, so the
// PATCH-only route never matched and the save answered 404.
func TestForum_TheUserPreferencesSaveReachesTheServer(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/users/"+user.Id,
		`{"data":{"type":"users","id":"`+user.Id+`","attributes":{"displayName":"Renamed"}}}`,
		sessionCookies(token, "csrf-token"),
		map[string]string{"X-HTTP-Method-Override": "PATCH"})

	require.Equal(t, http.StatusOK, recorder.Code,
		"the preferences save must reach the server: %s", recorder.Body.String())

	// The change was stored.
	repo := testRepo(t, app)
	stored, err := repo.FindUserProfile(user.Id)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "Renamed", stored.DisplayName)
}

// TestForum_TheDiscussionUpdateReachesTheServer pins the update.
//
// A discussion edit is a PATCH from the frontend's `Model.save()`, so
// it arrived as a POST — and the PATCH-only route did not match.
func TestForum_TheDiscussionUpdateReachesTheServer(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Original","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	discussionID := resourceIDOf(t, created)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","id":"`+discussionID+`","attributes":{"title":"Renamed"}}}`,
		sessionCookies(token, "csrf-token"),
		map[string]string{"X-HTTP-Method-Override": "PATCH"})

	require.Equal(t, http.StatusOK, recorder.Code,
		"the discussion update must reach the server: %s", recorder.Body.String())
}

// TestForum_TheDiscussionDeleteReachesTheServer pins the delete.
//
// The delete is a DELETE from the frontend, so it arrives as a POST
// with an override naming DELETE. It shares the update's path, so the
// OVERRIDE — not the URL — is what tells them apart.
func TestForum_TheDiscussionDeleteReachesTheServer(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Doomed","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	discussionID := resourceIDOf(t, created)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","id":"`+discussionID+`"}}`,
		sessionCookies(token, "csrf-token"),
		map[string]string{"X-HTTP-Method-Override": "DELETE"})

	require.Equal(t, http.StatusNoContent, recorder.Code,
		"the discussion delete must reach the server: %s", recorder.Body.String())
}

// TestForum_ThePostUpdateReachesTheServer pins the post update.
func TestForum_ThePostUpdateReachesTheServer(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	discussion := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Thread","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, discussion.Code, discussion.Body.String())
	discussionID := resourceIDOf(t, discussion)

	post := do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","relationships":{"discussion":{"data":{"type":"discussions","id":"`+discussionID+`"}}},"attributes":{"content":"Original"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, post.Code, post.Body.String())
	postID := resourceIDOf(t, post)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","id":"`+postID+`","attributes":{"content":"Renamed"}}}`,
		sessionCookies(token, "csrf-token"),
		map[string]string{"X-HTTP-Method-Override": "PATCH"})

	require.Equal(t, http.StatusOK, recorder.Code,
		"the post update must reach the server: %s", recorder.Body.String())
}

// TestForum_ThePostDeleteReachesTheServer pins the post delete.
func TestForum_ThePostDeleteReachesTheServer(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "author")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	discussion := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Thread","content":"First message"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, discussion.Code, discussion.Body.String())
	discussionID := resourceIDOf(t, discussion)

	post := do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","relationships":{"discussion":{"data":{"type":"discussions","id":"`+discussionID+`"}}},"attributes":{"content":"Doomed"}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusCreated, post.Code, post.Body.String())
	postID := resourceIDOf(t, post)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","id":"`+postID+`"}}`,
		sessionCookies(token, "csrf-token"),
		map[string]string{"X-HTTP-Method-Override": "DELETE"})

	require.Equal(t, http.StatusNoContent, recorder.Code,
		"the post delete must reach the server: %s", recorder.Body.String())
}

// TestForum_TheNotificationReadReachesTheServer pins the notification
// read.
func TestForum_TheNotificationReadReachesTheServer(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "reader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/notifications/someid",
		`{"data":{"type":"notifications","id":"someid"}}`,
		sessionCookies(token, "csrf-token"),
		map[string]string{"X-HTTP-Method-Override": "PATCH"})

	// The notification does not exist, so the answer is 404 — but it is
	// a RESOURCE 404, which means the route matched and the handler
	// ran. A route 404 (the bug) would answer the same status with a
	// different body, so the body is what tells them apart.
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "not_found",
		"the route must have matched: %s", recorder.Body.String())
}

// TestForum_APlainPostIsNotAWrite pins that the POST twin does not turn
// into an open door.
//
// The override header is what makes a POST a PATCH or a DELETE. A POST
// that names no method is a client that does not speak the protocol, so
// it is refused with a 405 — not silently treated as a write.
func TestForum_APlainPostIsNotAWrite(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	for _, path := range []string{
		"/forum/api/users/" + user.Id,
		"/forum/api/notifications/someid",
	} {
		recorder := do(t, handler, http.MethodPost, path,
			`{"data":{"type":"users","id":"`+user.Id+`","attributes":{"displayName":"Renamed"}}}`,
			sessionCookies(token, "csrf-token"))

		require.Equal(t, http.StatusMethodNotAllowed, recorder.Code,
			"a POST without the override header must be refused: %s", recorder.Body.String())
	}
}

// TestForum_APlainPostIsNotADelete pins the same for the routes whose
// POST twin dispatches on the method.
func TestForum_APlainPostIsNotADelete(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	for _, path := range []string{
		"/forum/api/discussions/someid",
		"/forum/api/posts/someid",
	} {
		recorder := do(t, handler, http.MethodPost, path,
			`{"data":{"type":"discussions","id":"someid"}}`,
			sessionCookies(token, "csrf-token"))

		require.Equal(t, http.StatusMethodNotAllowed, recorder.Code,
			"a POST without the override header must be refused: %s", recorder.Body.String())
	}
}

// TestForum_TheRealPatchStillWorks pins that the POST twin did not
// break the real thing.
//
// A client that CAN send a PATCH sends a PATCH, and the route is still
// registered for it.
func TestForum_TheRealPatchStillWorks(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPatch, "/forum/api/users/"+user.Id,
		`{"data":{"type":"users","id":"`+user.Id+`","attributes":{"displayName":"Renamed"}}}`,
		sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusOK, recorder.Code,
		"a real PATCH must still work: %s", recorder.Body.String())
}
