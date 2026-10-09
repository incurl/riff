// service.go is a port of github.com/usememos/memos/server/mcp/service.go
// with three deviations from upstream:
//
//  1. NewMCPService takes http.Handler instead of *echo.Echo, matching the
//     change in adapter.go. Any HTTP handler riff exposes can be passed.
//  2. The OpenAPI spec is loaded from disk at a caller-supplied path instead
//     of from an embedded byte slice. Riff's proto/gen does not currently
//     produce an openapi.yaml artifact; until it does, callers must run
//     `buf generate` and pass the resulting path. The default empty path
//     causes NewMCPService to return an explicit error at startup.
//  3. RegisterRoutes is replaced with Handler(), which returns an http.Handler
//     that applies the origin check, body size limit, and then dispatches
//     into the SDK streamable HTTP handler. Riff's main.go can mount it via
//     the standard library mux.

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"

	"github.com/incurl/riff/internal/profile"
)

// maxMCPRequestBytes caps the /mcp request body. Upstream tracks
// server/api.MaxAPIRequestBytes (256 MiB) because every MCP tool call is
// forwarded in-process through the API routes. Riff's API does not yet have
// an analogous constant, so the value is inlined here pending the larger
// server/api port (see docs/design/mcp-port.md).
//
// The SDK enforces its own 4 MiB default, so the value is passed to the
// transport as well; otherwise attachment uploads over MCP fail with 413
// before the server sees them.
const maxMCPRequestBytes int64 = 256 << 20

// toolCatalogTTL is the freshness hint advertised on tools/list and
// server/discover results. The catalog is fixed at startup, so clients may cache
// it for a long time instead of re-listing on every session.
const toolCatalogTTL = 24 * time.Hour

// MCPService serves the OpenAPI-driven MCP endpoint.
type MCPService struct {
	profile *profile.Profile

	operationsByTool map[string]*registeredOperation
	handler          http.Handler
}

// NewMCPService creates an MCP service backed by the in-process HTTP routes
// supplied via api. The OpenAPI specification is read from specPath on every
// call; pass the path of the file produced by `buf generate` (typically
// proto/gen/openapi.yaml). An empty specPath causes NewMCPService to return
// an error explaining the dependency.
func NewMCPService(p *profile.Profile, api http.Handler, specPath string) (*MCPService, error) {
	if specPath == "" {
		return nil, errSpecPathRequired
	}
	spec, err := loadMCPServiceOpenAPISpec(specPath)
	if err != nil {
		return nil, err
	}
	registry, err := buildOperationRegistry(spec)
	if err != nil {
		return nil, err
	}
	tools, operationsByTool, err := buildCuratedTools(registry)
	if err != nil {
		return nil, err
	}

	version := "dev"
	if p != nil && p.Version != "" {
		version = p.Version
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "memos",
		Version: version,
	}, &sdkmcp.ServerOptions{
		// The tool catalog never changes after startup, so do not advertise
		// listChanged: new-protocol clients would otherwise hold a
		// subscriptions/listen stream open for notifications that never come.
		// Leaving Logging unset also drops the deprecated logging capability.
		Capabilities: &sdkmcp.ServerCapabilities{
			Tools: &sdkmcp.ToolCapabilities{},
		},
	})
	server.AddReceivingMiddleware(catalogCacheMiddleware)

	adapter := newAPIAdapter(api)
	for _, tool := range tools {
		operation := operationsByTool[tool.Name]
		server.AddTool(tool, newMCPToolHandler(adapter, operation))
	}

	streamableHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{
		// Stateless is also what lets the SDK serve protocol version
		// 2026-07-28; stateful handlers negotiate down to 2025-11-25.
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: maxMCPRequestBytes,
		// riff is typically served behind a reverse proxy with the app bound to a
		// loopback address while the public Host header is a real domain. The SDK's
		// DNS-rebinding guard treats that shape as an attack and rejects every
		// request with 403 ("invalid Host header"). Disable it and rely on riff's
		// own Origin/Host allowlist (see Handler -> isAllowedMCPOrigin) for
		// CSRF / DNS-rebinding protection instead.
		DisableLocalhostProtection: true,
	})
	return &MCPService{
		profile:          p,
		operationsByTool: operationsByTool,
		handler:          streamableHandler,
	}, nil
}

// catalogCacheMiddleware stamps the static-catalog TTL onto results the SDK
// would otherwise mark immediately stale (ttlMs: 0).
func catalogCacheMiddleware(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
		result, err := next(ctx, method, request)
		if err != nil {
			return result, err
		}
		switch typed := result.(type) {
		case *sdkmcp.ListToolsResult:
			typed.TTLMs = int(toolCatalogTTL.Milliseconds())
		case *sdkmcp.DiscoverResult:
			typed.TTLMs = int(toolCatalogTTL.Milliseconds())
		default:
			// Other results carry no catalog TTL.
		}
		return result, nil
	}
}

// loadMCPServiceOpenAPISpec is a thin alias for loadOpenAPISpec, kept under
// the service.go name to match upstream so future porters reading both
// files in parallel land on the same entry point. It may be removed if the
// service acquires a non-trivial OpenAPI transform.
func loadMCPServiceOpenAPISpec(path string) (*openAPISpec, error) {
	return loadOpenAPISpec(path)
}

func newMCPToolHandler(adapter *apiAdapter, operation *registeredOperation) sdkmcp.ToolHandler {
	return func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		arguments := map[string]any{}
		if request.Params != nil && len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return newToolErrorResult(errors.Wrap(err, "failed to decode MCP tool arguments").Error()), nil
			}
		}
		if err := validateToolArguments(operation.InputSchema, arguments); err != nil {
			return newToolErrorResult(err.Error()), nil
		}

		authorization := ""
		if request.Extra != nil {
			authorization = request.Extra.Header.Get("Authorization")
		}
		return adapter.execute(ctx, operation.Operation, arguments, authorization)
	}
}

// Handler returns the http.Handler that should be mounted at /mcp. The
// returned handler enforces the Origin/Host allowlist, caps request bodies
// to maxMCPRequestBytes, and then dispatches into the SDK's streamable HTTP
// handler. Riff's main.go calls mcp.Register, which is the production
// caller: it binds the handler onto the PocketBase router from inside the
// OnServe hook. The function is exported so tests and alternative
// embeddings (e.g. a reverse-proxy layer in front of /mcp) can mount the
// service without going through PocketBase. See register.go and
// docs/design/mcp-port.md for the integration plan.
func (s *MCPService) Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !isAllowedMCPOrigin(request.Host, request.Header.Get("Origin"), s.profile) {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, maxMCPRequestBytes)
		s.handler.ServeHTTP(writer, request)
	})
}
