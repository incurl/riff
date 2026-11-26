package bark

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/mcptools"
)

// TestMCPTools_PathsAreRouted pins the contract the MCP catalog relies on:
// every path a declared tool names is a route Bark serves, and it answers
// JSON rather than the SPA's HTML shell.
//
// The push tool's device key does not exist in this test, which is what
// makes the assertion useful: a routed path answers Bark's own JSON 400
// ("failed to get device token"), and an unrouted one would answer a 404
// here and the SPA shell in the real binary.
func TestMCPTools_PathsAreRouted(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	for _, tool := range MCPTools("") {
		t.Run(tool.Name, func(t *testing.T) {
			path := tool.Path
			for _, parameter := range tool.Parameters {
				if parameter.In == "path" {
					path = strings.ReplaceAll(path, "{"+parameter.Name+"}", "unknown-key")
				}
			}

			var body *strings.Reader
			if tool.Method != http.MethodGet {
				body = strings.NewReader(`{"device_key":"unknown-key","body":"Hello"}`)
			} else {
				body = strings.NewReader("")
			}

			request := httptest.NewRequest(tool.Method, path, body)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)

			shape := strings.TrimSpace(recorder.Body.String())
			require.NotEqual(t, http.StatusNotFound, recorder.Code,
				"%s %s is not routed", tool.Method, path)
			require.NotContains(t, shape, "<!doctype",
				"%s %s answered an HTML shell instead of Bark's JSON", tool.Method, path)
			require.True(t, strings.HasPrefix(shape, "{"),
				"%s %s must answer Bark's JSON envelope; got %s", tool.Method, path, shape)
		})
	}
}

// TestMCPTools_TheCatalogCoversBark checks the declarations themselves.
func TestMCPTools_TheCatalogCoversBark(t *testing.T) {
	tools := MCPTools("")
	require.Len(t, tools, 2)

	names := make(map[string]mcptools.Tool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = tool
		require.NotEmpty(t, tool.Description, "%s needs a description", tool.Name)
		require.True(t, strings.HasPrefix(tool.Path, "/bark/"),
			"%s must call Bark's own routes; got %s", tool.Name, tool.Path)
	}

	push, ok := names["bark_push_notification"]
	require.True(t, ok)
	require.NotNil(t, push.Body, "a push carries a body")
	require.Contains(t, push.Body.Schema, "required")

	check, ok := names["bark_check_device"]
	require.True(t, ok)
	require.Equal(t, http.MethodGet, check.Method)

	// A host that moves the mount point moves the tools with it.
	moved := MCPTools("/push")
	require.True(t, strings.HasPrefix(moved[0].Path, "/push/"))
}
