package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/pb/flarum/repository"
)

// Core runs the forum's core migrations.
//
// The steps are applied in order and are individually idempotent: a step
// that finds its collection already in place succeeds. That is what makes
// the migration safe to re-run after a partial failure.
//
// The shared `users` collection is NOT created here: it belongs to the
// host, and the forum reuses it as its account source.
func Core(app core.App, _ *repository.Registry) error {
	steps := []func(core.App) error{
		createUserProfiles,
		createGroups,
		createGroupUsers,
		createDiscussions,
		createPosts,
		addDiscussionTaggedPostType,
		createDiscussionTags,
		createPermissions,
		createSettings,
		createPluginState,
		createPluginMigrations,
		createNotifications,
		createActivityLog,
	}

	for _, step := range steps {
		if err := step(app); err != nil {
			return err
		}
	}
	return nil
}

// saveIfMissing saves a collection unless one with the same name exists.
//
// It is the idempotency primitive every core step uses. A collection
// that already exists is adopted only when it carries the forum's
// ownership marker; a collection the host owns under the same name is
// left alone, and the pre-flight collision check has already refused
// to start in that case.
func saveIfMissing(app core.App, collection *core.Collection) error {
	existing, err := app.FindCollectionByNameOrId(collection.Name)
	if err == nil && existing != nil {
		if !repository.IsOwned(existing) {
			return &CollisionError{Name: existing.Name, Prefix: repository.DefaultPrefix, Existing: existing.Name}
		}
		return nil
	}
	if err := app.Save(collection); err != nil {
		return err
	}
	return repository.MarkOwned(app, collection)
}

