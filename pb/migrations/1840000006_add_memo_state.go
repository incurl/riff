package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addMemoStateColumn adds a `state` select to the
// `memos` collection, plus a `hidden_reason` text
// field and a `hidden_by` relation. The state
// column mirrors the proto enum in
// `proto/api/v1/common.proto::State`, which the
// existing `Memo` message has carried since
// before the PO-33 work — the column is
// genuinely net-new; the proto field is the
// long-standing surface the SPA reads.
//
// Values:
//
//	NORMAL    — the existing default. The brief's
//	            "PUBLISHED" is an alias for NORMAL
//	            in the upstream Memos vocabulary.
//	            Every existing memo is backfilled
//	            to NORMAL.
//	ARCHIVED  — the existing archivable state.
//	            Setting it removes the memo from
//	            feed + search but preserves the
//	            row on disk.
//	DRAFT     — net-new for the PO-33 sketch
//	            upload flow. A device upload lands
//	            in DRAFT; the owner invokes
//	            PublishSketch to flip it to NORMAL.
//	HIDDEN    — net-new admin-only side-state.
//	            The memo is preserved on disk but
//	            excluded from feed + search. The
//	            hidden_by relation records the
//	            admin who hid it (so the SPA can
//	            render "hidden by ..."), and
//	            hidden_reason is the human-readable
//	            note (so a future unhide doesn't
//	            lose context).
//
// Why this slice adds a column rather than
// reusing the existing `visibility` enum:
// `visibility` is a horizontal axis (who can
// see it: PRIVATE | PROTECTED | PUBLIC |
// SPACE); `state` is a vertical axis (lifecycle
// stage: DRAFT | NORMAL | ARCHIVED | HIDDEN).
// The two are independent — a sketch can be
// NORMAL + PRIVATE, NORMAL + PUBLIC, DRAFT +
// PRIVATE, etc. The proto already models them
// as two separate fields on `Memo`.
//
// Backfill: every existing row gets
// state="NORMAL". The hook in pb/hooks/memos.go
// already defaults a missing source to "text";
// a parallel default for state keeps the
// behaviour consistent without a second
// defaulting rule on the read path.
//
// Idempotency: the column adds are gated on
// Fields.GetByName so a re-run of migrations is
// safe. RunAllMigrations is what re-runs the
// body, and the rest of the riff migrations are
// written to be re-runnable.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		if memos.Fields.GetByName("state") == nil {
			memos.Fields.Add(&core.SelectField{
				Name:      "state",
				Required:  false,
				Values:    []string{"NORMAL", "ARCHIVED", "DRAFT", "HIDDEN"},
				MaxSelect: 1,
			})
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}
		if memos.Fields.GetByName("hidden_reason") == nil {
			memos.Fields.Add(&core.TextField{
				Name:     "hidden_reason",
				Required: false,
				Max:      1024,
			})
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}
		if memos.Fields.GetByName("hidden_by") == nil {
			memos.Fields.Add(&core.RelationField{
				Name:          "hidden_by",
				Required:      false,
				MaxSelect:     1,
				MinSelect:     0,
				CollectionId:  usersCollectionID(txApp),
				CascadeDelete: false,
			})
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}

		// Backfill: any existing row missing the
		// column reads back as the empty string
		// through PocketBase's GetString; we want
		// "NORMAL" so feed filters keep working.
		// The query is a one-shot scan on a
		// small collection; the cost is
		// irrelevant next to a migration's other
		// I/O. The `state = '' || state = null`
		// filter matches both the pre-migration
		// rows (the column does not exist) and
		// any row that was saved with an explicit
		// empty state.
		rows, err := txApp.FindRecordsByFilter("memos",
			"state = '' || state = null", "", 0, 0, nil)
		if err != nil {
			return err
		}
		for _, r := range rows {
			r.Set("state", "NORMAL")
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
		memos.Fields.RemoveByName("hidden_by")
		memos.Fields.RemoveByName("hidden_reason")
		memos.Fields.RemoveByName("state")
		return txApp.Save(memos)
	})
}

// usersCollectionID returns the row id of the
// `users` collection. The RelationField above
// needs the id, not the name; resolving it
// through the API keeps this migration
// independent of any hard-coded PocketBase
// internal id. On a fresh install the
// collection exists by the time migrations
// run; on a re-run the find is a single SELECT.
func usersCollectionID(txApp core.App) string {
	col, err := txApp.FindCollectionByNameOrId("users")
	if err != nil {
		return ""
	}
	return col.Id
}
