package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// dropRiffOnlyMemoColumns removes the riff-only flat
// columns from the `memos` collection and adds the
// upstream-Memos `pinned` field.
//
// Why this is a separate migration from M186
// (migratePo33ColumnsToSketch): M186 writes the new
// `sketch` column and backfills it. M186 is non-
// destructive; the flat columns still exist on disk
// after M186 runs. This migration is the destructive
// half: it drops the flat columns now that the data
// is in the `sketch` JSON column.
//
// Why the seven PO-33 columns are dropped (and the
// data lives on in `sketch`): the upstream Memos
// `Memo` proto does not have a flat `bpm` / `key` /
// `attribution` / `difficulty` / `license` /
// `sample_source` / `video_url` field. Carrying them
// as columns meant every SPA fetch had to read them
// and translate them into the proto shape by hand.
// Moving them into a single JSON column lets the
// translator decode one document and feed the
// proto-side fields in a single pass.
//
// Why `type` is dropped: the upstream Memos `Memo`
// proto has no `type` field. The `note` / `patch` /
// `sample_pack` / `link` enum was riff-only and
// nothing in the wire path differentiates between
// the four — the feed filter translated it back to
// a `source == "sketch"` predicate anyway. The
// `source` column is also dropped because the
// `sketch` column's presence is the same signal.
//
// Why `hidden_reason` and `hidden_by` are dropped:
// the admin "hide" feature was scoped to riff's
// pre-release preview; no client reads the columns,
// and shipping them as part of the upstream-shaped
// `memos` schema was a footgun. The feature can
// re-introduce them later as a separate migration
// if a SPA consumer asks for them.
//
// Why `pinned` is added: the upstream Memos `Memo`
// proto carries `bool pinned = 11`. Today the SPA
// does not use it; adding the column now means a
// future "pin this memo" feature only needs a
// migration-free schema bump.
//
// Indexes:
//
//	idx_memos_author   — kept (the author index is
//	                      load-bearing for the feed).
//	idx_memos_space    — kept (the space index is
//	                      load-bearing for SPACE-visibility
//	                      filters).
//	idx_memos_created  — kept (the feed sorts by created
//	                      desc).
//	idx_memos_pinned   — added (upstream Memos indexes
//	                      `pinned` for the "pinned first"
//	                      feed sort).
//
// Idempotency: every column drop is gated on
// Fields.GetByName; every column add is gated on
// Fields.GetByName; the index add is gated on
// collection.Indexes iteration. A re-run of
// migrations is a no-op.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		// Add `pinned` first. The column add is
		// non-destructive and lives in this
		// migration so a fresh install sees a
		// single `memos` schema (pinned present,
		// flat PO-33 columns absent).
		if memos.Fields.GetByName("pinned") == nil {
			memos.Fields.Add(&core.BoolField{
				Name: "pinned",
			})
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}

		// Add the upstream Memos `pinned` index
		// when absent. The Indexes slice is a list
		// of SQL CREATE INDEX strings, not a
		// structured column list, so the presence
		// check uses the collection's GetIndex
		// helper (which parses the SQL name out of
		// each string).
		if memos.GetIndex("idx_memos_pinned") == "" {
			memos.AddIndex("idx_memos_pinned", false, "pinned", "")
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}

		// Drop the riff-only flat columns. Each
		// drop is gated on Fields.GetByName so a
		// re-run is a no-op and so a fresh install
		// that somehow never had the columns (e.g.
		// a downstream fork that branched before
		// 1700000020) is also a no-op.
		toDrop := []string{
			"type",
			"bpm",
			"key",
			"sample_source",
			"difficulty",
			"license",
			"attribution",
			"video_url",
			"source",
			"hidden_reason",
			"hidden_by",
		}
		dropped := false
		for _, name := range toDrop {
			if memos.Fields.GetByName(name) != nil {
				memos.Fields.RemoveByName(name)
				dropped = true
			}
		}
		if dropped {
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}

		return nil
	}, func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		// Down migration: re-add the riff-only
		// columns so a rollback leaves a
		// schema-equivalent state. The data
		// itself cannot be recovered from the
		// `sketch` JSON column without a custom
		// pass; the down migration is a best-
		// effort schema rollback.
		if memos.Fields.GetByName("type") == nil {
			memos.Fields.Add(&core.SelectField{
				Name:     "type",
				Required: true,
				Values:   []string{"note", "patch", "sample_pack", "link"},
			})
		}
		if memos.Fields.GetByName("bpm") == nil {
			memos.Fields.Add(&core.NumberField{Name: "bpm", OnlyInt: true})
		}
		if memos.Fields.GetByName("key") == nil {
			memos.Fields.Add(&core.TextField{Name: "key", Max: 16})
		}
		if memos.Fields.GetByName("sample_source") == nil {
			memos.Fields.Add(&core.TextField{Name: "sample_source", Max: 80})
		}
		if memos.Fields.GetByName("difficulty") == nil {
			memos.Fields.Add(&core.SelectField{
				Name:   "difficulty",
				Values: []string{"beginner", "intermediate", "advanced"},
			})
		}
		if memos.Fields.GetByName("license") == nil {
			memos.Fields.Add(&core.TextField{Name: "license", Max: 80})
		}
		if memos.Fields.GetByName("attribution") == nil {
			memos.Fields.Add(&core.TextField{Name: "attribution", Max: 200})
		}
		if memos.Fields.GetByName("video_url") == nil {
			memos.Fields.Add(&core.URLField{Name: "video_url"})
		}
		if memos.Fields.GetByName("source") == nil {
			memos.Fields.Add(&core.SelectField{
				Name:   "source",
				Values: []string{"text", "sketch"},
			})
		}
		if memos.Fields.GetByName("hidden_reason") == nil {
			memos.Fields.Add(&core.TextField{Name: "hidden_reason", Max: 1024})
		}
		if memos.Fields.GetByName("hidden_by") == nil {
			memos.Fields.Add(&core.RelationField{
				Name:          "hidden_by",
				MaxSelect:     1,
				MinSelect:     0,
				CollectionId:  usersCollectionID(txApp),
				CascadeDelete: false,
			})
		}
		memos.Fields.RemoveByName("pinned")
		return txApp.Save(memos)
	})
}
