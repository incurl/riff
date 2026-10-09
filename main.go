// Package main is the riff binary entrypoint.
//
// riff is a PO-33 community site: a PocketBase-based backend serving
// memos, patches, samples, and spaces for PO-33 sampler creators.
package main

import (
	"log"
	"os"
	"path/filepath"

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

	// --- Business logic hooks -------------------------------------------
	// Register all event hooks (spaces cascade, memos author auto-
	// assign, hashtag parse, space-membership gates, realtime
	// broadcasts, etc.). See pb/hooks/hooks.go for the entry point
	// and the individual subpackages for behavior.
	hooks.Register(app)

	// --- Profile --------------------------------------------------------
	// Build a default profile so the MCP origin allow-list has
	// something to compare against. A future change can populate
	// Profile.InstanceURL from env or flags (RIFF_INSTANCE_URL);
	// for now, the default zero-value profile keeps the existing
	// "reject every cross-origin browser request" behaviour, which
	// is the safe failure mode until an operator explicitly opts
	// in to a broader allow-list.
	prof := &profile.Profile{}

	// --- Custom REST routes ---------------------------------------------
	// /api/po33/* endpoints (feed, space memos, invite, move).
	api.Register(app)

	// --- Connect/gRPC services ------------------------------------------
	// /memos.api.v1.*/* endpoints. The Memos React frontend
	// talks to these; this is what makes Phase 4 "drop the
	// existing frontend in unchanged" work.
	if err := connect.Register(app); err != nil {
		log.Fatal(err)
	}

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
