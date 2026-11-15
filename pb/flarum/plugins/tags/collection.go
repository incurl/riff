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
