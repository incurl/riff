// Package tags is the forum's first-party Tags plugin.
//
// It is the reference implementation of the three-layer plugin flow:
//
//  1. a migration that creates the plugin's PocketBase collections,
//  2. JSON:API resources and relationships contributed through the
//     extender mechanism,
//  3. a frontend bundle that adds the UI entries.
//
// The plugin mirrors upstream Flarum's `flarum/tags` extension: a
// hierarchy of tags, discussions carrying tags, and a restricted tag
// that has its own permission scope.
package tags

import (
	"context"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
const PluginID = "riff/tags"

// Version is the plugin's version.
const Version = "1.0.0"

// Plugin is the Tags plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
}

// New returns the Tags plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Tags",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#F28326",
				Glyph:           "fas fa-tags",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
//
// It creates the plugin's collection, contributes the tag relationship
// on discussions, and registers the tag endpoints.
func (*Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	physical := ext.Registry.Physical("tags")

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_tags",
		Up: func(_ context.Context, app core.App) error {
			return createTagsCollection(app, physical)
		},
	})

	// --- layer 2: the API ----------------------------------------------
	// The tag relationship rides on the discussion resource, which is
	// where the frontend reads it from.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypeDiscussion,
		Name:     "tags",
		Type:     api.TypeTag,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			ids, err := discussionTagIDs(req.App, req.RecordID)
			if err != nil {
				return nil, err
			}
			out := make([]plugin.Identifier, 0, len(ids))
			for _, id := range ids {
				out = append(out, plugin.Identifier{Type: api.TypeTag, ID: id})
			}
			return out, nil
		},
	})

	// The tag resource contributes its own attributes to the posts
	// endpoint when a post's discussion carries a tag.
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/tags",
		Handler: listTagsHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/tags/{id}",
		Handler: showTagHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/discussions/{id}/tags",
		Handler: addTagHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/discussions/{id}/tags/{tagId}",
		Handler: removeTagHandler,
	})

	// --- the permission key --------------------------------------------
	// The plugin registers its own permission keys through the
	// extender mechanism; the core's permission service reads them
	// from the same table.
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionEditTag,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	// --- layer 3: the frontend ----------------------------------------
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/tags/forum.js",
		Admin:    "plugins/tags/admin.js",
		CSS:      "plugins/tags/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
//
// The Tags plugin has none: everything happens in the Register phase and
// in the migrations.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
//
// The Tags plugin holds none.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// errNotFound reports a missing tag.
var errNotFound = errors.New("tags: not found")
