// Package main is the riff binary entrypoint.
//
// riff is a PO-33 community site: a PocketBase-based backend serving
// memos, patches, samples, and spaces for PO-33 sampler creators.
package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/incurl/riff/internal/ratelimit"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"

	"github.com/incurl/riff/internal/profile"
	"github.com/incurl/riff/pb/api"
	"github.com/incurl/riff/pb/connect"
	"github.com/incurl/riff/pb/hooks"

	// Register our Go migrations in core.AppMigrations. The blank import
	// is intentional: each migration file uses an init() function to call
	// migrations.Register(...), which is what makes them discoverable to
	// the migratecmd plugin and to RunAllMigrations() on Bootstrap.
	_ "github.com/incurl/riff/pb/migrations"

	"github.com/incurl/riff/server/frontend"
	"github.com/incurl/riff/server/mcp"
)

// mcpSpecPath returns the path to the OpenAPI document the MCP
// service reads at startup. The default is the file `buf generate`
// is expected to produce; MCP_OPENAPI_SPEC lets operators (and
// tests) point at an alternative location without rebuilding the
// binary. The path is resolved against the process working
// directory before returning so a relative override and a
// relative default behave the same.
func mcpSpecPath() string {
	raw := os.Getenv("MCP_OPENAPI_SPEC")
	if raw == "" {
		raw = filepath.Join("proto", "gen", "openapi.yaml")
	}
	return raw
}

