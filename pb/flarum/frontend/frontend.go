// Package frontend serves the reused Flarum SPA and the plugin bundles.
//
// The SPA is the vendored Flarum 2.x frontend (Mithril + TypeScript),
// built by `npm run build` in pb/flarum/frontend/packages/core. The Go backend serves
// the built bundles and renders the boot shell that starts the app.
//
// # The boot shell
//
// The frontend boots from an inline JSON payload:
//
//	app.load(data); app.bootExtensions(flarum.extensions); app.boot();
//
// The payload carries the resources (the forum document and the caller),
// the session (the caller's id and the CSRF token), the locale, and the
// asset URLs. The base path is one of the forum document's attributes,
// which is what makes the SPA work under a subpath: upstream Flarum
// already threads `basePath` through its router, its link router and its
// asset helpers.
package frontend

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"strings"

	"github.com/pkg/errors"
)

// assetsFS embeds the built frontend bundles.
//
// It deliberately embeds the whole `ui` directory rather than
// `ui/dist`: `.gitignore` ignores every `dist/` directory (they are
// build output), so a `//go:embed all:ui/dist` would not compile in a
// fresh clone. The tracked `ui/index.html` is a dependency-free
// fallback shell, and a frontend build dropped into `ui/dist/` takes
// precedence over it.
//
// The `all:` prefix keeps the dotfiles the bundles carry.
//
//go:embed all:ui
var assetsFS embed.FS

// Assets is the resolved frontend asset tree.
type Assets struct {
	// dist is the built bundle tree, nil when no build was dropped in.
	dist fs.FS
	// fallback is the tracked shell, served when there is no build.
	fallback []byte
	// revision identifies the build, for cache busting.
	revision string
}

// Load resolves the embedded frontend.
//
// The built bundles are optional: the tracked fallback shell is served
// when no build was dropped in, so a fresh clone compiles and runs
// (with a readable message instead of a blank page).
func Load() (*Assets, error) {
	// fs.Sub does NOT fail for a missing directory: it returns a valid
	// FS whose reads fail later. The directory's existence is therefore
	// checked by reading it.
	dist, err := fs.Sub(assetsFS, "ui/dist")
	if err != nil {
		dist = nil
	}
	if dist != nil {
		if _, err := fs.ReadDir(dist, "."); err != nil {
			// No build was dropped in: the fallback shell is the
			// frontend.
			dist = nil
		}
	}

	fallback, err := fs.ReadFile(assetsFS, "ui/index.html")
	if err != nil {
		return nil, errors.Wrap(err, "forum: the embedded UI fallback is missing")
	}

	return &Assets{dist: dist, fallback: fallback, revision: Revision()}, nil
}

// Revision returns the build revision.
//
// It is derived from the embedded bundle's size and modification time, so
// a rebuild produces a new revision and the browser fetches the new
// bundle. The revision travels in the asset URLs as a query parameter.
func Revision() string {
	entries, err := fs.ReadDir(assetsFS, "ui/dist")
	if err != nil {
		return "dev"
	}
	var size int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		size += info.Size()
	}
	return fmt.Sprintf("%d", size)
}

// Revision returns the build revision of the asset tree.
func (a *Assets) Revision() string {
	if a == nil {
		return "dev"
	}
	return a.revision
}

// FS returns the asset tree.
func (a *Assets) FS() fs.FS {
	if a == nil {
		return nil
	}
	return a.dist
}

// Open opens a file in the asset tree.
func (a *Assets) Open(name string) (fs.File, error) {
	if a == nil || a.dist == nil {
		return nil, fs.ErrNotExist
	}
	return a.dist.Open(name)
}

