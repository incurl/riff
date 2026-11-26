package leafwiki

import (
	"strings"

	"github.com/incurl/riff/internal/mcptools"
)

// DefaultPrefix is the mount point the wiki uses when the host does not pick
// one. It is exported so the MCP tool declarations and the registration agree
// about where the API lives.
const DefaultPrefix = "/wiki"

// MCPTools returns the wiki's MCP tool declarations.
//
// The wiki is one of the three modules that are not generated from a proto
// document, so its tools cannot be derived from OpenAPI; they are declared
// here, next to the routes they call (`routes.go` registers exactly these
// paths). The MCP service turns each declaration into a tool and executes the
// call in-process against the SPA-facing route with the caller's
// `Authorization` header, so the wiki's OWN rules decide: reads are public
// (LeafWiki's model), and a write needs a signed-in editor — a PocketBase
// bearer token or a session — unless the operator set
// `RIFF_WIKI_PUBLIC_WRITE=1`.
//
// An empty prefix means DefaultPrefix, so a caller that does not move the
// mount point does not have to repeat it.
func MCPTools(prefix string) []mcptools.Tool {
	base := normalisePrefix(prefix)
	api := base + "/api"

	return []mcptools.Tool{
		{
			Name:        "wiki_list_pages",
			Title:       "List wiki pages",
			Description: "List the wiki's page tree: every page's id, title, slug, path and nesting. Public — no credential is needed.",
			Method:      "GET",
			Path:        api + "/tree",
		},
		{
			Name:        "wiki_get_page",
			Title:       "Get wiki page",
			Description: "Read one wiki page, including its Markdown content and its version (pass that version back on an update so a concurrent edit is refused rather than overwritten). Public.",
			Method:      "GET",
			Path:        api + "/pages/{id}",
			Parameters: []mcptools.Parameter{
				mcptools.PathParameter("id", "The page id, as listed by wiki_list_pages."),
			},
		},
		{
			Name:        "wiki_search_pages",
			Title:       "Search wiki pages",
			Description: "Full-text search over the wiki's pages and their tags. Public.",
			Method:      "GET",
			Path:        api + "/search",
			Parameters: []mcptools.Parameter{
				{
					Name:        "q",
					In:          "query",
					Description: "The text to look for.",
					Required:    true,
					Schema:      map[string]any{"type": "string"},
				},
				{
					Name:        "limit",
					In:          "query",
					Description: "How many hits to return, 1-100 (default 10).",
					Schema:      map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				},
				{
					Name:        "offset",
					In:          "query",
					Description: "How many hits to skip, for paging.",
					Schema:      map[string]any{"type": "integer", "minimum": 0},
				},
			},
		},

		{
			Name:        "wiki_create_page",
			Title:       "Create wiki page",
			Description: "Create an empty page (write its content with wiki_update_page). Needs a signed-in editor — pass a bearer token (a PocketBase auth token or a riff `memos_pat_*` personal access token). Superusers and editors may write; anonymous callers only when the operator enabled public writing.",
			Method:      "POST",
			Path:        api + "/pages",
			Body: &mcptools.Body{
				Required: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"title"},
					"properties": map[string]any{
						"title":    map[string]any{"type": "string", "description": "The page title, shown in the tree and as the link text."},
						"slug":     map[string]any{"type": "string", "description": "The URL slug. Derived from the title when omitted."},
						"kind":     map[string]any{"type": "string", "description": "`page` (the default) or `section` (a folder whose index page holds the content)."},
						"parentId": map[string]any{"type": "string", "description": "The parent page id, or `root` for a top-level page."},
					},
				},
			},
		},
		{
			Name:        "wiki_update_page",
			Title:       "Update wiki page",
			Description: "Replace a page's Markdown content and, optionally, its title, slug and tags. Needs a signed-in editor. Pass the `version` you read so a concurrent edit is refused instead of overwritten; omit it to force the write.",
			Method:      "PUT",
			Path:        api + "/pages/{id}",
			Parameters: []mcptools.Parameter{
				mcptools.PathParameter("id", "The page id."),
			},
			Body: &mcptools.Body{
				Required: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"content"},
					"properties": map[string]any{
						"content": map[string]any{"type": "string", "description": "The page's Markdown body, in full."},
						"title":   map[string]any{"type": "string", "description": "A new title. Omit to keep the current one."},
						"slug":    map[string]any{"type": "string", "description": "A new URL slug. Omit to keep the current one."},
						"tags": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "The page's tags, in full. Omit to keep the current ones.",
						},
						"version": map[string]any{"type": "string", "description": "The version the edit was based on. A mismatch is refused as a conflict."},
					},
				},
			},
		},
		{
			Name:        "wiki_delete_page",
			Title:       "Delete wiki page",
			Description: "Delete a page and everything below it. Needs a signed-in editor. Destructive: the Markdown files are removed, not archived.",
			Method:      "DELETE",
			Path:        api + "/pages/{id}",
			Parameters: []mcptools.Parameter{
				mcptools.PathParameter("id", "The page id."),
			},
		},
		{
			Name:        "wiki_list_tags",
			Title:       "List wiki tags",
			Description: "List the tags in use across the wiki, with how many pages carry each. Public.",
			Method:      "GET",
			Path:        api + "/tags",
			Parameters: []mcptools.Parameter{
				{
					Name:        "q",
					In:          "query",
					Description: "Restrict the facets to tags whose name contains this text.",
					Schema:      map[string]any{"type": "string"},
				},
			},
		},
	}
}

// normalisePrefix turns an optional prefix into the mount path the routes are
// registered under.
func normalisePrefix(prefix string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(prefix), "/")
	if trimmed == "" {
		return DefaultPrefix
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}
