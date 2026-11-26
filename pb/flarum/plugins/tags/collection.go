package tags

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createTagsCollection creates the plugin's tag collection.
//
// The collection is idempotent: it is created only when it is missing,
// so re-running the migration is safe.
//
// The access rules are deliberately permissive on read and closed on
// write: the frontend lists the tags on the index page, and every
// mutation goes through the plugin's handlers, which enforce the
// permission service.
func createTagsCollection(app core.App, physical string) error {
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
		Name:     "slug",
		Required: true,
		Max:      255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "description",
		Max:  700,
	})
	// `parent` is a TEXT id, not a RelationField, and the reason is narrower
	// than "PocketBase cannot self-reference" — it can, just not here:
	//
	//   - a RelationField's `CollectionId` is validated against the
	//     database when the collection is SAVED, so a collection that is
	//     being created cannot name ITSELF. Measured: the one-shot create
	//     fails with `fields: (2: (collectionId: The relation collection
	//     doesn't exist..).).`, while create-then-add-the-field works;
	//   - this collection is created by one idempotent `app.Save` (below),
	//     and turning the column into a relation on an EXISTING instance
	//     would be a second schema migration over live rows;
	//   - the invariants that actually matter are POLICY, not existence:
	//     the parent must be a ROOT tag, a tag cannot be its own parent,
	//     and `parent`/`position`/`is_primary` must agree. `resolveTagParent`
	//     enforces all three; a relation would only cover "the id exists"
	//     (measured: PocketBase accepts a self-parent).
	//
	// The cost is that the database does not clean up after a DELETE, which
	// is what a relation's `RelationField` does for free (measured: deleting
	// a parent nulls the child's relation). That half is paid in Go —
	// `detachTagChildren` unplaces a deleted tag's children, because a
	// dangling parent makes a child belong to no parent AND keep out of the
	// root list, so the admin page would hide it while it still exists.
	collection.Fields.Add(&core.TextField{
		Name: "parent",
		Max:  15,
	})
	collection.Fields.Add(&core.TextField{
		Name: "color",
		Max:  255,
	})
	collection.Fields.Add(&core.TextField{
		Name: "icon",
		Max:  255,
	})
	collection.Fields.Add(&core.NumberField{
		Name: "position",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "discussion_count",
	})
	collection.Fields.Add(&core.TextField{
		Name: "default_sort",
		Max:  50,
	})
	collection.Fields.Add(&core.DateField{
		Name: "last_posted_at",
	})
	collection.Fields.Add(&core.TextField{
		Name: "last_posted_discussion",
		Max:  15,
	})
	collection.Fields.Add(&core.TextField{
		Name: "last_posted_user",
		Max:  15,
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_hidden",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_primary",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "is_restricted",
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

	collection.AddIndex("idx_"+physical+"_slug", true, "slug", "")
	collection.AddIndex("idx_"+physical+"_parent", false, "parent", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "tags: could not create the %q collection", physical)
	}
	return nil
}
