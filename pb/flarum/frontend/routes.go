package frontend

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Server serves the SPA and the asset bundles.
type Server struct {
	// assets are the built frontend bundles.
	assets *Assets
	// basePath is the module's mount path.
	basePath string
	// bundles are the plugin bundles to load, in dependency order.
	bundles []Bundle
	// boot renders the boot shell.
	boot BootRenderer
}

// Bundle is one plugin's frontend bundle.
type Bundle struct {
	// ID is the plugin's id.
	ID string
	// Version is the plugin's version, used for cache busting.
	Version string
	// Forum is the forum-app bundle path, relative to the asset root.
	Forum string
	// Admin is the admin-app bundle path, relative to the asset root.
	Admin string
	// CSS is the stylesheet path, relative to the asset root.
	CSS string
}

// BootRenderer renders the boot shell.
//
// It is an interface so the API layer can supply the payload without
// the frontend package depending on it.
type BootRenderer interface {
	// RenderBootShell renders the shell for the forum app.
	RenderBootShell(admin bool) ([]byte, error)
}

// ServerOptions configures the frontend server.
type ServerOptions struct {
	// Assets are the built frontend bundles.
	Assets *Assets
	// BasePath is the module's mount path.
	BasePath string
	// Bundles are the plugin bundles, in dependency order.
	Bundles []Bundle
	// Boot renders the boot shell.
	Boot BootRenderer
}

// NewServer returns a frontend server.
func NewServer(opts ServerOptions) *Server {
	return &Server{
		assets:   opts.Assets,
		basePath: opts.BasePath,
		bundles:  opts.Bundles,
		boot:     opts.Boot,
	}
}

// Mount registers the frontend routes.
//
// Every pattern carries an explicit method: a method-less catch-all
// would conflict with the host's own SPA catch-all and panic the
// router at startup.
func (s *Server) Mount(r *router.Router[*core.RequestEvent]) {
	base := s.basePath

	// --- assets -------------------------------------------------------
	// The bundles are served from the module's asset prefix, so a plugin
	// bundle never leaks outside the forum's subtree.
	r.GET(base+"/assets/{path...}", s.serveAssetHandler)

	// --- the plugin manifest ------------------------------------------
	// The SPA fetches it to learn which bundles to load, in order.
	r.GET(base+"/api/extensions", s.manifestHandler)

	// --- the SPA ------------------------------------------------------
	// The forum app and the admin app are the same SPA with a
	// different entry bundle, which is why the path decides which one
	// is booted.
	r.GET(base+"/admin", s.spaHandler)
	r.GET(base+"/admin/{path...}", s.spaHandler)
	r.GET(base, s.redirectHandler)
	r.GET(base+"/{path...}", s.spaHandler)
}

// redirectHandler sends the bare base path to its trailing-slash form.
//
// Without it the browser would request the base path without a slash
// for every relative asset URL.
func (s *Server) redirectHandler(e *core.RequestEvent) error {
	return e.Redirect(http.StatusMovedPermanently, s.basePath+"/")
}

// manifestHandler serves the enabled plugins' frontend manifest.
//
// The SPA loads the bundles in the order the manifest lists them, which
// is the plugins' dependency order.
func (s *Server) manifestHandler(e *core.RequestEvent) error {
	manifest := make([]map[string]any, 0, len(s.bundles))
	for _, bundle := range s.bundles {
		entry := map[string]any{
			"id":      bundle.ID,
			"version": bundle.Version,
		}
		if bundle.Forum != "" {
			entry["forum"] = s.assetURL(bundle.Forum, bundle.Version)
		}
		if bundle.Admin != "" {
			entry["admin"] = s.assetURL(bundle.Admin, bundle.Version)
		}
		if bundle.CSS != "" {
			entry["css"] = s.assetURL(bundle.CSS, bundle.Version)
		}
		manifest = append(manifest, entry)
	}

	e.Response.Header().Set("Content-Type", "application/json")
	return e.JSON(http.StatusOK, map[string]any{"extensions": manifest})
}

// assetURL builds the URL of an asset, with the version for cache
// busting.
func (s *Server) assetURL(path, version string) string {
	url := s.basePath + "/assets/" + strings.TrimPrefix(path, "/")
	if version != "" {
		url += "?v=" + version
	}
	return url
}

// serveAssetHandler serves a built bundle.
//
// The path is looked up in the embedded tree, so a traversal attempt
// ("../") simply does not resolve.
func (s *Server) serveAssetHandler(e *core.RequestEvent) error {
	path := e.Request.PathValue("path")
	if path == "" {
		return e.NotFoundError("That asset does not exist.", nil)
	}

	file, err := s.assets.Open(path)
	if err != nil {
		return e.NotFoundError("That asset does not exist.", nil)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return e.NotFoundError("That asset does not exist.", nil)
	}
	if info.IsDir() {
		return e.NotFoundError("That asset does not exist.", nil)
	}

	seeker, ok := file.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		return e.NotFoundError("That asset does not exist.", nil)
	}

	http.ServeContent(e.Response, e.Request, info.Name(), info.ModTime(), seeker)
	return nil
}

// spaHandler serves the boot shell.
//
// The shell is the same document for every SPA route: the frontend's
// router takes over once it boots.
//
// Without a frontend build the tracked fallback shell is served
// instead, which explains what to build rather than showing a blank
// page.
func (s *Server) spaHandler(e *core.RequestEvent) error {
	if !s.assets.HasBuild() {
		e.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
		e.Response.WriteHeader(http.StatusOK)
		_, err := e.Response.Write(s.assets.Fallback())
		return err
	}

	admin := strings.HasPrefix(e.Request.URL.Path, s.basePath+"/admin")

	body, err := s.boot.RenderBootShell(admin)
	if err != nil {
		return e.InternalServerError("The forum could not be loaded.", nil)
	}

	e.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	e.Response.WriteHeader(http.StatusOK)
	_, err = e.Response.Write(body)
	return err
}

// BundleManifest renders the bundle list as JSON.
//
// It is used by the tests and by the asset manifest endpoint.
func BundleManifest(bundles []Bundle) ([]byte, error) {
	return json.Marshal(bundles)
}
