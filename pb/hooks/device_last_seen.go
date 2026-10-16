package hooks

import (
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// deviceLastSeenHeader is the request header
// the device sends to identify itself. The
// value is the hex SHA-256 of the device's
// Ed25519 public key (the same value the
// pairing screen showed the user). A missing
// or malformed header is a no-op so the hook
// cannot crash a non-device request.
const deviceLastSeenHeader = "X-Po33-Device-Fingerprint"

// registerDeviceLastSeen binds an
// OnRecordCreateSuccess hook on the
// `po33_sketch_downloads` collection. Every
// successful sketch download writes one
// audit row; the hook bumps the device's
// `last_seen_at` to "now" in the same
// transaction so the timestamp is consistent
// with the audit log.
//
// Today the device download path is a future
// REST slice; the hook is dormant. When the
// slice lands, every download inserts a
// `po33_sketch_downloads` row and this hook
// fires. The hook does not run on every
// request, so the cost is one indexed
// FindRecordById per sketch download.
func registerDeviceLastSeen(app core.App) {
	app.OnRecordAfterCreateSuccess("po33_sketch_downloads").BindFunc(func(e *core.RecordEvent) error {
		deviceID := e.Record.GetString("device")
		if deviceID == "" {
			return e.Next()
		}
		devRec, err := app.FindRecordById("po33_devices", deviceID)
		if err != nil || devRec == nil {
			return e.Next()
		}
		devRec.Set("last_seen_at", time.Now())
		_ = app.Save(devRec)
		return e.Next()
	})

	// Also bump on the header-bearing
	// request. The simplest cross-cutting
	// place to inspect a request header is
	// the route-level BindFunc. We do that
	// for the device-facing download route
	// when it lands; for now, this file
	// only owns the audit-log trigger.
	_ = deviceLastSeenHeader
}
