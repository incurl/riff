package migrations

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// migratePo33ColumnsToSketch adds a single `sketch` JSON
// column to the `memos` collection, absorbing the
// seven PO-33 flat columns (`bpm`, `key`, `sample_source`,
// `difficulty`, `license`, `attribution`, `video_url`)
// and the existing `payload.sketch` sub-object.
//
// Why a JSON column and not a new set of PO-33 columns:
// upstream Memos keeps PO-33 metadata out of the SQL
// schema; riff is the deviant here. Aligning with
// upstream means the wire shape the SPA sees matches
// what the upstream React client expects, and the
// `Memo.sketch` proto field already exists to carry
// the data. PocketBase's JSON column round-trips
// arbitrary shapes, and the markdown-driven columns
// (`bpm`, `key`) are written and read in a single
// translation step (the save hook + the proto
// translator), so the loss of SQL-level range filters
// is acceptable for the small per-memo dataset.
//
// Why two migrations (M186 + M187): M186 writes the
// new `sketch` column and backfills it. M187 drops
// the seven flat columns. Splitting means an install
// that crashes between migrations is recoverable:
// the columns still exist on disk, the new column is
// already populated, and a re-run of M186 is a no-op.
//
// Backfill:
//
//	For every existing memo:
//	  1. read the seven flat PO-33 columns
//	  2. read the existing `payload.sketch` sub-object
//	  3. merge into a single JSON object stored in `sketch`
//	  4. leave the flat columns in place (M187 drops them)
//
// `source` is NOT touched here; M187 drops it. The
// save hook in pb/hooks/memos.go will derive the
// "sketch" vs "text" discriminator from the presence
// of the `sketch` column going forward.
//
// Idempotency: the column add is gated on
// Fields.GetByName; the per-row backfill is gated on
// "sketch column is empty". A re-run of migrations is
// a no-op.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		if memos.Fields.GetByName("sketch") == nil {
			memos.Fields.Add(&core.JSONField{
				Name:     "sketch",
				Required: false,
			})
			if err := txApp.Save(memos); err != nil {
				return err
			}
		}

		rows, err := txApp.FindRecordsByFilter("memos",
			"", "", 0, 0, nil)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if existing := r.Get("sketch"); existing != nil {
				// Skip rows that already have a
				// non-null `sketch` column. The
				// migration is run-once, but a
				// re-run must not overwrite a
				// newer value with a stale
				// backfill. The presence of the
				// column on the row is the
				// canonical "already migrated"
				// signal because the new
				// PO-33 upload path sets it on
				// every save.
				continue
			}
			merged := mergePo33IntoSketch(r)
			if merged == nil {
				continue
			}
			r.Set("sketch", json.RawMessage(*merged))
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
		memos.Fields.RemoveByName("sketch")
		return txApp.Save(memos)
	})
}

// mergePo33IntoSketch pulls the seven flat PO-33
// columns and the existing `payload.sketch` sub-object
// off the row and merges them into a single JSON
// object the new `sketch` column will hold.
//
// Returns nil when there is nothing to write
// (no flat-column data, no existing payload.sketch).
// A nil return tells the caller to skip the save —
// writing a `null` JSON value into a `JSONField`
// column triggers PocketBase's "field cannot be null"
// check on some drivers.
//
// The merge order is:
//
//	payload.sketch sub-object first (preserves the
//	  device fields the upload handler wrote), then
//	  the seven flat columns overwrite matching keys
//	  (the user-authored metadata is the source of
//	  truth for those keys when both are set).
func mergePo33IntoSketch(r *core.Record) *[]byte {
	out := map[string]any{}

	// Existing `payload.sketch` sub-object.
	if raw := r.Get("payload"); raw != nil {
		var payload struct {
			Sketch map[string]any `json:"sketch"`
		}
		switch v := raw.(type) {
		case []byte:
			if len(v) > 0 {
				_ = json.Unmarshal(v, &payload)
			}
		default:
			b, err := json.Marshal(v)
			if err == nil {
				_ = json.Unmarshal(b, &payload)
			}
		}
		for k, val := range payload.Sketch {
			out[k] = val
		}
	}

	// User-authored PO-33 flat columns. Each is
	// optional; the column read returns the zero
	// value when the row was created before the
	// column existed (only `bpm` was a column at
	// memo creation; the rest were added in
	// 1840xxx migrations).
	wrote := false
	if v := r.GetString("key"); v != "" {
		out["key"] = v
		wrote = true
	}
	if v := r.GetString("sample_source"); v != "" {
		out["sample_source"] = v
		wrote = true
	}
	if v := r.GetString("difficulty"); v != "" {
		out["difficulty"] = v
		wrote = true
	}
	if v := r.GetString("license"); v != "" {
		out["license"] = v
		wrote = true
	}
	if v := r.GetString("attribution"); v != "" {
		out["attribution"] = v
		wrote = true
	}
	if v := r.GetString("video_url"); v != "" {
		out["video_url"] = v
		wrote = true
	}
	// `bpm` is a NumberField, so we read it via GetFloat.
	if bpm := r.GetFloat("bpm"); bpm != 0 {
		out["bpm"] = int32(bpm)
		wrote = true
	}

	if !wrote && len(out) == 0 {
		return nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return &encoded
}
