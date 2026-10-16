package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// removeIsCreatorMigration removes the `is_creator` BoolField from the
// `users` auth collection.
//
// The field was added by 1700000001_extend_users.go as a "opt-in flag
// for verified PO-33 creators", but the design has moved: the
// verified-creator flow is now modelled as a separate domain concept
// (a junction between `users` and a future `creator_program` resource)
// rather than a flag on every user row. The flag was never exposed
// on the wire — no proto field, no API surface, no frontend code
// references it — so removing it is a pure schema change with no
// client-visible behaviour change.
//
// Idempotency:
//
//   - Up is a no-op on fresh installs of the current tree because the
//     1700000001 migration no longer adds the field. On existing
//     instances that picked up the field from a prior tree state, up
//     drops it. `Fields.RemoveByName` is a silent no-op when the
//     field is absent (PocketBase v0.40.5, core/fields_list.go:62),
//     so the migration is safe to re-apply.
//
//   - Down re-adds the field with the same shape as the original
//     definition. This restores the schema for any operator rolling
//     back past this point; the rollback is rare in practice because
//     the field is no longer referenced anywhere in the tree.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		// Idempotent: RemoveByName is a no-op when the field is
		// absent, which is the fresh-install case (the updated
		// 1700000001 no longer adds it) and the re-apply case.
		users.Fields.RemoveByName("is_creator")
		return txApp.Save(users)
	}, func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		if users.Fields.GetByName("is_creator") != nil {
			// Already restored; nothing to do.
			return nil
		}
		users.Fields.Add(&core.BoolField{
			Name:     "is_creator",
			System:   false,
			Required: false,
		})
		return txApp.Save(users)
	})
}
