// Package tags is the forum's first-party Tags plugin.
//
// It is the reference implementation of the three-layer plugin flow:
//
//  1. a migration that creates the plugin's PocketBase collections,
//  2. JSON:API resources and relationships contributed through the
//     extender mechanism,
//  3. a frontend bundle that adds the UI entries.
//
// The plugin mirrors upstream Flarum's `flarum/tags` extension: a
// hierarchy of tags, discussions carrying tags, and a restricted tag
// that has its own permission scope.
package tags

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
const PluginID = "riff/tags"

// Version is the plugin's version.
const Version = "1.0.0"

// Plugin is the Tags plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
}

// New returns the Tags plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Tags",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#F28326",
				Glyph:           "fas fa-tags",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
//
// It creates the plugin's collection, contributes the tag relationship
// on discussions, and registers the tag endpoints.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	physical := ext.Registry.Physical("tags")

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_tags",
		Up: func(_ context.Context, app core.App) error {
			return createTagsCollection(app, physical)
		},
	})

	// --- layer 2: the API ----------------------------------------------
	// The tag relationship rides on the discussion resource, which is
	// where the frontend reads it from.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypeDiscussion,
		Name:     "tags",
		Type:     api.TypeTag,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			ids, err := discussionTagIDs(req.App, req.RecordID)
			if err != nil {
				return nil, err
			}
			out := make([]plugin.Identifier, 0, len(ids))
			for _, id := range ids {
				out = append(out, plugin.Identifier{Type: api.TypeTag, ID: id})
			}
			return out, nil
		},
	})

	// The tag's own relationships. `children` is upstream's, and the
	// frontend sorts on it.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypeTag,
		Name:     "children",
		Type:     api.TypeTag,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			ids, err := childTagIDs(req.App, physicalName(nil), req.RecordID)
			if err != nil {
				return nil, err
			}
			out := make([]plugin.Identifier, 0, len(ids))
			for _, id := range ids {
				out = append(out, plugin.Identifier{Type: api.TypeTag, ID: id})
			}
			return out, nil
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypeTag,
		Name:     "lastPostedDiscussion",
		Type:     api.TypeDiscussion,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			tag, err := findTag(req.App, physicalName(nil), req.RecordID)
			if err != nil {
				return nil, err
			}
			if tag.LastPostedDiscussion == "" {
				return nil, nil
			}
			return []plugin.Identifier{{Type: api.TypeDiscussion, ID: tag.LastPostedDiscussion}}, nil
		},
	})

	// The tag resource contributes its own attributes to the posts
	// endpoint when a post's discussion carries a tag.
	// The discussion's `tags` relationship is WRITABLE, the way
	// upstream's is: the frontend saves the discussion with the
	// relationship, and the extension validates the whole set.
	ext.AddRelationshipWriter(plugin.RelationshipWriter{
		Resource: api.TypeDiscussion,
		Name:     "tags",
		Write:    p.writeDiscussionTags,
	})

	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/tags",
		Handler: p.listTagsHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/tags/{id}",
		Handler: p.showTagHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/tags",
		Handler: p.createTagHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "PATCH",
		Path:    "/api/tags/{id}",
		Handler: p.updateTagHandler,
	})
	// The PATCH and the DELETE share ONE POST twin: the frontend
	// rewrites every method other than GET and POST into a POST with
	// the intended method in `X-HTTP-Method-Override`, and PocketBase's
	// router matches on the REAL method before any handler runs — so a
	// route registered only for PATCH (or only for DELETE) is
	// UNREACHABLE from the frontend.
	//
	// They cannot have TWO POST routes: two patterns on the same path
	// CONFLICT in Go's `http.ServeMux` and the panic takes the process
	// down. The override header is what tells them apart.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/tags/{id}",
		Handler: p.tagWriteHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/tags/{id}",
		Handler: p.deleteTagHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/tags/order",
		Handler: p.orderTagsHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/discussions/{id}/tags",
		Handler: p.addTagHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "DELETE",
		Path:    "/api/discussions/{id}/tags/{tagId}",
		Handler: p.removeTagHandler,
	})

	// --- the permission key --------------------------------------------
	// The plugin registers its own permission keys through the
	// extender mechanism; the core's permission service reads them
	// from the same table.
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionEditTag,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	// --- layer 3: the frontend ----------------------------------------
	// --- the settings ---------------------------------------------------
	// The four count settings upstream's extension declares, with
	// upstream's keys.
	//
	// They are declared HERE because the server READS them: the
	// defaults are what the forum falls back to when a setting is
	// unset, and they must be the same values the admin portal shows.
	ext.AddSettings(plugin.SettingsSchema{
		Fields: []plugin.SettingField{
			{
				Key:     SettingMinPrimaryTags,
				Label:   "Minimum primary tags",
				Type:    "number",
				Default: strconv.Itoa(DefaultMinPrimaryTags),
			},
			{
				Key:     SettingMaxPrimaryTags,
				Label:   "Maximum primary tags",
				Type:    "number",
				Default: strconv.Itoa(DefaultMaxPrimaryTags),
			},
			{
				Key:     SettingMinSecondaryTags,
				Label:   "Minimum secondary tags",
				Type:    "number",
				Default: strconv.Itoa(DefaultMinSecondaryTags),
			},
			{
				Key:     SettingMaxSecondaryTags,
				Label:   "Maximum secondary tags",
				Type:    "number",
				Default: strconv.Itoa(DefaultMaxSecondaryTags),
			},
		},
	})

	// The four COUNT settings travel on the FORUM DOCUMENT, so the
	// composer's tag picker knows the limits before an author chooses
	// anything.
	//
	// Upstream publishes them on the forum resource (they are read as
	// `app.forum.attribute('minPrimaryTags')` and its three siblings), and
	// they have to be there for the same reason the picker exists: a modal
	// that ignored the limits would let an author compose a discussion the
	// server then REFUSES, which is the worst way to enforce a minimum.
	//
	// They are published as NUMBERS, because upstream's are numbers and
	// `parseInt` is what the composer applies to them. The values are the
	// settings the server reads (`tagCounts`), so the form and the write
	// path agree by construction.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: "forums",
		Name:     "minPrimaryTags",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			minPrimary, _, _, _ := tagCounts(req.App)
			return minPrimary, nil
		},
	})

	ext.AddAttribute(plugin.AttributeContributor{
		Resource: "forums",
		Name:     "maxPrimaryTags",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			_, maxPrimary, _, _ := tagCounts(req.App)
			return maxPrimary, nil
		},
	})

	ext.AddAttribute(plugin.AttributeContributor{
		Resource: "forums",
		Name:     "minSecondaryTags",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			_, _, minSecondary, _ := tagCounts(req.App)
			return minSecondary, nil
		},
	})

	ext.AddAttribute(plugin.AttributeContributor{
		Resource: "forums",
		Name:     "maxSecondaryTags",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			_, _, _, maxSecondary := tagCounts(req.App)
			return maxSecondary, nil
		},
	})

	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/tags/forum.js",
		Admin:    "plugins/tags/admin.js",
		CSS:      "plugins/tags/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