// createUserProfiles creates the forum's per-account extras.
//
// The account itself lives in the shared `users` collection; this
// collection holds only what the forum adds.
func createUserProfiles(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_user_profiles")
	collection.Fields.Add(&core.RelationField{
		Name:          "user",
		CollectionId:  users.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.TextField{
		Name: "display_name",
		Max:  255,
	})
	collection.Fields.Add(&core.JSONField{
		Name: "preferences",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_active",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_blocked",
	})
	collection.Fields.Add(&core.DateField{
		Name: "joined_at",
	})
	collection.Fields.Add(&core.DateField{
		Name: "last_seen_at",
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created",
		OnCreate: true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "updated",
		OnCreate: true,
		OnUpdate: true,
	})

	collection.ListRule = types.Pointer("user = @request.auth.id")
	collection.ViewRule = types.Pointer("user = @request.auth.id")
	collection.CreateRule = nil // server-only
	collection.UpdateRule = types.Pointer("user = @request.auth.id")
	collection.DeleteRule = types.Pointer("user = @request.auth.id")

	collection.AddIndex("idx_forum_user_profiles_user", true, "user", "")

	return saveIfMissing(app, collection)
}

// createGroups creates the permission groups.
func createGroups(app core.App) error {
	collection := core.NewBaseCollection("forum_groups")
	collection.Fields.Add(&core.TextField{
		Name:     "name_singular",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "name_plural",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "color",
		Max:  255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "icon",
		Max:  255,
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_hidden",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "position",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "member_count",
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created",
		OnCreate: true,
	})

	// The groups are readable by everyone (the frontend renders group
	// badges on posts), but only a superuser may change them; the admin
	// surface goes through the Connect-like handlers, which enforce the
	// permission service.
	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	return saveIfMissing(app, collection)
}

// createGroupUsers creates the group membership table.
func createGroupUsers(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}
	groups, err := app.FindCollectionByNameOrId("forum_groups")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_group_users")
	collection.Fields.Add(&core.RelationField{
		Name:          "user",
		CollectionId:  users.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.RelationField{
		Name:          "group",
		CollectionId:  groups.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created",
		OnCreate: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_group_users_user_group", true, "user, group", "")

	return saveIfMissing(app, collection)
}

// createDiscussions creates the threads.
func createDiscussions(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_discussions")
	collection.Fields.Add(&core.TextField{
		Name:     "title",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "slug",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "user",
		CollectionId: users.Id,
		Required:     true,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "last_posted_user",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.TextField{
		Name: "first_post",
		Max:  15,
	})
	collection.Fields.Add(&core.TextField{
		Name: "last_post",
		Max:  15,
	})
	collection.Fields.Add(&core.NumberField{
		Name: "last_post_number",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "comment_count",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "participant_count",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "view_count",
	})
	collection.Fields.Add(&core.SelectField{
		Name:   "state",
		Values: []string{"open", "locked", "hidden", "sticky"},
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "hidden_user",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created_at",
		OnCreate: true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "updated_at",
		OnCreate: true,
		OnUpdate: true,
	})
	collection.Fields.Add(&core.DateField{
		Name: "last_posted_at",
	})
	collection.Fields.Add(&core.DateField{
		Name: "hidden_at",
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_discussions_last_posted_at", false, "last_posted_at", "")
	collection.AddIndex("idx_forum_discussions_user", false, "user", "")
	collection.AddIndex("idx_forum_discussions_slug", false, "slug", "")

	return saveIfMissing(app, collection)
}

// createPosts creates the replies.
func createPosts(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}
	discussions, err := app.FindCollectionByNameOrId("forum_discussions")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_posts")
	collection.Fields.Add(&core.RelationField{
		Name:          "discussion",
		CollectionId:  discussions.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "user",
		CollectionId: users.Id,
		Required:     true,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.NumberField{
		Name:     "number",
		Required: true,
	})
	collection.Fields.Add(&core.EditorField{
		Name: "content",
	})
	collection.Fields.Add(&core.EditorField{
		Name: "content_html",
	})
	collection.Fields.Add(&core.TextField{
		Name: "content_type",
		Max:  255,
	})
	collection.Fields.Add(&core.SelectField{
		Name: "type",
		// `discussionTagged` is upstream's third post type: the event post
		// that records that a discussion's tags changed. The tags extension
		// writes it, and the migration that adds it to an EXISTING instance is
		// `1960000000_add_discussion_tagged_post_type` — this line is what a
		// fresh install gets.
		Values: []string{"comment", "discussionRenamed", "discussionTagged"},
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "edited_user",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "hidden_user",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_hidden",
	})
	collection.Fields.Add(&core.TextField{
		Name: "ip_address",
		Max:  255,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created_at",
		OnCreate: true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "updated_at",
		OnCreate: true,
		OnUpdate: true,
	})
	collection.Fields.Add(&core.DateField{
		Name: "edited_at",
	})
	collection.Fields.Add(&core.DateField{
		Name: "hidden_at",
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_posts_discussion_number", false, "discussion, number", "")
	collection.AddIndex("idx_forum_posts_user", false, "user", "")

	return saveIfMissing(app, collection)
}

// addDiscussionTaggedPostType adds the `discussionTagged` post type to an
// EXISTING posts collection.
//
// The forum's own migration creates the collection with the type already
// listed, so this step is a no-op on a fresh install. It exists for an
// instance whose posts collection predates the type: PocketBase refuses a
// value a SELECT field does not list, so without it the tags extension
// could not save the event post it creates when a discussion is retagged.
//
// Idempotency: a no-op when the value is already there.
func addDiscussionTaggedPostType(app core.App) error {
	collection, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		// The collection does not exist yet: `createPosts` owns it, and it
		// will list the value from the start.
		return nil //nolint:nilerr // a missing collection means nothing to alter
	}

	field := collection.Fields.GetByName("type")
	if field == nil {
		return nil
	}

	selectField, ok := field.(*core.SelectField)
	if !ok {
		return nil
	}

	for _, value := range selectField.Values {
		if value == "discussionTagged" {
			return nil
		}
	}

	selectField.Values = append(selectField.Values, "discussionTagged")

	return app.Save(collection)
}

// createDiscussionTags creates the discussion-to-tag link table.
//
// The tag collection itself belongs to the Tags plugin; the link table is
// core-owned because a discussion's tags are a core relationship.
func createDiscussionTags(app core.App) error {
	discussions, err := app.FindCollectionByNameOrId("forum_discussions")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_discussion_tags")
	collection.Fields.Add(&core.RelationField{
		Name:          "discussion",
		CollectionId:  discussions.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "tag",
		Required: true,
		Max:      15,
	})
	collection.Fields.Add(&core.NumberField{
		Name: "position",
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_discussion_tags_discussion_tag", true, "discussion, tag", "")

	return saveIfMissing(app, collection)
}

// createPermissions creates the granular access rules.
func createPermissions(app core.App) error {
	groups, err := app.FindCollectionByNameOrId("forum_groups")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_permissions")
	collection.Fields.Add(&core.RelationField{
		Name:          "group",
		CollectionId:  groups.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "permission",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "scope",
		Max:  255,
	})
	collection.Fields.Add(&core.BoolField{
		Name:     "allowed",
		Required: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_permissions_group_permission", true, "group, permission, scope", "")

	return saveIfMissing(app, collection)
}

// createSettings creates the site-wide and plugin settings.
func createSettings(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_settings")
	collection.Fields.Add(&core.TextField{
		Name:     "key",
		Required: true,
		Max:      191,
	})
	collection.Fields.Add(&core.JSONField{
		Name: "value",
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "updated_by",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "updated",
		OnCreate: true,
		OnUpdate: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_settings_key", true, "key", "")

	return saveIfMissing(app, collection)
}

// createPluginState creates the enabled/disabled state of the plugins.
func createPluginState(app core.App) error {
	collection := core.NewBaseCollection("forum_plugin_state")
	collection.Fields.Add(&core.TextField{
		Name:     "plugin_id",
		Required: true,
		Max:      191,
	})
	collection.Fields.Add(&core.BoolField{
		Name:     "enabled",
		Required: true,
	})
	collection.Fields.Add(&core.JSONField{
		Name: "config",
	})
	collection.Fields.Add(&core.TextField{
		Name: "installed_version",
		Max:  64,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "enabled_at",
		OnCreate: true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "updated",
		OnCreate: true,
		OnUpdate: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_plugin_state_plugin_id", true, "plugin_id", "")

	return saveIfMissing(app, collection)
}

// createPluginMigrations records the applied migration versions.
func createPluginMigrations(app core.App) error {
	collection := core.NewBaseCollection("forum_plugin_migrations")
	collection.Fields.Add(&core.TextField{
		Name:     "plugin_id",
		Required: true,
		Max:      191,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "version",
		Required: true,
		Max:      191,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "applied_at",
		OnCreate: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_plugin_migrations_plugin_version", true, "plugin_id, version", "")

	return saveIfMissing(app, collection)
}

// createNotifications creates the in-app notification inbox.
func createNotifications(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}
	discussions, err := app.FindCollectionByNameOrId("forum_discussions")
	if err != nil {
		return err
	}
	posts, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_notifications")
	collection.Fields.Add(&core.RelationField{
		Name:          "user",
		CollectionId:  users.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.SelectField{
		Name:   "type",
		Values: []string{"postLiked", "discussionTagged", "mentionedInPost", "commentOnPost"},
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "discussion",
		CollectionId: discussions.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "post",
		CollectionId: posts.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.TextField{
		Name: "subject",
		Max:  255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "excerpt",
		Max:  255,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "from_user",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.DateField{
		Name: "read_at",
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created_at",
		OnCreate: true,
	})

	collection.ListRule = types.Pointer("user = @request.auth.id")
	collection.ViewRule = types.Pointer("user = @request.auth.id")
	collection.CreateRule = nil // server-only
	collection.UpdateRule = types.Pointer("user = @request.auth.id")
	collection.DeleteRule = types.Pointer("user = @request.auth.id")

	collection.AddIndex("idx_forum_notifications_user_read", false, "user, read_at", "")

	return saveIfMissing(app, collection)
}

// createActivityLog creates the audit trail.
func createActivityLog(app core.App) error {
	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection("forum_activity_log")
	collection.Fields.Add(&core.RelationField{
		Name:         "actor",
		CollectionId: users.Id,
		MaxSelect:    1,
		MinSelect:    1,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "action",
		Required: true,
		Max:      191,
	})
	collection.Fields.Add(&core.TextField{
		Name: "target",
		Max:  191,
	})
	collection.Fields.Add(&core.JSONField{
		Name: "detail",
	})
	collection.Fields.Add(&core.SelectField{
		Name:   "severity",
		Values: []string{"info", "warning", "error"},
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created_at",
		OnCreate: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_forum_activity_log_created_at", false, "created_at", "")

	return saveIfMissing(app, collection)
}
