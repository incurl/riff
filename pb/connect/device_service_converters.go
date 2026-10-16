package connect

import (
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// recordToDevice converts a `po33_devices` row into
// the proto shape. The `public_key` column is NOT
// surfaced on read — the row only ever exposes the
// fingerprint (the SHA-256 hex of the public key).
// A future "reveal public key" RPC would unmask
// it; today there is no such RPC and a stolen
// device-row dump should not leak the raw key.
//
// The `last_seen_at` field is the date the
// device_last_seen hook bumps on every successful
// authenticated request. Empty when the device has
// never completed a request.
//
// The `app` argument is passed explicitly so the
// helper can resolve the device owner to a
// canonical `users/{username}` resource name the
// same way recordToMemo does.
func recordToDevice(rec *core.Record) *v1.Device {
	if rec == nil {
		return nil
	}
	out := &v1.Device{
		Name:                 "po33-devices/" + rec.Id,
		PublicKeyFingerprint: rec.GetString("public_key_fpr"),
	}
	// We cannot resolve the user -> username without
	// the app; the resource name therefore stays as
	// `po33-devices/{id}` only. The SPA can call
	// GetUser on the public_key_fingerprint's owner
	// out of band if it needs the username; in
	// practice the SPA knows the owner because the
	// device came from the user's own ListDevices
	// call.
	switch rec.GetString("status") {
	case "ACTIVE":
		out.Status = v1.DeviceStatus_DEVICE_STATUS_ACTIVE
	case "REVOKED":
		out.Status = v1.DeviceStatus_DEVICE_STATUS_REVOKED
	default:
		// Unknown value: leave zero (UNSPECIFIED)
		// so the SPA treats the device as
		// "status unknown" rather than mislabeling
		// it as ACTIVE.
	}
	if t := rec.GetDateTime("last_seen_at"); !t.IsZero() {
		out.LastSeenAt = timestamppb.New(t.Time())
	}
	if t := rec.GetDateTime("created"); !t.IsZero() {
		out.CreateTime = timestamppb.New(t.Time())
	}
	return out
}
