package flarum

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/mcptools"
)

// TestMCPTools_PathsAreRouted pins the contract the MCP catalog relies on:
// every path a declared tool names is a route the forum actually serves.
//
// It matters because an unrouted path in this codebase is not a 404 — the
// SPA's catch-all answers it with index.html and a 200 — so a tool whose
// path drifted from the mount point would still "succeed" at the HTTP layer
// and fail as a JSON decode at the tool layer, where the message says
// nothing about the cause.
func TestMCPTools_PathsAreRouted(t *testing.T) {
	_, handler := newTestForum(t)

	for _, tool := range MCPTools(DefaultBasePath) {
		t.Run(tool.Name, func(t *testing.T) {
			path := tool.Path
			for _, parameter := range tool.Parameters {
				if parameter.In == "path" {
					path = replacePathParameter(path, parameter.Name, "000000000000000")
				}
			}

			recorder := do(t, handler, tool.Method, path, requestBodyFor(tool), nil)

			// The routed/unrouted distinction is JSON versus the SPA shell, not
			// 200 versus 404: a routed path may legitimately answer 404 (the id
			// in this test does not exist), and an unrouted one answers 200 with
			// index.html because the SPA catch-all is the least specific route.
			body := strings.TrimSpace(recorder.Body.String())
			require.NotContains(t, body, "<!doctype",
				"%s %s answered the SPA shell instead of the API", tool.Method, path)
			require.True(t, strings.HasPrefix(body, "{") || body == "",
				"%s %s must answer JSON; got %s", tool.Method, path, body)
		})
	}
}

// TestMCPTools_TheCatalogCoversTheForumSurface is a light sanity check on the
// declarations themselves: the tools exist, they are snake_case (the catalog
// refuses anything else) and they name the forum's own paths.
func TestMCPTools_TheCatalogCoversTheForumSurface(t *testing.T) {
	tools := MCPTools(DefaultBasePath)
	require.NotEmpty(t, tools)

	names := make(map[string]mcptools.Tool, len(tools))
	for _, tool := range tools {
		require.NotContains(t, names, tool.Name, "a duplicate tool name would be a startup error")
		names[tool.Name] = tool
		require.NotEmpty(t, tool.Description, "%s needs a description", tool.Name)
		require.Contains(t, tool.Path, "/forum/api/", "%s must call the forum's own API", tool.Name)
	}

	for _, expected := range []string{
		"forum_list_discussions",
		"forum_get_discussion",
		"forum_list_posts",
		"forum_list_tags",
		"forum_create_discussion",
		"forum_reply",
	} {
		require.Contains(t, names, expected)
	}

	// The mount point is a parameter, because the forum is the one module
	// whose base path an operator can move.
	moved := MCPTools("/community")
	require.Contains(t, moved[0].Path, "/community/api/",
		"the declarations follow the configured base path")
}

// TestForum_ABearerCallerWritesWithoutCSRF pins the CSRF rule the MCP
// service depends on.
//
// The MCP adapter replays a tool call in-process with the caller's
// `Authorization` header and no cookie jar, so a forum write over MCP has no
// CSRF cookie to double-submit. That is correct, not a hole: a bearer token
// travels in an explicit header a cross-site page cannot set, so there is no
// ambient credential to forge. A COOKIE caller still needs the token, which
// the other half of this test pins.
func TestForum_ABearerCallerWritesWithoutCSRF(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "bearer_writer")
	token, err := author.NewAuthToken()
	require.NoError(t, err)

	body := `{"data":{"type":"discussions","attributes":{"title":"From an agent","content":"Hello"}}}`

	// No cookie, no CSRF header: the bearer token alone.
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, nil,
		map[string]string{"Authorization": "Bearer " + token})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	// The same request WITHOUT a credential is still refused — the branch
	// skips the double submit for token callers, it does not remove the
	// requirement for the cookie ones.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, nil, nil)
	require.NotEqual(t, http.StatusCreated, recorder.Code,
		"an anonymous write must not be accepted; got %s", recorder.Body.String())

	// A cookie caller without the CSRF header is refused too.
	cookieCaller := testUser(t, app, "cookie_writer")
	cookieToken, err := cookieCaller.NewAuthToken()
	require.NoError(t, err)
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body,
		[]*http.Cookie{{Name: "forum_access_token", Value: cookieToken}}, nil)
	require.Equal(t, http.StatusForbidden, recorder.Code,
		"a cookie caller still needs the CSRF double submit; got %s", recorder.Body.String())
}

// replacePathParameter substitutes one `{name}` placeholder.
func replacePathParameter(path, name, value string) string {
	return strings.ReplaceAll(path, "{"+name+"}", value)
}

// requestBodyFor returns the smallest body a tool's route accepts.
//
// The routing test only cares that the path reaches a handler: a valid body
// keeps the answer a validation error rather than a decode error, and an
// empty one keeps the read tools unchanged.
func requestBodyFor(tool mcptools.Tool) string {
	if tool.Body == nil {
		return ""
	}

	switch tool.Name {
	case "forum_create_discussion":
		return `{"data":{"type":"discussions","attributes":{"title":"Routed","content":"Body"}}}`
	case "forum_reply":
		return `{"data":{"type":"posts","attributes":{"content":"Body"},"relationships":{"discussion":{"data":{"type":"discussions","id":"000000000000000"}}}}}`
	default:
		return `{}`
	}
}
