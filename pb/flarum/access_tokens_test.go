package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/repository"
)

// TestForum_AccessTokensAndTheAPICatchAll pins the access-token list
// and the API's own catch-all.
//
// The cases share one app: booting a PocketBase app and running every
// migration is the expensive part of a forum test, and under the race
// detector it dominates the runtime.
func TestForum_AccessTokensAndTheAPICatchAll(t *testing.T) {
	app, handler := newTestForum(t)

	// The settings page's notification-preferences reset shares this app:
	// booting a PocketBase app and running every migration is the
	// expensive part of a forum test, and under the race detector it
	// dominates the runtime.
	t.Run("the notification-preferences reset", func(t *testing.T) {
		testNotificationPreferencesReset(t, app, handler)
	})

	// The frontend's user security page fetches the session tokens and
	// the developer tokens SEPARATELY and joins the two answers with
	// `Promise.all([...]).then(([sessions, developerTokens]) => …)`. The
	// destructuring fails with "e is not iterable" unless each answer is
	// an iterable, so the answer has to be a JSON:API ARRAY and not a
	// single resource object.
	t.Run("the access-token list is an array", func(t *testing.T) {
		user := testUser(t, app, "tokens_user")
		token, err := user.NewAuthToken()
		require.NoError(t, err)

		recorder := do(t, handler, http.MethodGet, "/forum/api/access-tokens", "", sessionCookies(token, "csrf-token"))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Equal(t, api.ContentType, recorder.Header().Get("Content-Type"))

		document := decodeDocument(t, recorder)

		resources, ok := document.Data.([]any)
		require.True(t, ok, "the data should be an ARRAY so Promise.all can destructure it")
		require.Len(t, resources, 1)

		resource, ok := resources[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "access-tokens", resource["type"])
		require.Equal(t, user.Id, resource["id"])

		attributes, ok := resource["attributes"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, attributes["isCurrent"])
		require.Equal(t, true, attributes["isSessionToken"])
	})

	// The upstream endpoint is authenticated.
	t.Run("the access-token list needs authentication", func(t *testing.T) {
		recorder := do(t, handler, http.MethodGet, "/forum/api/access-tokens", "", nil)
		require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	})

	// The forum document is served at `/forum/api` (no trailing slash)
	// and at the routes below it. It is NOT served as a subtree: in
	// Go's http.ServeMux a trailing slash is an anonymous "...\/"
	// wildcard, so registering `GET /forum/api/` answers EVERY unrouted
	// path under /forum/api/ with the forum document.
	//
	// That is worse than a 404. The document's `data` is a single
	// object rather than an array, so a caller that asked for a list
	// gets one object back, and the `Promise.all([...])` that
	// destructures the answer fails with "e is not iterable" — the
	// symptom the user sees on the user security page.
	t.Run("an unrouted API path is a JSON:API not found", func(t *testing.T) {
		recorder := do(t, handler, http.MethodGet, "/forum/api/access-tokenz", "", nil)
		require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
		require.Equal(t, api.ContentType, recorder.Header().Get("Content-Type"))

		var document api.ErrorDocument
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
		require.Len(t, document.Errors, 1)
		require.Equal(t, "404", document.Errors[0].Status)
		require.Equal(t, api.CodeNotFound, document.Errors[0].Code)
	})

	// The frontend reads the forum document out of the BOOT PAYLOAD, so
	// nothing in the SPA fetches it — the route exists for external
	// clients, and it must not answer for the rest of the API.
	t.Run("the forum document is not a subtree", func(t *testing.T) {
		recorder := do(t, handler, http.MethodGet, "/forum/api/", "", nil)
		require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())

		// The document itself is still served at the exact path.
		recorder = do(t, handler, http.MethodGet, "/forum/api", "", nil)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	})
}

