package flarum

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/repository"
)

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
func TestForum_NotificationPreferencesReset(t *testing.T) {
	app, handler := newTestForum(t)

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
