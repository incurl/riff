package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createDeviceAuthorizationsCollection backs the
// in-flight pairing flow described in
// docs/design/po33-device-binding.md.
//
// One row per pairing attempt. The user creates the row
// from the SPA (StartPairing) by POSTing to
// /api/po33/pair/start; the row carries a short
// Crockford-base32 user code the device owner types in.
// The device then signs a canonical request (see
// internal/ed25519verify.AuthorizationRequest) and POSTs
// to /api/po33/pair/finish. On a successful signature
// check the row is promoted into a `po33_devices` row
// and deleted. Expired rows are purged by
// pb/hooks/scheduler.go (every minute; the row also
// carries `expires_at` for the device to refuse late
// signatures).
//
// Fields:
//
//	user              (relation → users, single, required,
//	                    indexed) — the riff user the
//	                    device is being bound to.
//	user_code         (text, required, max 12, unique) —
//	                    the Crockford-base32 token the
//	                    user types on the device. The
//	                    unique index makes a second
//	                    in-flight pairing for the same
//	                    user take a different code.
//	authorization_id  (text, required, max 32, unique) —
//	                    a server-issued row id the device
//	                    echoes back. Like `user_code` but
//	                    not human-typed.
//	nonce             (text, required, max 64) — random
//	                    16-byte hex; bound into the
//	                    canonical signing input so a
//	                    captured signature cannot be
//	                    replayed against a different
//	                    authorization row.
//	public_key        (text, required, max 128) — the
//	                    hex Ed25519 public key the
//                    device presented at /pair/finish.
//	                    Stored before the signature is
//	                    checked so a late arrival can be
//	                    validated against the in-flight
//	                    row even if the device first
//	                    reads the QR code and then
//	                    signs the request asynchronously.
//	issued_at         (date, required)
//	expires_at        (date, required, indexed) —
//	                    scheduler scans this index to
//	                    purge stale rows.
//	status            (select, required, default "PENDING";
//	                    values: PENDING, CONSUMED,
//	                    EXPIRED, FAILED) — terminal
//	                    states are set when the device
//	                    finishes (CONSUMED on success,
//	                    FAILED on a bad signature) or
//	                    when the scheduler purges
//	                    (EXPIRED).
//	failure_reason    (text, optional, max 200) — set
//	                    when status=FAILED so the SPA
//	                    can render a precise error.
//	created           (autodate, onCreate)
//
// Access rules: every verb is nil. The Connect handler
// is the sole authority. The device-side /pair/finish
// is a noAuth REST handler that reads the row
// directly; the SPA-side /pair/start is an
// authenticated REST handler that creates a row.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		c := core.NewBaseCollection("device_authorizations")
		c.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		c.Fields.Add(&core.TextField{
			Name:     "user_code",
			Required: true,
			Min:      5,
			Max:      12,
		})
		c.Fields.Add(&core.TextField{
			Name:     "authorization_id",
			Required: true,
			Min:      1,
			Max:      64,
		})
		c.Fields.Add(&core.TextField{
			Name:     "nonce",
			Required: true,
			Max:      64,
		})
		c.Fields.Add(&core.TextField{
			Name:     "public_key",
			Required: false,
			Max:      128,
		})
		c.Fields.Add(&core.DateField{
			Name:     "issued_at",
			Required: true,
		})
		c.Fields.Add(&core.DateField{
			Name:     "expires_at",
			Required: true,
		})
		c.Fields.Add(&core.SelectField{
			Name:      "status",
			Required:  true,
			Values:    []string{"PENDING", "CONSUMED", "EXPIRED", "FAILED"},
			MaxSelect: 1,
		})
		c.Fields.Add(&core.TextField{
			Name: "failure_reason",
			Max:  200,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		c.AddIndex("idx_device_authorizations_user_code", true, "user_code", "")
		c.AddIndex("idx_device_authorizations_auth_id", true, "authorization_id", "")
		c.AddIndex("idx_device_authorizations_expires", false, "expires_at", "")

		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("device_authorizations")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}
