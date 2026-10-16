package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addAttachmentDeviceFingerprint attaches a
// `device_fingerprint_snapshot` text column to the
// `patches` and `samples` collections. The snapshot
// is the device fingerprint the sketch was uploaded
// from, copied into the attachment row at upload time.
//
// Why a snapshot rather than a live relation to
// `po33_devices`? When a user revokes a device, the
// `device` relation on the attachment is nulled out
// (see pb/hooks/device_revoked.go). The snapshot
// preserves the device's identity on the row itself
// so the audit trail keeps working: an operator can
// see "this sketch was uploaded from the device
// showing EF3A91C2, even though the device is now
// revoked".
//
// The field is OPTIONAL: a non-sketch upload (the
// common case today) leaves it null, and the
// converter in pb/connect/attachment.go treats
// null as "no snapshot". The maximum length is 64
// (the full SHA-256 hex).
//
// Idempotency: the column add is gated on
// Fields.GetByName so a re-run of migrations is
// safe. RunAllMigrations is what re-runs the body,
// and the rest of the riff migrations are written
// to be re-runnable.
func init() {
	migrations.Register(func(txApp core.App) error {
		for _, name := range []string{"patches", "samples"} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if col.Fields.GetByName("device_fingerprint_snapshot") != nil {
				continue
			}
			col.Fields.Add(&core.TextField{
				Name:     "device_fingerprint_snapshot",
				Required: false,
				Max:      64,
			})
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	}, func(txApp core.App) error {
		for _, name := range []string{"patches", "samples"} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if col.Fields.GetByName("device_fingerprint_snapshot") == nil {
				continue
			}
			col.Fields.RemoveByName("device_fingerprint_snapshot")
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	})
}
