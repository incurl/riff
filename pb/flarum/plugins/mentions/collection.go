package mentions

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createMentionsCollection creates the plugin's mention collection.
//
// It is upstream's `post_mentions_user` table: one row per (post,
// mentioned account) pair.
//
// **THE UNIQUE INDEX IS WHAT MAKES A MENTION IDEMPOTENT.** A post that
// mentions the same account twice mentions it once, and upstream's
// primary key on the pair does the same.
//
// The read rules are open and the write rules are closed: every write
// goes through the plugin, as with the other plugin collections.
func createMentionsCollection(app core.App, physical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return err
	}
	posts, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection(physical)
	collection.Fields.Add(&core.RelationField{
		Name:          "post",
		CollectionId:  posts.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.RelationField{
		Name:          "user",
		CollectionId:  users.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
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

	collection.AddIndex("idx_"+physical+"_post_user", true, "post, user", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "mentions: could not create the %q collection", physical)
	}
	return nil
}

// allowNotificationKind widens the notifications collection's `type`
// field so it accepts the mention kind.
//
// **THE COLLECTION IS THE AUTHORITY ON WHAT A NOTIFICATION MAY BE.** A
// `SelectField` refuses a value it does not allow — the write fails
// with "Invalid value …" — so a kind the collection does not carry
// cannot be written at all, and the notification is silently lost.
//
// The mention kind is upstream's `userMentioned`
// (`UserMentionedBlueprint::getType()`), and upstream's SPA looks the
// component up by that name. The collection already carried
// `mentionedInPost`, which upstream does not use, so the two spellings
// coexist: the old one stays valid for whatever already wrote it, and
// the new one is added.
//
// The step is IDEMPOTENT: a collection that already allows the kind is
// left alone, so re-running it changes nothing.
func allowNotificationKind(app core.App) error {
	collection, err := app.FindCollectionByNameOrId("forum_notifications")
	if err != nil {
		return errors.Wrap(err, "mentions: the forum_notifications collection is missing")
	}

	field, ok := collection.Fields.GetByName("type").(*core.SelectField)
	if !ok {
		return errors.New("mentions: the forum_notifications collection has no type field")
	}

	for _, value := range field.Values {
		if value == NotificationType {
			// Already allowed: nothing to do.
			return nil
		}
	}

	field.Values = append(field.Values, NotificationType)
	if err := app.Save(collection); err != nil {
		return errors.Wrap(err, "mentions: the notification kinds could not be widened")
	}
	return nil
}
