package flarum

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/frontend"
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/plugins/chinese"
	"github.com/incurl/riff/pb/flarum/session"
)

// The Chinese Simplified language pack is the reference implementation of
// a LOCALE plugin: a catalogue, not a feature. These tests pin the three
// things that make it work, each of which fails silently when it breaks:
//
//   - the pack declares its locale, so the module knows the code exists;
//   - the shell loads the ACTIVE locale's catalogue, and only that one —
//     loading a second one merges its keys over the first;
//   - an unset or unknown locale falls back to English rather than
//     serving no catalogue (which renders raw translation keys);
//   - the catalogue is actually embedded and carries Chinese text.
//
// A locale bug is invisible in the UI: the page renders, everything is
// where it should be, and the labels read `core.forum.header.x` instead of
// their text.

// shellScriptSrc matches the shell's script tags.
var localeShellScriptSrc = regexp.MustCompile(`<script[^>]*src="([^"]+)"`)

// newLocaleTestForum boots a forum with a specific locale option.
//
// It mirrors `newTestForumModule`; that helper is fixed to the default
// options, and the locale is the thing under test here.
func newLocaleTestForum(t *testing.T, locale string) (core.App, http.Handler, *Module) {
	t.Helper()

	app := newTestApp(t)

	module, err := newModule(app, Options{
		BasePath: DefaultBasePath,
		Logger:   slogDiscard(),
		Locale:   locale,
	})
	require.NoError(t, err, "the module should build")
	require.NoError(t, module.boot(context.Background()), "the plugins should load")

	r := newTestRouter(t, app)
	module.mount(r)

	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")

	return app, mux, module
}

// shellScripts returns the script URLs the shell loads.
func shellScripts(t *testing.T, handler http.Handler) []string {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	matches := localeShellScriptSrc.FindAllStringSubmatch(recorder.Body.String(), -1)

	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		urls = append(urls, match[1])
	}
	return urls
}

// containsURL reports whether one of the URLs names the given asset.
func containsURL(urls []string, asset string) bool {
	for _, url := range urls {
		if strings.HasSuffix(url, "/"+asset) {
			return true
		}
	}
	return false
}

// TestLocale_TheChinesePackIsAFreeStandingPlugin pins that the language
// pack is registered like any other first-party plugin and declares the
// locale upstream advertises.
func TestLocale_TheChinesePackIsAFreeStandingPlugin(t *testing.T) {
	plugin := chinese.New()

	manifest := plugin.Manifest()
	require.Equal(t, chinese.PluginID, manifest.ID)
	require.Equal(t, "flarum-lang/chinese-simplified", manifest.ID, "the id mirrors upstream's composer name")
	require.NotEmpty(t, manifest.Name)

	// The locale code is upstream's `extra.flarum-locale.code`: it is what
	// the host passes and what the asset name carries.
	require.Equal(t, "zh-Hans", chinese.LocaleCode)
	require.Equal(t, "简体中文", chinese.LocaleTitle)
}

// TestLocale_TheModuleKnowsEveryImportedLocale pins the advertised set: the
// built-in English catalogue plus every pack this port imports, each listed
// under its own name.
//
// The list is what the frontend's language selector renders, so a pack that
// is registered but not advertised is a language a reader cannot pick — and
// one that is advertised but not registered is a 404.
func TestLocale_TheModuleKnowsEveryImportedLocale(t *testing.T) {
	_, _, module := newTestForumModule(t)

	locales := module.locales()

	require.Equal(t, "English", locales["en"], "the core ships English")
	require.Equal(t, "简体中文", locales["zh-Hans"], "the Chinese pack is advertised")
	require.Equal(t, "Français", locales["fr"], "the French pack is advertised")
	require.Len(t, locales, 3)
}

// TestLocale_EnglishIsTheDefault pins the unset case: no option means the
// core's own catalogue.
func TestLocale_EnglishIsTheDefault(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, handler := newTestForum(t)

	require.Contains(t, shellScripts(t, handler), "/forum/assets/forum-en.js")
}

