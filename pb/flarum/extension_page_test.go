package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/frontend"
)

// adminPayload reads the admin boot payload.
func adminPayload(t *testing.T, handler http.Handler, cookies []*http.Cookie) frontend.BootPayload {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)
	require.NotNil(t, payload.Admin, "the admin shell should carry the admin payload")

	return payload
}

// TestExtensionPage_TheExtensionIdsArePathSegments pins the route.
//
// **AN EXTENSION ID WITH A SLASH IS NOT A PATH SEGMENT.** A plugin's
// id is `vendor/name` — it CONTAINS A SLASH — and the admin app's
// extension page lives at `/extension/:id`, a pattern with ONE
// segment. The route builder substitutes the id verbatim, so an id
// with a slash becomes two segments and the route does not match: the
// extension's settings page is unreachable, and every entry in the
// admin sidebar links to a URL the router cannot resolve.
//
// Upstream avoids this the same way: `Extension::nameToId()` flattens
// `flarum/tags` to `flarum-tags`, and THAT is the key the admin app's
// `app.data.extensions` is keyed by.
//
// The flattened id is therefore both the map key and the route
// parameter, and they agree.
func TestExtensionPage_TheExtensionIdsArePathSegments(t *testing.T) {
	app, handler := newTestForum(t)

	admin := adminPayload(t, handler, testSuperuser(t, app)).Admin
	require.NotEmpty(t, admin.Extensions, "the admin payload should carry the extensions")

	for id := range admin.Extensions {
		require.NotContains(t, id, "/",
			"the extension id must be one path segment: %q", id)

		extension, ok := admin.Extensions[id].(map[string]any)
		require.True(t, ok, "the extension should be an object")

		require.Equal(t, id, extension["id"],
			"the extension's id must be the key it is filed under")
	}
}

// TestExtensionPage_TheEnabledListUsesTheSameIds pins the enable check.
//
// `isExtensionEnabled` asks whether `extensions_enabled` holds the
// extension's id. A list of the RAW `vendor/name` ids would report
// EVERY plugin as disabled, and the extension page would then show
// "enable to see" instead of its settings — the page would be
// unreachable by a different route.
func TestExtensionPage_TheEnabledListUsesTheSameIds(t *testing.T) {
	app, handler := newTestForum(t)

	admin := adminPayload(t, handler, testSuperuser(t, app)).Admin

	raw, ok := admin.Settings["extensions_enabled"].(string)
	require.True(t, ok, "the admin payload should carry extensions_enabled")
	require.NotEmpty(t, raw, "the enabled list should not be empty")

	var enabled []string
	require.NoError(t, json.Unmarshal([]byte(raw), &enabled))
	require.NotEmpty(t, enabled, "the enabled list should not be empty")

	for _, id := range enabled {
		require.NotContains(t, id, "/",
			"an enabled extension id must be one path segment: %q", id)
		require.Contains(t, admin.Extensions, id,
			"the enabled list and the extension map must agree on the id")
	}
}

// TestExtensionPage_TheToggleAnswers pins the enable/disable command.
//
// The admin application's switch posts `{enabled: …}` to
// `PATCH /api/extensions/{id}`. Without the route the toggle is a
// 404, and the switch shows the plugin as enabled whatever the
// server says.
func TestExtensionPage_TheToggleAnswers(t *testing.T) {
	app, handler := newTestForum(t)
	cookies := testSuperuser(t, app)

	admin := adminPayload(t, handler, cookies).Admin

	// The likes plugin: a plugin the forum ships, so the state table
	// has a row for it.
	const id = "riff-likes"
	require.Contains(t, admin.Extensions, id, "the likes plugin should be listed")

	// The route answers, and it records the state.
	recorder := do(t, handler, http.MethodPatch, "/forum/api/extensions/"+id,
		`{"enabled":false}`, cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// **A DISABLED PLUGIN IS A ROW THAT IS NOT THERE.** The state
	// table's `enabled` column is a `BoolField` with `Required: true`,
	// and PocketBase's `Required` on a bool means the value must be
	// "TRUE" — so a `false` can never be stored ("enabled: cannot be
	// blank"). The state is therefore expressed by the row's ABSENCE,
	// which is the same shape the permission table uses.
	collection, err := app.FindCollectionByNameOrId("forum_plugin_state")
	require.NoError(t, err)

	records, err := app.FindAllRecords(collection)
	require.NoError(t, err)

	for _, record := range records {
		// The state row carries the RAW id, which is the id the
		// plugin manager knows the plugin by. The admin application
		// addresses it by the flattened one, and the handler maps
		// between them.
		require.Contains(t, record.GetString("plugin_id"), "/",
			"the state row carries the raw id")
		require.True(t, record.GetBool("enabled"),
			"a row that exists says the plugin is enabled")
	}

	// The plugin is disabled: the module reads no row for it, so it
	// is off.
	disabled := disabledPluginIDs(t, app)
	require.Contains(t, disabled, "riff/likes",
		"the toggled plugin should be disabled")

	// A plugin that is not installed is refused, rather than recorded
	// under an id nothing knows.
	recorder = do(t, handler, http.MethodPatch, "/forum/api/extensions/nope",
		`{"enabled":false}`, cookies)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

// disabledPluginIDs returns the plugins the module reads as disabled.
//
// It is the READ side of the state: a plugin with no state row is
// enabled, and a plugin whose row was removed is disabled.
func disabledPluginIDs(t *testing.T, app core.App) []string {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_plugin_state")
	require.NoError(t, err)

	records, err := app.FindAllRecords(collection)
	require.NoError(t, err)

	disabled := map[string]bool{}
	for _, manifest := range []string{"riff/likes", "riff/tags", "riff/uploads"} {
		disabled[manifest] = true
	}

	for _, record := range records {
		if record.GetBool("enabled") {
			disabled[record.GetString("plugin_id")] = false
		}
	}

	out := make([]string, 0, len(disabled))
	for id, off := range disabled {
		if off {
			out = append(out, id)
		}
	}

	return out
}

// TestExtensionPage_TheToggleIsRefusedToAMember pins the admin gate.
func TestExtensionPage_TheToggleIsRefusedToAMember(t *testing.T) {
	app, handler := newTestForum(t)

	member := testUser(t, app, "member")
	token, err := member.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPatch, "/forum/api/extensions/riff-likes",
		`{"enabled":false}`, sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}
