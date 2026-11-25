package flarum

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/frontend"
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/plugins/uploads"
	"github.com/incurl/riff/pb/flarum/repository"
	"github.com/incurl/riff/pb/flarum/session"
)

// mount binds the module's routes.
//
// Everything is mounted beneath the module's base path, so the forum
// never takes a route the host application already owns.
func (m *Module) mount(r *router.Router[*core.RequestEvent]) {
	// --- the JSON:API --------------------------------------------------
	m.api.Mount(r)

	// --- the session ---------------------------------------------------
	// The login and logout routes are not under /api: upstream Flarum
	// posts them to the forum's own root, and the reused frontend's
	// Session class does the same.
	r.POST(m.opts.BasePath+"/login", m.loginHandler)
	r.POST(m.opts.BasePath+"/logout", m.logoutHandler)

	// --- the avatars ----------------------------------------------------
	// The profile picture is PocketBase's `users.avatar` file, streamed
	// through this module rather than linked at PocketBase's native
	// `/api/files/...` URL: that URL lives at the root of the listener,
	// which this module does not own (and a reverse proxy in front of a
	// sub-path install puts the static site there).
	r.GET(m.opts.BasePath+"/avatars/{id}", m.avatarHandler)

	// --- the uploaded files ---------------------------------------------
	// The Uploads plugin's bytes are streamed through this module for
	// the same reason the avatars are: PocketBase's native file URL
	// lives at the root of the listener, which the forum does not own.
	//
	// The route lives in the module rather than in the plugin because
	// the module owns the base path: a plugin route is mounted beneath
	// the module root, but this one has to sit NEXT to the avatars
	// rather than under `/api`.
	//
	// The handler is wrapped so the API server travels with the
	// request: the plugin's handler resolves the caller through it,
	// exactly as the core's own handlers do. A plugin route gets the
	// wrapper for free (`mountPluginRoute`); this one is registered by
	// the module, so it needs the wrapper here.
	r.GET(m.opts.BasePath+"/files/{id}", func(e *core.RequestEvent) error {
		api.WithServer(e, m.api)
		return uploads.ServeFileHandler(e)
	})

	// --- the forum's branding assets ------------------------------------
	// The logo, the dark-mode logo, the email logo, the favicon and the
	// app icon. Upstream's Appearance page uploads them, and the forum
	// document reads them back into `logoUrl` and friends — so without
	// these routes the settings exist and nothing can ever write them.
	//
	// The serve route is a single-segment sibling of the SPA catch-all,
	// the same shape `/files/{id}` and `/avatars/{id}` use. It is NOT
	// under `/assets/`: that prefix is the BUILT BUNDLE tree.
	r.GET(m.opts.BasePath+"/branding/{name}", m.serveAssetHandler)

	// The routes are LITERAL paths rather than an `/api/{asset}`
	// wildcard: a wildcard under `/api/` would overlap every other API
	// route, and Go's `http.ServeMux` panics on patterns that overlap
	// without one being strictly more specific. The asset name is
	// therefore closed over, not read from the request.
	for asset := range assetUploads {
		asset := asset

		r.POST(m.opts.BasePath+"/api/"+asset, func(e *core.RequestEvent) error {
			return m.uploadAssetHandler(e, asset)
		})
		r.DELETE(m.opts.BasePath+"/api/"+asset, func(e *core.RequestEvent) error {
			return m.deleteAssetHandler(e, asset)
		})
	}

	// --- the frontend --------------------------------------------------
	// The advertised bundles are validated before the shell emits them.
	// The shell writes a <script>/<link> per registered bundle whether or
	// not the file was embedded, and a tag for a file that is not there
	// is a SILENT 404: the page renders, the console stays empty, and the
	// plugin's UI simply never appears. That is what "the tags and likes
	// are not visible" looks like, so the build names the missing file.
	m.warnMissingAssets()

	frontendServer := frontend.NewServer(frontend.ServerOptions{
		Assets:   m.assets,
		BasePath: m.opts.BasePath,
		Bundles:  m.bundles(),
		Boot:     m,
	})
	frontendServer.Mount(r)

	// --- the plugin routes ---------------------------------------------
	// A plugin route is mounted beneath the module root, never at the
	// host application root. The routes are collected in the plugins'
	// dependency order, so a plugin's route cannot shadow a plugin it
	// depends on.
	for _, id := range m.plugins.Order() {
		extenders := m.plugins.PluginExtenders()[id]
		if extenders == nil {
			continue
		}
		for _, route := range extenders.Routes() {
			mountPluginRoute(r, m.opts.BasePath, m.api, route)
		}
	}
}

