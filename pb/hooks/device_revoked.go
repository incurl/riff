package hooks

import (
	"github.com/pocketbase/pocketbase/core"
)

// registerDeviceRevoked binds an
// OnRecordAfterUpdateSuccess hook on the
// `po33_devices` collection. The hook fires
// only on the ACTIVE -> REVOKED transition
// (the RevokeDevice handler is the only path
// that flips the status; a re-revoke is a
// no-op because the handler refuses to write
// REVOKED over REVOKED).
//
// On the transition the hook is the
// natural place to walk every patches and
// samples row whose live `device` relation
// points at this row, null the relation,
// and copy public_key_fpr into
// `device_fingerprint_snapshot` so the
// audit trail still carries the device's
// identity after the live relation is gone.
//
// Today the patches / samples schema does
// not carry a live `device` relation (it
// only has the snapshot column), so the
// hook is a no-op. The function body is
// kept here so a future Phase 2 migration
// that adds the live relation can fill in
// the cascade body without changing the
// call site. The transition-detection
// logic is unchanged: it only walks rows
// when the live relation is present, and
// the live relation is added by the
// migration, not by this hook.
func registerDeviceRevoked(app core.App) {
	app.OnRecordAfterUpdateSuccess("po33_devices").BindFunc(func(e *core.RecordEvent) error {
		old := e.Record.Original()
		newRec := e.Record
		if old.GetString("status") != "ACTIVE" {
			return e.Next()
		}
		if newRec.GetString("status") != "REVOKED" {
			return e.Next()
		}
		// Intentional no-op: the patches /
		// samples schema does not carry a
		// live `device` relation today.
		// When the Phase 2 migration
		// adds the relation, replace
		// this body with the cascade
		// logic.
		_ = newRec
		return e.Next()
	})
}
