package leafwiki

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadSPA_HasAFallback pins that the module always ships a usable UI: the
// fallback reader is tracked in git (ui/index.html), while a real LeafWiki
// build lives in ui/dist and is gitignored because it is build output.
func TestLoadSPA_HasAFallback(t *testing.T) {
	assets, err := loadSPA()
	require.NoError(t, err)
	require.NotNil(t, assets)
	assert.NotEmpty(t, assets.index(), "the embedded fallback index must not be empty")
}

// TestSPAAssets_PrefersTheBuiltBuild pins the precedence: a copied-in LeafWiki
// build wins over the fallback reader.
func TestSPAAssets_PrefersTheBuiltBuild(t *testing.T) {
	built := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("built-spa")},
	}
	assets := &spaAssets{dist: built, fallback: []byte("fallback")}
	assert.Equal(t, "built-spa", string(assets.index()))

	// A dist/ directory without an index.html still falls back.
	empty := &spaAssets{dist: fstest.MapFS{}, fallback: []byte("fallback")}
	assert.Equal(t, "fallback", string(empty.index()))

	var missing *spaAssets
	assert.Nil(t, missing.index())
}

// TestRenderIndex_RewritesTheViteBasePath pins the asset rewrite.
//
// Vite builds LeafWiki's SPA with base "./", so the built index.html
// references its entry chunk as "./static/main-<hash>.js". A relative
// reference resolves against the CURRENT URL, so on a deep link such as
// /wiki/guides/setup the browser would request
// /wiki/guides/static/main-<hash>.js — the SPA shell instead of the
// script, and the page would render blank with nothing in the console.
//
// Upstream LeafWiki avoids this by rewriting `"./static/` to
// `"<basePath>/static/` while rendering the shell (see
// internal/http/shell.go), and this port must do the same or the built SPA
// does not work behind a mount prefix.
func TestRenderIndex_RewritesTheViteBasePath(t *testing.T) {
	built := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><meta name="base-path" content="{{__BASE_PATH__}}">` +
				`<link rel="icon" href="{{__FAVICON_HREF__}}">` +
				`<title>{{__PAGE_TITLE__}}</title></head>` +
				`<body><div id="root">{{__BODY__}}</div>` +
				`<script type="module" crossorigin src="./static/main-abc123.js"></script>` +
				`</body></html>`,
		)},
	}

	server := &Server{opts: Options{Prefix: "/wiki"}, ui: &spaAssets{dist: built}}
	rendered := server.renderIndex()

	assert.Contains(t, rendered, `src="/wiki/static/main-abc123.js"`,
		"the chunk reference must be absolute, or a deep link loads the shell instead of the script")
	assert.NotContains(t, rendered, `"./static/`,
		"no relative chunk reference may survive the render")

	// The tokens the SPA depends on must all be substituted.
	assert.Contains(t, rendered, `content="/wiki"`, "the SPA reads the base path from this meta tag")
	assert.Contains(t, rendered, `href="/wiki/favicon.svg"`, "the favicon must live under the mount prefix")
	assert.Contains(t, rendered, "<title>Wiki</title>")
	assert.Contains(t, rendered, `<div id="root"></div>`, "the body placeholder must be emptied")
	assert.NotContains(t, rendered, "{{__", "no placeholder may survive the render")
}

// TestRenderIndex_WithoutABuiltSPAServesTheFallback pins that the fallback
// reader is still rendered when no build was copied in.
func TestRenderIndex_WithoutABuiltSPAServesTheFallback(t *testing.T) {
	server := &Server{opts: Options{Prefix: "/wiki"}, ui: &spaAssets{fallback: []byte(`<meta name="base-path" content="{{__BASE_PATH__}}">`)}}
	rendered := server.renderIndex()
	assert.Contains(t, rendered, `content="/wiki"`)
}
