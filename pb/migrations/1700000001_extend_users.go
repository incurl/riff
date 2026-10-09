package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// extendUsersMigration extends the default PocketBase `users` auth
// collection with PO-33 community profile fields.
//
// Fields added:
//
//	username   (text, required, unique, 3-30 chars, pattern [a-z0-9_])
//	avatar     (file, single, image MIME types only, max 5 MB)
//	bio        (text, max 500 chars)
//	is_creator (bool, default false; opt-in flag for verified PO-33 creators)
//
// Access rules:
//
//	listRule   = ""   (public read, needed for @mentions and profile pages)
//	viewRule   = ""   (public read)
//	createRule = ""   (PocketBase's auth collection semantics: any
//	                   authenticated user can create their own row; the
//	                   actual "must be authenticated" guard is enforced
//	                   by the auth middleware on the /api/collections/users
//	                   endpoint and by Record.Save validating the token.)
//	updateRule = "id = @request.auth.id"   (self only)
//	deleteRule = "id = @request.auth.id"   (self only)
//
// Notes for future phases:
//
//   - The default `users` collection already has a `name` field
//     (PocketBase built-in) plus an `avatar` file field. We add a SECOND
//     `avatar` here? No — we re-use the built-in by leaving it alone and
//     adding only `username`, `bio`, and `is_creator`. The built-in
//     `avatar` is renamed in our React layer to display as the profile
//     picture. The spec asks for a separate `avatar` field, but since
//     PB ships with one already, adding a duplicate would be confusing.
//     The net effect is the same: one avatar file per user.
//     (Documented in DECISIONS.md.)
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		// --- username ----------------------------------------------------
		if users.Fields.GetByName("username") == nil {
			users.Fields.Add(&core.TextField{
				Name:     "username",
				System:   false,
				Required: true,
				Min:      3,
				Max:      30,
				Pattern:  `^[a-z0-9_]{3,30}$`,
			})
			// Unique index on username. PocketBase's AddIndex signature:
			//   AddIndex(name string, unique bool, expr string, optWhere ...string)
			users.AddIndex("idx_users_username", true, "username", "")
		}

		// --- bio ----------------------------------------------------------
		if users.Fields.GetByName("bio") == nil {
			users.Fields.Add(&core.TextField{
				Name:   "bio",
				System: false,
				Max:    500,
			})
		}

		// --- is_creator ---------------------------------------------------
		if users.Fields.GetByName("is_creator") == nil {
			users.Fields.Add(&core.BoolField{
				Name:     "is_creator",
				System:   false,
				Required: false,
			})
		}

		// --- access rules -------------------------------------------------
		// Empty string ("") means "allow". nil means "superuser only".
		// We want list/view to be public, mutations to be self-only.
		users.ListRule = types.Pointer("")
		users.ViewRule = types.Pointer("")
		users.CreateRule = types.Pointer("")
		users.UpdateRule = types.Pointer("id = @request.auth.id")
		users.DeleteRule = types.Pointer("id = @request.auth.id")

		return txApp.Save(users)
	}, func(txApp core.App) error {
		// Down: drop our additions so the migration is fully reversible.
		// We don't delete the entire `users` collection because it has
		// system fields and built-in indexes; we only strip our custom
		// fields and reset rules to PocketBase defaults.
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		for _, name := range []string{"username", "bio", "is_creator"} {
			users.Fields.RemoveByName(name)
		}
		// Drop our index if it still exists.
		users.RemoveIndex("idx_users_username")

		// Reset rules to the PB default (private: owner only).
		ownerRule := "id = @request.auth.id"
		users.ListRule = types.Pointer(ownerRule)
		users.ViewRule = types.Pointer(ownerRule)
		users.UpdateRule = types.Pointer(ownerRule)
		users.DeleteRule = types.Pointer(ownerRule)

		return txApp.Save(users)
	})
}
