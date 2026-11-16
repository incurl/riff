package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
)

// TestForum_AccessTokensAreListed pins the shape of the access-token
// list.
//
// The frontend's user security page fetches the session tokens and the
// developer tokens SEPARATELY and joins the two answers with
// `Promise.all([...]).then(([sessions, developerTokens]) => …)`. The
// destructuring fails with "e is not iterable" unless each answer is
// an iterable, so the answer has to be a JSON:API ARRAY and not a
// single resource object.
func TestForum_AccessTokensAreListed(t *testing.T) {
	app, handler := newTestForum(t)

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
}

// TestForum_AccessTokensNeedAuthentication pins the auth gate.
//
// The upstream endpoint is authenticated.
func TestForum_AccessTokensNeedAuthentication(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/access-tokens", "", nil)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
}

// TestForum_UnroutedAPIPathIsAJSONAPINotFound pins the API's own
// catch-all.
//
// The forum document is served at `/forum/api` (no trailing slash) and
// at the routes below it. It is NOT served as a subtree: in Go's
// http.ServeMux a trailing slash is an anonymous ".../" wildcard, so
// registering `GET /forum/api/` answers EVERY unrouted path under
// /forum/api/ with the forum document.
//
// That is worse than a 404. The document's `data` is a single object
// rather than an array, so a caller that asked for a list gets one
// object back, and the `Promise.all([...])` that destructures the
// answer fails with "e is not iterable" — the symptom the user sees
// on the user security page.
func TestForum_UnroutedAPIPathIsAJSONAPINotFound(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/access-tokenz", "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	require.Equal(t, api.ContentType, recorder.Header().Get("Content-Type"))

	var document api.ErrorDocument
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
	require.Len(t, document.Errors, 1)
	require.Equal(t, "404", document.Errors[0].Status)
	require.Equal(t, api.CodeNotFound, document.Errors[0].Code)
}

// TestForum_ForumDocumentIsNotASubtree pins that the forum document is
// served at its own path and nowhere else.
//
// The frontend reads the forum document out of the BOOT PAYLOAD, so
// nothing in the SPA fetches it — the route exists for external
// clients, and it must not answer for the rest of the API.
func TestForum_ForumDocumentIsNotASubtree(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/", "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())

	// The document itself is still served at the exact path.
	recorder = do(t, handler, http.MethodGet, "/forum/api", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}
