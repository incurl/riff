package flarum

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAdmin_ShellProvidesTheNavigationTarget pins the admin shell.
//
// The admin app is the same SPA with a different entry bundle, and its
// mount() calls `m.mount(document.getElementById('admin-navigation'), …)`.
// The forum shell has no such element — rendering the forum shell for the
// admin app throws on a null container and the page stays blank, which is
// exactly what "empty admin page" was.
func TestAdmin_ShellProvidesTheNavigationTarget(t *testing.T) {
	app, handler := newTestForum(t)

	admin := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, admin.Code, admin.Body.String())

	body := admin.Body.String()
	require.Contains(t, body, `id="admin-navigation"`, "the admin app mounts its nav here")
	require.Contains(t, body, `class="sideNavOffset"`, "the admin content sits beside the nav")

	// Every container the admin application mounts into.
	for _, id := range []string{"admin-navigation", "app-navigation", "header-navigation", "header-primary", "header-secondary", "content", "modal", "alerts"} {
		require.Contains(t, body, `id="`+id+`"`, "the admin shell should provide #%s", id)
	}
}

// TestAdmin_ForumShellIsNotTheAdminShell pins that the forum app is
// unaffected: an empty nav column in the forum layout would be visible.
func TestAdmin_ForumShellIsNotTheAdminShell(t *testing.T) {
	app, handler := newTestForum(t)

	forum := do(t, handler, http.MethodGet, "/forum/", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, forum.Code, forum.Body.String())

	body := forum.Body.String()
	require.NotContains(t, body, "admin-navigation")
	require.NotContains(t, body, "sideNavOffset")
}

// TestAdmin_PayloadCarriesWhatTheAdminAppReadsAtBoot pins the admin
// payload.
//
// `BasicsPage.register` walks `displayNameDrivers`, `avatarDrivers` and
// `slugDrivers` during boot (via `runBeforeMount`), so a payload without
// them throws before anything renders. `locales` is read the same way.
func TestAdmin_PayloadCarriesWhatTheAdminAppReadsAtBoot(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)
	admin := payload.Admin

	// A local first: the field is an EMBEDDED pointer, so reading a
	// promoted field before this nil check would panic.
	require.NotNil(t, admin, "the admin shell should carry the admin data")
	require.Equal(t, []string{"username"}, admin.DisplayNameDrivers)
	require.Equal(t, []string{"default"}, admin.AvatarDrivers)
	require.NotEmpty(t, admin.SlugDrivers, "the boot walks Object.keys(slugDrivers)")
	require.NotEmpty(t, payload.Locales, "the boot walks Object.keys(locales)")

	// The extensions list is the forum's own plugins: real data, not a
	// placeholder.
	//
	// **THE IDS ARE FLATTENED**, because the admin application
	// addresses an extension by the id in its URL and that id is a
	// PATH SEGMENT: a plugin's raw id is `vendor/name`, which
	// contains a slash and would be two segments. Upstream flattens the
	// same way (`Extension::nameToId()`).
	require.Contains(t, admin.Extensions, "riff-tags", "the tags plugin should be listed")
	require.Contains(t, admin.Extensions, "riff-likes", "the likes plugin should be listed")
	require.Contains(t, admin.Extensions, "flarum-mentions", "the mentions plugin should be listed")

	for id := range admin.Extensions {
		require.NotContains(t, id, "/", "an extension id must be one path segment: %q", id)
	}
}

// TestAdmin_ForumPayloadHasNoAdminData pins that the forum app's payload
// does not grow the admin fields.
func TestAdmin_ForumPayloadHasNoAdminData(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	payload := bootPayloadOf(t, recorder)

	require.Nil(t, payload.Admin, "the forum app does not read the admin data")
}