// TestLocale_TheChineseCatalogueIsLoadedWhenSelected is the regression pin
// for the feature: `RIFF_FORUM_LOCALE=zh-Hans` must load the pack's
// bundle instead of the English one.
func TestLocale_TheChineseCatalogueIsLoadedWhenSelected(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, handler, module := newLocaleTestForum(t, "zh-Hans")

	require.Equal(t, "zh-Hans", module.activeLocale(nil, session.Actor{}))

	scripts := shellScripts(t, handler)

	require.True(t, containsURL(scripts, "forum-zh-Hans.js"), "the Chinese catalogue must be loaded, got %v", scripts)
	require.False(
		t,
		containsURL(scripts, "forum-en.js"),
		"the English catalogue must NOT also load: its keys would merge over the Chinese ones, got %v",
		scripts,
	)

	// The entry comes first, so `trans()` has the catalogue by the time the
	// app registers.
	require.True(t, containsURL(scripts, "forum.js"))
}

// TestLocale_TheAdminShellLoadsTheAdminCatalogue pins that the admin app
// gets the admin bundle, not the forum one.
func TestLocale_TheAdminShellLoadsTheAdminCatalogue(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, _, module := newLocaleTestForum(t, "zh-Hans")

	admin := module.scriptURLs(true, "zh-Hans")
	forum := module.scriptURLs(false, "zh-Hans")

	require.True(t, containsURL(admin, "admin-zh-Hans.js"), "the admin shell needs the admin catalogue, got %v", admin)
	require.False(t, containsURL(admin, "forum-zh-Hans.js"), "the admin shell must not load the forum catalogue")

	require.True(t, containsURL(forum, "forum-zh-Hans.js"), "the forum shell needs the forum catalogue, got %v", forum)
	require.False(t, containsURL(forum, "admin-zh-Hans.js"), "the forum shell must not load the admin catalogue")
}

// TestLocale_AnUnknownLocaleFallsBackToEnglish pins the tolerance rule: a
// locale with no catalogue behind it must NOT be served, because the UI
// would render every label as its raw key.
func TestLocale_AnUnknownLocaleFallsBackToEnglish(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, handler, module := newLocaleTestForum(t, "de")

	require.Equal(t, "en", module.activeLocale(nil, session.Actor{}), "an unknown locale must fall back to the built-in one")

	scripts := shellScripts(t, handler)
	require.True(t, containsURL(scripts, "forum-en.js"), "the fallback catalogue must load, got %v", scripts)
	require.False(t, containsURL(scripts, "forum-de.js"), "a catalogue that does not exist must not be requested")
}

// TestLocale_TheBootPayloadAdvertisesTheActiveLocale pins the payload
// contract the frontend reads:
//
//	Application.load -> translator.setLocale(payload.locale)
//
// so a payload that says `en` while the Chinese bundle loaded would leave
// the UI half-translated.
func TestLocale_TheBootPayloadAdvertisesTheActiveLocale(t *testing.T) {
	_, handler, _ := newLocaleTestForum(t, "zh-Hans")

	payload := bootPayloadJSON(t, handler)

	require.Equal(t, "zh-Hans", payload["locale"], "the payload must name the active locale")

	locales, ok := payload["locales"].(map[string]any)
	require.True(t, ok, "the payload must carry the locale map, got %T", payload["locales"])
	require.Equal(t, "简体中文", locales["zh-Hans"])
	require.Equal(t, "English", locales["en"])
}

// TestLocale_TheCatalogueIsServedAndTranslated pins the asset itself: it
// must serve, be the `addTranslations` call the translator expects, and
// actually carry Chinese text.
//
// This is the check that catches a locale that was declared but never
// staged, which is the failure this plugin can most easily acquire.
func TestLocale_TheCatalogueIsServedAndTranslated(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, handler := newTestForum(t)

	for _, asset := range []string{"forum-zh-Hans.js", "admin-zh-Hans.js"} {
		require.True(t, assets.Exists("plugins/chinese/"+asset), "the catalogue %s must be embedded", asset)

		recorder := do(t, handler, http.MethodGet, "/forum/assets/plugins/chinese/"+asset, "", nil)
		require.Equal(t, http.StatusOK, recorder.Code, "%s must serve", asset)

		body := recorder.Body.String()

		require.Contains(t, body, "app.translator.addTranslations(", "%s must register its catalogue", asset)

		// A catalogue with no CJK in it is the English file under another
		// name — which would "work" and translate nothing.
		require.True(t, containsCJK(body), "%s must carry Chinese text", asset)
	}
}