// mountPluginRoute mounts one plugin route.
//
// The handler is wrapped so the API server travels with the request:
// a plugin handler resolves the caller and asks the permission service
// through the request context, exactly as the core's own handlers do.
func mountPluginRoute(r *router.Router[*core.RequestEvent], basePath string, server *api.Server, route plugin.RouteDef) {
	path := basePath + "/" + strings.TrimPrefix(route.Path, "/")
	method := strings.ToUpper(route.Method)
	if method == "" {
		method = "GET"
	}

	handler := func(e *core.RequestEvent) error {
		api.WithServer(e, server)
		return route.Handler(e)
	}

	switch method {
	case "GET":
		r.GET(path, handler)
	case "POST":
		r.POST(path, handler)
	case "PUT":
		r.PUT(path, handler)
	case "PATCH":
		r.PATCH(path, handler)
	case "DELETE":
		r.DELETE(path, handler)
	default:
		// An unknown method is not mounted: the route is then
		// unreachable, which is better than answering every method.
	}
}

// bundles returns the plugins' frontend bundles, in dependency order.
//
// The order is the plugins' load order, which is their dependency
// order: a plugin's initializer may extend a core component, so the
// core bundle comes first and the plugins follow in dependency order.
func (m *Module) bundles() []frontend.Bundle {
	bundles := make([]frontend.Bundle, 0)
	for _, id := range m.plugins.Order() {
		extenders := m.plugins.PluginExtenders()[id]
		if extenders == nil {
			continue
		}
		for _, asset := range extenders.Assets() {
			bundles = append(bundles, frontend.Bundle{
				ID:      asset.PluginID,
				Version: asset.Version,
				Forum:   asset.Forum,
				Admin:   asset.Admin,
				CSS:     asset.CSS,
			})
		}
	}
	return bundles
}

// coreFrontendAssets are the bundles the shell emits before the plugins'.
//
// The locale bundles are NOT listed here: their names depend on the
// active locale, so they are resolved by [Module.localeAssetNames] and
// reported by the same check.
//
// They are listed so a PARTIAL build is reported. `ui/dist` is build
// output and is gitignored, so a tree that carries only some of it is
// easy to produce (a core-only rebuild, a stale directory kept between
// builds). The shell would then reference files that are not embedded.
var coreFrontendAssets = []string{
	"forum.js", "forum.css",
	"admin.js", "admin.css",
}

// warnMissingAssets logs every advertised frontend asset the embedded
// tree does not carry.
//
// The shell emits a script/link tag per registered bundle, and a tag for
// a file that is not embedded is a silent 404: nothing reaches the
// console and the plugin's UI never appears. The logs are therefore the
// only place the cause is visible, and they are written ONCE (from
// mount) because RenderBootShell runs per request.
//
// It is skipped entirely when no build was dropped in: that is the
// documented fallback-shell case, and warning about every asset in it
// would bury the real signal.
func (m *Module) warnMissingAssets() {
	if m.assets == nil || !m.assets.HasBuild() {
		return
	}

	for _, name := range coreFrontendAssets {
		if !m.assets.Exists(name) {
			m.logger.Warn("forum: the embedded frontend does not carry a core asset", "asset", name)
		}
	}

	for _, bundle := range m.bundles() {
		for _, asset := range []string{bundle.Forum, bundle.Admin, bundle.CSS} {
			if asset == "" {
				continue
			}
			if !m.assets.Exists(strings.TrimPrefix(asset, "/")) {
				m.logger.Warn(
					"forum: a plugin's frontend asset is not embedded, so its UI will not load",
					"plugin", bundle.ID,
					"asset", asset,
				)
			}
		}
	}

	// The locale bundles are per-language, so they are checked for EVERY
	// declared locale rather than only the active one: a missing one is a
	// build fault, and it should surface at startup even on an instance
	// that currently runs in another language.
	for _, bundle := range m.localeBundles() {
		for _, asset := range []string{bundle.Forum, bundle.Admin} {
			if asset == "" {
				continue
			}
			if !m.assets.Exists(strings.TrimPrefix(asset, "/")) {
				m.logger.Warn(
					"forum: a language pack's catalogue is not embedded, so that locale will render raw keys",
					"plugin", bundle.PluginID,
					"locale", bundle.Code,
					"asset", asset,
				)
			}
		}
	}
}

// localeBundles returns the language packs the plugins declared, in
// dependency order.
func (m *Module) localeBundles() []plugin.LocaleBundle {
	bundles := make([]plugin.LocaleBundle, 0)
	for _, id := range m.plugins.Order() {
		extenders := m.plugins.PluginExtenders()[id]
		if extenders == nil {
			continue
		}
		bundles = append(bundles, extenders.Locales()...)
	}
	return bundles
}

// defaultLocale is the locale the core ships a catalogue for.
//
// The core's own compile step (`packages/core/build-locale.cjs`) produces
// `forum-en.js` and `admin-en.js`, so it is always available and is the
// fallback for an unset or unknown request.
const defaultLocale = "en"