// TestAdmin_AnonymousVisitorIsRefused pins the admin gate.
//
// Upstream serves the admin application only to an administrator: its
// admin routes carry the `RequireAdministrateAbility` middleware. The
// same gate is here, and it is load-bearing rather than cosmetic: the
// admin app assumes a signed-in administrator at BOOT time —
// `MailPage.register` reads `app.session.user!.email()`, which throws
// on a null user — so an anonymous visitor served the admin shell gets
// a BLANK page, not a login prompt.
func TestAdmin_AnonymousVisitorIsRefused(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", nil)

	require.Equal(t, http.StatusForbidden, recorder.Code, "an anonymous visitor must not get the admin shell")
}

// TestAdmin_NonAdminIsRefused pins the gate for a signed-in
// non-administrator: they are as unable to boot the admin app as an
// anonymous visitor is.
func TestAdmin_NonAdminIsRefused(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "regular")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusForbidden, recorder.Code, "a non-administrator must not get the admin shell")
}

// TestAdmin_AssetsAreServed pins that the admin bundle and its locale
// bundle load, since a 404 there is another way to get a blank page.
func TestAdmin_AssetsAreServed(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))
	body := recorder.Body.String()

	for _, asset := range []string{"/forum/assets/admin.js", "/forum/assets/admin-en.js", "/forum/assets/admin.css"} {
		require.Contains(t, body, asset, "the admin shell should reference %s", asset)
	}

	// The forum app's entry bundle must not be loaded by the admin app.
	require.NotContains(t, body, "/forum/assets/forum.js", "the admin app boots its own entry bundle")
}

// TestAdmin_SettingsAreParseable pins the settings the admin app
// PARSES.
//
// `isExtensionEnabled` does `JSON.parse(app.data.settings
// .extensions_enabled)`, and `JSON.parse(undefined)` throws — so an
// absent or empty value is a blank admin page, not a missing field.
//
// `ExtensionBisect` parses `extension_bisect_state` the same way.
func TestAdmin_SettingsAreParseable(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))
	payload := bootPayloadOf(t, recorder)
	admin := payload.Admin
	require.NotNil(t, admin)

	// The keys the admin app parses as JSON must be valid JSON.
	for _, key := range []string{"extensions_enabled", "extension_bisect_state"} {
		raw, ok := admin.Settings[key].(string)
		require.True(t, ok, "the %q setting must be a string", key)
		require.NotEmpty(t, raw, "the %q setting must not be empty", key)

		var decoded any
		require.NotPanics(t, func() {
			decoded = json.Unmarshal([]byte(raw), &decoded)
		}, "the %q setting must be valid JSON", key)
	}

	// The enabled plugins are the forum's own, so the admin app shows
	// the right ones as enabled.
	//
	// **THE IDS ARE FLATTENED.** The admin application looks an
	// extension up by the id in its URL, and that id is a PATH
	// SEGMENT: a plugin's raw id is `vendor/name`, which contains a
	// slash and would be two segments. Upstream flattens the same way
	// (`Extension::nameToId()`), and the flattened id is what the
	// extension map is keyed by, so the two agree.
	var enabled []string
	require.NoError(t, json.Unmarshal([]byte(admin.Settings["extensions_enabled"].(string)), &enabled))
	require.Contains(t, enabled, "riff-tags")
	require.Contains(t, enabled, "riff-likes")

	for _, id := range enabled {
		require.NotContains(t, id, "/", "an extension id must be one path segment: %q", id)
		require.Contains(t, admin.Extensions, id,
			"the enabled list and the extension map must agree on the id")
	}
}

