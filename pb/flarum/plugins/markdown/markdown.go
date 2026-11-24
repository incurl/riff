// Package markdown is the forum's Markdown plugin.
//
// It mirrors upstream Flarum's `flarum/markdown` extension, and it is
// deliberately the THINNEST plugin in the tree: the formatter itself is not
// reimplemented, it is riff's own markdown engine.
//
// Upstream's extension does two things:
//
//  1. registers the Litedown formatter with the post pipeline, and overrides
//     the inline-spoiler template so a spoiler renders as
//     `<span class="spoiler" data-spoiler="">` and is revealed on click;
//  2. ships a composer toolbar of formatting buttons.
//
// (1) is already the forum's behaviour: `pb/flarum/api/service.go`'s
// `renderContent` calls `markdown.Service.RenderHTML`, so a post's
// `contentHtml` carries every extension that engine has. The one piece of
// upstream's syntax that engine did not have was the spoiler, which
// `markdown/extensions/spoiler.go` adds — so this plugin contributes no
// formatter code of its own.
//
// (2) is what the plugin does add: a toolbar bundle. That is the whole
// reason the plugin exists as a plugin rather than as a line in the module.
package markdown

import (
	"context"

	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
//
// It is upstream's extension id, so the documentation and the composer
// bundle namespace line up with `flarum/markdown`.
const PluginID = "flarum/markdown"

// Version is the plugin's version.
const Version = "1.0.0"

// Plugin is the Markdown plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
}

// New returns the Markdown plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Markdown",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#000000",
				Glyph:           "fab fa-markdown",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
//
// There is no migration, no route and no attribute: the formatter is the
// core's, and the toolbar is the only thing a bundle has to add.
func (*Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/markdown/forum.js",
		Admin:    "plugins/markdown/admin.js",
		CSS:      "plugins/markdown/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}