// defaultLocaleTitle is the name the built-in locale is listed under.
const defaultLocaleTitle = "English"

// locales returns the available locales as code -> title.
//
// The built-in English catalogue is always present; a language pack adds
// its own entry. The map is what the boot payload's `locales` carries and
// what the frontend's locale list shows.
func (m *Module) locales() map[string]string {
	out := map[string]string{defaultLocale: defaultLocaleTitle}
	for _, bundle := range m.localeBundles() {
		if bundle.Code == "" || bundle.Code == defaultLocale {
			continue
		}
		title := bundle.Title
		if title == "" {
			title = bundle.Code
		}
		out[bundle.Code] = title
	}
	return out
}

// activeLocale returns the locale the shell should load.
//
// An unset value means English. A value with no catalogue behind it also
// means English, for two separate reasons:
//
//   - no language pack declares it (a typo in `RIFF_FORUM_LOCALE`);
//   - one declares it but its bundle was never built or staged.
//
// Both would otherwise render raw translation keys
// (`core.forum.header.back_to_index_tooltip`) in the UI, which reads as a
// frontend bug rather than a configuration or build one. warnMissingAssets
// still names the missing file, so the degradation is visible in the logs
// rather than silent.
func (m *Module) activeLocale() string {
	requested := strings.TrimSpace(m.opts.Locale)
	if requested == "" || requested == defaultLocale {
		return defaultLocale
	}

	if _, ok := m.locales()[requested]; !ok {
		return defaultLocale
	}

	// Declared but not embedded: the catalogue the UI needs is not there.
	if name := m.localeAssetName(requested, false); name == "" || !m.assets.Exists(name) {
		return defaultLocale
	}

	return requested
}

// localeAssetName returns the locale bundle for one frontend.
//
// The core ships its own catalogue for `en`; any other locale comes from a
// language pack, which declares the two file names. The path is returned
// as declared (relative to the asset root) because a language pack may put
// it anywhere — `plugins/chinese/forum-zh-Hans.js` here.
//
// An empty string means no bundle is declared for that frontend, which the
// caller treats as "nothing to load".
func (m *Module) localeAssetName(locale string, admin bool) string {
	if locale == defaultLocale {
		if admin {
			return "admin-" + defaultLocale + ".js"
		}
		return "forum-" + defaultLocale + ".js"
	}

	for _, bundle := range m.localeBundles() {
		if bundle.Code != locale {
			continue
		}
		path := bundle.Forum
		if admin {
			path = bundle.Admin
		}
		return strings.TrimPrefix(path, "/")
	}

	return ""
}

// RenderBootShell renders the SPA shell.
//
// It is the frontend package's BootRenderer implementation: it builds
// the payload the app boots with and lists the bundles to load.
func (m *Module) RenderBootShell(e *core.RequestEvent, admin bool) ([]byte, error) {
	payload, err := m.bootPayload(e, admin)
	if err != nil {
		return nil, err
	}

	scripts := m.scriptURLs(admin)
	styles := m.styleURLs(admin)

	return frontend.Render(payload, scripts, styles, map[string]string{
		"forum.js": m.assets.Revision(),
		"admin.js": m.assets.Revision(),
	}, admin)
}

// scriptURLs returns the script URLs to load, in order.
//
// The core bundle comes first, then its locale bundle, then the plugin
// bundles in dependency order: a plugin's initializer may extend a core
// component, so the core must be registered first.
//
// The locale bundle is the compiled `app.translator.addTranslations(…)`
// call (see packages/core/build-locale.cjs). It is a separate script
// rather than part of the entry because the translations are a
// per-locale asset upstream, and the boot payload advertises which
// locale is wanted. Without it every `trans()` call renders the raw
// key: the UI shows "core.forum.header.back_to_index_tooltip" instead
// of the label.
func (m *Module) scriptURLs(admin bool) []string {
	base := m.opts.BasePath + "/assets/"
	urls := []string{}

	entry := "forum.js"
	if admin {
		entry = "admin.js"
	}
	urls = append(urls, base+entry)

	// The active locale's catalogue, right after the entry: `trans()` is
	// called while the entry registers, and the bundle is what stops the
	// keys rendering raw.
	//
	// It is resolved through the locale rather than hardcoded: the core
	// ships `en`, and a language pack ships the other codes. Only the
	// active locale's pair is loaded — a second catalogue would merge its
	// keys over the first.
	if name := m.localeAssetName(m.activeLocale(), admin); name != "" && m.assets.Exists(name) {
		urls = append(urls, base+name)
	}

	for _, bundle := range m.bundles() {
		path := bundle.Forum
		if admin {
			path = bundle.Admin
		}
		if path == "" {
			continue
		}
		// A bundle the embedded tree does not carry is skipped rather
		// than emitted: the tag would 404 silently and the plugin's UI
		// would never appear, with nothing in the console to say why.
		// warnMissingAssets names the file in the logs.
		if !m.assets.Exists(strings.TrimPrefix(path, "/")) {
			continue
		}
		urls = append(urls, base+strings.TrimPrefix(path, "/")+"?v="+bundle.Version)
	}
	return urls
}

