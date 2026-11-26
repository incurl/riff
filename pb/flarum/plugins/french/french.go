// Package french is the forum's French language pack.
//
// It follows the same shape as the Simplified Chinese pack
// (`pb/flarum/plugins/chinese`), which is the reference implementation of
// a locale plugin: a catalogue of translations rather than a code bundle.
// The plugin owns
//
//  1. a vendored `locale/*.yml` catalogue (MIT, see LICENSE and README.md),
//  2. a build step that compiles it into the two bundles the SPA loads,
//  3. a locale declaration so the shell loads that pair when the locale is
//     active.
//
// There is no migration, no route and no attribute contribution: a locale
// changes what the UI says, not what the server stores.
package french

import (
	"context"

	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
//
// It is upstream's composer name, so the extension key and the
// documentation line up with https://github.com/flarum-lang/french.
const PluginID = "flarum-lang/french"

// Version is the plugin's version.
//
// It tracks the vendored upstream catalogue rather than an arbitrary
// number, so a reader can tell which translations are inside. Upstream's
// `master` is the line Flarum 2.x installs use (its `composer.json` declares
// the `flarum-locale` entry both major versions read).
const Version = "2.0.0"

// LocaleCode is the locale code the pack provides.
//
// It is upstream's `extra.flarum-locale.code`: what the host passes as the
// active locale (`RIFF_FORUM_LOCALE=fr`, or the admin's Basics page writes
// it as `default_locale`), and what the asset name carries
// (`forum-fr.js`).
const LocaleCode = "fr"

// LocaleTitle is the locale's name in its own language.
//
// It is upstream's `extra.flarum-locale.title`, and what the frontend's
// language list shows.
const LocaleTitle = "Français"

// Plugin is the French language pack.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
}

// New returns the French language pack.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Français",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#0055A4",
				Glyph:           "fas fa-language",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
//
// The only contribution is the locale: the catalogue itself is compiled at
// build time and embedded with the rest of the frontend, so there is
// nothing to create in the database and nothing to serve.
func (*Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	ext.AddLocale(plugin.LocaleBundle{
		PluginID: PluginID,
		Code:     LocaleCode,
		Title:    LocaleTitle,
		Version:  Version,
		// The paths are relative to the embedded asset root, exactly as a
		// plugin's code bundles are. The build stages them there; a
		// missing file is reported by the module's startup check rather
		// than left as a silent 404.
		Forum: "plugins/french/forum-" + LocaleCode + ".js",
		Admin: "plugins/french/admin-" + LocaleCode + ".js",
	})

	return nil
}

// Boot runs the plugin's startup work.
//
// A language pack has none: its catalogue is data, compiled into the
// bundle.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
//
// A language pack holds none.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}
