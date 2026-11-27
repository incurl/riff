package flarum

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// The locale RESOLUTION ORDER, which is upstream's
// (`framework/core/src/Http/Middleware/SetLocale.php`):
//
//  1. a signed-in caller's `locale` preference;
//  2. a guest's `locale` cookie;
//  3. the `default_locale` SETTING (the admin's Basics page);
//  4. `RIFF_FORUM_LOCALE`;
//  5. `en`.
//
// The port used to read only (4), so an operator could pick a default
// language in the admin and the forum kept rendering whatever the
// deployment was built for — a control that changes nothing.

// TestLocale_TheDefaultLocaleSettingApplies is the regression pin for the
// reported bug.
func TestLocale_TheDefaultLocaleSettingApplies(t *testing.T) {
	app, handler := newTestForum(t)

	setForumSetting(t, app, "default_locale", "zh-Hans")

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)
	require.Equal(t, "zh-Hans", payload.Locale,
		"the admin's default language must decide the forum's locale")

	body := recorder.Body.String()
	require.Contains(t, body, "forum-zh-Hans.js",
		"the default language's catalogue must be the one loaded")
	// The CORE English catalogue, named by its own path. A bare "forum-en.js"
	// substring also matches an EXTENSION's English catalogue
	// (`plugins/polls/forum-en.js`), which loads regardless: an extension adds its
	// keys BEFORE the active locale's, as upstream's AssetBundle does.
	require.NotContains(t, body, "/assets/forum-en.js",
		"the English catalogue must not also load: its keys would merge over the chosen ones")
}

// TestLocale_TheCallersPreferenceWins pins the first rung.
//
// The language selector saves the choice with
// `savePreferences({ locale })`, which this port stores in the account's
// profile preferences.
func TestLocale_TheCallersPreferenceWins(t *testing.T) {
	app, handler := newTestForum(t)

	// The forum's default is French ...
	setForumSetting(t, app, "default_locale", "fr")

	// ... and one reader asks for Chinese.
	user := testUser(t, app, "localechooser")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := do(t, handler, http.MethodPatch, "/forum/api/users/"+user.Id,
		`{"data":{"type":"users","id":"`+user.Id+`","attributes":{"preferences":{"locale":"zh-Hans"}}}}`,
		cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)
	require.Equal(t, "zh-Hans", payload.Locale,
		"a caller's own choice must win over the forum's default")

	// The same request WITHOUT the session gets the forum's default.
	recorder = do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "fr", bootPayloadOf(t, recorder).Locale)
}
