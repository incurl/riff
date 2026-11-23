// Package uploads is the forum's first-party Uploads plugin.
//
// It is the forum's implementation of post attachments, and it exists
// because upstream Flarum v2.0.0's CORE has none: the `post_attachments`
// table and the `Post->upload()` relationship were removed from the
// core and live in the `fof/upload` extension instead (upstream's
// `flarum/core: ^2.0.0`). The extension is the de-facto standard for
// Flarum 2.x attachments, and this plugin mirrors its contract:
//
//   - the resource type is `files` (`FileResource::type()`);
//   - a file carries `url`, `thumbnailUrl`, `size`, `humanSize`,
//     `imageWidth`, `imageHeight`, `hidden`, `baseName` and `type`;
//   - a file is attached to a post through a post's `files`
//     relationship.
//
// The bytes live in PocketBase's own filesystem, and they are served
// through the forum's OWN file route rather than through PocketBase's
// native `/api/files/...` URL, which lives at the root of the listener
// that the forum does not own.
//
// The plugin is the three-layer flow the Tags and Likes plugins
// establish: a migration that creates the collection, API attributes
// and relationships contributed through the extender mechanism, and a
// frontend bundle that adds the UI.
package uploads

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
const PluginID = "riff/uploads"

// Version is the plugin's version.
const Version = "1.0.0"

// Plugin is the Uploads plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
}

// New returns the Uploads plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Uploads",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#8B5CF6",
				Glyph:           "fas fa-paperclip",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
//
// It creates the plugin's collection, contributes the `files`
// relationship on posts, and registers the file endpoints.
func (*Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	physical := ext.Registry.Physical(filesCollection)

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_files",
		Up: func(_ context.Context, app core.App) error {
			return createFilesCollection(app, physical)
		},
	})

	// --- layer 2: the API ----------------------------------------------
	// The files ride on the post resource, which is where the frontend
	// reads them from.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "files",
		Type:     TypeFile,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			ids, err := filesForPostIDs(req.App, physical, req.RecordID)
			if err != nil {
				return nil, err
			}
			out := make([]plugin.Identifier, 0, len(ids))
			for _, id := range ids {
				out = append(out, plugin.Identifier{Type: TypeFile, ID: id})
			}
			return out, nil
		},
	})

	// The upload, the attach, the delete and the listing.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/uploads",
		Handler: uploadHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/posts/{id}/files",
		Handler: attachFileHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/files/{id}",
		Handler: deleteFileHandler,
	})
	// The delete is registered for POST as well, because the frontend
	// does not send a DELETE: it turns every method other than GET and
	// POST into a POST with an `X-HTTP-Method-Override` header (see
	// the core's `Application.request`).
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/files/{id}",
		Handler: deleteFileHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/files",
		Handler: listFilesHandler,
	})
	// The single-file route is the JSON:API view of a file: the
	// frontend needs its attributes (the name, the size, the URL) to
	// render the attachment, and the relationship on the post carries
	// only the identifier.
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/files/{id}",
		Handler: showFileHandler,
	})

	// The bytes are served by the forum's own route. It is registered
	// by the module (not by the plugin), because the module owns the
	// base path and the route has to live beneath it — the same place
	// the avatar route is registered.

	// --- layer 3: the frontend ----------------------------------------
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/uploads/forum.js",
		Admin:    "plugins/uploads/admin.js",
		CSS:      "plugins/uploads/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
//
// The Uploads plugin has none: everything happens in the Register
// phase and in the migrations.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
//
// The Uploads plugin holds none.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}
