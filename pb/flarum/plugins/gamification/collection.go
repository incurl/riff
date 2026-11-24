package gamification

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/pb/flarum/repository"
)

// createVotesCollection creates the plugin's vote collection.
//
// It is upstream's `post_votes`, with the same shape: one row per
// (post, user) pair, carrying a value of +1 or -1.
//
// The UNIQUE index on (post, user) is load-bearing, and it is what makes a
// vote idempotent: the second vote for the same post by the same account
// updates the row rather than adding one, so a double-clicked button cannot
// inflate the score. Upstream reaches the same result with an application
// level check; the index makes it the database's job, which is the only
// place it can be enforced against two concurrent requests.
func createVotesCollection(app core.App, physical, postsPhysical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return errors.Wrap(err, "gamification: the shared users collection is missing")
	}

	posts, err := app.FindCollectionByNameOrId(postsPhysical)
	if err != nil {
		return errors.Wrap(err, "gamification: the posts collection is missing")
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
	collection.Fields.Add(&core.NumberField{
		Name: "value",
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

	// The read rules are open: the SCORE is public (a post's votes are
	// shown to everybody), and the rows themselves carry nothing that is
	// not already on the post. The write rules are closed because every
	// vote goes through the plugin's route, which checks the permission.
	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_"+physical+"_post_user", true, "post, user", "")
	collection.AddIndex("idx_"+physical+"_user", false, "user", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "gamification: could not create the %q collection", physical)
	}

	return nil
}

// createRanksCollection creates the plugin's rank collection.
//
// It is upstream's `ranks`: a name, a colour and the points needed to
// reach it.
func createRanksCollection(app core.App, physical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	collection := core.NewBaseCollection(physical)
	collection.Fields.Add(&core.TextField{
		Name:     "name",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "color",
		Max:  255,
	})
	collection.Fields.Add(&core.NumberField{
		Name: "points",
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

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_"+physical+"_points", false, "points", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "gamification: could not create the %q collection", physical)
	}

	return nil
}
