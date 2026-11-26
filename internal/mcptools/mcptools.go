// Package mcptools describes the MCP tools a module declares for itself.
//
// The MCP service derives its catalog from the generated OpenAPI document,
// which covers the Connect/gRPC surface (`/api/v1/...`). Three modules are
// NOT generated from a proto document — the wiki (an upstream LeafWiki
// port), the forum (Flarum's JSON:API) and Bark (a push-notification
// server) — so their tools have to be declared by hand.
//
// This package is that declaration, and nothing else: a tool is the same
// shape as an OpenAPI operation (method, path, parameters, optional JSON
// body) with no behaviour attached. `server/mcp` turns a declaration into
// the same tool an OpenAPI operation becomes and executes it through the
// same in-process adapter, so a declared tool authenticates, authorizes and
// reports errors exactly like the HTTP surface it calls. A module therefore
// declares its tools next to the routes they call, instead of teaching the
// MCP server what a wiki or a forum is.
//
// It lives in `internal/` because both sides need it and neither may import
// the other: `pb/leafwiki` (and the forum, and Bark) are siblings of
// `server/mcp` in the import graph, not a chain.
package mcptools

// Tool is one MCP tool a module declares.
//
// The zero value is not usable: Name, Description, Method and Path are
// required, and the MCP service refuses a declaration that omits one.
type Tool struct {
	// Name is the tool name the client sees. It must be snake_case and
	// unique across the whole catalog, curated tools included — a
	// duplicate is a startup error, not a silently shadowed tool.
	Name string

	// Title is the human-readable name. Derived from Name when empty.
	Title string

	// Description tells an agent what the tool does and what the caller
	// needs to be allowed to do. It is the ONLY thing most agents read
	// before calling, so it is not a summary of the code: name the
	// permission, the authentication and the effect.
	Description string

	// Method is the HTTP method: GET, POST, PUT, PATCH or DELETE.
	//
	// It is also what the tool's annotations are derived from, and the
	// derivation is the same one the curated tools get: GET is read-only
	// and idempotent, DELETE is destructive and idempotent, everything
	// else mutates.
	Method string

	// Path is the request path, with `{name}` placeholders for path
	// parameters. It is the module's OWN mount prefix plus the API
	// path — `/wiki/api/tree`, not `/api/tree`.
	Path string

	// Parameters are the path and query parameters. A `{placeholder}`
	// in Path needs a matching parameter with In == "path".
	Parameters []Parameter

	// Body is the JSON request body; nil when the tool sends none.
	//
	// A body is passed to the tool as one nested `body` argument, which
	// is the same shape the OpenAPI-derived tools use — a client written
	// against those needs no special case for a declared tool.
	Body *Body
}

// Parameter is one path or query parameter.
type Parameter struct {
	// Name is the parameter name as the HTTP surface spells it, which is
	// not always snake_case: the forum filters are `filter[q]` and
	// `page[offset]`, and renaming them would send the request somewhere
	// the server does not look.
	Name string

	// In is "path" or "query".
	In string

	// Description is what the value means. An agent reads it.
	Description string

	// Required marks the parameter as required in the tool's input
	// schema. A path parameter must be required: the request cannot be
	// built without it.
	Required bool

	// Schema is the parameter's JSON Schema. It defaults to a string,
	// because most of these are identifiers, slugs and search terms —
	// a value that needs a different type must say so.
	Schema map[string]any
}

// Body is a tool's JSON request body.
type Body struct {
	// Required marks the body as required. A tool whose body carries the
	// whole payload (a new page, a new discussion) wants this; a tool
	// that only sends optional hints does not.
	Required bool

	// Schema is the body's JSON Schema.
	Schema map[string]any
}

// PathParameter is a shorthand for a required string path parameter.
//
// Every declared path parameter is a record id or a slug, so the long form
// is rarely worth writing out.
func PathParameter(name, description string) Parameter {
	return Parameter{
		Name:        name,
		In:          "path",
		Description: description,
		Required:    true,
	}
}

// QueryParameter is a shorthand for an optional string query parameter.
func QueryParameter(name, description string) Parameter {
	return Parameter{
		Name:        name,
		In:          "query",
		Description: description,
		Schema:      map[string]any{"type": "string"},
	}
}
