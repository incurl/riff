// Package frontend embeds the riff SPA build and serves it from
// the root of the PocketBase HTTP listener.
//
// The SPA itself lives under web/ and is built with Vite. The
// build target is server/frontend/dist (one level up from web/),
// per web/package.json's `release` script:
//
//	vite build --mode release --outDir=../server/frontend/dist --emptyOutDir
//
// This package does two things:
//
//  1. Compile-time embed of that dist tree via //go:embed. The
//     resulting files are baked into the riff binary, mirroring
//     how PocketBase's own admin UI ships. CI produces the
//     artifact in a separate job (the `build-frontend` step in
//     .github/workflows/release.yml) and downloads it into this
//     path before `go build` runs in `build-binaries`; local
//     developers must run `cd web && pnpm release` before
//     `go run .` or `go build` for the embed to compile.
//
//  2. A single static route registered on the PocketBase router
//     at OnServe time. The route uses {path...} (the Go 1.22
//     http.ServeMux wildcard that PocketBase wraps) and falls
//     back to index.html on misses so the SPA's client-side
//     router handles deep links. PocketBase's own routes
//     (/_/* for the admin UI, /api/* for the REST + Connect
//     endpoints, /mcp for the MCP service) win on specificity,
//     so this catch-all only handles what PocketBase does not.
package frontend

import (
	"embed"
	"io/fs"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// distFS holds the build output of the SPA. The all: prefix
// means dotfiles (for example a future .well-known asset) are
// included; without it, `go:embed` silently drops them.
//
//go:embed all:dist
var distFS embed.FS

// Register mounts the SPA on the PocketBase app's HTTP listener
// at OnServe time. The route is GET /{path...} with index.html
// fallback; PocketBase's more specific routes (/_/*, /api/*,
// /mcp) take precedence and are unaffected.
//
// Register is the only entry point main.go needs. The embedded
// fs.FS is exposed (via [FS]) for tests that want to inspect
// the bundled assets without starting the server.
func Register(app core.App) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// fs.Sub on a directory we just declared with
		// go:embed cannot fail in practice; panic rather
		// than swallow the mistake.
		panic("server/frontend: dist subdirectory missing from embed: " + err.Error())
	}
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// indexFallback=true forwards missing paths to
		// index.html so the SPA's client-side router can
		// handle deep links (e.g. /m/123, /explore).
		e.Router.GET("/{path...}", apis.Static(sub, true))
		return e.Next()
	})
}

// FS returns the embedded SPA tree rooted at dist/. The
// returned fs.FS is read-only; callers must not mutate it.
// Exposed for tests; production code uses Register.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Same rationale as Register: a missing sub is a
		// build-time error, not a runtime one.
		panic("server/frontend: dist subdirectory missing from embed: " + err.Error())
	}
	return sub
}
