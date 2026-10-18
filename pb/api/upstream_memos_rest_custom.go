package api

import (
	"fmt"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// customBuilder returns the hand-written handler
// registered for the given builder key. The 2026-10-17
// design has one builder: "not_implemented", which
// returns 501 for routes that upstream Memos exposed
// but riff has not yet implemented (e.g. /users/{name}/usage).
// The 2026-10-18 addition added three keys for the
// legacy /api/v1/... surface: the version-probe
// handlers (`v1_status`, `v1_ping`) and the AIP-style
// custom-verb dispatcher (`v1_user_stats`).
func customBuilder(app core.App, builder string) func(*core.RequestEvent) error {
	switch builder {
	case "not_implemented":
		return notImplementedHandler()
	case "v1_status":
		return v1StatusHandler(app)
	case "v1_ping":
		return v1PingHandler(app)
	case "v1_user_stats":
		return v1UserStatsHandler(app)
	default:
		return unknownFlavorHandler("custom:" + builder)
	}
}

// notImplementedHandler returns a closure that emits
// a 501 with a JSON error body. The Memos frontend
// does not hit any of these routes in normal use;
// they are only here to keep the route table
// complete and document the gap in the OpenAPI
// fragment emitted by the registration loop.
func notImplementedHandler() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		e.Response.Header().Set("Content-Type", "application/json")
		e.Response.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(e.Response,
			fmt.Sprintf(`{"code":12,"message":"%s %s is not implemented by riff"}`,
				e.Request.Method, e.Request.URL.Path))
		return nil
	}
}
