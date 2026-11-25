package flarum

import (
	"net/http"
	"path"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
)

// The forum's ASSET uploads.
//
// Upstream stores the forum's logo, its dark-mode logo, its email
// logo, its favicon and its app icon as files, and the admin
// application's Appearance page uploads them.
//
// The settings that hold them (`logo_path` and friends) are read by
// the forum document — the shell's `<img>` and the favicon come from
// them — so without an upload route they are settings no operator can
// ever set: the Appearance page's upload buttons all answered 404.
//
// The files live in PocketBase's filesystem, under the forum's own
// prefix, and they are served by the forum's own route for the same
// reason the avatar is: the URL has to be relative to the mount
// point, because PocketBase's native `/api/files/...` URL lives at the
// root of the riff listener — which the forum does not own.

// assetUploads are the assets the Appearance page uploads.
//
// Each entry names the SETTING that holds the uploaded file's path and
// the FILE NAME it is stored under. The names are upstream's: the
// routes are `/logo`, `/logo-dark-mode`, `/email-logo`, `/favicon` and
// `/app-icon`, and the frontend builds them from its `routePath`.
var assetUploads = map[string]string{
	"logo":           "logo_path",
	"logo-dark-mode": "logo_dark_mode_path",
	"email-logo":     "email_logo_path",
	"favicon":        "favicon_path",
	"app-icon":       "app_icon_path",
}

// assetFilesPrefix is where the uploaded assets live in the
// filesystem.
//
// It is under the forum's own prefix so that the forum's assets and
// PocketBase's records do not share a directory.
const assetFilesPrefix = "forum_assets"

// uploadAssetHandler serves POST /api/{asset}.
//
// It is upstream's `UploadLogoController` and its siblings: the body
// carries the file, and the answer is the uploaded asset's URL.
//
// Only an administrator may upload: these are the forum's own assets,
// and they are what every page's header and favicon show.
func (m *Module) uploadAssetHandler(e *core.RequestEvent, name string) error {
	settingKey, ok := assetUploads[name]
	if !ok {
		return e.NotFoundError("that asset does not exist", nil)
	}

	if !m.IsAdmin(e) {
		return e.ForbiddenError("you are not allowed to change the forum's assets", nil)
	}

	if err := e.Request.ParseMultipartForm(32 << 20); err != nil {
		return e.BadRequestError("the upload could not be read", err)
	}

	file, header, err := e.Request.FormFile("upload")
	if err != nil {
		return e.BadRequestError("the upload could not be read", err)
	}
	defer func() { _ = file.Close() }()

	fsys, err := m.app.NewFilesystem()
	if err != nil {
		return e.InternalServerError("could not open the filesystem", err)
	}
	defer func() { _ = fsys.Close() }()

	// The file is stored under a stable key, so a second upload
	// REPLACES the first rather than accumulating files.
	key := assetFilesPrefix + "/" + name

	if err := fsys.UploadMultipart(header, key); err != nil {
		return e.InternalServerError("the asset could not be stored", err)
	}

	// The setting holds the path the forum document builds its URL
	// from: `{basePath}/assets{path}`.
	repo := m.repo
	if err := repo.SaveSetting(settingKey, "/"+name, ""); err != nil {
		return e.InternalServerError("the asset could not be saved", err)
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: map[string]any{
		"type": "asset",
		"id":   name,
		"attributes": map[string]any{
			"path": "/" + name,
			"url":  m.opts.BasePath + "/branding/" + name,
		},
	}})
}

// deleteAssetHandler serves DELETE /api/{asset}.
//
// It is the other half of the upload: the Appearance page's button
// removes the asset as well as setting one.
func (m *Module) deleteAssetHandler(e *core.RequestEvent, name string) error {
	settingKey, ok := assetUploads[name]
	if !ok {
		return e.NotFoundError("that asset does not exist", nil)
	}

	if !m.IsAdmin(e) {
		return e.ForbiddenError("you are not allowed to change the forum's assets", nil)
	}

	fsys, err := m.app.NewFilesystem()
	if err != nil {
		return e.InternalServerError("could not open the filesystem", err)
	}
	defer func() { _ = fsys.Close() }()

	if err := fsys.Delete(assetFilesPrefix + "/" + name); err != nil {
		// A missing file is not an error: the state the operator
		// asked for is the one they have.
		m.logger.Debug("forum: the asset could not be removed", "asset", name, "error", err.Error())
	}

	if err := m.repo.SaveSetting(settingKey, "", ""); err != nil {
		return e.InternalServerError("the asset could not be saved", err)
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: map[string]any{
		"type": "asset",
		"id":   name,
		"attributes": map[string]any{
			"path": "",
			"url":  "",
		},
	}})
}

// serveAssetHandler serves GET {basePath}/branding/{name}.
//
// It is the counterpart of the upload: the forum document's logo URLs
// point here, because PocketBase's native `/api/files/...` URL lives
// at the root of the riff listener, which the forum does not own.
//
// The prefix is NOT `/assets/`: that is the BUILT BUNDLE tree, served
// from the embedded frontend, and `{basePath}/{path...}` is the SPA
// catch-all. A single-segment sibling of the catch-all is exactly the
// shape `/files/{id}` and `/avatars/{id}` already use.
func (m *Module) serveAssetHandler(e *core.RequestEvent) error {
	name := e.Request.PathValue("name")

	// A path is a path SEGMENT: the file names are the ones this
	// module stores, and a traversal attempt is refused rather than
	// resolved.
	if name == "" || name != path.Base(name) || strings.Contains(name, "..") {
		return e.NotFoundError("asset not found", nil)
	}

	if _, ok := assetUploads[name]; !ok {
		return e.NotFoundError("asset not found", nil)
	}

	fsys, err := m.app.NewFilesystem()
	if err != nil {
		return e.InternalServerError("could not open the filesystem", err)
	}
	defer func() { _ = fsys.Close() }()

	key := assetFilesPrefix + "/" + name

	if err := fsys.Serve(e.Response, e.Request, key, name); err != nil {
		return e.NotFoundError("asset not found", nil)
	}

	return nil
}
