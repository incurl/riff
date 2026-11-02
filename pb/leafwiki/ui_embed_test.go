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