// styleURLs returns the stylesheet URLs to load, in order.
//
// The core stylesheet comes first — it carries the framework's own
// classes, and a plugin's stylesheet assumes them — then the plugin
// stylesheets in dependency order.
//
// The core stylesheet is compiled from the vendored Less tree
// (packages/core/less) into `forum.css` and `admin.css`. The forum and
// the admin app are the same SPA with a different entry bundle, so the
// path decides which stylesheet is loaded, exactly as it does for the
// script.
func (m *Module) styleURLs(admin bool) []string {
	base := m.opts.BasePath + "/assets/"
	urls := []string{}

	if admin {
		urls = append(urls, base+"admin.css")
	} else {
		urls = append(urls, base+"forum.css")
	}

	for _, bundle := range m.bundles() {
		if bundle.CSS == "" {
			continue
		}
		// Same rule as the scripts: a stylesheet that was not embedded
		// is skipped rather than emitted as a dead link.
		if !m.assets.Exists(strings.TrimPrefix(bundle.CSS, "/")) {
			continue
		}
		urls = append(urls, base+strings.TrimPrefix(bundle.CSS, "/")+"?v="+bundle.Version)
	}
	return urls
}

// bootPayload builds the payload the SPA boots with.
//
// The resources are the forum document and, when the caller is signed
// in, the caller. The frontend reads them out of the payload and pushes
// them into its store.
// bootPayload builds the payload the SPA boots with.
//
// The session part is what makes the login work: the frontend reads
// `session.userId` and `session.csrfToken` (see the core's
// Application.load) and looks the caller up in the store by that id.
// Without it the SPA always boots anonymous, even with a valid session
// cookie — the login would succeed and the page would still show as
// signed out.
//
// The caller is resolved through the same session resolver the API
// uses, so a bearer token, the PocketBase auth middleware and the
// session cookie are all accepted.
func (m *Module) bootPayload(e *core.RequestEvent, admin bool) (frontend.BootPayload, error) {
	payload := frontend.BootPayload{
		Resources:      []any{},
		Session:        frontend.BootSession{},
		Locale:         m.activeLocale(),
		Locales:        m.locales(),
		AssetsRevision: m.assets.Revision(),
	}

	document, err := m.forumDocument(e)
	if err != nil {
		return payload, err
	}
	payload.Resources = append(payload.Resources, document)

	// The admin application's data. It does NOT depend on the caller, so
	// it is set before the actor is resolved: an anonymous visit to
	// /admin still has to boot (the app then sends the visitor to log in).
	if admin {
		payload.Admin = m.adminPayload()
	}

	// The caller. A failure to resolve is not fatal: the page still
	// boots, anonymously.
	actor, resolveErr := m.sessions.Resolve(e)
	if resolveErr != nil {
		m.logger.Debug("forum: the boot payload could not resolve the caller",
			slog.String("error", resolveErr.Error()))
		return payload, nil
	}
	if actor.User == nil {
		return payload, nil
	}

	payload.Session.UserID = actor.User.ID
	payload.Session.CSRFToken = actor.CSRFToken

	// The caller is also a resource in the payload, because the frontend
	// reads the user from the store — `store.getById('users', userId)` —
	// and a relationship's identifier alone carries no attributes. The
	// resource goes in beside the forum document, exactly as upstream's
	// API document carries the actor.
	payload.Resources = append(payload.Resources, m.api.UserResource(actor.User))

	return payload, nil
}

