package api

import (
	"fmt"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// customBuilder returns the hand-written handler
// registered for the given builder key. The 2026-10-17
// design has only one builder: "not_implemented",
// which returns 501 for routes that upstream Memos
// exposed but riff has not yet implemented (e.g.
// /users/{name}/usage). Future builder keys
// (webhook signing secret helper, avatar upload,
// custom shape handlers) are added to the switch
// below.
func customBuilder(app core.App, builder string) func(*core.RequestEvent) error {
	switch builder {
	case "not_implemented":
		return notImplementedHandler()
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
