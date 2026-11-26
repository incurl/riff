package flarum

import (
	"strings"

	"github.com/incurl/riff/internal/mcptools"
)

// MCPTools returns the forum's MCP tool declarations.
//
// The forum speaks Flarum's JSON:API and has no OpenAPI document, so its
// tools are declared here, next to the routes they call (`api/routes.go`
// registers exactly these paths). The MCP service executes each call
// in-process with the caller's `Authorization` header, and the forum's
// session resolver already accepts a PocketBase bearer token — so the
// permissions the forum itself enforces are the ones that answer: a member
// may start a discussion and reply, a moderator may pin and lock, and an
// anonymous caller may only read what the forum shows to guests.
//
// An empty basePath means DefaultBasePath.
func MCPTools(basePath string) []mcptools.Tool {
	api := normaliseBasePath(basePath) + "/api"

	return []mcptools.Tool{
		{
			Name:        "forum_list_discussions",
			Title:       "List discussions",
			Description: "List the forum's discussions, newest activity first. Signed-out callers see what the forum shows to guests; a private tag's discussions are the moderator's.",
			Method:      "GET",
			Path:        api + "/discussions",
			Parameters: []mcptools.Parameter{
				{
					Name:        "filter[q]",
					In:          "query",
					Description: "Search the discussion titles and their first posts.",
					Schema:      map[string]any{"type": "string"},
				},
				{
					Name:        "filter[tag]",
					In:          "query",
					Description: "Only discussions carrying this tag, by slug or id.",
					Schema:      map[string]any{"type": "string"},
				},
				{
					Name:        "sort",
					In:          "query",
					Description: "The order: `-lastPostedAt` (the default), `-commentCount`, `-createdAt`, `createdAt`, `title` or `-title`.",
					Schema: map[string]any{
						"type": "string",
						"enum": []string{"-lastPostedAt", "-commentCount", "-createdAt", "createdAt", "title", "-title"},
					},
				},
				{
					Name:        "page[offset]",
					In:          "query",
					Description: "How many discussions to skip, for paging.",
					Schema:      map[string]any{"type": "integer", "minimum": 0},
				},
				{
					Name:        "page[limit]",
					In:          "query",
					Description: "How many discussions to return (default 20).",
					Schema:      map[string]any{"type": "integer", "minimum": 1},
				},
			},
		},
		{
			Name:        "forum_get_discussion",
			Title:       "Get discussion",
			Description: "Read one discussion: its title, tags, author and counters. The posts are a separate call (forum_list_posts).",
			Method:      "GET",
			Path:        api + "/discussions/{id}",
			Parameters: []mcptools.Parameter{
				mcptools.PathParameter("id", "The discussion id (or its id-prefixed slug)."),
			},
		},
		{
			Name:        "forum_list_posts",
			Title:       "List posts",
			Description: "List the posts of one discussion, in order, with their rendered HTML and their author.",
			Method:      "GET",
			Path:        api + "/posts",
			Parameters: []mcptools.Parameter{
				{
					Name:        "filter[discussion]",
					In:          "query",
					Description: "The discussion id whose posts to read.",
					Required:    true,
					Schema:      map[string]any{"type": "string"},
				},
				{
					Name:        "page[offset]",
					In:          "query",
					Description: "How many posts to skip, for paging through a long thread.",
					Schema:      map[string]any{"type": "integer", "minimum": 0},
				},
				{
					Name:        "page[limit]",
					In:          "query",
					Description: "How many posts to return (default 20).",
					Schema:      map[string]any{"type": "integer", "minimum": 1},
				},
			},
		},
		{
			Name:        "forum_create_discussion",
			Title:       "Start a discussion",
			Description: "Start a discussion with its first post. Needs a signed-in caller — a bearer token, either a PocketBase auth token or a riff `memos_pat_*` personal access token — holding the forum's start-discussion permission (the member group has it by default). Tags are not set here — a moderator sets them afterwards.",
			Method:      "POST",
			Path:        api + "/discussions",
			Body: &mcptools.Body{
				Required: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"data"},
					"properties": map[string]any{
						"data": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"attributes"},
							"properties": map[string]any{
								"type": map[string]any{"type": "string", "description": "Always `discussions`."},
								"attributes": map[string]any{
									"type":                 "object",
									"additionalProperties": false,
									"required":             []string{"title", "content"},
									"properties": map[string]any{
										"title":   map[string]any{"type": "string", "description": "The discussion title."},
										"content": map[string]any{"type": "string", "description": "The first post's Markdown body."},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			Name:        "forum_reply",
			Title:       "Reply to a discussion",
			Description: "Add a post to an existing discussion. Needs a signed-in caller (a PocketBase auth token or a riff `memos_pat_*` token) holding the forum's reply permission. A locked discussion refuses a reply unless the caller may also unlock it.",
			Method:      "POST",
			Path:        api + "/posts",
			Body: &mcptools.Body{
				Required: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"data"},
					"properties": map[string]any{
						"data": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"attributes", "relationships"},
							"properties": map[string]any{
								"type": map[string]any{"type": "string", "description": "Always `posts`."},
								"attributes": map[string]any{
									"type":                 "object",
									"additionalProperties": false,
									"required":             []string{"content"},
									"properties": map[string]any{
										"content": map[string]any{"type": "string", "description": "The reply's Markdown body."},
									},
								},
								"relationships": map[string]any{
									"type":                 "object",
									"additionalProperties": false,
									"required":             []string{"discussion"},
									"properties": map[string]any{
										"discussion": map[string]any{
											"type":                 "object",
											"additionalProperties": false,
											"required":             []string{"data"},
											"properties": map[string]any{
												"data": map[string]any{
													"type":                 "object",
													"additionalProperties": false,
													"required":             []string{"type", "id"},
													"properties": map[string]any{
														"type": map[string]any{"type": "string", "description": "Always `discussions`."},
														"id":   map[string]any{"type": "string", "description": "The discussion to reply to."},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			Name:        "forum_list_tags",
			Title:       "List tags",
			Description: "List the forum's tags with their colour, slug and discussion counts. Use a slug with `filter[tag]` on forum_list_discussions.",
			Method:      "GET",
			Path:        api + "/tags",
		},
	}
}

// normaliseBasePath turns an optional base path into the mount path the
// forum's routes are registered under.
func normaliseBasePath(basePath string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(basePath), "/")
	if trimmed == "" {
		return DefaultBasePath
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}
