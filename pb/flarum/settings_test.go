package flarum

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The theme settings live on the FORUM DOCUMENT.
//
// Upstream carries them there (framework/core/src/Api/Resource/
// ForumResource.php:104-112):
//
//	Schema\Str::make('themePrimaryColor')
//	    ->get(fn () => $this->settings->get('theme_primary_color')),
//	Schema\Str::make('colorScheme')
//	    ->get(fn () => $this->settings->get('color_scheme')),
//
// and the frontend reads them from there:
//
//	forumDefault ??= app.forum.attribute('colorScheme') ?? 'auto';
//
// (framework/core/js/src/common/Application.tsx:508)
//
// So a setting the forum document does not carry is a setting the forum
// IGNORES: an administrator picks "dark" in the Appearance page, the
// admin saves it, and the forum still renders in light mode.

// TestForum_TheForumDocumentCarriesTheColorScheme pins that the
// colour scheme reaches the forum document.
//
// The value comes from the SETTINGS, so an administrator's choice is
// what the forum shows.
func TestForum_TheForumDocumentCarriesTheColorScheme(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	// The administrator picks "dark" in the Appearance page.
	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"color_scheme":"dark","allow_user_color_scheme":"0"}`, cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// The forum document carries it.
	document := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, document.Code, document.Body.String())

	attributes := forumAttributesOf(t, document.Body.String())
	require.Equal(t, "dark", attributes["colorScheme"],
		"the forum document must carry the colour scheme: %s", document.Body.String())
}

// TestForum_TheForumDocumentCarriesTheThemeColours pins the same for
// the two theme colours.
func TestForum_TheForumDocumentCarriesTheThemeColours(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"theme_primary_color":"#123456","theme_secondary_color":"#654321"}`, cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	document := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, document.Code, document.Body.String())

	attributes := forumAttributesOf(t, document.Body.String())
	require.Equal(t, "#123456", attributes["themePrimaryColor"])
	require.Equal(t, "#654321", attributes["themeSecondaryColor"])
}

// TestForum_TheForumDocumentCarriesTheWelcomeText pins the settings
// that the forum document carries as text.
func TestForum_TheForumDocumentCarriesTheWelcomeText(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"welcome_title":"Welcome","welcome_message":"Hello"}`, cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	document := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, document.Code, document.Body.String())

	attributes := forumAttributesOf(t, document.Body.String())
	require.Equal(t, "Welcome", attributes["welcomeTitle"])
	require.Equal(t, "Hello", attributes["welcomeMessage"])
}

// TestForum_TheAdminPayloadCarriesTheAppearanceSettings pins that the
// admin payload carries the keys the Appearance page reads.
//
// The upload buttons read `app.data.settings.logo_path` and its
// siblings, and the colour scheme is read through the same map. A key
// that is missing reads as undefined, and the control shows nothing.
func TestForum_TheAdminPayloadCarriesTheAppearanceSettings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	shell := do(t, handler, http.MethodGet, "/forum/admin", "", cookies)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	settings := adminSettingsOf(t, shell.Body.String())
	for _, key := range []string{
		"color_scheme",
		"allow_user_color_scheme",
		"show_theme_selector",
		"theme_primary_color",
		"theme_secondary_color",
		"logo_path",
		"logo_dark_mode_path",
		"favicon_path",
	} {
		require.Contains(t, settings, key,
			"the admin payload must carry the %q setting", key)
	}
}

// TestForum_TheAdminPayloadCarriesTheSavedAppearanceSettings pins that
// a SAVED setting reaches the admin payload.
//
// The admin payload's defaults are what the admin app shows before
// anything is saved, and the stored value has to win over them.
func TestForum_TheAdminPayloadCarriesTheSavedAppearanceSettings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"color_scheme":"dark"}`, cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	shell := do(t, handler, http.MethodGet, "/forum/admin", "", cookies)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	settings := adminSettingsOf(t, shell.Body.String())
	require.Equal(t, "dark", settings["color_scheme"],
		"the admin payload must carry the saved colour scheme")
}

// adminSettingsOf reads the admin payload's settings map.
func adminSettingsOf(t *testing.T, body string) map[string]any {
	t.Helper()

	const open = `<script id="flarum-json-payload" type="application/json">`
	start := strings.Index(body, open)
	require.GreaterOrEqual(t, start, 0, "the shell should carry the boot payload")

	rest := body[start+len(open):]
	end := strings.Index(rest, "</script>")
	require.GreaterOrEqual(t, end, 0, "the boot payload should be closed")

	var payload struct {
		Settings map[string]any `json:"settings"`
	}
	require.NoError(t, json.Unmarshal([]byte(rest[:end]), &payload))
	require.NotEmpty(t, payload.Settings, "the admin payload should carry settings")
	return payload.Settings
}
