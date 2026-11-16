package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/frontend"
)

// bootPayloadOf reads the boot payload out of the forum shell.
//
// The shell embeds it as a JSON script tag, so that is where the
// frontend reads it from too.
func bootPayloadOf(t *testing.T, recorder *httptest.ResponseRecorder) frontend.BootPayload {
	t.Helper()

	body := recorder.Body.String()

	const open = `<script id="flarum-json-payload" type="application/json">`
	start := strings.Index(body, open)
	require.GreaterOrEqual(t, start, 0, "the shell should carry the boot payload: %s", body)

	rest := body[start+len(open):]
	end := strings.Index(rest, "</script>")
	require.GreaterOrEqual(t, end, 0, "the boot payload should be closed: %s", body)

	var payload frontend.BootPayload
	require.NoError(t, json.Unmarshal([]byte(rest[:end]), &payload), "the boot payload should be JSON")
	return payload
}

// TestForum_LoginSetsTheSession pins the login round-trip.
//
// The frontend logs in with an XHR (the core's Session.login) and then
// reloads the page, so the login must answer JSON, set the session
// cookies, and the RELOADED page must boot signed in.
func TestForum_LoginSetsTheSession(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	login := do(t, handler, http.MethodPost, "/forum/login",
		`{"identification":"writer","password":"password12345","remember":true}`, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())

	// The session cookies.
	var access, csrf *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		switch cookie.Name {
		case "forum_access_token":
			access = cookie
		case "forum_csrf":
			csrf = cookie
		default:
			// The login sets no other cookie.
		}
	}
	require.NotNil(t, access, "the login should set the access token cookie")
	require.True(t, access.HttpOnly, "the access token cookie should be HttpOnly")
	require.NotNil(t, csrf, "the login should set the CSRF cookie")
	require.False(t, csrf.HttpOnly, "the CSRF cookie must be readable by the SPA")

	cookies := []*http.Cookie{access, csrf}

	// The reloaded page must boot signed in. This is the part that was
	// broken: the boot payload carried no session, so the SPA always
	// booted anonymous and the login looked like it had done nothing.
	shell := do(t, handler, http.MethodGet, "/forum/", "", cookies)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	payload := bootPayloadOf(t, shell)
	require.Equal(t, user.Id, payload.Session.UserID,
		"the boot payload should carry the caller's id")
	require.NotEmpty(t, payload.Session.CSRFToken,
		"the boot payload should carry the CSRF token")

	// The caller must also be a RESOURCE in the payload: the frontend
	// reads the user from the store with `getById('users', userId)`, and
	// a relationship's identifier alone carries no attributes.
	var found bool
	for _, entry := range payload.Resources {
		resource, ok := entry.(map[string]any)
		require.True(t, ok, "a payload resource should be an object")

		if resource["type"] != "users" || resource["id"] != user.Id {
			continue
		}

		found = true
		attributes, ok := resource["attributes"].(map[string]any)
		require.True(t, ok, "the user resource should carry attributes")
		require.Equal(t, "writer", attributes["username"])
	}
	require.True(t, found, "the boot payload should carry the caller as a resource")
}

// TestForum_AnonymousBootHasNoSession pins the anonymous case: the
// session must be empty, not a broken value.
func TestForum_AnonymousBootHasNoSession(t *testing.T) {
	_, handler := newTestForum(t)

	shell := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	payload := bootPayloadOf(t, shell)
	require.Empty(t, payload.Session.UserID, "an anonymous caller has no id")
	require.Empty(t, payload.Session.CSRFToken, "an anonymous caller has no CSRF token")
}

// TestForum_LoginWithBadCredentialsIsRejected pins that a wrong
// password does not mint a session.
func TestForum_LoginWithBadCredentialsIsRejected(t *testing.T) {
	app, handler := newTestForum(t)
	testUser(t, app, "writer")

	login := do(t, handler, http.MethodPost, "/forum/login",
		`{"identification":"writer","password":"wrong","remember":true}`, nil)
	require.Equal(t, http.StatusUnauthorized, login.Code, login.Body.String())

	for _, cookie := range login.Result().Cookies() {
		require.NotEqual(t, "forum_access_token", cookie.Name,
			"a rejected login must not set the access token cookie")
	}
}

// TestForum_LogoutRedirects pins the logout contract.
//
// The frontend logs out with a FORM submission (the core's
// Session.logout builds a form and submits it), so the browser
// navigates to the response: it must be a redirect, not a JSON body.
func TestForum_LogoutRedirects(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	logout := do(t, handler, http.MethodPost, "/forum/logout", "", cookies)
	require.Equal(t, http.StatusFound, logout.Code, logout.Body.String())
	require.Equal(t, "/forum/", logout.Header().Get("Location"))

	// The session cookie is cleared.
	for _, cookie := range logout.Result().Cookies() {
		if cookie.Name == "forum_access_token" {
			require.Empty(t, cookie.Value, "the access token cookie should be cleared")
			require.Negative(t, cookie.MaxAge, "the access token cookie should expire")
		}
	}
}

// TestForum_LogoutClearsTheCookieButNotTheToken pins the logout's
// actual reach.
//
// The cookie is cleared, so the browser stops sending the session. The
// TOKEN itself survives: PocketBase's auth tokens are stateless JWTs
// with no server-side store to revoke them in, so a token that was
// captured keeps working until it expires (the `users` collection's
// authToken duration, five days by default).
//
// This is PocketBase's model, not the forum's — the wiki's logout has
// the same shape — but it is worth pinning, because "logged out" reads
// as "the credential is dead" and it is not.
func TestForum_LogoutClearsTheCookieButNotTheToken(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// The cookie is cleared.
	logout := do(t, handler, http.MethodPost, "/forum/logout", "", cookies)
	require.Equal(t, http.StatusFound, logout.Code)

	var cleared bool
	for _, cookie := range logout.Result().Cookies() {
		if cookie.Name == "forum_access_token" {
			cleared = true
			require.Empty(t, cookie.Value, "the access token cookie should be cleared")
		}
	}
	require.True(t, cleared, "the logout should clear the access token cookie")

	// The token itself still resolves — that is the documented limit.
	resolved, err := app.FindAuthRecordByToken(token, "auth")
	require.NoError(t, err, "the token should still resolve")
	require.Equal(t, user.Id, resolved.Id)
}

// TestForum_LogoutReturnIsRestricted pins that the logout's `return`
// parameter cannot be turned into an open redirect.
func TestForum_LogoutReturnIsRestricted(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	cases := []struct {
		name     string
		returnTo string
		expected string
	}{
		{"a path inside the forum", "/forum/d/1", "/forum/d/1"},
		{"the forum root", "/forum/", "/forum/"},
		{"an absolute url", "https://evil.example.com/", "/forum/"},
		{"a scheme-relative url", "//evil.example.com/", "/forum/"},
		{"outside the forum", "/community/memos", "/forum/"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logout := do(t, handler, http.MethodPost, "/forum/logout?return="+testCase.returnTo, "", cookies)
			require.Equal(t, http.StatusFound, logout.Code)
			require.Equal(t, testCase.expected, logout.Header().Get("Location"))
		})
	}
}
