package likes

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createLikesCollection creates the plugin's like collection.
//
// The unique index on (post, user) is what prevents a duplicate like:
// the database refuses the second row, so a double-clicked like button
// cannot inflate the count.
func createLikesCollection(app core.App, physical string) error {
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
		return errors.Wrapf(err, "likes: could not create the %q collection", physical)
	}
	return nil
}
