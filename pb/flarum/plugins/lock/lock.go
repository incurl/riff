// Package lock is the forum's Lock plugin.
//
// It mirrors upstream Flarum's `flarum/lock` extension: a moderator can
// close a discussion to new replies, the discussion shows a lock badge, and
// the reply control disappears for everyone who cannot lock.
//
// Upstream keeps the flag in its own `is_locked` column, independent of the
// discussion's visibility and of pinning. This port used to encode it in
// `state` (one of open/locked/hidden/sticky), which made locking and
// pinning mutually exclusive: pinning a locked discussion silently
// unlocked it. The migration here adds the column and the plugin owns it.
package lock

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
//
// It is upstream's extension id, so the permission key, the catalogue
// namespace and the documentation all line up with `flarum/lock`.
const PluginID = "flarum/lock"

// Version is the plugin's version.
const Version = "1.0.0"

// discussionCollection is the logical name of the collection whose records
// carry the flag.
const discussionCollection = "discussions"

// lockedField is the column the plugin owns.
const lockedField = "is_locked"

// Plugin is the Lock plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// physical is the discussions collection's real name, resolved once at
	// Register time because the prefix is the host's choice.
	physical string
}

// New returns the Lock plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Lock",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#ddd",
				Glyph:           "fas fa-lock",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	physical := ext.Registry.Physical(discussionCollection)
	p.physical = physical

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "add_is_locked",
		Up: func(_ context.Context, app core.App) error {
			return addLockedColumn(app, physical)
		},
	})

	// --- layer 2: the API ---------------------------------------------
	// The frontend reads `isLocked` to draw the badge and `canLock` to
	// decide whether to offer the control. Upstream exposes the same two
	// (`flarum/lock`'s DiscussionResourceFields adds `isLocked`, and
	// `canLock` comes from the policy).
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "isLocked",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return isLocked(req.App, physical, req.RecordID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "canLock",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			// The same check the toggle route makes, so the control and the
			// endpoint cannot disagree.
			return req.Can(domain.PermissionLockDiscussion, api.TypeDiscussion, req.RecordID), nil
		},
	})

	// The toggle is its own route rather than a PATCH attribute: the
	// permission that governs it (`lock`) is the plugin's, and the
	// discussion update handler does not consult it.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/discussions/{id}/lock",
		Handler: p.lockHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/discussions/{id}/lock",
		Handler: p.unlockHandler,
	})

	// --- the permission -------------------------------------------------
	// Upstream's admin page registers this as `discussion.lock`
	// (flarum-lock/js/src/admin/extend.tsx); the forum stores the flat
	// name, and `domain.permission_keys.go` translates between them.
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionLockDiscussion,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	// --- layer 3: the frontend ------------------------------------------
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/lock/forum.js",
		Admin:    "plugins/lock/admin.js",
		CSS:      "plugins/lock/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
//
// The Lock plugin has none: the flag is a column and the routes are
// mounted from the extender list.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
//
// The Lock plugin holds none.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}
