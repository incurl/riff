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
// legacy /api/v1/... V1 surface: the version-probe
// handlers (`v1_status`, `v1_ping`) and the AIP-style
// custom-verb dispatcher (`v1_user_stats`). The
// 2026-10-19 addition added eleven keys for the
// legacy /api/v1/... V0 (Memos 0.21) surface that
// the MoeMemos client (a V0+V1 hybrid) still uses;
// see upstream_memos_v0_rest.go for the full table.
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
	case "v1_get_user_or_stats":
		return v1GetUserOrStatsHandler(app)
	// V0 (Memos 0.21) handlers. See
	// upstream_memos_v0_rest_handlers.go for
	// the per-handler shape conversion.
	case "v0_user_me":
		return v0UserMeHandler(app)
	case "v0_memo_list":
		return v0MemoListHandler(app)
	case "v0_memo_all_list":
		return v0MemoAllListHandler(app)
	case "v0_memo_create":
		return v0MemoCreateHandler(app)
	case "v0_memo_organizer":
		return v0MemoOrganizerHandler(app)
	case "v0_memo_patch":
		return v0MemoPatchHandler(app)
	case "v0_memo_delete":
		return v0MemoDeleteHandler(app)
	case "v0_resource_list":
		return v0ResourceListHandler(app)
	case "v0_resource_upload":
		return v0ResourceUploadHandler(app)
	case "v0_resource_delete":
		return v0ResourceDeleteHandler(app)
	case "v0_tag_list":
		return v0TagListHandler(app)
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
