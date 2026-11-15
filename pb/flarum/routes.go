package flarum

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/frontend"
	"github.com/incurl/riff/pb/flarum/plugin"
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
func (m *Module) RenderBootShell(admin bool) ([]byte, error) {
	payload, err := m.bootPayload(admin)
	if err != nil {
		return nil, err
	}

	scripts := m.scriptURLs(admin)
	styles := m.styleURLs(admin)

	return frontend.Render(payload, scripts, styles, map[string]string{
		"forum.js": m.assets.Revision(),
		"admin.js": m.assets.Revision(),
	})
}

// scriptURLs returns the script URLs to load, in order.
//
// The core bundle comes first, then the plugin bundles in dependency
// order: a plugin's initializer may extend a core component, so the
// core must be registered first.
func (m *Module) scriptURLs(admin bool) []string {
	base := m.opts.BasePath + "/assets/"
	urls := []string{}
	if admin {
		urls = append(urls, base+"admin.js")
	} else {
		urls = append(urls, base+"forum.js")
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
func (m *Module) styleURLs(_ bool) []string {
	base := m.opts.BasePath + "/assets/"
	urls := []string{}

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
func (m *Module) bootPayload(_ bool) (frontend.BootPayload, error) {
	payload := frontend.BootPayload{
		Resources:      []any{},
		Session:        frontend.BootSession{},
		Locale:         "en",
		Locales:        map[string]string{"en": "English"},
		AssetsRevision: m.assets.Revision(),
	}

	document, err := m.forumDocument()
	if err != nil {
		return payload, err
	}
	payload.Resources = append(payload.Resources, document)

	return payload, nil
}

// forumDocument builds the forum resource the SPA boots with.
//
// It is the same document the API's GET /api/ returns, so the SPA and
// the API cannot drift apart.
func (m *Module) forumDocument() (map[string]any, error) {
	attributes := map[string]any{
		"title":                   "Forum",
		"description":             "",
		"baseUrl":                 m.opts.BasePath,
		"basePath":                m.opts.BasePath,
		"baseOrigin":              "",
		"apiUrl":                  m.opts.BasePath + "/api",
		"debug":                   false,
		"welcomeTitle":            "",
		"welcomeMessage":          "",
		"showLanguageSelector":    false,
		"showThemeSelector":       false,
		"showPostSource":          false,
		"allowSignUp":             false,
		"defaultRoute":            "/",
		"canViewForum":            true,
		"canStartDiscussion":      false,
		"canSearchUsers":          false,
		"canCreateAccessToken":    false,
		"canModerateAccessTokens": false,
		"canEditUserCredentials":  false,
		"serviceWorker":           false,
		"assetsBaseUrl":           m.opts.BasePath + "/assets",
		"jsChunksBaseUrl":         m.opts.BasePath + "/assets/js",
		"adminUrl":                m.opts.BasePath + "/admin",
		"version":                 CoreVersion,
	}

	groups, err := m.repo.ListGroups()
	if err != nil {
		return nil, err
	}
	groupIDs := make([]string, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
	}

	return map[string]any{
		"type":       api.TypeForum,
		"id":         "1",
		"attributes": attributes,
		"relationships": map[string]any{
			"groups": map[string]any{
				"data": identifiers(api.TypeGroup, groupIDs),
			},
			"actor": map[string]any{
				"data": nil,
			},
		},
	}, nil
}

// identifiers builds a list of JSON:API identifiers.
func identifiers(resourceType string, ids []string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, map[string]any{"type": resourceType, "id": id})
	}
	return out
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
func (m *Module) logoutHandler(e *core.RequestEvent) error {
	m.sessions.ClearSessionCookie(e)
	e.Response.Header().Set("Content-Type", api.ContentType)
	return e.JSON(http.StatusOK, map[string]any{"message": "Logged out."})
}

// unusedContext keeps the context import honest.
var _ = context.Background
