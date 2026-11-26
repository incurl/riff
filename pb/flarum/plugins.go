package flarum

import (
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/plugins/chinese"
	"github.com/incurl/riff/pb/flarum/plugins/french"
	"github.com/incurl/riff/pb/flarum/plugins/gamification"
	"github.com/incurl/riff/pb/flarum/plugins/likes"
	"github.com/incurl/riff/pb/flarum/plugins/lock"
	"github.com/incurl/riff/pb/flarum/plugins/markdown"
	"github.com/incurl/riff/pb/flarum/plugins/mentions"
	"github.com/incurl/riff/pb/flarum/plugins/polls"
	"github.com/incurl/riff/pb/flarum/plugins/statistics"
	"github.com/incurl/riff/pb/flarum/plugins/sticky"
	"github.com/incurl/riff/pb/flarum/plugins/suspend"
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
//
// Chinese and French are a different shape: a language pack is a
// catalogue, not a feature. They are registered by default because a
// translation that has to be installed explicitly is a translation nobody
// uses, and a pack costs nothing when the active locale is `en` (its
// bundles are embedded, not loaded).
func defaultPlugins() []plugin.Plugin {
	return []plugin.Plugin{
		tags.New(),
		likes.New(),
		uploads.New(),
		lock.New(),
		sticky.New(),
		suspend.New(),
		markdown.New(),
		mentions.New(),
		gamification.New(),
		polls.New(),
		statistics.New(),
		chinese.New(),
		french.New(),
	}
}
