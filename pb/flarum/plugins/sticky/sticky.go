// Package sticky is the forum's Sticky plugin.
//
// It mirrors upstream Flarum's `flarum/sticky` extension: a moderator can
// pin a discussion to the top of the list, and the discussion shows a
// sticky badge.
//
// Pinning is an INDEPENDENT column (`is_sticky`), as upstream models it,
// so a discussion can be pinned and locked at the same time. This port
// used to encode pinning in `state`, which made the two mutually
// exclusive.
package sticky

import (
	"context"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
const PluginID = "flarum/sticky"

// Version is the plugin's version.
const Version = "1.0.0"

// discussionCollection is the logical name of the collection whose records
// carry the flag.
const discussionCollection = "discussions"

// stickyField is the column the plugin owns.
const stickyField = "is_sticky"

// Plugin is the Sticky plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// physical is the discussions collection's real name.
	physical string
}

// New returns the Sticky plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Sticky",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#D13E32",
				Glyph:           "fas fa-thumbtack",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	p.physical = ext.Registry.Physical(discussionCollection)

	ext.AddMigration(plugin.Migration{
		Name: "add_is_sticky",
		Up: func(_ context.Context, app core.App) error {
			return addStickyColumn(app, p.physical)
		},
	})

	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "isSticky",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return isSticky(req.App, p.physical, req.RecordID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "canSticky",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return req.Can(domain.PermissionStickyDiscussion, api.TypeDiscussion, req.RecordID), nil
		},
	})

	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/discussions/{id}/sticky",
		Handler: p.stickyHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/discussions/{id}/sticky",
		Handler: p.unstickyHandler,
	})

	// Upstream's admin page registers this as `discussion.sticky`
	// (flarum-sticky/js/src/admin/extend.tsx).
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionStickyDiscussion,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/sticky/forum.js",
		Admin:    "plugins/sticky/admin.js",
		CSS:      "plugins/sticky/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// addStickyColumn adds the `is_sticky` column and backfills it.
//
// Both halves are needed: the column is added only when missing, and the
// rows a previous version stored as `state = "sticky"` become
// `is_sticky = true`. Without the backfill every pinned discussion comes
// unpinned on upgrade.
func addStickyColumn(app core.App, physical string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "sticky: the %q collection is missing", physical)
	}

	if collection.Fields.GetByName(stickyField) == nil {
		collection.Fields.Add(&core.BoolField{Name: stickyField})

		if err := app.Save(collection); err != nil {
			return errors.Wrap(err, "sticky: the is_sticky column could not be added")
		}
	}

	records, err := app.FindAllRecords(collection)
	if err != nil {
		// The column exists; the reader also falls back to `state`, so a
		// failed scan must not abort bootstrap.
		return nil //nolint:nilerr // the column is what matters
	}

	for _, record := range records {
		if record.GetString("state") != "sticky" || record.GetBool(stickyField) {
			continue
		}

		record.Set(stickyField, true)

		if err := app.Save(record); err != nil {
			return errors.Wrap(err, "sticky: a pinned discussion could not be backfilled")
		}
	}

	return nil
}
