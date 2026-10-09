package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createSpacesCollection creates the `spaces` collection.
//
// A Space is an instance-scoped collaboration resource per the Memos
// 0.31 multi-spaces design doc. A memo can be Unassigned or assigned
// to exactly one Space; Space membership lives in the `space_members`
// junction (see 1700000011_create_space_members.go).
//
// Fields:
//
//	uid          (text, required, unique, [a-z0-9-]{3,40}) — URL slug
//	title        (text, required, max 80)
//	description  (text, max 500)
//	icon         (text, max 16) — emoji or short glyph; matches Memos
//	owner        (relation → users, single, required) — the creator.
//	             The design doc says "the creator becomes the first
//	             ADMIN; there is no permanent owner", but we model the
//	             creator explicitly so the React layer can render
//	             "Created by <user>" without joining space_members.
//	             Phase 3 will populate this in OnRecordAfterCreateRequest
//	             from @request.auth.id.
//
// Access rules (Phase 2 baseline; Phase 3 will layer the design-doc
// semantics on top via hooks):
//
//	listRule   = ""   (public read; Phase 3 will narrow this to
//	                   "active members or invitation holders")
//	viewRule   = id = "" || ""   (public; same as list for now)
//	createRule = ""   (any authenticated user; the auth collection
//	                   middleware enforces "must be logged in")
//	updateRule = owner = @request.auth.id  (owner only; Phase 3
//	                   will relax this to "active space ADMIN")
//	deleteRule = owner = @request.auth.id  (owner only; Phase 3 will
//	                   relax this to "active space ADMIN")
//
// Hard-deleting a Space is supposed to atomically delete its directly
// assigned memos, memberships, and owned resources (per the design
// doc). Phase 2 does not implement this; Phase 3 will wire the
// OnRecordAfterDeleteRequest hook on `spaces` to do it. CascadeDelete
// on the `memos.space` relation is deliberately left false because
// PocketBase's CascadeDelete only fires when the relation goes to
// zero, which is the opposite of what we want.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		spaces := core.NewBaseCollection("spaces")

		spaces.Fields.Add(&core.TextField{
			Name:     "uid",
			Required: true,
			Min:      3,
			Max:      40,
			Pattern:  `^[a-z0-9][a-z0-9-]{2,39}$`,
		})
		spaces.Fields.Add(&core.TextField{
			Name:     "title",
			Required: true,
			Max:      80,
		})
		spaces.Fields.Add(&core.TextField{
			Name: "description",
			Max:  500,
		})
		spaces.Fields.Add(&core.TextField{
			Name: "icon",
			Max:  16,
		})
		spaces.Fields.Add(&core.RelationField{
			Name:          "owner",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: false,
		})

		spaces.ListRule = types.Pointer("")
		spaces.ViewRule = types.Pointer("")
		spaces.CreateRule = types.Pointer("")
		spaces.UpdateRule = types.Pointer("owner = @request.auth.id")
		spaces.DeleteRule = types.Pointer("owner = @request.auth.id")

		// Indexes
		spaces.AddIndex("idx_spaces_uid", true, "uid", "")

		return txApp.Save(spaces)
	}, func(txApp core.App) error {
		// Down: drop the entire collection. PocketBase will refuse
		// if it has memos referencing it; Phase 3 owns the
		// dependency-ordering for down migrations.
		spaces, err := txApp.FindCollectionByNameOrId("spaces")
		if err != nil {
			return err
		}
		return txApp.Delete(spaces)
	})
}
