// Package statistics is the forum's Statistics plugin.
//
// It mirrors the DATA half of upstream Flarum's `flarum/statistics`
// extension: `GET /api/statistics`, admin-only, answering either the
// lifetime totals or a time series for one entity.
//
// Upstream's extension is an admin-panel page with charts. The endpoint is
// the part another surface can build on — and the part whose contract
// matters — so this port implements it and leaves the admin widgets as a
// follow-up (recorded in DECISIONS.md). The plugin therefore declares NO
// asset bundle: shipping a bundle that drew nothing would be the empty-tag
// problem the module's startup check exists to surface.
package statistics

import (
	"context"

	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/repository"
)

// PluginID is the plugin's id.
const PluginID = "flarum/statistics"

// Version is the plugin's version.
const Version = "1.0.0"

// The entities the endpoint can report on.
//
// They are upstream's three (`users`, `discussions` and comment `posts`),
// named by the same keys so a client that speaks upstream's dialect works
// unchanged.
const (
	// EntityUsers counts accounts.
	EntityUsers = "users"
	// EntityDiscussions counts discussions.
	EntityDiscussions = "discussions"
	// EntityPosts counts comment posts.
	EntityPosts = "posts"
)

// Plugin is the Statistics plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// entities maps the reported entity onto its collection's real name.
	//
	// It is resolved once, at Register time, because the collection prefix
	// is the host's choice and a handler cannot look it up without the
	// module's registry.
	entities map[string]string
}

// New returns the Statistics plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Statistics",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#6932d1",
				Glyph:           "fas fa-chart-bar",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	p.entities = map[string]string{
		EntityUsers:       repository.SharedUsersCollection,
		EntityDiscussions: ext.Registry.Physical("discussions"),
		EntityPosts:       ext.Registry.Physical("posts"),
	}

	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/statistics",
		Handler: p.showStatisticsHandler,
	})

	return nil
}

// Boot runs the plugin's startup work.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error { return nil }

// Shutdown releases the plugin's resources.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error { return nil }
