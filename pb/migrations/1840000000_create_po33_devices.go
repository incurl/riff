package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createPo33DevicesCollection backs the PO-33 device
// binding flow described in
// docs/design/po33-device-binding.md.
//
// One row per physical PO-33 sampler that has been
// paired with a riff user. The row is the long-term
// identity the device uses to fetch sketches; the
// per-pairing secret is stored on the
// `device_authorizations` row, not here.
//
// Fields:
//
//	user              (relation → users, single, required,
//	                    indexed) — the riff user that owns
//	                    the device.
//	label             (text, optional, max 80) — the
//	                    human-friendly name the user gave
//	                    the device ("Studio PO-33").
//	                    Editable; not authoritative.
//	public_key        (text, required, max 128) — the
//	                    hex-encoded Ed25519 public key the
//	                    device presented at pairing time.
//	                    The Connect handler validates the
//	                    device's signature against this
//	                    key on every request.
//	public_key_fpr    (text, required, max 64, indexed,
//	                    unique per user) — the SHA-256
//	                    fingerprint of `public_key`. Stored
//	                    separately so list queries do not
//	                    scan a hex blob; the unique index
//	                    enforces "one row per device per
//	                    user" without forcing a fresh
//	                    long-form collision check.
//	status            (select, required, default "ACTIVE";
//	                    values: ACTIVE, REVOKED) — when
//	                    REVOKED, the device can no longer
//	                    fetch sketches. The state change is
//	                    triggered by the user
//	                    (RevokeDevice) or the admin
//	                    (instance-wide rotation).
//	last_seen_at      (date, optional) — bumped on every
//	                    successful authenticated request
//	                    by pb/hooks/device_last_seen.go.
//	                    Helps the SPA show a "last seen"
//	                    affordance.
//	created           (autodate, onCreate)
//	updated           (autodate, onCreate + onUpdate)
//
// Access rules: every verb is nil. The Connect handler
// is the sole authority. PocketBase's record REST API
// is therefore inert on this collection: a request
// that hits /api/collections/po33_devices/* directly
// will be rejected by the rule engine before reaching
// the handler, so a malicious user cannot enumerate or
// mutate another user's devices through the framework
// surface.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		c := core.NewBaseCollection("po33_devices")
		c.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // a user with bound devices is hard-deleted alongside their devices
		})
		c.Fields.Add(&core.TextField{
			Name: "label",
			Max:  80,
		})
		c.Fields.Add(&core.TextField{
			Name:     "public_key",
			Required: true,
			Max:      128,
		})
		c.Fields.Add(&core.TextField{
			Name:     "public_key_fpr",
			Required: true,
			Max:      64,
		})
		c.Fields.Add(&core.SelectField{
			Name:     "status",
			Required: true,
			Values:   []string{"ACTIVE", "REVOKED"},
			MaxSelect: 1,
		})
		c.Fields.Add(&core.DateField{
			Name: "last_seen_at",
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnUpdate: true,
		})

		// One row per (user, fingerprint). Re-pairing
		// the same physical device for the same user
		// refreshes the row in place rather than
		// creating a duplicate; see
		// pb/connect/device_service.go (Bind).
		c.AddIndex("idx_po33_devices_user_fpr", true, "user, public_key_fpr", "")

		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("po33_devices")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}