// adminPayload builds the data the admin application reads.
//
// The admin app is the same SPA with a different entry bundle and a much
// larger payload: `BasicsPage.register` walks the driver lists during
// boot, so a payload without them throws and the page renders blank.
//
// The extensions list is the forum's own plugins, which is real data.
// Everything riff has no equivalent for (composer packages, editable
// instance settings, the php-side dashboard counts) is left empty, so a
// page shows nothing rather than something invented.
func (m *Module) adminPayload() *frontend.Admin {
	extensions := map[string]any{}
	for _, manifest := range m.plugins.Manifests() {
		// **THE EXTENSION ID IS FLATTENED, BECAUSE IT IS A PATH
		// SEGMENT.** A plugin's id is `vendor/name` — it CONTAINS A
		// SLASH — and the admin app's extension page lives at
		// `/extension/:id`, a pattern with ONE segment. The route
		// builder substitutes the id verbatim, so an id with a slash
		// becomes two segments and the route does not match: the
		// extension's settings page is unreachable, and every entry
		// in the admin sidebar links to a URL the router cannot
		// resolve.
		//
		// Upstream avoids this the same way: `Extension::nameToId()`
		// flattens `flarum/tags` to `flarum-tags`, and THAT is the
		// key the admin app's `app.data.extensions` is keyed by. The
		// flattened id is therefore both the map key and the route
		// parameter, and they agree.
		id := repository.ExtensionID(manifest.ID)

		extensions[id] = map[string]any{
			"id":          id,
			"name":        manifest.Name,
			"version":     manifest.Version,
			"description": "",
			"icon":        map[string]any{"name": manifest.Icon.Glyph},
			"links":       map[string]any{},
			"extra": map[string]any{
				"flarum-extension": map[string]any{"title": manifest.Name},
			},
		}
	}

	return &frontend.Admin{
		Extensions:        extensions,
		InstalledPackages: []string{},
		Settings:          m.adminSettings(),
		ModelStatistics:   m.adminModelStatistics(),
		// The display name is the username, and the avatar is PocketBase's
		// own file — upstream's `default` driver.
		DisplayNameDrivers: []string{"username"},
		AvatarDrivers:      []string{"default"},
		SlugDrivers: map[string][]string{
			"users":       {"username"},
			"discussions": {"id"},
		},
		SearchDrivers:       map[string][]string{},
		Permissions:         m.adminPermissions(),
		MaintenanceByConfig: false,
		DBDriver:            m.databaseDriver(),
		DBVersion:           "",
		DBDriverMismatch:    nil,
		DBVersionStatus:     nil,
		DBOptions:           map[string]any{},
		PHPVersion:          "",
		// riff has no job queue, so the queue is synchronous.
		QueueDriver:                 "sync",
		PausedQueues:                []string{},
		KnownQueues:                 []string{},
		SchedulerStatus:             "never-run",
		SessionDriver:               "cookie",
		SessionByConfig:             false,
		SessionLifetime:             session.SessionCookieMaxAge / 60,
		SessionCookieExpiresOnClose: false,
		AccessTokenLifetimes:        []map[string]any{},
		Bisecting:                   false,
		SafeModeExtensions:          []string{},
		SafeModeExtensionsConfig:    []string{},
		AnnouncementsDisabled:       true,
		FontAwesomeByConfig:         false,
		FontAwesomeConfig:           map[string]any{},
		DebugEnabled:                false,
		ThemeImages:                 map[string]any{},
		SortMaps:                    adminSortMaps(),
	}
}

