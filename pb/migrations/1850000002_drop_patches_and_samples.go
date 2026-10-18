package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// dropPatchesAndSamples removes the `patches` and `samples`
// collections after the data copy in 1850000001 has finished.
// Idempotent: a no-op when either collection is already gone.
//
// The two collections existed while riff was PO-33-only; the
// `attachments` collection (1850000000) plus the data copy
// (1850000001) now subsume both. This migration is the
// destructive final step and is intentionally split out so
// the data copy and the schema drop can be reasoned about
// (and rolled back) independently.
//
// Rollback: this migration does not restore the data (the
// prior migration's rollback deletes the migrated rows).
// Restoring the empty collection shape is a no-op schema
// change — operators who need to re-enable the split shape
// should re-run the original 1700000040 / 1700000041
// migrations by hand.
func init() {
	migrations.Register(func(txApp core.App) error {
		for _, name := range []string{"patches", "samples"} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				// Already gone — idempotent.
				continue
			}
			if err := txApp.Delete(col); err != nil {
				return err
			}
		}
		return nil
	}, func(txApp core.App) error {
		// Rollback: leave the dropped collections gone.
		// Restoring them would mean replaying the original
		// create migrations, which is a no-op for an empty
		// schema. Operators who need them back must
		// re-apply 1700000040 / 1700000041 by hand.
		return nil
	})
}
