package leafwiki

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPI_LoginSetsASessionCookieAndMeReadsIt(t *testing.T) {
	_, app, handler := newTestAPI(t)
	testUser(t, app, "alice")

	login := doJSON(t, handler, http.MethodPost, "/wiki/api/auth/login", map[string]any{
		"identifier": "alice@example.com",
		"password":   "passw0rd!",
	}, nil)
	require.Equal(t, http.StatusOK, login.Code, "body: %s", login.Body.String())

	payload := decodeJSON[authResponse](t, login)
	assert.Equal(t, "alice", payload.User.Username)
	assert.Equal(t, "editor", payload.User.Role)
	assert.Greater(t, payload.AccessTokenExpiresAt, int64(0))

	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		switch cookie.Name {
		case accessCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		default:
			// Any other cookie is not part of this contract.
		}
	}
	require.NotNil(t, sessionCookie, "login must set the session cookie")
	require.NotNil(t, csrfCookie, "login must set the CSRF cookie")
	assert.True(t, sessionCookie.HttpOnly)
	assert.False(t, csrfCookie.HttpOnly, "the SPA has to read the CSRF cookie")

	me := httptest.NewRecorder()
	meRequest := httptest.NewRequest(http.MethodGet, "/wiki/api/auth/me", nil)
	meRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(me, meRequest)
	require.Equal(t, http.StatusOK, me.Code)
	assert.Equal(t, "alice", decodeJSON[authUser](t, me).Username)

	// Anonymous callers get JSON null, not an error.
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/wiki/api/auth/me", nil))
	require.Equal(t, http.StatusOK, anonymous.Code)
	assert.Equal(t, "null", anonymous.Body.String())

	// The cookie alone is enough to write, but only with the CSRF header.
	pageBody, err := json.Marshal(map[string]any{"title": "Session Page", "slug": "session-page", "kind": "page"})
	require.NoError(t, err)

	withoutCSRF := httptest.NewRecorder()
	requestWithoutCSRF := httptest.NewRequest(http.MethodPost, "/wiki/api/pages", bytes.NewReader(pageBody))
	requestWithoutCSRF.Header.Set("Content-Type", "application/json")
	requestWithoutCSRF.AddCookie(sessionCookie)
	handler.ServeHTTP(withoutCSRF, requestWithoutCSRF)
	assert.Equal(t, http.StatusForbidden, withoutCSRF.Code)

	withCSRF := httptest.NewRecorder()
	requestWithCSRF := httptest.NewRequest(http.MethodPost, "/wiki/api/pages", bytes.NewReader(pageBody))
	requestWithCSRF.Header.Set("Content-Type", "application/json")
	requestWithCSRF.Header.Set(csrfHeaderName, csrfCookie.Value)
	requestWithCSRF.AddCookie(sessionCookie)
	requestWithCSRF.AddCookie(csrfCookie)
	handler.ServeHTTP(withCSRF, requestWithCSRF)
	require.Equal(t, http.StatusCreated, withCSRF.Code, "body: %s", withCSRF.Body.String())

	// A wrong password is rejected.
	bad := doJSON(t, handler, http.MethodPost, "/wiki/api/auth/login", map[string]any{
		"identifier": "alice",
		"password":   "wrong",
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, bad.Code)

	// Logout clears the session cookie.
	logout := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/wiki/api/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(logout, logoutRequest)
	require.Equal(t, http.StatusOK, logout.Code)

	cleared := false
	for _, cookie := range logout.Result().Cookies() {
		if cookie.Name == accessCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared, "logout must expire the session cookie")
}

func TestAPI_SuperusersAreAdmins(t *testing.T) {
	_, app, handler := newTestAPI(t)

	superusers, err := app.FindCollectionByNameOrId("_superusers")
	require.NoError(t, err)
	admin := core.NewRecord(superusers)
	admin.Set("email", "admin@example.com")
	admin.Set("password", "passw0rd!")
	require.NoError(t, app.Save(admin))

	login := doJSON(t, handler, http.MethodPost, "/wiki/api/auth/login", map[string]any{
		"identifier": "admin@example.com",
		"password":   "passw0rd!",
	}, nil)
	require.Equal(t, http.StatusOK, login.Code, "body: %s", login.Body.String())
	assert.Equal(t, "admin", decodeJSON[authResponse](t, login).User.Role)
}
