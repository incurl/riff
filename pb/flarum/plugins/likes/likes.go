// Package likes is the forum's first-party Likes plugin.
//
// Like the Tags plugin, it is a reference implementation of the
// three-layer plugin flow: a migration that creates the plugin's
// collection, JSON:API attributes and relationships contributed through
// the extender mechanism, and a frontend bundle that adds the UI.
//
// The plugin mirrors upstream Flarum's `flarum/likes` extension: a post
// carries a set of likes, the current user's like state is a writable
// attribute, and the likers are exposed as a relationship.
package likes

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/repository"
)

// PluginID is the plugin's id.
const PluginID = "riff/likes"

// Version is the plugin's version.
const Version = "1.0.0"

// MaxLikes is how many likers a post's `likes` relationship exposes.
//
// It matches upstream Flarum's `PostResourceFields::$maxLikes`.
const MaxLikes = 4

// SettingLikeOwnPost is the setting that governs whether an account
// may like its own post.
//
// It is upstream's `flarum-likes.like_own_post`, and upstream's
// default is TRUE: liking your own post is allowed unless an
// administrator turns it off.
const SettingLikeOwnPost = "flarum-likes.like_own_post"

// Plugin is the Likes plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// bus is the forum's event bus, used to announce a like.
	bus *flarumcore.EventBus
	// repo is the forum's repository, used to write the notification.
	repo *repository.Repository
}

// New returns the Likes plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Likes",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#3A649D",
				Glyph:           "far fa-thumbs-up",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	physical := ext.Registry.Physical("likes")

	// The bus announces a like, and the repository writes the
	// notification it produces. Both come from the forum's own
	// container, so a plugin does not build its own.
	p.bus = ext.Bus
	if repo, err := flarumcore.Resolve[*repository.Repository](ext.Container); err == nil {
		p.repo = repo
	}

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_likes",
		Up: func(_ context.Context, app core.App) error {
			return createLikesCollection(app, physical)
		},
	})

	// --- layer 2: the API ----------------------------------------------
	// The like state and the count are attributes on the post resource;
	// the likers are a relationship.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "likesCount",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return likeCount(req.App, req.RecordID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "isLiked",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			if req.ActorID == "" {
				return false, nil
			}
			return isLiked(req.App, req.RecordID, req.ActorID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "canLike",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			// The flag must answer the SAME question the write path
			// answers: a signed-in account may like a post, unless
			// the post is its own and the `like_own_post` setting
			// forbids that.
			//
			// A flag that says "yes" where the endpoint says "no"
			// is the control the reader clicks and the server
			// refuses.
			if req.ActorID == "" {
				return false, nil
			}

			// The author's own post is not likeable unless the
			// setting permits it — the same rule the write path
			// enforces.
			if !likeOwnPostAllowedAt(req.App, SettingLikeOwnPost, true) {
				author, err := postAuthor(req.App, req.RecordID)
				if err != nil {
					// A post that cannot be read is a post that
					// cannot be liked.
					return false, nil //nolint:nilerr // an unreadable post is not likeable
				}

				return author != req.ActorID, nil
			}

			return true, nil
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "likes",
		Type:     api.TypeUser,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			// The current user's like comes FIRST, so the frontend
			// can tell which of the listed likers is the reader.
			// That is upstream's ordering: it sorts the actor's own
			// row to the front before the `created_at` order.
			ids, err := likerIDs(req.App, req.RecordID, MaxLikes, req.ActorID)
			if err != nil {
				return nil, err
			}
			out := make([]plugin.Identifier, 0, len(ids))
			for _, id := range ids {
				out = append(out, plugin.Identifier{Type: api.TypeUser, ID: id})
			}
			return out, nil
		},
	})

	// The like toggle lives on its own route, because the frontend's
	// like button posts to it rather than patching the post.
	//
	// It is registered for BOTH verbs and resolves the effective method
	// itself. PocketBase bakes the method into the route pattern, and the
	// frame's frontend rewrites every DELETE into a POST carrying
	// `X-HTTP-Method-Override: DELETE` — so a DELETE-only route is
	// unreachable from the SPA, and a POST-only one cannot unlike. The
	// rewrite lands on the POST pattern, which is why the POST half has to
	// read the header back.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/posts/{id}/like",
		Handler: p.toggleHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/posts/{id}/like",
		Handler: p.toggleHandler,
	})

	// --- the notification kind ------------------------------------------
	// Declared so the settings page offers a preference for it. A kind a
	// plugin sends and does not declare is a notification the reader cannot
	// switch off, while the settings page shows them nothing at all.
	//
	// "alert" is upstream's only channel for this blueprint
	// (`PostLikedBlueprint implements AlertableInterface`): the forum does
	// not email a like, so an `email` preference would be a checkbox that
	// toggles nothing.
	ext.AddNotificationType(plugin.NotificationType{
		Type:     NotificationType,
		Channels: []string{"alert"},
	})

	// --- the permission key --------------------------------------------
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionLikePost,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	// --- the settings ---------------------------------------------------
	// The one setting upstream's extension declares, with upstream's
	// key: `flarum-likes.like_own_post`.
	//
	// It is declared HERE because the server READS it: the default is
	// what the forum falls back to when the setting is unset, and it
	// must be the same value the admin portal shows.
	ext.AddSettings(plugin.SettingsSchema{
		Fields: []plugin.SettingField{
			{
				Key:     SettingLikeOwnPost,
				Label:   "Allow liking your own posts",
				Type:    "bool",
				Default: "1",
			},
		},
	})

	// --- layer 3: the frontend ----------------------------------------
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/likes/forum.js",
		Admin:    "plugins/likes/admin.js",
		CSS:      "plugins/likes/style.css",
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