// envBool reports whether name is set in the process environment
// to a truthy value (1, true, yes, on — case-insensitive). The
// default is false so the rest of the binary sees "feature off"
// unless an operator explicitly opts in. Used today for
// RIFF_RATE_LIMIT.
func envBool(name string) bool {
	v := os.Getenv(name)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

func main() {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: "./pb_data",
	})

	// --- CLI plugins -----------------------------------------------------
	// The migratecmd plugin wires our Go migrations into PocketBase's
	// `migrate` subcommand and `automigrate` flag, mirroring what the
	// jsvm-backed JS migrations do for the standalone binary.
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: true,
		Dir:         "pb/migrations",
	})

	// --- Profile --------------------------------------------------------
	// Build a default profile so the MCP origin allow-list has
	// something to compare against. RIFF_INSTANCE_URL populates
	// the canonical URL the email dispatcher and MCP origin
	// check share; an empty value is the safe default that
	// disables both the email side and the cross-origin
	// allow-list.
	prof := buildProfileFromEnv()

	// --- Rate limiter ---------------------------------------------------
	// The Connect registration loop consults this limiter per
	// procedure (see pb/connect/ratelimit.go for the procedure
	// table). A nil limiter keeps the previous unbounded behaviour,
	// which is the default; operators that flip
	// Profile.RateLimit (today's only knob is RIFF_RATE_LIMIT=1
	// wired up here) get the conservative in-memory policy from
	// ratelimit.DefaultPolicy() instead. A persistent or
	// distributed limiter can be slotted in here without
	// touching pb/connect/ — the ratelimit.Limiter interface is
	// the seam.
	var rateLimiter ratelimit.Limiter
	if prof.RateLimit {
		rateLimiter = ratelimit.NewMemoryLimiter(ratelimit.DefaultPolicy())
	}

	// --- Business logic hooks -------------------------------------------
	// Register all event hooks (spaces cascade, memos author auto-
	// assign, hashtag parse, space-membership gates, realtime
	// broadcasts, etc.). See pb/hooks/hooks.go for the entry point
	// and the individual subpackages for behavior.
	hooks.Register(app)

	// --- Notification email dispatcher ---------------------------------
	// The Phase 5 inbox work introduced a notification email
	// dispatcher that turns new notification rows into SMTP
	// messages. It is feature-gated on RIFF_INSTANCE_URL because
	// the dispatcher's email bodies link to URLs under
	// prof.InstanceURL; without that, every email would have a
	// broken link and the dispatcher would no-op with a warn
	// log. Wire it after hooks.Register so the in-process
	// goroutine can find the email-setting row by collection
	// name.
	if prof.InstanceURL != "" {
		hooks.InstallEmailDispatcher(prof, app)
	}

	// --- Custom REST routes ---------------------------------------------
	// /api/po33/* endpoints (feed, space memos, invite, move).
	//
	// SetAdvertisedVersion applies RIFF_MEMOS_VERSION before any
	// route is mounted, so the very first /api/v1/status probe
	// an operator runs after restart already sees the new
	// string. The default is "0.21.0" so a missing env var is
	// a no-op rather than a silent regression.
	api.SetAdvertisedVersion(os.Getenv("RIFF_MEMOS_VERSION"))
	connect.SetAdvertisedVersion(os.Getenv("RIFF_MEMOS_VERSION"))
	api.Register(app, rateLimiter)

	// --- Connect/gRPC services ------------------------------------------
	// /memos.api.v1.*/* endpoints. The Memos React frontend
	// talks to these; this is what makes Phase 4 "drop the
	// existing frontend in unchanged" work.
	//
	// connect.Register bootstraps the install-wide
	// signed-URL signer (see
	// pb/connect/sketch_signer_bootstrap.go). The
	// same signer is shared with the REST handlers
	// in pb/api/po33_device_pull.go via
	// SetDevicePullSigner, so the device-pull
	// `/sketches/{memoId}/download` route verifies
	// against the same key the SPA-facing
	// SketchService.PushSketch RPC used to mint the
	// URL. The signer is a closure-driven pointer
	// so a slow OnServe bootstrap does not race a
	// fast first request: the OnServe hook runs
	// before the HTTP listener is bound.
	// RIFF_FTS routes memo content search through the SQLite FTS5
	// index instead of the LIKE scan. Set before Register so the memo
	// server picks the mode up at construction.
	connect.SetFTSSearch(prof.FTS)
	if err := connect.Register(app, rateLimiter); err != nil {
		log.Fatal(err)
	}

	// Share the connect-side signer with the
	// REST handlers in pb/api/po33_device_pull.go
	// so the device-pull /sketches/{memoId}/download
	// route verifies against the same key the
	// SPA-facing SketchService.PushSketch RPC
	// used to mint the URL. The OnServe hook
	// runs after the connect package's hook
	// (which is what mints the signer) so the
	// accessor returns a non-nil value.
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		api.SetDevicePullSigner(connect.InstallSignedURLSignerForREST())
		return e.Next()
	})

	// --- SPA static handler --------------------------------------------
	// Serve the Vite build of the Memos React app from the root of
	// the HTTP listener. The package embeds server/frontend/dist
	// at compile time (the `pnpm release` step must run before
	// `go build` for that to compile; CI does this in the
	// build-frontend job, see .github/workflows/release.yml).
	// PocketBase's more specific routes (/_/*, /api/*, /mcp) take
	// precedence over the package's {path...} catch-all, so this
	// only serves what the framework does not.
	frontend.Register(app)

	// --- MCP endpoint ---------------------------------------------------
	// /mcp (Streamable HTTP). The service builds its tool catalog
	// from the OpenAPI document the connect handlers advertise.
	// A missing spec surfaces as a fatal startup error that
	// tells the operator to run `buf generate` — see
	// server/mcp/service.go for the message. The profile is
	// forwarded so the origin allow-list can match the
	// canonical instance URL (server/mcp/origin.go).
	if err := mcp.Register(app, prof, mcpSpecPath()); err != nil {
		log.Fatal(err)
	}

	// --- Custom serve port ----------------------------------------------
	// PB_PORT overrides the default 8090 at the very start of the serve
	// event, before the listener is bound. When set, we bind on
	// 0.0.0.0:<PB_PORT> so the server is reachable from outside the
	// container (Fly.io, Docker, etc.). Empty / unset leaves the
	// built-in default (127.0.0.1:8090) intact.
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		if port := os.Getenv("PB_PORT"); port != "" {
			e.Server.Addr = "0.0.0.0:" + port
		}
		return e.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// buildProfileFromEnv reads the operator-facing env vars and
// returns a Profile with the corresponding fields populated.
//
// RIFF_INSTANCE_URL is the canonical external URL of the
// instance. Both the MCP origin allow-list and the notification
// email dispatcher link back to it, so a missing value is the
// single switch that turns off both features at once.
//
// RIFF_RATE_LIMIT flips the in-process rate limiter on. The
// knob is read here (and not on the Profile struct directly)
// so the env var name stays the operator-facing one while the
// profile type keeps its domain meaning.
func buildProfileFromEnv() *profile.Profile {
	p := &profile.Profile{
		InstanceURL: os.Getenv("RIFF_INSTANCE_URL"),
	}
	if envBool("RIFF_RATE_LIMIT") {
		p.RateLimit = true
	}
	if envBool("RIFF_FTS") {
		p.FTS = true
	}
	return p
}
