package bark

import (
	"strings"

	"github.com/incurl/riff/internal/mcptools"
)

// DefaultPrefix is the mount point Bark uses when the host does not pick one.
const DefaultPrefix = "/bark"

// MCPTools returns Bark's MCP tool declarations for the instance's own
// `/mcp` catalog.
//
// Bark already serves an MCP endpoint of its own (`/bark/mcp`, upstream
// bark-server's), which answers a single `notify` tool. Those tools are
// scoped to the module and take the device key as an argument or from the
// path; declaring them here as well puts push notifications in the SAME
// catalog as the memos, the wiki and the forum, so an agent that already
// holds a PocketBase token can send a notification without a second
// endpoint and a second credential.
//
// THE DEVICE KEY IS THE CREDENTIAL. Bark's HTTP surface has no accounts: a
// push is addressed by the device key and authenticates itself, exactly as
// the `/bark/{device_key}` routes do. So a caller of these tools does not
// need to be signed in — but knowing the key is the whole of the
// authorization, and the key is listed as a required argument for that
// reason.
//
// An empty prefix means DefaultPrefix.
func MCPTools(prefix string) []mcptools.Tool {
	p := normalisePrefix(prefix)

	return []mcptools.Tool{
		{
			Name:        "bark_push_notification",
			Title:       "Send a push notification",
			Description: "Send an iOS push notification to one Bark device. The device key is the credential: whoever holds it may send. Returns the delivery status.",
			Method:      "POST",
			Path:        p + "/push",
			Body: &mcptools.Body{
				Required: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"device_key", "body"},
					"properties": map[string]any{
						"device_key": map[string]any{"type": "string", "description": "The device's key, as registered by the Bark app."},
						"body":       map[string]any{"type": "string", "description": "The notification text."},
						"title":      map[string]any{"type": "string", "description": "The notification title."},
						"subtitle":   map[string]any{"type": "string", "description": "A subtitle, shown under the title."},
						"sound":      map[string]any{"type": "string", "description": "The notification sound name."},
						"level": map[string]any{
							"type":        "string",
							"description": "How urgent the notification is: `active` (the default), `timeSensitive`, `passive` or `critical`.",
							"enum":        []string{"active", "timeSensitive", "passive", "critical"},
						},
					},
				},
			},
		},
		{
			Name:        "bark_check_device",
			Title:       "Check a Bark device",
			Description: "Report whether a Bark device key is registered, so an agent can tell a typo from an undelivered notification.",
			Method:      "GET",
			Path:        p + "/register/{device_key}",
			Parameters: []mcptools.Parameter{
				mcptools.PathParameter("device_key", "The device's key."),
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
