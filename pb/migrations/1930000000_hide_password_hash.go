package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// hidePasswordHash marks the `password_hash` column Hidden on the
// `users` and `_superusers` collections.
//
// The column holds a bcrypt digest of the account password. It was
// not marked Hidden when it was first added, which meant the
// Memos-shaped `pb_records_crud` surface exported it to any caller
// that could read a user row: the digest is an offline
// password-cracking target.
//
// Migration 1710000001 (users) and 1790000000 (_superusers) now
// create the column with Hidden:true, so a fresh install never has
// the window. This migration closes it on existing instances.
//
// Idempotency: the migration is a no-op when the field is missing
// (the collection has not been created yet) or already Hidden.
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
			field := col.Fields.GetByName("password_hash")
			if field == nil || field.GetHidden() {
				continue
			}
			field.SetHidden(true)
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
