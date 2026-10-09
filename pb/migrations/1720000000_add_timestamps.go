package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addTimestamps backfills `created` and `updated` autodate fields onto
// the collections that predate the timestamp convention.
//
// Only `memos` and `notifications` were created with explicit autodate
// fields; `spaces`, `space_members`, `memo_relations`, `reactions`,
// `patches`, and `samples` were not. That broke things twice over:
//
//   - The Connect handlers page through lists with `-created` (newest
//     first). PocketBase rejects an unknown sort field with
//     `invalid sort field "created"`, so those queries failed outright
//     and the services returned empty or internal errors.
//   - The Memos protos expose create_time/update_time on every
//     resource, and the converters read them from GetDateTime("created").
//
// `created` is set on insert and `updated` on insert and update,
// matching the memos collection. Rows written before this migration
// keep an empty value and surface as an unset timestamp; riff is
// pre-release, so no backfill is attempted.
func init() {
	// Collections created before the timestamp convention was applied.
	names := []string{
		"spaces",
		"space_members",
		"memo_relations",
		"reactions",
		"patches",
		"samples",
	}

	migrations.Register(func(txApp core.App) error {
		for _, name := range names {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if col.Fields.GetByName("created") == nil {
				col.Fields.Add(&core.AutodateField{
					Name:     "created",
					OnCreate: true,
				})
			}
			if col.Fields.GetByName("updated") == nil {
				col.Fields.Add(&core.AutodateField{
					Name:     "updated",
					OnCreate: true,
					OnUpdate: true,
				})
			}
			// Index `created` so the newest-first list queries the
			// handlers issue stay cheap as data grows.
			indexName := "idx_" + name + "_created"
			if col.GetIndex(indexName) == "" {
				col.AddIndex(indexName, false, "created", "")
			}
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	}, func(txApp core.App) error {
		for _, name := range names {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			col.Fields.RemoveByName("created")
			col.Fields.RemoveByName("updated")
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	})
}
