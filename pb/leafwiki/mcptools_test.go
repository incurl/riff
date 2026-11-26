package leafwiki

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/mcptools"
)

// TestMCPTools_PathsAreRouted pins the contract the MCP catalog relies on:
// every path a declared tool names is a route the wiki serves.
//
// The distinction that matters is JSON versus the SPA shell. This harness
// mounts only the wiki, so an unrouted path answers a 404; in the real
// binary the SPA's catch-all answers it with index.html and a 200, which is
// why `server/mcp` refuses an HTML reply with an explicit message.
func TestMCPTools_PathsAreRouted(t *testing.T) {
	_, _, handler := newTestAPI(t)

	for _, tool := range MCPTools("") {
		t.Run(tool.Name, func(t *testing.T) {
			path := tool.Path
			for _, parameter := range tool.Parameters {
				if parameter.In == "path" {
					// The id does not have to exist: a routed path answers a JSON
					// 404 for an unknown page, which is what the assertion reads.
					path = strings.ReplaceAll(path, "{"+parameter.Name+"}", "000000000000000")
				}
			}

			body := any(nil)
			if tool.Body != nil {
				body = map[string]any{"title": "Routed", "content": "Body"}
			}

			recorder := doJSON(t, handler, tool.Method, path, body, nil)
			shape := strings.TrimSpace(recorder.Body.String())
			require.NotContains(t, shape, "<!doctype",
				"%s %s answered the SPA shell instead of the wiki API", tool.Method, path)
			require.True(t, strings.HasPrefix(shape, "{") || strings.HasPrefix(shape, "[") || shape == "",
				"%s %s must answer JSON; got %s", tool.Method, path, shape)
		})
	}
}

// TestMCPTools_TheCatalogCoversTheWikiSurface checks the declarations
// themselves: snake_case names, a description each, and the wiki's own paths.
func TestMCPTools_TheCatalogCoversTheWikiSurface(t *testing.T) {
	tools := MCPTools("")
	require.NotEmpty(t, tools)

	names := make(map[string]mcptools.Tool, len(tools))
	for _, tool := range tools {
		require.NotContains(t, names, tool.Name, "a duplicate tool name would be a startup error")
		names[tool.Name] = tool
		require.NotEmpty(t, tool.Description, "%s needs a description", tool.Name)
		require.True(t, strings.HasPrefix(tool.Path, "/wiki/api/"),
			"%s must call the wiki's own API; got %s", tool.Name, tool.Path)
	}

	for _, expected := range []string{
		"wiki_list_pages",
		"wiki_get_page",
		"wiki_search_pages",
		"wiki_create_page",
		"wiki_update_page",
		"wiki_delete_page",
		"wiki_list_tags",
	} {
		require.Contains(t, names, expected)
	}

	// The mount point is a parameter, so a host that moves the wiki moves the
	// tools with it.
	moved := MCPTools("/docs")
	require.True(t, strings.HasPrefix(moved[0].Path, "/docs/api/"))
}
