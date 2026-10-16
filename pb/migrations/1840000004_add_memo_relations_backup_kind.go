package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addMemoRelationsBackupKind extends the
// `memo_relations.type` select to allow a new value:
// `BACKUP_FILE`. The new value is the link between a
// sketch memo and the .bkp file the device uploaded
// (a sketch is a two-attachment payload: a patches
// row for the .wav side and a samples row for the
// .wav side; a "backup" .bkp is optional and rides
// as a second relation kind so the SPA can render
// it on the memo card without changing the
// MemoRelation_Type enum's wire shape).
//
// PocketBase stores the `Values` list as a column
// comment, not a real CHECK constraint, so extending
// the slice in place and saving the collection is
// enough — existing rows keep their stored value,
// and a future insert with type="BACKUP_FILE" passes
// ValidateValue because the slice now contains it.
//
// Idempotency: we check the slice for BACKUP_FILE
// before mutating so a re-run of migrations is safe.
// RunAllMigrations is what re-runs the body, and the
// rest of the riff migrations are written to be
// re-runnable.
func init() {
	migrations.Register(func(txApp core.App) error {
		rel, err := txApp.FindCollectionByNameOrId("memo_relations")
		if err != nil {
			return err
		}
		t := rel.Fields.GetByName("type")
		if t == nil {
			return nil
		}
		sel, ok := t.(*core.SelectField)
		if !ok {
			return nil
		}
		for _, v := range sel.Values {
			if v == "BACKUP_FILE" {
				return nil
			}
		}
		sel.Values = append(sel.Values, "BACKUP_FILE")
		return txApp.Save(rel)
	}, func(txApp core.App) error {
		rel, err := txApp.FindCollectionByNameOrId("memo_relations")
		if err != nil {
			return err
		}
		t := rel.Fields.GetByName("type")
		if t == nil {
			return nil
		}
		sel, ok := t.(*core.SelectField)
		if !ok {
			return nil
		}
		out := sel.Values[:0]
		for _, v := range sel.Values {
			if v != "BACKUP_FILE" {
				out = append(out, v)
			}
		}
		sel.Values = out
		return txApp.Save(rel)
	})
}