//
// The Tags plugin has none: everything happens in the Register phase and
// in the migrations.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// Shutdown releases the plugin's resources.
//
// The Tags plugin holds none.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error {
	return nil
}

// errNotFound reports a missing tag.
var errNotFound = errors.New("tags: not found")

// writeDiscussionTags applies a discussion's `tags` relationship.
//
// It is the WRITE side of the relationship, and it is what upstream's
// frontend calls: `discussion.save({ relationships: { tags } })`.
//
// The count rules are enforced HERE and not on the attach route,
// because this is where the whole set is visible at once — which is
// where upstream validates them too (its `tags` relationship setter).
// The attach route only checks the maximums, because a minimum is a
// property of the FINISHED set.
//
// The tags are replaced, not merged: the relationship names the
// discussion's COMPLETE tag set, so a tag the client left out is
// removed.
func (*Plugin) writeDiscussionTags(ctx context.Context, req *plugin.RelationshipWriteRequest) error {
	// TAGGING A DISCUSSION AS IT IS CREATED is a different question from
	// changing an existing discussion's tags, and upstream asks it
	// differently.
	//
	// Upstream's `DiscussionPolicy::tag()` governs CHANGING the tags of a
	// discussion that exists: the author may, while `allow_tag_change`
	// permits it, and a moderator may through the `discussion.tag`
	// permission. Both of those are about a discussion ALREADY POSTED.
	//
	// A discussion that is being CREATED has no author to be "the author
	// who may still change it", and `allow_tag_change` is explicitly about
	// changing tags AFTER posting. So on create the question is the
	// ordinary one: may this actor start a discussion at all, and with
	// these tags? That is `canStartDiscussion` on each tag — the same
	// answer the composer's picker used when it offered the tag in the
	// first place.
	//
	// Without this branch the composer could not create a TAGGED
	// discussion at all: the writer demanded the "change tags" permission
	// for a discussion that did not exist yet, and every tagged create
	// answered 422.
	if req.Creating {
		for _, id := range req.IDs {
			if _, err := findTag(req.App, physicalName(nil), id); err != nil {
				return errors.New("that tag does not exist")
			}

			// The same question the tag resource answers with
			// `canStartDiscussion`: may this actor start a discussion in
			// this tag? A tag with its own permission scope is the case
			// that matters, and the check is the SCOPED one — the same
			// `Can` the attribute uses.
			if !req.Can(domain.PermissionStartDiscussion, api.TypeTag, id) {
				return errors.New("you are not allowed to start a discussion with that tag")
			}
		}

		return writeTags(req, req.IDs)
	}

	// Changing an EXISTING discussion's tags. Two ways, and upstream's
	// `DiscussionPolicy::tag()` is the FIRST of them:
	//
	//   - the AUTHOR, while the `allow_tag_change` setting permits it;
	//   - a MODERATOR, through the `discussion.tag` permission that
	//     upstream's own migration seeds for the moderator group.
	//
	// The author rule needs the discussion and the setting, so it is
	// the core's `canTagDiscussion` — the very same check the `canTag`
	// attribute and the attach route use, so the flag and the write
	// always agree.
	allowed := req.Can(domain.PermissionTagDiscussion, api.TypeDiscussion, req.RecordID)
	if !allowed {
		var err error
		allowed, err = api.CanTagDiscussionAs(ctx, req.ActorID, req.RecordID)
		if err != nil {
			return err
		}
	}
	if !allowed {
		return errors.New("you are not allowed to change that discussion's tags")
	}

	return writeTags(req, req.IDs)
}

