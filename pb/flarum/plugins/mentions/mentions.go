// Package mentions is the forum's Mentions plugin.
//
// It mirrors upstream Flarum's `flarum/mentions` extension: a post can
// reference an account with `@username`, the reference is STORED, the
// mentioned account is notified, and the post resource says who else
// mentioned it.
//
// What is NOT ported is recorded in DECISIONS.md: the composer
// autocomplete, the quoted `@"Display Name"#id` syntax, the post, group
// and tag mention forms, and the "mentioned by" modal. Each is a
// surface of its own, and half of one is worse than none.
package mentions

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
//
// It is upstream's extension id, so the documentation and the catalogue
// namespace line up with `flarum/mentions`.
const PluginID = "flarum/mentions"

// Version is the plugin's version.
const Version = "1.0.0"

// MaxMentionedBy is how many mentioning posts the `mentionedBy`
// relationship exposes.
//
// It is upstream's `PostResourceFields::$maxMentionedBy = 4`. The COUNT
// is the full number, so the UI can say "and 3 more".
const MaxMentionedBy = 4

// NotificationType is the kind of notification a mention produces.
//
// It is upstream's `UserMentionedBlueprint::getType()` (`userMentioned`).
//
// The forum's notifications collection is the authority on what a
// notification may be: a SelectField refuses a value it does not
// allow, so a kind the collection does not carry cannot be written at
// all.
const NotificationType = "userMentioned"

// The logical collection name the plugin owns.
const mentionsCollection = "post_mentions_user"

// Plugin is the Mentions plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// bus is the forum's event bus. The plugin subscribes to it to
	// learn about a post that was written.
	bus *flarumcore.EventBus
	// repo is the forum's repository, used to write the notification.
	repo *repository.Repository
	// physical is the mentions collection's real name.
	physical string
}

// New returns the Mentions plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Mentions",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#4B6E88",
				Glyph:           "fas fa-at",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	p.physical = ext.Registry.Physical(mentionsCollection)
	p.bus = ext.Bus
	if repo, err := flarumcore.Resolve[*repository.Repository](ext.Container); err == nil {
		p.repo = repo
	}

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_post_mentions_user",
		Up: func(_ context.Context, app core.App) error {
			return createMentionsCollection(app, p.physical)
		},
	})
	ext.AddMigration(plugin.Migration{
		Name: "allow_the_mention_notification_kind",
		Up: func(_ context.Context, app core.App) error {
			return allowNotificationKind(app)
		},
	})

	// --- layer 2: the API ----------------------------------------------
	// `mentionedByCount` and `mentionedBy` are upstream's
	// `PostResourceFields` contributions: the count says how many posts
	// mention this one, and the relationship names the first few of
	// them.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "mentionedByCount",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			authorID, err := p.postAuthor(req.App, req.RecordID)
			if err != nil {
				return 0, err
			}
			return mentionedByCount(req.App, authorID, p.postVisibility(req.Can))
		},
	})
	// The post's OWN mentions: the accounts it names. It is upstream's
	// `mentionsUsers` relation, and it is the READ side of the stored
	// rows — the post that carries the `@username`.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "mentionsUsers",
		Type:     api.TypeUser,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			ids, err := mentionedUserIDs(req.App, req.RecordID)
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
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "mentionedBy",
		Type:     api.TypePost,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			authorID, err := p.postAuthor(req.App, req.RecordID)
			if err != nil {
				return nil, err
			}
			ids, err := mentionedByPostIDs(req.App, authorID, MaxMentionedBy, p.postVisibility(req.Can))
			if err != nil {
				return nil, err
			}
			out := make([]plugin.Identifier, 0, len(ids))
			for _, id := range ids {
				out = append(out, plugin.Identifier{Type: api.TypePost, ID: id})
			}
			return out, nil
		},
	})

	// --- the frontend --------------------------------------------------
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/mentions/forum.js",
		Admin:    "plugins/mentions/admin.js",
		CSS:      "plugins/mentions/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
//
// The plugin SUBSCRIBES to the event bus here rather than in Register:
// the subscription is a side effect, and Register must not have any.
func (p *Plugin) Boot(_ context.Context, ext *plugin.Extenders) error {
	if ext.Bus == nil {
		return nil
	}

	handler := func(_ context.Context, event flarumcore.Event) {
		p.handlePostWritten(event)
	}

	// A post that was CREATED and a post that was EDITED are handled the
	// same way: the mention set is REPLACED with the one the stored
	// content carries.
	ext.Bus.SubscribeAfter(flarumcore.EventPostCreated, PluginID, 0, handler)
	ext.Bus.SubscribeAfter(flarumcore.EventPostEdited, PluginID, 0, handler)

	return nil
}

// Shutdown releases the plugin's resources.
//
// The subscription is removed with the plugin's id as the owner, so a
// disabled plugin leaves no listener behind.
func (*Plugin) Shutdown(_ context.Context, ext *plugin.Extenders) error {
	if ext.Bus != nil {
		ext.Bus.Unsubscribe(PluginID)
	}
	return nil
}

// postAuthor returns the account that wrote a post.
//
// The `mentionedBy` relation is a relation on the ACCOUNT, not on the
// post: it names the other places the post's AUTHOR was mentioned. So
// the post's author is what the lookup starts from.
func (p *Plugin) postAuthor(_ core.App, postID string) (string, error) {
	if p.repo == nil {
		return "", nil
	}
	post, err := p.repo.FindPost(postID)
	if err != nil {
		return "", err
	}
	return post.UserID, nil
}

// postVisibility returns the predicate that decides whether a post
// counts as visible to the caller.
//
// It is upstream's `whereVisibleTo`: the count of the mentioning posts
// must not reveal a post the caller cannot read.
//
// A HIDDEN post is visible to a moderator and to nobody else, and a
// post in a HIDDEN DISCUSSION is hidden with it — which is the forum's
// own rule (`api.Server.discussionResource` hides the discussion, and
// the discussion's posts go with it).
//
// The `can` argument is the permission checker: it answers for the
// CALLER, so the answer depends on who is asking.
func (p *Plugin) postVisibility(can func(action, subjectType, subjectID string) bool) func(postID string) bool {
	return func(postID string) bool {
		if p.repo == nil {
			return false
		}

		post, err := p.repo.FindPost(postID)
		if err != nil || post == nil {
			return false
		}

		if post.IsHidden {
			return can(domain.PermissionHidePost, api.TypePost, postID)
		}

		discussion, err := p.repo.FindDiscussion(post.DiscussionID)
		if err != nil || discussion == nil {
			return false
		}
		if discussion.IsHidden {
			return can(domain.PermissionHideDiscussion, api.TypeDiscussion, discussion.ID)
		}

		return true
	}
}
