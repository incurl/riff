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
	"strconv"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
const PluginID = "riff/uploads"

// Version is the plugin's version.
const Version = "1.1.0"

// SettingMaxFileSize is the upload limit, in KIBIBYTES.
//
// It is upstream's key (`fof-upload.maxFileSize`), in upstream's unit:
// the extension's validation compares the file's size against it in KiB,
// and its admin page labels the field that way.
const SettingMaxFileSize = "fof-upload.maxFileSize"

// MaxFileSizeKiB is the limit when the setting is unset: 32 MiB.
//
// Upstream's default is 2048 (2 MiB). This port takes what the memo
// attachment surface takes (`pb/migrations/1850000000_create_attachments.go`),
// so one upload limit holds across the whole instance; an operator can
// lower it here, and the collection's own field maximum (MaxFileSize) is
// the ceiling either way.
const MaxFileSizeKiB = MaxFileSize / 1024

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

	// The shared library's column is a SEPARATE step: a plugin
	// migration that has already been recorded never runs again, so an
	// instance created before the flag existed has to be upgraded by its
	// own idempotent step.
	ext.AddMigration(plugin.Migration{
		Name: "add_shared_column",
		Up: func(_ context.Context, app core.App) error {
			return ensureSharedField(app, physical)
		},
	})

	// The settings the plugin READS. Only what it reads is declared: a
	// setting the admin form shows and nothing honours is a control that
	// changes nothing.
	//
	// `maxFileSize` is upstream's key, in KIBIBYTES, because that is
	// how upstream's `maxFileSize` is documented and how its own
	// validation reads it. The DEFAULT differs from upstream's 2048 KiB
	// on purpose: this port accepts what the memo attachment surface
	// accepts (32 MiB — see `pb/migrations/1850000000_create_attachments.go`),
	// so a file the Memos side takes is taken here too.
	ext.AddSettings(plugin.SettingsSchema{
		Fields: []plugin.SettingField{
			{
				Key:     SettingMaxFileSize,
				Label:   "Maximum file size (KiB)",
				Type:    "number",
				Default: strconv.Itoa(MaxFileSizeKiB),
			},
		},
	})

	// --- layer 2: the API ----------------------------------------------
	// The forum document carries `fof-upload.canUpload`, which is the flag
	// the composer's attach button reads.
	//
	// It is the SAME permission the upload endpoint checks, asked through
	// the permission service (`AttributeRequest.Can`), so the button is
	// offered exactly when the request would be accepted. Upstream
	// publishes the same attribute from its `UploadServiceProvider`
	// (`$actor->can('fof-upload.upload')`), and its button branches on it
	// too: a hardcoded `true` is a button the server refuses, and a
	// hardcoded `false` is a permission an operator can grant that changes
	// nothing.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "fof-upload.canUpload",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return req.Can(domain.PermissionUploadFiles, api.TypeForum, "1"), nil
		},
	})

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
	//
	// The SECOND path on each row is upstream's `fof/upload` spelling
	// (`extend.php`: `->post('/fof/upload', …)`, `->get('/fof/uploads', …)`,
	// `->delete('/fof/upload/delete/{uuid}', …)`,
	// `->get('/fof/download/{uuid}', …)`, `->patch('/fof/upload/hide', …)`).
	// Both are served because the port's own composer used the first and
	// a client written against the extension uses the second; the paths
	// differ only in spelling, and the handlers are the same.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/uploads",
		Handler: uploadHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/fof/upload",
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
		Method:  "DELETE",
		Path:    "/api/fof/upload/delete/{uuid}",
		Handler: deleteFileHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/fof/upload/delete/{uuid}",
		Handler: deleteFileHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/files",
		Handler: listFilesHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/fof/uploads",
		Handler: listFilesHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/fof/upload/shared-files",
		Handler: listFilesHandler,
	})
	// The media manager's hide switch.
	ext.AddRoute(plugin.RouteDef{
		Method:  "PATCH",
		Path:    "/api/fof/upload/hide",
		Handler: hideFileHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/fof/upload/hide",
		Handler: hideFileHandler,
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
	// Upstream's download routes, so a client that builds the URL from
	// `File::url` (`/api/fof/download/{uuid}`) can fetch the bytes. The
	// three-segment form carries the post and a CSRF token upstream
	// strips; here the segments are ignored, because the file's own
	// visibility rule is what authorizes the read.
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/fof/download/{uuid}",
		Handler: ServeFileHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/fof/download/{uuid}/{post}/{csrf}",
		Handler: ServeFileHandler,
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
