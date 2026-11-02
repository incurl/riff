package leafwiki

import (
	"embed"
	"io/fs"

	"github.com/pkg/errors"
)

// uiFS embeds the wiki's front end.
//
// It deliberately embeds the whole `ui` directory rather than `ui/dist`:
// `.gitignore` ignores every `dist/` directory (they are build output), so a
// `//go:embed all:ui/dist` would not compile in a fresh clone. The tracked
// `ui/index.html` is a dependency-free fallback reader, and a LeafWiki SPA
// build dropped into `ui/dist/` takes precedence over it.
//
// The `all:` prefix keeps dotfiles, which a plain pattern silently drops.
//
//go:embed all:ui
var uiFS embed.FS

// spaAssets is the resolved front end: the optional LeafWiki build plus the
// always-present fallback.
type spaAssets struct {
	// dist is the built SPA, or an empty directory when none was copied in.
	dist fs.FS
	// fallback is ui/index.html, served when dist has no index.html.
	fallback []byte
}

// loadSPA resolves the embedded front end.
func loadSPA() (*spaAssets, error) {
	dist, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		// The directory does not exist because no SPA was built in; that is
		// the default, not an error.
		dist = nil
	}
	fallback, err := fs.ReadFile(uiFS, "ui/index.html")
	if err != nil {
		return nil, errors.Wrap(err, "leafwiki: the embedded UI fallback is missing")
	}
	return &spaAssets{dist: dist, fallback: fallback}, nil
}

// index returns the SPA's index.html, falling back to the bundled reader.
func (a *spaAssets) index() []byte {
	if a == nil {
		return nil
	}
	if a.dist != nil {
		if raw, err := fs.ReadFile(a.dist, "index.html"); err == nil {
			return raw
		}
	}
	return a.fallback
}
