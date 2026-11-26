package mcp

import (
	"context"
	"net/http"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/mcptools"
)

// The declared-tool seam: the wiki, the forum and Bark publish their tools as
// `mcptools.Tool` declarations, and these tests pin both halves — that a
// declaration becomes a catalog entry indistinguishable from a curated one,
// and that a malformed declaration fails at STARTUP rather than on the first
// call.

func testWikiTool() mcptools.Tool {
	return mcptools.Tool{
		Name:        "wiki_get_page",
		Description: "Read one wiki page. Public: no credential is needed.",
		Method:      "GET",
		Path:        "/wiki/api/pages/{id}",
		Parameters: []mcptools.Parameter{
			mcptools.PathParameter("id", "The page id."),
		},
	}
}

func TestDeclaredToolBecomesACatalogEntry(t *testing.T) {
	tools, operations, err := buildDeclaredTools([]mcptools.Tool{testWikiTool()})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	tool := tools[0]
	require.Equal(t, "wiki_get_page", tool.Name)
	require.Equal(t, "Wiki Get Page", tool.Title, "the title is derived from the name")
	require.Equal(t, "GET", tool.Meta["method"])
	require.Equal(t, "/wiki/api/pages/{id}", tool.Meta["path"])
	require.True(t, tool.Annotations.ReadOnlyHint, "a GET tool is read-only")
	schema, ok := tool.InputSchema.(jsonSchema)
	require.True(t, ok, "the input schema is a JSON Schema object")
	require.Equal(t, false, schema["additionalProperties"],
		"the input schema is closed, like the curated ones")

	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, properties, "id")
	require.Equal(t, []string{"id"}, schema["required"],
		"a path parameter is required, because the request cannot be built without it")

	require.Contains(t, operations, "wiki_get_page")
	require.Equal(t, "wiki_get_page", operations["wiki_get_page"].OperationID)
}

func TestDeclaredToolCarriesItsBodyAsOneArgument(t *testing.T) {
	tool := mcptools.Tool{
		Name:        "forum_create_discussion",
		Description: "Start a discussion. Requires the discussion.start permission.",
		Method:      "POST",
		Path:        "/forum/api/discussions",
		Body: &mcptools.Body{
			Required: true,
			Schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"title", "content"},
				"properties": map[string]any{
					"title":   map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
			},
		},
	}

	tools, _, err := buildDeclaredTools([]mcptools.Tool{tool})
	require.NoError(t, err)

	schema, ok := tools[0].InputSchema.(jsonSchema)
	require.True(t, ok, "the input schema is a JSON Schema object")
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, properties, "body", "a body is one nested argument, as the curated tools have it")
	require.Equal(t, []string{"body"}, schema["required"])
	require.False(t, tools[0].Annotations.ReadOnlyHint, "a POST tool mutates")
}

func TestDeclaredToolExecutesThroughTheSameAdapter(t *testing.T) {
	var seen struct {
		method        string
		path          string
		authorization string
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen.method = request.Method
		seen.path = request.URL.Path
		seen.authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"page1","title":"Setup"}`))
	})

	_, operations, err := buildDeclaredTools([]mcptools.Tool{testWikiTool()})
	require.NoError(t, err)

	result, err := newAPIAdapter(handler).execute(context.Background(),
		operations["wiki_get_page"].Operation, map[string]any{"id": "page1"}, "Bearer token")
	require.NoError(t, err)
	require.False(t, result.IsError)

	require.Equal(t, http.MethodGet, seen.method)
	require.Equal(t, "/wiki/api/pages/page1", seen.path)
	require.Equal(t, "Bearer token", seen.authorization,
		"the caller's credential is forwarded, so the module authorizes the call")
	require.Equal(t, map[string]any{"id": "page1", "title": "Setup"}, result.StructuredContent)
}

func TestDeclaredToolReportsAnUnroutedPath(t *testing.T) {
	// The SPA's catch-all answers an unrouted path with index.html and a 200,
	// which used to surface as "failed to decode API JSON response".
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte("<!doctype html><html></html>"))
	})

	_, operations, err := buildDeclaredTools([]mcptools.Tool{testWikiTool()})
	require.NoError(t, err)

	result, err := newAPIAdapter(handler).execute(context.Background(),
		operations["wiki_get_page"].Operation, map[string]any{"id": "page1"}, "")
	require.NoError(t, err)
	require.True(t, result.IsError)

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "not an API route")
}

func TestDeclaredToolValidationFailsAtStartup(t *testing.T) {
	cases := map[string]struct {
		mutate   func(*mcptools.Tool)
		expected string
	}{
		"camel case name": {
			mutate:   func(tool *mcptools.Tool) { tool.Name = "WikiGetPage" },
			expected: "snake_case",
		},
		"no description": {
			mutate:   func(tool *mcptools.Tool) { tool.Description = " " },
			expected: "description is required",
		},
		"unsupported method": {
			mutate:   func(tool *mcptools.Tool) { tool.Method = "OPTIONS" },
			expected: "unsupported method",
		},
		"relative path": {
			mutate:   func(tool *mcptools.Tool) { tool.Path = "wiki/api/pages/{id}" },
			expected: `must start with "/"`,
		},
		"query in path": {
			mutate:   func(tool *mcptools.Tool) { tool.Path = "/wiki/api/pages/{id}?full=true" },
			expected: "must not carry a query",
		},
		"placeholder without a parameter": {
			mutate:   func(tool *mcptools.Tool) { tool.Path = "/wiki/api/pages/{pageId}/assets" },
			expected: `the path placeholder "pageId" has no parameter`,
		},
		"unknown parameter location": {
			mutate: func(tool *mcptools.Tool) {
				tool.Parameters = []mcptools.Parameter{{Name: "id", In: "header"}}
			},
			expected: `only "path" and "query" are supported`,
		},
		"duplicate parameter": {
			mutate: func(tool *mcptools.Tool) {
				tool.Parameters = append(tool.Parameters, mcptools.PathParameter("id", "Again."))
			},
			expected: "declared twice",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			tool := testWikiTool()
			testCase.mutate(&tool)

			_, _, err := buildDeclaredTools([]mcptools.Tool{tool})
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.expected)
		})
	}
}

func TestDeclaredToolCannotShadowACuratedOne(t *testing.T) {
	curated := []*sdkmcp.Tool{{
		Name:  "wiki_get_page",
		Title: "Wiki Get Page",
	}}
	curatedOperations := map[string]*registeredOperation{
		"wiki_get_page": {ToolName: "wiki_get_page"},
	}

	_, _, err := declareCatalog(curated, curatedOperations, []mcptools.Tool{testWikiTool()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides",
		"a collision is a startup error: the client would see one tool and the server would keep the other")
}
