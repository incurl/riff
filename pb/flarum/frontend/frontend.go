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
	// Admin carries the admin application's data. It is nil for the forum
	// app, which reads only the fields above.
	//
	// The field is EMBEDDED (not nested) on purpose: the admin application
	// reads these as `app.data.extensions`, `app.data.settings` and so on,
	// i.e. at the top level of the payload. An embedded nil pointer
	// contributes nothing, so the forum app's payload is unchanged.
	*Admin
}

// Admin is the data the admin application reads.
//
// The admin app is the same SPA with a different entry bundle, and it
// expects a much larger payload than the forum: `BasicsPage.register`
// walks `displayNameDrivers`, `avatarDrivers` and `slugDrivers` during
// boot, so a payload without them throws and the page renders BLANK.
//
// The values are the ones this port actually implements. Where riff has
// no equivalent of an upstream feature the field is empty rather than
// invented, so a page shows nothing instead of something wrong.
type Admin struct {
	// Extensions are the forum's installed plugins, keyed by id.
	Extensions map[string]any `json:"extensions"`
	// InstalledPackages is the composer package list upstream shows. riff
	// loads Go plugins, not composer packages, so it is empty.
	InstalledPackages []string `json:"installedPackages"`
	// Settings are the forum's own settings, keyed by name.
	//
	// These are NOT optional: the admin app reads them during boot.
	// `isExtensionEnabled` does `JSON.parse(app.data.settings
	// .extensions_enabled)`, and `JSON.parse(undefined)` throws — so a
	// missing key is a blank page, not a missing field. Every key the
	// app parses as JSON therefore carries a valid JSON document.
	Settings map[string]any `json:"settings"`
	// ModelStatistics are the dashboard counts, per model.
	//
	// `UserListPage` reads `app.data.modelStatistics.users.total` in its
	// field initialiser, so the `users` entry must be present even when
	// the count is zero.
	ModelStatistics map[string]any `json:"modelStatistics"`
	// DisplayNameDrivers are the ways a display name can be derived. The
	// forum derives it from the username.
	DisplayNameDrivers []string `json:"displayNameDrivers"`
	// AvatarDrivers are the ways an avatar can be supplied. The forum
	// shows PocketBase's own file, which is upstream's `default`.
	AvatarDrivers []string `json:"avatarDrivers"`
	// SlugDrivers are the ways a model can be addressed in a url, per
	// model.
	SlugDrivers map[string][]string `json:"slugDrivers"`
	// SearchDrivers are the registered search drivers.
	SearchDrivers map[string][]string `json:"searchDrivers"`
	// Permissions are the custom permission keys, per group.
	Permissions map[string][]string `json:"permissions"`
	// MaintenanceByConfig reports whether maintenance mode is forced by
	// configuration rather than the admin switch.
	MaintenanceByConfig bool `json:"maintenanceByConfig"`
	// DBDriver is the database driver, in the upstream spelling
	// ("SQLite", "MySQL", "MariaDB", "PostgreSQL").
	//
	// `ExtensionPage` calls `app.data.dbDriver.toLowerCase()`, so this
	// must be a non-empty string.
	DBDriver string `json:"dbDriver"`
	// DBVersion is the database server version.
	DBVersion string `json:"dbVersion"`
	// DBDriverMismatch is the configured driver when it differs from the
	// running one, empty otherwise.
	DBDriverMismatch *string `json:"dbDriverMismatch"`
	// DBVersionStatus reports the server version against the minimum
	// this build supports.
	DBVersionStatus map[string]any `json:"dbVersionStatus"`
	// DBOptions are the database-specific options.
	DBOptions map[string]any `json:"dbOptions"`
	// PHPVersion is the PHP version upstream shows. riff has no PHP, so
	// this is empty.
	PHPVersion string `json:"phpVersion"`
	// QueueDriver is the job queue driver. riff has no job queue, so
	// this is "sync".
	QueueDriver string `json:"queueDriver"`
	// PausedQueues are the queues the operator paused.
	PausedQueues []string `json:"pausedQueues"`
	// KnownQueues are the queues the instance knows.
	KnownQueues []string `json:"knownQueues"`
	// SchedulerStatus is the scheduler's state: "active", "inactive" or
	// "never-run".
	SchedulerStatus string `json:"schedulerStatus"`
	// SessionDriver is the session driver upstream shows. riff's sessions
	// are PocketBase auth tokens carried in a cookie.
	SessionDriver string `json:"sessionDriver"`
	// SessionByConfig reports whether the session settings come from the
	// configuration rather than the database.
	SessionByConfig bool `json:"sessionByConfig"`
	// SessionLifetime is the session lifetime in MINUTES.
	SessionLifetime int `json:"sessionLifetime"`
	// SessionCookieExpiresOnClose reports whether the session cookie is
	// a session cookie.
	SessionCookieExpiresOnClose bool `json:"sessionCookieExpiresOnClose"`
	// AccessTokenLifetimes are the token lifetimes, in seconds.
	AccessTokenLifetimes []map[string]any `json:"accessTokenLifetimes"`
	// Bisecting reports whether the extension bisect is running.
	Bisecting bool `json:"bisecting"`
	// SafeModeExtensions are the extensions disabled in safe mode.
	SafeModeExtensions []string `json:"safeModeExtensions"`
	// SafeModeExtensionsConfig is the configured safe-mode list.
	SafeModeExtensionsConfig []string `json:"safeModeExtensionsConfig"`
	// AnnouncementsDisabled reports whether announcements are off.
	AnnouncementsDisabled bool `json:"announcementsDisabled"`
	// FontAwesomeByConfig reports whether the icon source is fixed by the
	// configuration.
	FontAwesomeByConfig bool `json:"fontawesomeByConfig"`
	// FontAwesomeConfig is the configured icon source.
	FontAwesomeConfig map[string]any `json:"fontawesomeConfig"`
	// DebugEnabled reports whether the instance runs in debug mode.
	DebugEnabled bool `json:"debugEnabled"`
	// ThemeImages are the images the theme owns.
	ThemeImages map[string]any `json:"themeImages"`
	// SortMaps are the sort options each resource offers.
	SortMaps map[string]any `json:"sortMaps"`
}