// adminSettings returns the settings the admin application reads.
//
// The values are the forum's own settings, plus the upstream defaults
// for the keys the admin app dereferences. The defaults matter: the
// admin app PARSES some of them as JSON (`isExtensionEnabled` does
// `JSON.parse(app.data.settings.extensions_enabled)`), and
// `JSON.parse(undefined)` throws — so an absent key is a blank page.
//
// The forum's own settings win over the defaults, so an operator's
// choice is what the admin app shows.
func (m *Module) adminSettings() map[string]any {
	settings := map[string]any{
		// The defaults upstream writes at install. The keys the admin app
		// reads are the ones that matter; the rest fill the settings
		// forms with the same values upstream shows.
		"allow_hide_own_posts": "reply",
		"allow_post_editing":   "reply",
		"allow_renaming":       "10",
		// Upstream's tags extension defaults this to "0" — "Never".
		// Until an administrator picks an option, the author of a
		// discussion may not change its tags.
		"allow_tag_change":               "0",
		"allow_sign_up":                  "1",
		"avatar_driver":                  "default",
		"custom_less":                    "",
		"default_locale":                 "en",
		"default_route":                  "/all",
		"display_name_driver":            "username",
		"extension_bisect_state":         `{"ids":[],"low":0,"high":0}`,
		"fontawesome_source":             "local",
		"fontawesome_cdn_url":            "",
		"fontawesome_kit_url":            "",
		"forum_description":              "",
		"forum_title":                    "Forum",
		"mail_driver":                    "mail",
		"mail_format":                    "multipart",
		"mail_from":                      "noreply@localhost",
		"open_as_app":                    "1",
		"show_advanced_settings":         "0",
		"slug_driver_Flarum\\User\\User": "default",
		"theme_colored_header":           "0",
		"color_scheme":                   "auto",
		"allow_user_color_scheme":        "1",
		"show_theme_selector":            "0",
		"theme_primary_color":            "#4D698E",
		"theme_secondary_color":          "#4D698E",
		// The uploaded assets. The Appearance page's upload buttons read
		// these paths, and they are SETTINGS — so a stored value has to
		// win over the default.
		"logo_path":                "",
		"logo_dark_mode_path":      "",
		"favicon_path":             "",
		"email_logo_path":          "",
		"app_icon_path":            "",
		"fontawesome_forced_style": "",
		"welcome_message":          "",
		"welcome_title":            "",
	}

	// The enabled plugins, as upstream's setting holds them: a JSON
	// array of the plugin ids.
	//
	// This is the value `isExtensionEnabled` parses, so it must be a
	// JSON document and not a Go slice.
	//
	// The ids are the FLATTENED ones, because that is what the admin
	// app looks up: `isExtensionEnabled` asks whether the list holds
	// the extension's id, and the extension's id is the flattened one
	// the admin payload published. A list of the raw `vendor/name`
	// ids would report EVERY plugin as disabled, and the extension
	// page would then show "enable to see" instead of its settings.
	enabled := []string{}
	for _, manifest := range m.plugins.Manifests() {
		enabled = append(enabled, repository.ExtensionID(manifest.ID))
	}
	settings["extensions_enabled"] = mustJSON(enabled)

	// The plugins' declared settings carry their DEFAULTS, so the
	// admin application shows a setting's value even before an
	// operator has saved one.
	//
	// The default is the value the plugin's own reader falls back to,
	// so the form and the forum agree: a form that showed "1" while
	// the forum used "0" would be a setting the operator could not
	// trust.
	for _, schema := range m.PluginSettingSchemas() {
		for _, field := range schema.Fields {
			if field.Key == "" {
				continue
			}
			if _, taken := settings[field.Key]; taken {
				// A stored setting wins over the default.
				continue
			}
			if field.Default == nil {
				continue
			}
			settings[field.Key] = field.Default
		}
	}

	// The forum's own settings win.
	if stored, err := m.repo.AllSettings(); err == nil {
		for key, value := range stored {
			settings[key] = value
		}
	}

	// EVERY setting is a STRING, the way upstream's settings table
	// holds them.
	//
	// Upstream's settings column is TEXT, so `Settings::all()` hands
	// the admin application a map of strings and the application types
	// every setting as `Stream<string>`. This port's settings column
	// is a JSON field, so a stored value arrives as the JSON type it
	// was written as — a number as `float64`, a boolean as `bool`.
	//
	// That mismatch is not cosmetic: the admin application reads a
	// setting into a `Stream<string>`, so a number lands in a stream
	// typed as a string, and the form saves it back as a number. A
	// boolean switch reads `!!value && value !== '0'`, which happens
	// to work for a JSON `false`, but the SETTING the operator edits
	// is then a number where upstream's is the string "0" — and the
	// server's own reader has to cope with both.
	//
	// Normalising here means the admin application sees what upstream
	// shows it: strings.
	for key, value := range settings {
		settings[key] = adminSettingString(value)
	}

	return settings
}

// adminSettingString renders a setting value the way upstream's
// settings table holds it.
//
// Upstream stores every setting as a string, so a boolean is "1" or
// "0" and a number is its decimal form. The two keys the admin
// application PARSES as JSON are exempt: they are JSON documents, and
// their value must stay a document.
func adminSettingString(value any) any {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.RawMessage:
		return adminSettingStringFromJSON(typed)
	case types.JSONRaw:
		return adminSettingStringFromJSON(typed)
	default:
		return value
	}
}

// adminSettingStringFromJSON renders a JSON-encoded setting value as
// the string upstream's settings table would hold.
//
// The settings table's `value` column is a JSON field, so a stored
// value arrives as RAW JSON BYTES (`types.JSONRaw`). A number arrives
// as `0`, a boolean as `true`, and a string as `"0"` — WITH ITS
// QUOTES, because it is a JSON string.
//
// The quotes are stripped, because upstream's settings are the bare
// text: a boolean switch reads `value !== '0'`, and a quoted `"0"`
// is neither `0` nor `"0"`'s unquoted sense — it would read as ON.
func adminSettingStringFromJSON(raw []byte) any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}

	// A JSON string arrives quoted: the quotes are part of the
	// encoding, not of the value.
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
		return trimmed
	}

	// A JSON document (an object or an array) stays a document: the
	// admin application PARSES those (`isExtensionEnabled` does
	// `JSON.parse(app.data.settings.extensions_enabled)`), and a
	// quoted document would not parse.
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return trimmed
	}

	// A number, a boolean or null.
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}

	var flag bool
	if err := json.Unmarshal(raw, &flag); err == nil {
		if flag {
			return "1"
		}
		return "0"
	}

	return trimmed
}

