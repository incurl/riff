package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addRemoteURLColumns adds an optional text column `remote_url` to
// the `patches` and `samples` collections. The column stores the
// public URL of the file when it has been mirrored to a remote
// storage backend (S3, R2, MinIO) by the storage bridge hook in
// pb/hooks/storage_bridge.go. When the column is empty, the
// PocketBase local file (the `file` or `files` FileField) is the
// canonical source of truth.
//
// This migration is idempotent: it skips collections that already
// have the column, and it leaves the column empty on existing rows
// so that the bridge hook can lazily populate it on the next save.
func init() {
	migrations.Register(func(txApp core.App) error {
		for _, name := range []string{"patches", "samples"} {
			coll, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if coll.Fields.GetByName("remote_url") != nil {
				// Column already present; nothing to do.
				continue
			}
			coll.Fields.Add(&core.TextField{
				Name:     "remote_url",
				Required: false,
				Max:      2048,
			})
			if err := txApp.Save(coll); err != nil {
				return err
			}
		}
		return nil
	}, func(txApp core.App) error {
		// Down-migration: drop the column from both collections.
		// We swallow the "field not found" error because the
		// column may never have been added.
		for _, name := range []string{"patches", "samples"} {
			coll, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}
			if coll.Fields.GetByName("remote_url") == nil {
				continue
			}
			coll.Fields.RemoveByName("remote_url")
			_ = txApp.Save(coll)
		}
		return nil
	})
}