// TestLocale_TheCatalogueTranslatesTheTagsExtension pins that the pack
// covers the extensions this port actually serves, not only the core: a
// forum whose core is Chinese and whose plugins are English reads as
// half-finished.
func TestLocale_TheCatalogueTranslatesTheTagsExtension(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/assets/plugins/chinese/forum-zh-Hans.js", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	body := recorder.Body.String()

	for _, namespace := range []string{"flarum-tags.forum.", "flarum-likes.forum.", "core.forum."} {
		require.Contains(t, body, namespace, "the catalogue must cover %s*", namespace)
	}
}

// bootPayloadJSON returns the shell's boot payload.
func bootPayloadJSON(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	body := recorder.Body.String()

	const open = `<script id="flarum-json-payload" type="application/json">`
	start := strings.Index(body, open)
	require.GreaterOrEqual(t, start, 0, "the shell must carry the boot payload")

	rest := body[start+len(open):]
	end := strings.Index(rest, `</script>`)
	require.GreaterOrEqual(t, end, 0, "the payload script must be closed")

	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(rest[:end]), &payload), "the boot payload must be JSON")

	return payload
}

// containsCJK reports whether the text carries a CJK ideograph.
//
// The Chinese catalogue's keys are ASCII; only its VALUES are Chinese, so
// the test would pass on an English catalogue if it looked for keys.
func containsCJK(text string) bool {
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// phantomLocalePlugin declares a locale nothing builds.
//
// It stands in for a language pack that was declared but never staged,
// which is the failure a locale most easily acquires: the option names a
// real locale, the pack declares it, and the bundle is missing. Serving it
// anyway would render every label as its raw key.
type phantomLocalePlugin struct{}

// Manifest describes the plugin.
func (phantomLocalePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "test/phantom-locale",
		Name:        "Phantom locale",
		Version:     "1.0.0",
		CoreRequire: ">=2.0.0",
		BackendKind: plugin.BackendStaticGo,
	}
}

// Register declares the locale.
func (phantomLocalePlugin) Register(_ context.Context, ext *plugin.Extenders) error {
	ext.AddLocale(plugin.LocaleBundle{
		PluginID: "test/phantom-locale",
		Code:     "xx-XX",
		Title:    "Phantom",
		Forum:    "plugins/phantom/forum-xx-XX.js",
		Admin:    "plugins/phantom/admin-xx-XX.js",
	})
	return nil
}

// Boot does nothing.
func (phantomLocalePlugin) Boot(context.Context, *plugin.Extenders) error { return nil }

// Shutdown does nothing.
func (phantomLocalePlugin) Shutdown(context.Context, *plugin.Extenders) error { return nil }

// TestLocale_ALocaleThatWasNotBuiltFallsBackToEnglish pins the second
// tolerance rule: declared is not the same as usable.
func TestLocale_ALocaleThatWasNotBuiltFallsBackToEnglish(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	app := newTestApp(t)

	module, err := newModule(app, Options{
		BasePath: DefaultBasePath,
		Logger:   slogDiscard(),
		Locale:   "xx-XX",
		Plugins:  []plugin.Plugin{phantomLocalePlugin{}},
	})
	require.NoError(t, err, "the module should build")
	require.NoError(t, module.boot(context.Background()), "the plugins should load")

	// The locale IS declared ...
	require.Equal(t, "Phantom", module.locales()["xx-XX"], "the pack declares the locale")
	require.False(t, assets.Exists("plugins/phantom/forum-xx-XX.js"), "nothing builds the phantom bundle")

	// ... but it is not served, because the catalogue is not embedded.
	require.Equal(t, "en", module.activeLocale(nil, session.Actor{}))

	r := newTestRouter(t, app)
	module.mount(r)

	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")

	scripts := shellScripts(t, mux)
	require.True(t, containsURL(scripts, "forum-en.js"), "the fallback catalogue must load, got %v", scripts)
	require.False(
		t,
		containsURL(scripts, "forum-xx-XX.js"),
		"a catalogue that is not embedded must not be requested, got %v",
		scripts,
	)
}