// writeTags resolves the tag ids, enforces the count rules, and sets the
// discussion's tags.
//
// It is the half both paths share: creating a discussion with tags and
// changing an existing one's tags differ in WHO may do it, not in what
// happens to the tags.
func writeTags(req *plugin.RelationshipWriteRequest, ids []string) error {
	tags := make([]*domain.Tag, 0, len(ids))
	for _, id := range ids {
		tag, err := findTag(req.App, physicalName(nil), id)
		if err != nil {
			return errors.New("that tag does not exist")
		}
		tags = append(tags, tag)
	}

	// The count rules are upstream's `validateTagCount`, and they are
	// skipped for a caller who may bypass them.
	if !mayBypassTagCounts(req.Can) {
		if err := validateTagCounts(req.App, tags); err != nil {
			return err
		}
	}

	// The OLD set, read BEFORE the change: it is what the event post
	// records, so it has to be the set as it was.
	//
	// A discussion being CREATED has no old set, and upstream raises its
	// `DiscussionWasTagged` event only for an existing discussion — so no
	// post is created for a create.
	oldIDs, err := discussionTagIDs(req.App, req.RecordID)
	if err != nil {
		return err
	}

	if err := setDiscussionTags(req.App, req.RecordID, tags); err != nil {
		return err
	}

	// A CHANGE is recorded in the stream, so a reader can see that a
	// discussion was retagged and when. Upstream creates the post only when
	// the set actually changed — a client that re-sends the same tags has
	// not changed anything, and a post saying so is noise.
	newIDs := make([]string, 0, len(tags))
	for _, tag := range tags {
		newIDs = append(newIDs, tag.ID)
	}

	if !sameTagSet(oldIDs, newIDs) {
		if err := createTaggedEventPost(req.App, req.RecordID, req.ActorID, oldIDs, newIDs); err != nil {
			return err
		}
	}

	return nil
}
