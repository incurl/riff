package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addMemoSourceColumn adds a `source` select to the
// `memos` collection. The column is the discriminator
// the SPA, the device, and the feed all use to separate
// "this memo is a sketch uploaded by a PO-33" from
// "this memo is a regular text note".
//
// Values:
//
//	text    — the existing default; covers every memo
//	          written today (no behavioural change).
//	sketch  — a PO-33 device upload. The
//	          `payload.sketch` sub-message is also
//	          populated (proto-side companion change)
//	          so a memo with source=sketch carries the
//	          device metadata (slot count, BPM, model)
//	          inline in the MemoPayload envelope.
//
// Why a separate column and not just `type`? The
// existing `type` enum (note | patch | sample_pack |
// link) is content-shaped: a `note` with sketch upload
// metadata is still a note on the SPA feed. The
// `source` enum is upload-shaped: it says how the
// memo got into the database. The two axes are
// independent, and a sketch memo can have a text
// content body (the user typed notes alongside the
// upload), so they must live on different columns.
//
// Backfill: every existing row gets source="text".
// PocketBase's RunAllMigrations runs the Up
// function at the end of a fresh schema bootstrap
// too, so the backfill is a no-op on a fresh install
// (no rows yet) and a one-shot write on an existing
// install.
//
// Idempotency: the column add is gated on
// Fields.GetByName so a re-run of migrations is
// safe. RunAllMigrations is what re-runs the body,
// and the rest of the riff migrations are written
// to be re-runnable.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		if memos.Fields.GetByName("source") == nil {
			memos.Fields.Add(&core.SelectField{
				Name:     "source",
				Required: false,
				Values:   []string{"text", "sketch"},
			})
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}

		// Backfill: any existing row missing the
		// column reads back as the empty string
		// through PocketBase's GetString; we want
		// "text" so feed filters keep working. The
		// query is a one-shot scan on a small
		// collection; the cost is irrelevant next
		// to a migration's other I/O.
		rows, err := txApp.FindRecordsByFilter("memos",
			"source = '' || source = null", "", 0, 0, nil)
		if err != nil {
			return err
		}
		for _, r := range rows {
			r.Set("source", "text")
			if err := txApp.Save(r); err != nil {
				return err
			}
		}
		return nil
	}, func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		memos.Fields.RemoveByName("source")
		return txApp.Save(memos)
	})
}
