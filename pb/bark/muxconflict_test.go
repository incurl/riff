package bark

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/require"
)

// TestMethodLessMCPWouldPanic proves that the route table's explicit
// method lists are load-bearing.
//
// This test registers upstream's method-less MCP pattern next to the V1
// wildcard route and asserts that the standard library REFUSES it. If a
// future change to net/http made the method-less pattern acceptable
// again, this test would start failing — which would mean the explicit
// method lists in routes.go could be relaxed (or, more likely, that the
// upstream port would silently start working).
//
// The point is that the port's method lists are not a stylistic choice:
// they are the only thing standing between a verbatim copy of upstream's
// route table and a server that panics on startup.
func TestMethodLessMCPWouldPanic(t *testing.T) {
	require.Panics(t, func() {
		mux := http.NewServeMux()
		// Upstream's V1 route...
		mux.HandleFunc("GET /bark/{device_key}", func(http.ResponseWriter, *http.Request) {})
		// ...and upstream's method-less MCP route.
		mux.HandleFunc("/bark/mcp", func(http.ResponseWriter, *http.Request) {})
	}, "a method-less /bark/mcp must conflict with GET /bark/{device_key}")
}

// TestMethodLessMCPDeviceKeyWouldPanic is the same check for the
// device-scoped MCP route, which collides with the two-segment V1 route.
func TestMethodLessMCPDeviceKeyWouldPanic(t *testing.T) {
	require.Panics(t, func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /bark/{device_key}/{body}", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("/bark/mcp/{device_key}", func(http.ResponseWriter, *http.Request) {})
	}, "a method-less /bark/mcp/{device_key} must conflict with GET /bark/{device_key}/{body}")
}

// TestExplicitMethodsAvoidTheConflict is the positive counterpart: the
// port's actual registration must build cleanly.
func TestExplicitMethodsAvoidTheConflict(t *testing.T) {
	require.NotPanics(t, func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /bark/{device_key}", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("GET /bark/{device_key}/{body}", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("GET /bark/mcp", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("POST /bark/mcp", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("DELETE /bark/mcp", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("GET /bark/mcp/{device_key}", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("POST /bark/mcp/{device_key}", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("DELETE /bark/mcp/{device_key}", func(http.ResponseWriter, *http.Request) {})
	})
}

// TestBarkRoutesDoNotCollideWithTheSPACatchAll pins that the Bark
// surface coexists with the method-less SPA catch-all that riff mounts
// at "/{path...}".
//
// This is the same class of conflict that AGENTS.md documents for the
// SPA catch-all: two method-less patterns that overlap would panic at
// startup, taking the whole instance down.
func TestBarkRoutesDoNotCollideWithTheSPACatchAll(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)

	require.NotPanics(t, func() {
		r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
		svc.mount(r)
		// The SPA catch-all, exactly as server/frontend/embed.go
		// mounts it.
		r.Any("/{path...}", func(*core.RequestEvent) error { return nil })
		_, err := r.BuildMux()
		require.NoError(t, err)
	})
}
