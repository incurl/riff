package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// dropPasswordHash removes the parallel `password_hash` column from
// the `users` and `_superusers` collections.
//
// The column was added by migration 1710000001 because its author
// believed PocketBase's `password` field was hashed with PBKDF2 and
// therefore "not directly comparable from Go". That is not the
// case: PocketBase v0.40 hashes the field with bcrypt
// (core/field_password.go) and exposes
// [core.Record.ValidatePassword] for exactly the comparison the
// Connect sign-in path needs. The column was a duplicate bcrypt
// digest maintained in lockstep with the real one.
//
// # No backfill is needed
//
// The worry would be an account that has a `password_hash` but no
// usable `password` — one that could sign in through the Connect
// path but not through PocketBase's own auth endpoint.
//
// That state is unreachable. PocketBase FORCES the `password` field
// of an auth collection to be Required (core/collection_model.go:
//
//	initPasswordField() -> field.Required = true
//
//), so every auth row has always carried a password hash, and the
// Connect path and the native auth endpoint have always read the
// same value. The duplicate column was redundant from the moment it
// was added.
//
// # Idempotency
//
// A no-op when the column is already gone (a fresh install never
// creates it after this migration) or when the collection does not
// exist yet.
func init() {
	migrations.Register(func(txApp core.App) error {
		for _, name := range []string{"users", core.CollectionNameSuperusers} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				// The collection does not exist yet on a fresh
				// install; its own create migration owns the
				// schema.
				continue
			}
			if col.Fields.GetByName("password_hash") == nil {
				continue
			}
			col.Fields.RemoveByName("password_hash")
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
