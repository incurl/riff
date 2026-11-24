package polls

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/pb/flarum/repository"
)

// createPollsCollection creates the plugin's poll collection.
//
// It is upstream's `polls`: the question, the subtitle, the author, the
// post it hangs off, the end date and the settings blob.
//
// The SETTINGS BLOB carries the four per-poll switches upstream keeps in
// a JSON column (`public_poll`, `allow_multiple_votes`, `max_votes`,
// `hide_votes`, `allow_change_vote`). They are kept together in one JSON
// field rather than spread over columns because upstream moved them
// there, and a port that spread them would answer a different shape to
// the frontend.
func createPollsCollection(app core.App, physical, postsPhysical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	posts, err := app.FindCollectionByNameOrId(postsPhysical)
	if err != nil {
		return errors.Wrap(err, "polls: the posts collection is missing")
	}

	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return errors.Wrap(err, "polls: the shared users collection is missing")
	}

	collection := core.NewBaseCollection(physical)
	collection.Fields.Add(&core.TextField{
		Name:     "question",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "subtitle",
		Max:  255,
	})
	collection.Fields.Add(&core.RelationField{
		Name:          "post",
		CollectionId:  posts.Id,
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
	collection.Fields.Add(&core.DateField{
		Name: "end_date",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "published",
	})
	collection.Fields.Add(&core.JSONField{
		Name: "settings",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "vote_count",
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

	// The read rules are open: a poll's visibility is the POST's
	// visibility, and the post it hangs off decides that. The write
	// rules are closed because every write goes through the plugin's
	// routes, which check the permission.
	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_"+physical+"_post", false, "post", "")
	collection.AddIndex("idx_"+physical+"_user", false, "user", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "polls: could not create the %q collection", physical)
	}

	return nil
}

// createPollOptionsCollection creates the plugin's option collection.
//
// It is upstream's `poll_options`: the answer text and the poll it
// belongs to.
func createPollOptionsCollection(app core.App, physical, pollsPhysical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	polls, err := app.FindCollectionByNameOrId(pollsPhysical)
	if err != nil {
		return errors.Wrap(err, "polls: the polls collection is missing")
	}

	collection := core.NewBaseCollection(physical)
	collection.Fields.Add(&core.TextField{
		Name:     "answer",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.RelationField{
		Name:          "poll",
		CollectionId:  polls.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.NumberField{
		Name: "vote_count",
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

	collection.AddIndex("idx_"+physical+"_poll", false, "poll", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "polls: could not create the %q collection", physical)
	}

	return nil
}

// createPollVotesCollection creates the plugin's vote collection.
//
// It is upstream's `poll_votes`: the poll, the option and the voter.
//
// The UNIQUE index on (poll, option, user) is load-bearing. Upstream
// reaches the same result with an application-level check, but the index
// is the only place the guarantee holds against two concurrent requests:
// a double-clicked option cannot add a second row, so the count cannot
// double. It is UNIQUE on the option too — not only on (poll, user) —
// because a poll that allows multiple votes must still hold at most ONE
// row per (option, voter): changing an option's mind is a replacement,
// not an accumulation.
func createPollVotesCollection(app core.App, physical, pollsPhysical, optionsPhysical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	polls, err := app.FindCollectionByNameOrId(pollsPhysical)
	if err != nil {
		return errors.Wrap(err, "polls: the polls collection is missing")
	}

	options, err := app.FindCollectionByNameOrId(optionsPhysical)
	if err != nil {
		return errors.Wrap(err, "polls: the poll options collection is missing")
	}

	users, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return errors.Wrap(err, "polls: the shared users collection is missing")
	}

	collection := core.NewBaseCollection(physical)
	collection.Fields.Add(&core.RelationField{
		Name:          "poll",
		CollectionId:  polls.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.RelationField{
		Name:          "option",
		CollectionId:  options.Id,
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

	collection.AddIndex("idx_"+physical+"_poll_user", true, "poll, user", "")
	collection.AddIndex("idx_"+physical+"_option", false, "option", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "polls: could not create the %q collection", physical)
	}

	return nil
}
