package mcp

import (
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/profile"
)

// Register mounts the MCP endpoint at /mcp on the given PocketBase
// app. It is the public integration point main.go uses to wire the
// server/mcp package into the binary.
//
// p is forwarded to NewMCPService so the MCP origin allow-list
// (server/mcp/origin.go) can compare browser Origin headers
// against the instance's canonical URL. Without it, any
// Origin outside the request Host is rejected as a cross-site
// forgery attempt; with it, a same-instance UI running on the
// canonical origin works as expected. nil is tolerated for tests
// that do not care about origin matching — production callers
// must pass the live profile.
//
// specPath is the on-disk location of the OpenAPI document the
// service reads at startup to derive its tool catalog. Riff's
// `buf generate` does not currently produce an openapi.yaml
// artifact; the constructor returns an explicit error in that
// case, so a missing file surfaces as a startup failure with an
// actionable message. See DECISIONS.md and docs/design/mcp-port.md
// for the integration rationale.
//
// options are passed through to NewMCPService. The one that matters here is
// WithTools: the wiki, the forum and Bark declare their own tools, because
// they are hand-written surfaces with no OpenAPI document to derive a
// catalog from.
//
// The MCP service needs to forward tool calls into the same HTTP
// handler the public API serves, so service construction is
// deferred to the OnServe hook (the handler only exists after
// e.Router.BuildMux() has been called). Returning an error from
// the OnServe hook fails the binary startup the same way a
// missing spec at registration time would.
//
// Register does not start the server. The caller still has to
// call app.Start() after all Register-style functions have run.
func Register(app core.App, p *profile.Profile, specPath string, options ...Option) error {
	if specPath == "" {
		// NewMCPService also rejects an empty specPath, but
		// surfacing the error here keeps the failure mode
		// consistent with the rest of the binary: invalid
		// config is caught before OnServe fires.
		return errSpecPathRequired
	}
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// BuildMux assembles the PocketBase router into the
		// same http.Handler that e.Server.Handler is set to
		// during PocketBase's own OnServe step. Calling it
		// here gives the MCP service the in-process API
		// target its adapter dispatches tool calls into.
		// It is called a second time inside the framework
		// step; the result is deterministic, so the second
		// assignment is a no-op.
		apiHandler, err := e.Router.BuildMux()
		if err != nil {
			return err
		}

		svc, err := NewMCPService(p, apiHandler, specPath, options...)
		if err != nil {
			return err
		}
		mcpHandler := svc.Handler()

		// The endpoint answers at `/mcp`, and at `/mcp/` too.
		//
		// The trailing-slash spelling is not cosmetic: several MCP clients
		// normalise the URL they were configured with to end in a slash, and
		// an unrouted path is not a 404 here — the SPA's catch-all is the
		// least specific pattern in the mux, so `/mcp/` was answered with
		// index.html and a 200, which the client then parses as a JSON-RPC
		// reply.
		//
		// A pattern ending in "/" is a SUBTREE in `http.ServeMux`, so the
		// second registration also matches `/mcp/anything`. Only the two
		// spellings of the endpoint are it; anything deeper answers a 404
		// rather than reaching the handler, which would otherwise treat every
		// sub-path as the endpoint.
		serveMCP := func(re *core.RequestEvent) error {
			switch re.Request.URL.Path {
			case "/mcp", "/mcp/":
				mcpHandler.ServeHTTP(re.Response, re.Request)
				return nil
			default:
				return re.NotFoundError("The MCP endpoint is /mcp.", nil)
			}
		}

		e.Router.Any("/mcp", serveMCP)
		e.Router.Any("/mcp/", serveMCP)
		return e.Next()
	})
	return nil
}
