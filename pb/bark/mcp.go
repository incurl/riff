// mcp.go exposes the Bark push as an MCP `notify` tool.
//
// Upstream bark-server serves this with mark3labs/mcp-go. This package
// uses the Model Context Protocol Go SDK that riff already depends on
// (github.com/modelcontextprotocol/go-sdk), so no new dependency is
// introduced and the endpoint behaves like riff's own /mcp.
//
// Two endpoints are served, matching upstream:
//
//	/bark/mcp             — the device key is a tool argument
//	/bark/mcp/{device_key} — the device key is taken from the path
//
// The path form wins over the argument form, exactly as upstream's
// context value does.
package bark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// mountMCP registers the Bark MCP endpoints.
//
// The routes are registered per method (GET, POST, DELETE) rather than
// method-less. Go's http.ServeMux — which PocketBase's router is built
// on — panics when a method-less pattern overlaps a method-specific one,
// and upstream's method-less "/mcp" collides with the "/{device_key}" V1
// routes:
//
//	pattern "/bark/mcp" conflicts with pattern "GET /bark/{device_key}"
func (s *server) mountMCP(r *router.Router[*core.RequestEvent]) {
	p := s.prefix

	handler := s.newMCPHandler()

	r.GET(p+"/mcp", handler)
	r.POST(p+"/mcp", handler)
	r.DELETE(p+"/mcp", handler)

	r.GET(p+"/mcp/{device_key}", handler)
	r.POST(p+"/mcp/{device_key}", handler)
	r.DELETE(p+"/mcp/{device_key}", handler)
}

// newMCPHandler builds the HTTP handler that serves the Bark MCP
// endpoints.
//
// The handler is created lazily per request path so the device key from
// the URL can be threaded into the tool call: the SDK's streamable HTTP
// handler dispatches on the request, and the path value is read from it.
func (s *server) newMCPHandler() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		deviceKey := e.Request.PathValue("device_key")

		server := sdkmcp.NewServer(&sdkmcp.Implementation{
			Name:    "bark",
			Version: s.opts.Version,
		}, &sdkmcp.ServerOptions{
			// The tool catalog never changes after startup, so
			// listChanged is not advertised: a client that
			// subscribed to it would hold a stream open for
			// notifications that never come.
			Capabilities: &sdkmcp.ServerCapabilities{
				Tools: &sdkmcp.ToolCapabilities{},
			},
		})

		server.AddTool(newNotifyTool(), s.newNotifyHandler(deviceKey))

		streamable := sdkmcp.NewStreamableHTTPHandler(
			func(*http.Request) *sdkmcp.Server { return server },
			&sdkmcp.StreamableHTTPOptions{
				// Stateless matches upstream's "disable streaming"
				// configuration and keeps the endpoint free of
				// long-lived server-to-client connections.
				Stateless:           true,
				JSONResponse:        true,
				MaxRequestBodyBytes: maxMCPRequestBytes,
				// riff is typically served behind a reverse proxy
				// with the app bound to a loopback address while
				// the public Host header is a real domain. The
				// SDK's DNS-rebinding guard treats that shape as an
				// attack and answers 403. The Bark endpoints are
				// authenticated by the device key in the tool
				// arguments, so the guard is disabled here for
				// the same reason it is disabled on riff's own
				// /mcp endpoint.
				DisableLocalhostProtection: true,
			},
		)

		streamable.ServeHTTP(e.Response, e.Request)
		return nil
	}
}

// maxMCPRequestBytes caps the size of an MCP request body.
const maxMCPRequestBytes = 1024 * 1024

// newNotifyTool describes the `notify` tool.
//
// The input schema mirrors upstream's mcp-go tool options field for
// field, so an MCP client that was written against bark-server sees the
// same arguments. The `device_key` argument is only needed on the
// generic endpoint; the path endpoint pre-fills it.
func newNotifyTool() *sdkmcp.Tool {
	properties := map[string]any{
		"title":    map[string]any{"type": "string", "description": "Notification title"},
		"subtitle": map[string]any{"type": "string", "description": "Notification subtitle"},
		"body":     map[string]any{"type": "string", "description": "Notification content"},
		"markdown": map[string]any{"type": "string", "description": "Basic Markdown notification content. Overrides body."},
		"level": map[string]any{
			"type":        "string",
			"description": "Notification level",
			"enum":        []any{"critical", "active", "timeSensitive", "passive"},
		},
		"volume": map[string]any{
			"type":        "number",
			"description": "Alert volume for important notification",
			"default":     5,
			"maximum":     10,
			"minimum":     0,
		},
		"badge":      map[string]any{"type": "number", "description": "Badge number"},
		"call":       map[string]any{"type": "string", "description": "Set to '1' to repeat the notification ringtone"},
		"sound":      map[string]any{"type": "string", "description": "Notification sound"},
		"icon":       map[string]any{"type": "string", "description": "Notification icon URL"},
		"image":      map[string]any{"type": "string", "description": "Notification image URL"},
		"group":      map[string]any{"type": "string", "description": "Notification group"},
		"isArchive":  map[string]any{"type": "string", "description": "Set to '1' to save the notification or any other value to skip saving"},
		"ttl":        map[string]any{"type": "number", "description": "Time to live in seconds for archived messages; expired items are automatically deleted"},
		"url":        map[string]any{"type": "string", "description": "Click action URL"},
		"copy":       map[string]any{"type": "string", "description": "Text to copy on copy action"},
		"device_key": map[string]any{"type": "string", "description": "Device Key"},
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	return &sdkmcp.Tool{
		Name:        "notify",
		Description: "Send a notification to a device via Bark",
		InputSchema: schema,
	}
}

// newNotifyHandler returns the handler for the `notify` tool.
//
// pathDeviceKey is the device key taken from the URL, if any. It wins
// over the tool argument, matching upstream's context precedence.
func (s *server) newNotifyHandler(pathDeviceKey string) sdkmcp.ToolHandler {
	return func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args := map[string]any{}
		if request.Params != nil && len(request.Params.Arguments) > 0 {
			// The arguments arrive as an opaque JSON value, so they
			// are re-encoded and decoded into a map. A malformed
			// argument set is a TOOL error (an MCP error result),
			// not a transport error, so it is reported through the
			// result rather than as a Go error.
			raw, err := json.Marshal(request.Params.Arguments)
			if err != nil {
				return newToolErrorResult("Invalid arguments format"), nil //nolint:nilerr // an MCP tool error is a result, not a transport error
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return newToolErrorResult("Invalid arguments format"), nil //nolint:nilerr // an MCP tool error is a result, not a transport error
			}
		}

		// Resolve the device key: the path wins over the argument.
		deviceKey := pathDeviceKey
		if deviceKey == "" {
			if v, ok := args["device_key"].(string); ok {
				deviceKey = v
			}
		}
		if deviceKey == "" {
			return newToolErrorResult("device_key is required"), nil
		}
		args["device_key"] = deviceKey

		params := pushParams{}
		for k, v := range args {
			params[k] = v
		}

		code, err := s.pushService().push(params)
		if err != nil {
			return newToolErrorResult(fmt.Sprintf("Failed to send notification: %v (code %d)", err, code)), nil
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "Notification sent successfully"},
			},
		}, nil
	}
}

// newToolErrorResult reports a tool execution error.
//
// Error results carry text content only: a structured payload would have
// to satisfy the tool's output schema, and an {"error": ...} object does
// not.
func newToolErrorResult(message string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: message},
		},
		IsError: true,
	}
}