// adminSortMaps returns the sort options each resource offers.
//
// The admin application reads them as `app.data.sortMaps`, and a
// plugin's admin page uses them to offer a choice: upstream's tags
// extension lists a tag's DEFAULT SORT from this map, so that a sort
// an extension added can be chosen for a tag without that extension
// knowing anything about tags.
//
// The map is keyed by resource, and each entry maps the sort's URL
// key to the API sort behind it — the same shape the forum's own
// `DiscussionListState.sortMap()` builds.
func adminSortMaps() map[string]any {
	return map[string]any{
		"discussions": map[string]any{
			"latest":    "-lastPostedAt",
			"top":       "-commentCount",
			"newest":    "-createdAt",
			"oldest":    "createdAt",
			"az":        "title",
			"za":        "-title",
			"relevance": "",
		},
	}
}

// PluginSettingSchemas returns every plugin's declared settings.
//
// A plugin declares the settings it READS, so the admin application
// can show each one with the value the forum would use for it.
//
// It is exported because the contract it describes spans the two
// halves of the forum: the schema names the settings, and the
// plugin's admin bundle registers the form that edits them. A caller
// that wants to check the two agree needs both.
func (m *Module) PluginSettingSchemas() []plugin.SettingsSchema {
	var schemas []plugin.SettingsSchema

	for _, extenders := range m.plugins.PluginExtenders() {
		schemas = append(schemas, extenders.Settings()...)
	}

	return schemas
}

// NotificationTypes returns the notification kinds the plugins send.
//
// The forum publishes a preference for every declared (kind, channel) pair,
// which is what the settings page renders a checkbox for — so a kind nobody
// declares is a kind a reader cannot switch off.
func (m *Module) NotificationTypes() []plugin.NotificationType {
	var types []plugin.NotificationType

	for _, extenders := range m.plugins.PluginExtenders() {
		types = append(types, extenders.NotificationTypes()...)
	}

	return types
}

// notificationDefaults turns the declared kinds into preference keys.
//
// It is upstream's `Notification::getDefaults()`: every registered blueprint
// × every channel it supports, enabled. The settings page reads them merged
// under the reader's own answers, and the write path treats a missing key as
// enabled — so an undeclared kind still notifies, it just cannot be turned
// off.
func (m *Module) notificationDefaults() map[string]bool {
	defaults := map[string]bool{}

	for _, notificationType := range m.NotificationTypes() {
		for _, channel := range notificationType.Channels {
			defaults[repository.NotificationPreferenceKey(notificationType.Type, channel)] = true
		}
	}

	return defaults
}

// adminPermissions returns the granted permissions, per permission.
//
// It is upstream's `Permission::map()`
// (`framework/core/src/Admin/Content/AdminPayload.php` reads
// `$document->payload['permissions'] = Permission::map();`), and the
// admin application reads it into `app.data.permissions`.
//
// The map is NOT optional. `PermissionDropdown.tsx` reads
// `app.data.permissions[permission]` for the permission being edited and
// for every permission it requires, and uses the answer to decide
// which groups the permission may be granted to — so an empty map makes
// the whole grid inert: every dropdown reads "no group holds this",
// the "everyone" and "members" badges never light up, and a required
// permission ("you cannot reply unless you can see the forum") narrows
// every group to nothing.
//
// The keys are translated into upstream's names, because the admin
// application registers upstream's names
// (`permission: 'discussion.reply'`) and looks them up verbatim.
//
// The SCOPED rows are in the same map, under their scoped key
// (`tag{id}.{permission}`): the admin grid's tag column asks about
// exactly that key, so a scoped grant that is not in this map is
// invisible to the grid — the column would show that nobody holds the
// permission even though the row says otherwise.
func (m *Module) adminPermissions() map[string][]string {
	stored, err := m.repo.PermissionMap()
	if err != nil {
		// The permissions could not be read. An empty map is what the
		// admin app can cope with; a wrong one is not.
		return map[string][]string{}
	}

	permissions := make(map[string][]string, len(stored))
	for permission, groups := range stored {
		key := domain.UpstreamPermissionKey(permission)
		permissions[key] = append(permissions[key], groups...)
	}

	scoped, err := m.repo.ScopedPermissionMap()
	if err == nil {
		for key, groups := range scoped {
			permissions[key] = append(permissions[key], groups...)
		}
	}

	return permissions
}

// adminModelStatistics returns the counts the admin app reads.
//
// `UserListPage` reads `app.data.modelStatistics.users.total` in a
// field initialiser, so the `users` entry must be there even when the
// forum has no accounts yet.
func (m *Module) adminModelStatistics() map[string]any {
	users := 0
	if collection, err := m.app.FindCollectionByNameOrId(repository.SharedUsersCollection); err == nil {
		if count, err := m.app.CountRecords(collection); err == nil {
			users = int(count)
		}
	}

	return map[string]any{
		"users": map[string]any{"total": users},
	}
}

