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

	"github.com/pocketbase/dbx"

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

// statEntity is one reported entity.
type statEntity struct {
	// collection is the entity's physical collection name.
	//
	// It is resolved once, at Register time, because the collection prefix
	// is the host's choice and a handler cannot look it up without the
	// module's registry.
	collection string
	// filterColumn and filterValue narrow the counted rows; an empty column
	// counts every row.
	//
	// Upstream counts only COMMENT posts (`Post::where('type', 'comment')`),
	// so a future post type does not inflate the number the dashboard
	// shows. It is a COLUMN/VALUE pair rather than a filter string because
	// the lifetime totals go through `CountRecords`, which takes dbx
	// expressions, while the timed series goes through
	// `FindRecordsByFilter`, which takes a filter string — one predicate,
	// two spellings, so the two halves cannot disagree.
	filterColumn string
	filterValue  string
}

// exprs returns the entity's narrowing as dbx expressions, for the
// COUNT path.
func (s statEntity) exprs() []dbx.Expression {
	if s.filterColumn == "" {
		return nil
	}
	return []dbx.Expression{dbx.HashExp{s.filterColumn: s.filterValue}}
}

// The posts predicate. `discussionRenamed` is the collection's other
// selectable post type — a rename event is stored as a post so the stream
// can render it, and it must not count as a comment.
const (
	postTypeColumn  = "type"
	postCommentType = "comment"
)

// Plugin is the Statistics plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// entities maps the reported entity onto its collection and filter.
	entities map[string]statEntity
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
	p.entities = map[string]statEntity{
		EntityUsers:       {collection: repository.SharedUsersCollection},
		EntityDiscussions: {collection: ext.Registry.Physical("discussions")},
		EntityPosts:       {collection: ext.Registry.Physical("posts"), filterColumn: postTypeColumn, filterValue: postCommentType},
	}

	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/statistics",
		Handler: p.showStatisticsHandler,
	})

	// The entity list travels on the FORUM DOCUMENT, to administrators
	// only. Upstream publishes it with
	//
	//	(new Extend\ApiResource(ForumResource::class))->fields(fn () => [
	//	    Schema\Arr::make('statisticsEntities')
	//	        ->visible(fn ($model, $context) => $context->getActor()->isAdmin())
	//	        ->get(fn () => array_keys(resolve('flarum-statistics.entities'))),
	//	])
	//
	// and the admin page lays its widgets out from it BEFORE any number
	// arrives — `statisticsEntities()` reads
	// `app.forum.attribute('statisticsEntities') ?? ['users','discussions','posts']`.
	// Publishing it is what lets an extension add an entity without the
	// page knowing about it; the fallback is why a missing attribute still
	// draws the built-in three.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: "forums",
		Name:     "statisticsEntities",
		Visible: func(req *plugin.AttributeRequest) bool {
			// Upstream's field is admin-only, and an invisible field is
			// ABSENT rather than null.
			return req.IsAdmin
		},
		Value: func(context.Context, *plugin.AttributeRequest) (any, error) {
			return p.EntityNames(), nil
		},
	})

	// The admin page and the dashboard widget. There is no forum bundle:
	// upstream's extension is admin-only, and a forum bundle that drew
	// nothing would be the empty-tag problem the module's startup check
	// exists to surface.
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Admin:    "plugins/statistics/admin.js",
		CSS:      "plugins/statistics/style.css",
	})

	return nil
}

// EntityNames returns the reported entities in upstream's order.
//
// The order is what the page lays its widgets out in, and upstream's is
// the order the entities were registered with the extender — users,
// discussions, posts — so it is fixed here rather than taken from the map.
func (*Plugin) EntityNames() []string {
	return []string{EntityUsers, EntityDiscussions, EntityPosts}
}

// Boot runs the plugin's startup work.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error { return nil }

// Shutdown releases the plugin's resources.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error { return nil }