// Exists reports whether an asset is present.
func (a *Assets) Exists(name string) bool {
	if a == nil || a.dist == nil {
		return false
	}
	file, err := a.dist.Open(name)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// HasBuild reports whether a frontend build was dropped in.
func (a *Assets) HasBuild() bool {
	return a != nil && a.dist != nil
}

// Fallback returns the tracked shell.
func (a *Assets) Fallback() []byte {
	if a == nil {
		return nil
	}
	return a.fallback
}

// BootPayload is the payload the SPA boots with.
//
// The field names are the frontend's `ApplicationData` type.
type BootPayload struct {
	// Resources are the resource objects the app starts with: the forum
	// document and the caller.
	Resources []any `json:"resources"`
	// Session carries the caller's id and the CSRF token.
	Session BootSession `json:"session"`
	// Locale is the active locale.
	Locale string `json:"locale"`
	// Locales are the available locales.
	Locales map[string]string `json:"locales"`
	// AssetsRevision identifies the build the page booted with.
	AssetsRevision string `json:"assetsRevision"`
	// MaintenanceMode is set when the forum is in maintenance mode.
	MaintenanceMode string `json:"maintenanceMode,omitempty"`
}

// BootSession is the session part of the boot payload.
type BootSession struct {
	// UserID is the caller's id, 0 for an anonymous caller.
	UserID int `json:"userId"`
	// CSRFToken is the caller's CSRF token.
	CSRFToken string `json:"csrfToken"`
}

// PluginBundle describes one plugin's frontend bundle.
//
// The SPA loads the bundles in dependency order and registers each
// plugin's exports on `flarum.extensions`.
type PluginBundle struct {
	// ID is the plugin's id.
	ID string `json:"id"`
	// Version is the plugin's version, used for cache busting.
	Version string `json:"version"`
	// Forum is the forum-app bundle URL.
	Forum string `json:"forum,omitempty"`
	// Admin is the admin-app bundle URL.
	Admin string `json:"admin,omitempty"`
	// CSS is the stylesheet URL.
	CSS string `json:"css,omitempty"`
}

// shellData is what the boot shell template renders.
type shellData struct {
	// Title is the document title.
	Title string
	// BasePath is the module's mount path.
	BasePath string
	// Payload is the boot payload, JSON-encoded.
	Payload template.JS
	// Revisions is the asset revision manifest, JSON-encoded.
	Revisions template.JS
	// Scripts are the script URLs to load, in order.
	Scripts []string
	// Styles are the stylesheet URLs to load, in order.
	Styles []string
	// Debug reports whether the debug mode is on.
	Debug bool
}

// shellTemplate is the boot shell.
//
// It mirrors upstream Flarum's `frontend/app.blade.php`: the loading
// indicator, the `flarum` global with its `extensions` registry, the
// bundle scripts, and the boot call.
//
// The payload is injected as JSON in a script tag rather than inline in
// the boot script, so a value containing "</script>" cannot break out of
// the tag.
var shellTemplate = template.Must(template.New("shell").Parse(`<!doctype html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{ .Title }}</title>
    {{ range .Styles }}<link rel="stylesheet" href="{{ . }}">
    {{ end }}
</head>
<body>
    <div id="flarum-loading" style="display: none">
        <div style="text-align:center;padding:50px 0;font-size:18px;color:#aaa">Loading...</div>
    </div>
    <div id="flarum-loading-error" style="display: none">
        <div style="text-align:center;padding:50px 0;font-size:18px;color:#aaa"></div>
    </div>
    <div id="app" class="App">
        <a href="#content" class="sr-only sr-only-focusable-custom">Skip to content</a>
        <div id="app-navigation" class="App-navigation"></div>
        <div id="drawer" class="App-drawer">
            <header id="header" class="App-header">
                <div id="header-navigation" class="Header-navigation"></div>
                <div class="container">
                    <div class="Header-title"><a href="{{ .BasePath }}/" id="home-link">{{ .Title }}</a></div>
                    <div id="header-primary" class="Header-primary"></div>
                    <div id="header-secondary" class="Header-secondary"></div>
                </div>
            </header>
        </div>
        <main class="App-content">
            <div id="notices"></div>
            <div id="content"></div>
            <div class="App-composer">
                <div class="container"><div id="composer"></div></div>
            </div>
        </main>
        <footer class="App-footer" id="footer"></footer>
    </div>
    <div id="modal"></div>
    <div id="alerts"></div>
    <script>
        document.getElementById('flarum-loading').style.display = 'block';
        var flarum = {extensions: {}, debug: {{ .Debug }}};
    </script>
    {{ range .Scripts }}<script src="{{ . }}"></script>
    {{ end }}
    <script id="flarum-rev-manifest" type="application/json">{{ .Revisions }}</script>
    <script id="flarum-json-payload" type="application/json">{{ .Payload }}</script>
    <script>
        (function () {
            var payload = JSON.parse(document.getElementById('flarum-json-payload').textContent);
            document.getElementById('flarum-loading').style.display = 'none';
            try {
                app.load(payload);
                app.bootExtensions(flarum.extensions);
                app.boot();
            } catch (e) {
                var error = document.getElementById('flarum-loading-error');
                error.style.display = 'block';
                throw e;
            }
        })();
    </script>
</body>
</html>
`))

// Render renders the boot shell.
func Render(payload BootPayload, scripts, styles []string, revisions map[string]string) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "forum: could not encode the boot payload")
	}
	revisionJSON, err := json.Marshal(revisions)
	if err != nil {
		return nil, errors.Wrap(err, "forum: could not encode the revision manifest")
	}

	data := shellData{
		Title:     "Forum",
		BasePath:  "",
		Payload:   template.JS(encoded),
		Revisions: template.JS(revisionJSON),
		Scripts:   scripts,
		Styles:    styles,
	}

	var out strings.Builder
	if err := shellTemplate.Execute(&out, data); err != nil {
		return nil, errors.Wrap(err, "forum: could not render the boot shell")
	}
	return []byte(out.String()), nil
}
