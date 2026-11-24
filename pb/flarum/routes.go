package flarum

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

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

	// --- the frontend --------------------------------------------------
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
	if admin {
		urls = append(urls, base+"admin.js", base+"admin-en.js")
	} else {
		urls = append(urls, base+"forum.js", base+"forum-en.js")
	}

	for _, bundle := range m.bundles() {
		path := bundle.Forum
		if admin {
			path = bundle.Admin
		}
		if path == "" {
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
		Locale:         "en",
		Locales:        map[string]string{"en": "English"},
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
		extensions[manifest.ID] = map[string]any{
			"id":          manifest.ID,
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
		SortMaps:                    map[string]any{},
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
		"theme_primary_color":            "#4D698E",
		"theme_secondary_color":          "#4D698E",
		"welcome_message":                "",
		"welcome_title":                  "",
	}

	// The enabled plugins, as upstream's setting holds them: a JSON
	// array of the plugin ids.
	//
	// This is the value `isExtensionEnabled` parses, so it must be a
	// JSON document and not a Go slice.
	enabled := []string{}
	for _, manifest := range m.plugins.Manifests() {
		enabled = append(enabled, manifest.ID)
	}
	settings["extensions_enabled"] = mustJSON(enabled)

	// The forum's own settings win.
	if stored, err := m.repo.AllSettings(); err == nil {
		for key, value := range stored {
			settings[key] = value
		}
	}

	return settings
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