// TestForum_NotificationPreferencesReset pins the
// `DELETE {apiUrl}/users/{id}/notification-preferences` route.
//
// The frontend's settings page calls it to put the user's notification
// preferences back to the defaults. The route has to EXIST: an
// unrouted path falls through to the SPA catch-all, which answers with
// the SPA's index.html, and the frontend's request helper throws on a
// non-2xx response.
//
// The failure is loud in the browser: the error carries the whole HTML
// document as its `responseText`, because the body is HTML rather than
// JSON, so the message the user sees is a page of markup.
//
// The cases share one app: booting a PocketBase app and running every
// migration is the expensive part of a forum test, and under the race
// detector it is by far the dominant cost.
func testNotificationPreferencesReset(t *testing.T, app core.App, handler http.Handler) {
	t.Run("answers with the user resource", func(t *testing.T) {
		user := testUser(t, app, "prefs_user")
		token, err := user.NewAuthToken()
		require.NoError(t, err)

		recorder := do(t, handler, http.MethodDelete, "/forum/api/users/"+user.Id+"/notification-preferences", "", sessionCookies(token, "csrf-token"))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

		// The answer is the user resource, which the frontend pushes into
		// its store.
		require.Contains(t, recorder.Body.String(), `"type":"users"`)
		require.Contains(t, recorder.Body.String(), user.Id)
	})

	t.Run("drops the notification keys and keeps the rest", func(t *testing.T) {
		user := testUser(t, app, "prefs_keys")
		token, err := user.NewAuthToken()
		require.NoError(t, err)

		repo := testRepo(t, app)

		// A profile with a notification preference and a preference of
		// another kind. The profile is created lazily, so a user that
		// never posted has no row yet.
		profile := &repository.UserProfile{
			UserID:   user.Id,
			IsActive: true,
			Preferences: map[string]any{
				"notify_discussion_alert": false,
				"notify_mention_email":    false,
				"discloseOnline":          true,
			},
		}
		require.NoError(t, repo.SaveUserProfile(profile))

		recorder := do(t, handler, http.MethodDelete, "/forum/api/users/"+user.Id+"/notification-preferences", "", sessionCookies(token, "csrf-token"))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

		saved, err := repo.FindUserProfile(user.Id)
		require.NoError(t, err)
		require.NotNil(t, saved)

		// Upstream forgets the `notify_{subject}_{driver}` keys so the
		// registered defaults apply again.
		require.NotContains(t, saved.Preferences, "notify_discussion_alert")
		require.NotContains(t, saved.Preferences, "notify_mention_email")
		require.Contains(t, saved.Preferences, "discloseOnline", "a preference of another kind is kept")
	})

	t.Run("is scoped to the owner", func(t *testing.T) {
		owner := testUser(t, app, "prefs_owner")
		other := testUser(t, app, "prefs_other")

		token, err := other.NewAuthToken()
		require.NoError(t, err)

		recorder := do(t, handler, http.MethodDelete, "/forum/api/users/"+owner.Id+"/notification-preferences", "", sessionCookies(token, "csrf-token"))
		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	})

	t.Run("needs authentication", func(t *testing.T) {
		user := testUser(t, app, "prefs_anon")

		recorder := do(t, handler, http.MethodDelete, "/forum/api/users/"+user.Id+"/notification-preferences", "", nil)
		require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	})

	t.Run("accepts the method override", func(t *testing.T) {
		// The frontend does not send a DELETE: it rewrites every method
		// other than GET and POST into a POST with the intended method in
		// the `X-HTTP-Method-Override` header, because "not all servers
		// and clients support" PATCH or DELETE (see the core's
		// `Application.request`). Upstream's API carries a
		// `FakeHttpMethods` middleware that reads the header back.
		//
		// Without that, the frontend's request reaches the API as a POST
		// and the route does not match, so the answer is the SPA's
		// index.html rather than the user resource.
		user := testUser(t, app, "prefs_override")
		token, err := user.NewAuthToken()
		require.NoError(t, err)

		recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/users/"+user.Id+"/notification-preferences", "",
			sessionCookies(token, "csrf-token"), map[string]string{"X-HTTP-Method-Override": "DELETE"})
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Contains(t, recorder.Body.String(), `"type":"users"`)
	})

	t.Run("rejects a plain post", func(t *testing.T) {
		// The route accepts a POST because the frontend sends one, but
		// the POST is only a DELETE when the header says so. A plain POST
		// is a client mistake and is answered with a 405 rather than
		// silently performing the reset.
		user := testUser(t, app, "prefs_plain_post")
		token, err := user.NewAuthToken()
		require.NoError(t, err)

		recorder := do(t, handler, http.MethodPost, "/forum/api/users/"+user.Id+"/notification-preferences", "", sessionCookies(token, "csrf-token"))
		require.Equal(t, http.StatusMethodNotAllowed, recorder.Code, recorder.Body.String())
	})

	t.Run("reads the preferences column back", func(t *testing.T) {
		// The column is a PocketBase JSONField, and `record.Get` hands
		// back the stored document as `types.JSONRaw` (a []byte of
		// JSON), not as a `map[string]any`. A reader that only handles
		// the map therefore reads EVERY profile as having no
		// preferences: the values are written correctly and then never
		// read back, so a user's privacy, colour scheme and haptic
		// preferences silently reset on every page load.
		//
		// The settings page is where this shows: the switch for
		// "disclose online" renders from `user.preferences()`, so it
		// reads as off no matter what the user set.
		user := testUser(t, app, "prefs_roundtrip")

		profile := &repository.UserProfile{
			UserID:   user.Id,
			IsActive: true,
			Preferences: map[string]any{
				"discloseOnline": true,
				"colorScheme":    "dark",
			},
		}
		require.NoError(t, testRepo(t, app).SaveUserProfile(profile))

		read, err := testRepo(t, app).FindUserProfile(user.Id)
		require.NoError(t, err)
		require.NotNil(t, read, "the profile should exist")

		require.NotNil(t, read.Preferences, "the preferences should be read back")
		require.Equal(t, true, read.Preferences["discloseOnline"])
		require.Equal(t, "dark", read.Preferences["colorScheme"])
	})
}
