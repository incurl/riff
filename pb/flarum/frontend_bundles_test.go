package flarum

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/frontend"
)

// The boot shell advertises the plugins' frontend bundles as plain
// <script>/<link> tags. A tag for a file that was never built is a
// SILENT 404 — the page renders, the console stays empty, and the
// plugin's UI simply never appears.
//
// That is exactly how "the tags and likes are not visible" presents
// itself, for these reasons:
//
//   - the plugin's extender DOES run and DOES add its item, but the
//     item's markup never renders because the bundle that adds it was
//     never fetched;
//   - the shell writes the tag from the plugin REGISTRY, which knows
//     nothing about what was built;
//   - nothing logs the 404.
//
// So the pin is: every bundle the registry advertises must resolve to a
// real embedded asset, and the shell must not advertise a bundle that
// was not embedded.

// shellScriptSrc matches the shell's script tags.
var shellScriptSrc = regexp.MustCompile(`<script[^>]*src="([^"]+)"`)

// shellStylesheetHref matches the shell's stylesheet links.
var shellStylesheetHref = regexp.MustCompile(`<link[^>]*href="([^"]+)"`)

// requiredBundleAssets is what the three first-party plugins advertise.
//
// They are named explicitly rather than read from the registry so the
// test keeps failing if a plugin stops registering its bundle.
var requiredBundleAssets = []string{
	"plugins/tags/forum.js",
	"plugins/tags/style.css",
	"plugins/likes/forum.js",
	"plugins/likes/style.css",
	"plugins/uploads/forum.js",
	"plugins/uploads/style.css",
}

// coreFrontendAssetsUnderTest are the bundles the shell loads before the
// plugins'.
var coreFrontendAssetsUnderTest = []string{"forum.js", "forum-en.js", "forum.css"}

// TestFrontendBundles_EveryAdvertisedAssetIsServable pins that the
// embedded build carries every bundle the shell advertises, and that
// each advertised URL answers 200.
//
// It skips when `ui/dist` has not been built: that directory is build
// output and is gitignored, so a fresh clone serves the fallback shell
// instead. `scripts/build_flarum_spa.sh` produces it.
func TestFrontendBundles_EveryAdvertisedAssetIsServable(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	// Every advertised bundle must be embedded. A missing one is the
	// silent-404 regression.
	wanted := slices.Concat(coreFrontendAssetsUnderTest, requiredBundleAssets)
	for _, name := range wanted {
		require.True(t, assets.Exists(name), "the embedded frontend must carry %q", name)
	}

	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	body := recorder.Body.String()

	// The shell must reference the plugin bundles: an asset that is
	// embedded but not advertised is just as invisible.
	for _, name := range requiredBundleAssets {
		require.Contains(t, body, "/forum/assets/"+name, "the shell must load %s", name)
	}

	// And every advertised URL must serve bytes rather than a 404.
	for _, match := range shellScriptSrc.FindAllStringSubmatch(body, -1) {
		url := match[1]
		recorder := do(t, handler, http.MethodGet, url, "", nil)
		require.Equal(t, http.StatusOK, recorder.Code, "%s must serve", url)
	}

	for _, match := range shellStylesheetHref.FindAllStringSubmatch(body, -1) {
		url := match[1]
		recorder := do(t, handler, http.MethodGet, url, "", nil)
		require.Equal(t, http.StatusOK, recorder.Code, "%s must serve", url)
	}
}

// TestFrontendBundles_TheShellNeverAdvertisesAnUnembeddedAsset pins the
// invariant behind the guard: a URL in the shell always corresponds to
// an embedded file.
//
// The positive case is covered by the test above; this one states the
// rule in the form the guard implements, so a later change that drops
// the existence check is caught even when the local build is complete.
func TestFrontendBundles_TheShellNeverAdvertisesAnUnembeddedAsset(t *testing.T) {
	assets, err := frontend.Load()
	require.NoError(t, err)

	if !assets.HasBuild() {
		t.Skip("the forum frontend has not been built; run scripts/build_flarum_spa.sh")
	}

	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	body := recorder.Body.String()

	assetPrefix := "/forum/assets/"

	for _, match := range shellScriptSrc.FindAllStringSubmatch(body, -1) {
		name := strings.TrimPrefix(stripVersionQuery(match[1]), assetPrefix)
		require.NotContains(t, name, assetPrefix, "the URL must sit under %s", assetPrefix)
		require.True(t, assets.Exists(name), "%s is advertised but not embedded", name)
	}

	for _, match := range shellStylesheetHref.FindAllStringSubmatch(body, -1) {
		name := strings.TrimPrefix(stripVersionQuery(match[1]), assetPrefix)
		require.NotContains(t, name, assetPrefix, "the URL must sit under %s", assetPrefix)
		require.True(t, assets.Exists(name), "%s is advertised but not embedded", name)
	}
}

// stripVersionQuery drops the `?v=` cache-busting suffix.
func stripVersionQuery(url string) string {
	if index := strings.IndexByte(url, '?'); index >= 0 {
		return url[:index]
	}
	return url
}