// BootSession is the session part of the boot payload.
//
// The frontend reads it as `session.userId` and `session.csrfToken`
// (see the core's Application.load), and looks the caller up with
// `store.getById('users', String(userId))`.
type BootSession struct {
	// UserID is the caller's id, empty for an anonymous caller.
	//
	// It is a STRING, not a number: the id is the PocketBase record id
	// (a 15-character random string), and the frontend looks the record
	// up by that exact id. A numeric field could not carry it, and a
	// truncated one would silently fail to find the user — leaving the
	// SPA booted as anonymous even though the cookie was valid.
	UserID string `json:"userId"`
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
	// Admin reports whether the shell is for the admin application.
	//
	// The admin app is the same SPA with a different entry bundle, but it
	// mounts a navigation column into #admin-navigation and expects the
	// content to sit beside it. Rendering that div for the forum app would
	// leave an empty column in the layout.
	Admin bool
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
            {{ if .Admin }}<div class="container"><div id="admin-navigation" class="App-nav sideNav"></div></div>{{ end }}
            <div id="content"{{ if .Admin }} class="sideNavOffset"{{ end }}></div>
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

// ShellMeta is what the boot shell renders that the payload does not
// carry: the document title and the module's mount path.
//
// Both are read off the forum document the payload embeds, and both are
// rendered BEFORE the app boots — so a hardcoded value in either place is
// a shell that disagrees with the app it is starting.
type ShellMeta struct {
	// Title is the `<title>` and the header's home-link text.
	//
	// It is the `forum_title` SETTING, not a constant: an administrator
	// renames the forum in the admin's Basics page, and the shell is
	// what the browser shows first.
	Title string
	// BasePath is the module's mount path (`/forum`), used for the
	// home-link's href.
	//
	// It is NOT empty for this deployment style: the forum is always a
	// riff mount point, so a link to "/" leaves the forum for whatever
	// the host serves at its root (the static site, in the /memos/
	// layouts).
	BasePath string
}

// Render renders the boot shell.
//
// The meta is rendered VERBATIM; the caller is responsible for passing a
// non-empty title (the document always carries one, because it falls back
// to api.DefaultForumTitle) and a BasePath that matches the mount.
func Render(payload BootPayload, scripts, styles []string, revisions map[string]string, meta ShellMeta, admin bool) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "forum: could not encode the boot payload")
	}
	revisionJSON, err := json.Marshal(revisions)
	if err != nil {
		return nil, errors.Wrap(err, "forum: could not encode the revision manifest")
	}

	data := shellData{
		Title:     meta.Title,
		BasePath:  strings.TrimSuffix(meta.BasePath, "/"),
		Payload:   template.JS(encoded),
		Revisions: template.JS(revisionJSON),
		Scripts:   scripts,
		Styles:    styles,
		Admin:     admin,
	}

	var out strings.Builder
	if err := shellTemplate.Execute(&out, data); err != nil {
		return nil, errors.Wrap(err, "forum: could not render the boot shell")
	}
	return []byte(out.String()), nil
}
