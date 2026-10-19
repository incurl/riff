package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addDevicePendingPullColumn adds a `pending_pull` JSON
// column to the `po33_devices` collection. The column
// holds the most-recent pending sketch push the device
// owner has queued for the device. The full lifecycle is
// documented in docs/design/po33-device-binding.md
// ("Push direction — device-pulls, 2026-10-19 appendix").
//
// Why a column and not a new collection: the column lives
// on the device row because the push is device-shaped (it
// has exactly one pending target per device at a time),
// and a per-row column lets the device-side
// `GET /api/po33/sketches/pending` handler answer the
// poll with a single `FindRecordById`. A new collection
// with a (device, state) index would force the device
// poll to read a list and pick the latest entry, and it
// would need its own cascade on device deletion (which
// the column picks up implicitly).
//
// Why a JSON column and not a oneof sub-table: the
// payload is a `pending_pull` envelope
// { push_id, sketch_memo_id, signed_url, signed_expires_at,
//   version_no, queued_at }. The envelope is small
// (six keys), the device reads it as a single document,
// and the owner-facing REST handler writes it as a
// single transaction. There is no SQL-level query on
// the envelope's fields, so a JSON column carries no
// query-shape penalty.
//
// Idempotency: the column add is gated on
// Fields.GetByName, matching the pattern the other
// 1860xxx / 1840xxx migrations use. A re-run of
// migrations is a no-op.
func init() {
	migrations.Register(func(txApp core.App) error {
		devices, err := txApp.FindCollectionByNameOrId("po33_devices")
		if err != nil {
			return err
		}
		if devices.Fields.GetByName("pending_pull") != nil {
			// Already added by an earlier run; nothing to do.
			return nil
		}
		devices.Fields.Add(&core.JSONField{
			Name:     "pending_pull",
			Required: false,
		})
		return txApp.Save(devices)
	}, func(txApp core.App) error {
		devices, err := txApp.FindCollectionByNameOrId("po33_devices")
		if err != nil {
			return err
		}
		if devices.Fields.GetByName("pending_pull") == nil {
			return nil
		}
		devices.Fields.RemoveByName("pending_pull")
		return txApp.Save(devices)
	})
}
