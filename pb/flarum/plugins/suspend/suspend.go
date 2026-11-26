// Package suspend is the forum's Suspend plugin.
//
// It mirrors upstream Flarum's `flarum/suspend` extension: a moderator can
// suspend an account until a date, with a reason (moderators only) and a
// message (shown to the suspended account), and a suspended account loses
// its participation powers while keeping its identity.
//
// The revocation is not a per-handler check: `domain.Actor.Suspended` makes
// the permission service treat the account as a guest, which is upstream's
// `RevokeAccessFromSuspendedUsers` rule and means every permission — the
// ones this plugin knows about and the ones added later — is revoked in one
// place.
package suspend

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/repository"
)

// PluginID is the plugin's id.
const PluginID = "flarum/suspend"

// Version is the plugin's version.
const Version = "1.0.0"

// The columns the plugin owns on the shared users collection.
const (
	// suspendedUntilField is when the suspension ends.
	suspendedUntilField = "suspended_until"
	// reasonField is why, visible to moderators only.
	reasonField = "suspend_reason"
	// messageField is what the account is shown.
	messageField = "suspend_message"
)

// Plugin is the Suspend plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
}

// New returns the Suspend plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Suspend",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#ddd",
				Glyph:           "fas fa-ban",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (*Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	ext.AddMigration(plugin.Migration{
		Name: "add_user_suspension",
		Up: func(_ context.Context, app core.App) error {
			return addSuspensionColumns(app)
		},
	})

	// The user resource carries the flags the frontend draws the badge and
	// the modal from. Upstream adds the same four
	// (`flarum-suspend/src/Api/UserResourceFields.php`), with the same
	// visibility rules: the reason is a moderator's note, the message and
	// the date are the suspended account's business too.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "canSuspend",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return canSuspend(req), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "suspendedUntil",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			until, message, _, err := suspension(req.App, req.RecordID)
			if err != nil {
				return nil, err
			}
			if !visibleTo(req, until) {
				return nil, nil //nolint:nilnil // hidden from this caller
			}
			_ = message
			return timestampOrNil(until), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "suspendMessage",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			until, message, _, err := suspension(req.App, req.RecordID)
			if err != nil {
				return nil, err
			}
			if !visibleTo(req, until) || message == "" {
				return nil, nil //nolint:nilnil // hidden from this caller
			}
			return message, nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "suspendReason",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			_, _, reason, err := suspension(req.App, req.RecordID)
			if err != nil {
				return nil, err
			}
			// The reason is a moderator's note: upstream makes it visible
			// to the actors who may suspend, and to nobody else.
			if !canSuspend(req) || reason == "" {
				return nil, nil //nolint:nilnil // hidden from this caller
			}
			return reason, nil
		},
	})

	// The FORUM document carries whether the caller may suspend ANYBODY.
	//
	// Upstream serves it on `ForumResource` (`canSuspendUsers`, from
	// `$actor->hasPermission('user.suspend')`), and it is a different question
	// from the per-user `canSuspend` above: that one is "may this caller
	// suspend THIS account" (it also excludes self-suspension and a
	// higher-ranked actor), while this one is "may this caller suspend at
	// all", which is what a user-list control branches on.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "canSuspendUsers",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return req.Can(domain.PermissionSuspendUser, api.TypeForum, "1"), nil
		},
	})

	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/users/{id}/suspend",
		Handler: suspendHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/users/{id}/suspend",
		Handler: unsuspendHandler,
	})

	// Upstream's admin page registers this as `user.suspend`
	// (flarum-suspend/js/src/admin/extend.tsx).
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionSuspendUser,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/suspend/forum.js",
		Admin:    "plugins/suspend/admin.js",
		CSS:      "plugins/suspend/style.css",
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

// canSuspend reports whether the caller may suspend the account.
//
// It is upstream's `Suspend\Access\UserPolicy::suspend()`: the caller needs
// the `user.suspend` permission, and nobody may suspend themselves. The
// administrator rule needs the target, so it lives in [suspendable].
func canSuspend(req *plugin.AttributeRequest) bool {
	if !req.Can(domain.PermissionSuspendUser, api.TypeUser, req.RecordID) {
		return false
	}
	return req.ActorID != req.RecordID
}

// visibleTo reports whether a suspension is the caller's business.
//
// Upstream shows the date and the message to the suspended account and to
// the actors who may suspend; everyone else sees the account as ordinary.
func visibleTo(req *plugin.AttributeRequest, until time.Time) bool {
	if until.IsZero() {
		return false
	}
	return canSuspend(req) || req.ActorID == req.RecordID
}

// addSuspensionColumns adds the three columns to the shared users collection.
//
// The columns are added only when missing, so re-running is safe. There is
// nothing to backfill: no previous version stored a suspension.
func addSuspensionColumns(app core.App) error {
	collection, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return errors.Wrap(err, "suspend: the shared users collection is missing")
	}

	changed := false

	if collection.Fields.GetByName(suspendedUntilField) == nil {
		collection.Fields.Add(&core.DateField{Name: suspendedUntilField})
		changed = true
	}
	if collection.Fields.GetByName(reasonField) == nil {
		collection.Fields.Add(&core.TextField{Name: reasonField, Max: 500})
		changed = true
	}
	if collection.Fields.GetByName(messageField) == nil {
		collection.Fields.Add(&core.TextField{Name: messageField, Max: 1000})
		changed = true
	}

	if !changed {
		return nil
	}

	if err := app.Save(collection); err != nil {
		return errors.Wrap(err, "suspend: the suspension columns could not be added")
	}

	return nil
}