// databaseDriver reports the running database driver, in the spelling
// the admin app expects.
//
// `ExtensionPage` calls `app.data.dbDriver.toLowerCase()`, so the value
// must be a non-empty string.
func (m *Module) databaseDriver() string {
	// `app.DB()` is a `*dbx.DB` in the common case, but PocketBase wraps
	// it in a `dualDBBuilder` when the concurrent and non-concurrent
	// handles differ, so the assertion is checked rather than assumed.
	driver := ""
	if db, ok := m.app.DB().(*dbx.DB); ok {
		driver = db.DriverName()
	}

	switch driver {
	case "mysql":
		return "MySQL"
	case "postgres":
		return "PostgreSQL"
	default:
		// SQLite, and any driver riff does not know, is reported as
		// SQLite rather than left empty: `ExtensionPage` calls
		// `app.data.dbDriver.toLowerCase()`, so an empty string throws.
		return "SQLite"
	}
}

// mustJSON encodes a value as JSON.
//
// It cannot fail for the values it is given (a list of strings), so a
// failure would be a programming error rather than a runtime condition.
func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// IsAdmin reports whether the caller administers the forum.
//
// The admin application is only served to an administrator, exactly as
// upstream serves it: upstream's admin routes carry the
// `RequireAdministrateAbility` middleware, so a non-administrator never
// receives the admin shell.
//
// The admin app assumes a signed-in administrator at BOOT time —
// `MailPage.register` reads `app.session.user!.email()`, which throws on
// a null user — so this gate is what keeps the admin page from
// rendering blank for an anonymous visitor.
func (m *Module) IsAdmin(e *core.RequestEvent) bool {
	actor, err := m.sessions.Resolve(e)
	if err != nil {
		return false
	}
	return actor.IsAdmin()
}

// forumDocument builds the forum resource the SPA boots with.
//
// It is the API server's document, unchanged: the same document the
// API's GET /api/ returns, so the SPA and the API cannot drift apart.
//
// It is PER-CALLER, because the `can*` attributes are the caller's
// permissions. Upstream computes them the same way
// (`ForumResource.php`: `$context->getActor()->can('startDiscussion')`),
// and the frontend's own affordances are built on them: the "New
// Discussion" button is disabled when `canStartDiscussion` is false.
//
// The two copies used to be separate, and the boot payload's copy
// hardcoded the flags to false — so the button was disabled for every
// signed-in account even though the API reported the right answer.
func (m *Module) forumDocument(e *core.RequestEvent) (api.Resource, error) {
	return m.api.ForumDocument(e)
}

// loginHandler serves POST {basePath}/login.
//
// The body is the frontend's Session.login payload:
// `{"identification": ..., "password": ..., "remember": ...}`.
func (m *Module) loginHandler(e *core.RequestEvent) error {
	var body session.LoginRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The request body could not be read.")
	}

	user, token, err := m.sessions.Login(e, body)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "Those credentials were not accepted.")
	}

	csrf, err := session.NewCSRFToken()
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The session could not be created.")
	}
	m.sessions.SetSessionCookie(e, token, csrf)

	e.Response.Header().Set("Content-Type", api.ContentType)
	return e.JSON(http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":          user.ID,
			"username":    user.Username,
			"displayName": user.DisplayName,
			"avatarUrl":   user.AvatarURL,
			"isAdmin":     user.IsAdmin,
		},
		"csrfToken": csrf,
	})
}

// logoutHandler serves POST {basePath}/logout.
//
// The frontend logs out with a FORM submission (see the core's
// `Session.logout`, which builds a form and submits it), so the
// response is a REDIRECT to the forum, not a JSON body: the browser
// navigates to whatever the handler answers, and a JSON body would
// leave the reader staring at `{"message":"Logged out."}`.
//
// A `return` query parameter names where to go afterwards. It is
// restricted to a path inside the forum: an absolute URL would make
// the logout an open redirect.
func (m *Module) logoutHandler(e *core.RequestEvent) error {
	m.sessions.ClearSessionCookie(e)

	return e.Redirect(http.StatusFound, m.logoutReturnURL(e))
}

// logoutReturnURL resolves the `return` query parameter.
//
// Only a path within the forum is honoured, so the logout cannot be
// turned into an open redirect; anything else falls back to the forum
// root.
func (m *Module) logoutReturnURL(e *core.RequestEvent) string {
	root := m.opts.BasePath + "/"

	target := strings.TrimSpace(e.Request.URL.Query().Get("return"))
	if target == "" {
		return root
	}

	// A path, not a URL: an absolute one (or a scheme-relative one)
	// would send the reader off the site.
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return root
	}

	// Outside the forum's own subtree is not ours to redirect to.
	if !strings.HasPrefix(target, root) {
		return root
	}

	return target
}

// unusedContext keeps the context import honest.
var _ = context.Background
