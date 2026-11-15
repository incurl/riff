package flarum

import (
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/plugins/likes"
	"github.com/incurl/riff/pb/flarum/plugins/tags"
)

// defaultPlugins returns the forum's first-party plugins.
//
// Tags and Likes are the two v1 features the specification requires as
// first-party plugins, and they double as the reference implementation
// of the three-layer flow (migration → API field/relationship → UI
// entry).
func defaultPlugins() []plugin.Plugin {
	return []plugin.Plugin{
		tags.New(),
		likes.New(),
	}
}
