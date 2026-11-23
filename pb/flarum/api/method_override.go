package api

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// methodOverrideHeader is the header the frontend uses to send a
// method it cannot send directly.
//
// The reused Flarum frontend turns every method other than GET and
// POST into a POST with the intended method in this header, because
// "not all servers and clients support" PATCH or DELETE (see the
// core's `Application.request`). Upstream's API carries a
// `FakeHttpMethods` middleware that reads it back; this port has to do
// the same, or the frontend's DELETE requests reach the API as POSTs
// and the route does not match.
const methodOverrideHeader = "X-HTTP-Method-Override"

// EffectiveMethod returns the method a request should be treated as.
//
// A POST carrying the override header is treated as the method the
// header names. Any other request keeps its own method: the override
// only applies to a POST, because that is the only method the
// frontend rewrites.
//
// It is exported because a PLUGIN route needs it too: PocketBase's
// router matches on the real method before any handler runs, so a
// plugin that answers a DELETE has to register its route for POST as
// well and resolve the effective method itself.
func EffectiveMethod(e *core.RequestEvent) string {
	if e.Request.Method != http.MethodPost {
		return e.Request.Method
	}

	override := strings.ToUpper(strings.TrimSpace(e.Request.Header.Get(methodOverrideHeader)))
	switch override {
	case http.MethodDelete, http.MethodPatch, http.MethodPut:
		return override
	default:
		return e.Request.Method
	}
}

// RequireMethod rejects a request whose effective method is not the
// expected one.
//
// It answers 405 with an `Allow` header, which is what the method
// mismatch deserves: the route exists, but not for this verb.
//
// It is exported for the plugins, which register their routes for both
// verbs and resolve the effective method themselves.
func RequireMethod(e *core.RequestEvent, expected string) error {
	if EffectiveMethod(e) == expected {
		return nil
	}

	e.Response.Header().Set("Allow", expected)
	return newAPIError(http.StatusMethodNotAllowed, CodeValidationFailed,
		"The method is not allowed for this route.", nil)
}