// TestAdmin_ModelStatisticsCarryTheUserCount pins the count the admin
// app reads.
//
// `UserListPage` reads `app.data.modelStatistics.users.total` in a
// FIELD INITIALISER, so `modelStatistics.users` must be present even
// when the forum has no accounts.
func TestAdmin_ModelStatisticsCarryTheUserCount(t *testing.T) {
	app, handler := newTestForum(t)
	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", cookies)
	payload := bootPayloadOf(t, recorder)
	admin := payload.Admin
	require.NotNil(t, admin)

	users, ok := admin.ModelStatistics["users"].(map[string]any)
	require.True(t, ok, "modelStatistics.users must be present")
	require.Contains(t, users, "total")

	// The count is the number of accounts. The administrator lives in
	// `_superusers`, not in the shared `users` collection, so it is not
	// one of them.
	testUser(t, app, "counted_user")

	recorder = do(t, handler, http.MethodGet, "/forum/admin", "", cookies)
	payload = bootPayloadOf(t, recorder)
	admin = payload.Admin
	require.NotNil(t, admin)
	users, ok = admin.ModelStatistics["users"].(map[string]any)
	require.True(t, ok, "modelStatistics.users must be present")
	require.Equal(t, float64(1), users["total"], "the count should follow the accounts")
}

// TestAdmin_DBDriverIsSet pins the driver string.
//
// `ExtensionPage` calls `app.data.dbDriver.toLowerCase()`, so an empty
// driver throws when the extension page renders.
func TestAdmin_DBDriverIsSet(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))
	payload := bootPayloadOf(t, recorder)
	admin := payload.Admin
	require.NotNil(t, admin)

	require.NotEmpty(t, admin.DBDriver, "the driver must not be empty")
	require.NotPanics(t, func() {
		_ = strings.ToLower(admin.DBDriver)
	}, "the driver must be a non-empty string")
}

// TestAdmin_MailSettingsEndpointAnswers pins the mail settings
// endpoint.
//
// The admin application's mail page fetches `/api/mail/settings` on
// mount and reads `response.data.attributes`. A 404 (or a body with no
// `data`) leaves `response.data` undefined, and the page throws on it —
// so the route has to answer with the upstream shape.
func TestAdmin_MailSettingsEndpointAnswers(t *testing.T) {
	app, handler := newTestForum(t)
	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/mail/settings", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var document struct {
		Data struct {
			Attributes struct {
				Fields               map[string]any `json:"fields"`
				Sending              bool           `json:"sending"`
				Errors               []any          `json:"errors"`
				CanReceiveWebhooks   bool           `json:"canReceiveWebhooks"`
				CanProvisionWebhooks bool           `json:"canProvisionWebhooks"`
				WebhookURL           *string        `json:"webhookUrl"`
			} `json:"attributes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	attributes := document.Data.Attributes
	require.NotNil(t, attributes.Fields, "the fields must be present")
	require.NotNil(t, attributes.Errors, "the errors must be present")
	require.False(t, attributes.CanReceiveWebhooks)
	require.False(t, attributes.CanProvisionWebhooks)
}

// TestAdmin_ShellIsNotCached pins the cache header on the admin shell.
//
// The admin shell carries the boot payload, and the payload changes
// with the instance: the enabled plugins, the user count, the
// settings. A cached copy replays a STALE payload, which reads as a
// blank page when the app dereferences a field the old payload did not
// have.
//
// Upstream sends the same header for the same reason — its admin
// middleware carries `DisableBrowserCache`.
func TestAdmin_ShellIsNotCached(t *testing.T) {
	app, handler := newTestForum(t)
	cookies := testSuperuser(t, app)

	admin := do(t, handler, http.MethodGet, "/forum/admin", "", cookies)
	require.Equal(t, http.StatusOK, admin.Code, admin.Body.String())
	require.Equal(t, "max-age=0, no-store", admin.Header().Get("Cache-Control"),
		"the admin shell must not be cached")

	// The forum shell is a different document and keeps its own
	// caching behaviour: the forum's payload does not carry the admin
	// data, and the upstream frontend does not disable the cache there.
	forum := do(t, handler, http.MethodGet, "/forum/", "", cookies)
	require.Equal(t, http.StatusOK, forum.Code, forum.Body.String())
	require.Empty(t, forum.Header().Get("Cache-Control"),
		"the forum shell keeps its own caching behaviour")
}
