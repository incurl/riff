package flarum

import (
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/plugins/likes"
	"github.com/incurl/riff/pb/flarum/plugins/tags"
	"github.com/incurl/riff/pb/flarum/plugins/uploads"
)

// defaultPlugins returns the forum's first-party plugins.
//
// Tags and Likes are the two v1 features the specification requires as
// first-party plugins, and they double as the reference implementation
// of the three-layer flow (migration → API field/relationship → UI
// entry).
//
// Uploads is the third: upstream Flarum v2.0.0's core has NO
// attachment support at all — attachments moved to the `fof/upload`
// extension — so the forum implements them as a plugin that mirrors
// that extension's contract.
func defaultPlugins() []plugin.Plugin {
	return []plugin.Plugin{
		tags.New(),
		likes.New(),
		uploads.New(),
	}
}
