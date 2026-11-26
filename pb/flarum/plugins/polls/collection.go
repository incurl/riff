package polls

import (
	"strings"

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

	collection.AddIndex("idx_"+physical+"_poll_option_user", true, "poll, option, user", "")
	collection.AddIndex("idx_"+physical+"_option", false, "option", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "polls: could not create the %q collection", physical)
	}

	return nil
}

// The index names the votes collection carries.
//
// The poll/option/user index is the one that matters: it is what makes
// a vote IDEMPOTENT, and what makes a MULTI-VOTE poll possible.
const (
	// pollVoteIndexName is the unique index on (poll, option, user).
	pollVoteIndexName = "poll_option_user"
	// pollVoteIndexColumns is the column list of that index.
	pollVoteIndexColumns = "poll, option, user"
)

// repairVoteIndex replaces the votes collection's unique index with the
// one the multi-vote poll needs.
//
// **THE WRONG INDEX WAS SHIPPED.** The index was on `(poll, user)`, so
// a voter held at most ONE vote row per poll — which makes
// `allow_multiple_votes` impossible, and the vote route's own
// multi-option path fails with a constraint violation.
//
// The collection is created by a PLUGIN migration, and a plugin
// migration that has already been recorded does NOT run again: the
// applied versions live in the forum's `plugin_migrations` table. So
// changing the creation step fixes a FRESH install and leaves every
// existing instance with the old index — and the repair would never
// run either, because it would be a new migration step on an instance
// whose `create_polls` step is already recorded.
//
// This repair is therefore IDEMPOTENT and RE-RUNNABLE: it looks at the
// indexes the collection actually carries and replaces the wrong one,
// and it is a separate step so an instance that already ran the
// creation step runs it on the next boot.
//
// The old index is DROPPED rather than left in place: a unique index
// on (poll, user) is not merely useless, it is what makes the
// multi-vote path fail.
func repairVoteIndex(app core.App, physical string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	// The index is written as a CREATE statement, and the name is
	// embedded in it, so the indexes are read and matched by name.
	wanted := "idx_" + physical + "_" + pollVoteIndexName
	legacy := "idx_" + physical + "_poll_user"

	for _, index := range collection.Indexes {
		if !strings.Contains(index, legacy) {
			continue
		}
		collection.RemoveIndex(legacy)
	}

	// The repair is idempotent: an index that is already right is left
	// alone, so the step can run on every boot.
	if !hasIndex(collection, wanted) {
		collection.AddIndex(wanted, true, pollVoteIndexColumns, "")
	}

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "polls: the %q collection's index could not be repaired", physical)
	}

	return nil
}

// hasIndex reports whether a collection carries an index with the name.
//
// The index is written as a CREATE statement, so the name is matched
// inside it. The comparison is case-insensitive, because SQLite's own
// index names are not case sensitive either.
func hasIndex(collection *core.Collection, name string) bool {
	for _, index := range collection.Indexes {
		if strings.Contains(strings.ToLower(index), strings.ToLower(name)) {
			return true
		}
	}

	return false
}
