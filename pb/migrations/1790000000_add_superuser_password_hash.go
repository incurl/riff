package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addSuperuserPasswordHash mirrors the password_hash field that
// migration 1710000001 added to the `users` collection, but for
// the built-in `_superusers` collection.
//
// The Connect CreateUser handler provisions a superuser record on
// the bootstrap path (when the instance has no real admin yet) so
// the Memos SPA can run its first-paint installer. The same
// Connect SignIn path that signs in `users` records also needs to
// authenticate the bootstrap admin, and the only portable way to
// verify a password from Go code is to read the bcrypt hash
// directly. PocketBase's internal `password` field is hashed with
// PBKDF2 and not directly comparable, which is why the parallel
// `password_hash` column exists on `users` in the first place.
//
// The field is added idempotently: a fresh install runs the
// migration on bootstrap; an existing instance is skipped if the
// column is already present.
func init() {
	migrations.Register(func(txApp core.App) error {
		superusers, err := txApp.FindCollectionByNameOrId(core.CollectionNameSuperusers)
		if err != nil {
			return err
		}
		if superusers.Fields.GetByName("password_hash") != nil {
			return nil // already added
		}
		superusers.Fields.Add(&core.TextField{
			Name: "password_hash",
			Max:  200,
			// Hidden so the bcrypt digest is never exported; see
			// migration 1710000001 for the same field on `users`.
			Hidden: true,
		})
		return txApp.Save(superusers)
	}, func(txApp core.App) error {
		superusers, err := txApp.FindCollectionByNameOrId(core.CollectionNameSuperusers)
		if err != nil {
			return err
		}
		superusers.Fields.RemoveByName("password_hash")
		return txApp.Save(superusers)
	})
}
