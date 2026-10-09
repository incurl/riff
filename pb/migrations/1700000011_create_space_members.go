package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createSpaceMembersCollection creates the `space_members` junction.
//
// Per the Memos 0.31 multi-spaces design doc, the Space-user
// relationship is a single row per pair with `status` (INVITED or
// ACTIVE) and `role` (ADMIN or USER). The same row serves as both
// invitation and membership; accept atomically flips status from
// INVITED to ACTIVE.
//
// Fields:
//
//	space   (relation → spaces, single, required)
//	user    (relation → users,  single, required)
//	status  (select, required, no default; values: INVITED, ACTIVE)
//	role    (select, required, no default; values: ADMIN, USER)
//
// The design doc explicitly says: "The initial version does not
// record inviter, invitation, Space, or membership timestamps; they
// can be added when a concrete attribution, expiration, audit, or
// ordering requirement exists." So we do not add created/updated
// fields on this collection.
//
// Access rules (Phase 2 baseline; Phase 3 will tighten):
//
//	listRule   = user = @request.auth.id   (you can see your own rows;
//	                                        phase 3 will add "or active
//	                                        member of the same space")
//	viewRule   = user = @request.auth.id
//	createRule = ""   (any authenticated user; phase 3 will restrict
//	                   to "active ADMIN of the space" via a hook)
//	updateRule = user = @request.auth.id   (a user can update their
//	                                        own invitation — e.g.
//	                                        accept/decline — which
//	                                        phase 3 will allow only on
//	                                        INVITED rows via a hook)
//	deleteRule = user = @request.auth.id
//
// A unique composite index on (space, user) enforces the
// "one current relationship slot" invariant from the design doc.
func init() {
	migrations.Register(func(txApp core.App) error {
		spaces, err := txApp.FindCollectionByNameOrId("spaces")
		if err != nil {
			return err
		}
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		sm := core.NewBaseCollection("space_members")

		sm.Fields.Add(&core.RelationField{
			Name:          "space",
			CollectionId:  spaces.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting a space removes its memberships
		})
		sm.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: false, // a user can be hard-deleted only after leaving all spaces
		})
		sm.Fields.Add(&core.SelectField{
			Name:     "status",
			Required: true,
			Values:   []string{"INVITED", "ACTIVE"},
		})
		sm.Fields.Add(&core.SelectField{
			Name:     "role",
			Required: true,
			Values:   []string{"ADMIN", "USER"},
		})

		sm.ListRule = types.Pointer("user = @request.auth.id")
		sm.ViewRule = types.Pointer("user = @request.auth.id")
		sm.CreateRule = types.Pointer("")
		sm.UpdateRule = types.Pointer("user = @request.auth.id")
		sm.DeleteRule = types.Pointer("user = @request.auth.id")

		// Indexes
		sm.AddIndex("idx_space_members_unique", true, "space, user", "")
		sm.AddIndex("idx_space_members_user", false, "user", "")

		return txApp.Save(sm)
	}, func(txApp core.App) error {
		sm, err := txApp.FindCollectionByNameOrId("space_members")
		if err != nil {
			return err
		}
		return txApp.Delete(sm)
	})
}
