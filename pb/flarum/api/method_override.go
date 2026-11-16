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

// effectiveMethod returns the method a request should be treated as.
//
// A POST carrying the override header is treated as the method the
// header names. Any other request keeps its own method: the override
// only applies to a POST, because that is the only method the
// frontend rewrites.
func effectiveMethod(e *core.RequestEvent) string {
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

// requireMethod rejects a request whose effective method is not the
// expected one.
//
// It answers 405 with an `Allow` header, which is what the method
// mismatch deserves: the route exists, but not for this verb.
func requireMethod(e *core.RequestEvent, expected string) error {
	if effectiveMethod(e) == expected {
		return nil
	}

	e.Response.Header().Set("Allow", expected)
	return newAPIError(http.StatusMethodNotAllowed, CodeValidationFailed,
		"The method is not allowed for this route.", nil)
}
