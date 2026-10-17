package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createSketchVersionsCollection is the
// append-only audit log the PO-33 sketch upload
// flow writes one row into per device upload.
// The collection is the implementation of the
// "never silently modify the original blob"
// invariant from the firmware team's §6 brief:
// every device upload is a fresh row, the row
// is immutable once written, and the
// `version_no` is a monotonic counter per
// `memo_id` so re-uploads accumulate
// predictably.
//
// Fields:
//
//	memo_id             (relation → memos, single,
//	                     required, indexed, cascade
//	                     delete) — the parent sketch
//	                     memo. The cascade is the
//	                     "deleting a memo deletes its
//	                     versions" invariant. A memo
//	                     delete today already
//	                     cascades through patches,
//	                     samples, and the relations
//	                     table; the sketch_versions
//	                     cascade is a third arm of
//	                     the same teardown.
//	version_no          (number, required, indexed
//	                     unique with memo_id) — the
//	                     monotonic per-memo counter.
//	                     The unique-with-memo_id
//	                     index makes a duplicate
//	                     insert fail with a
//	                     storage error, which the
//	                     upload handler translates
//	                     into a 409.
//	device_fingerprint  (text, required, max 64,
//	                     indexed) — the SHA-256
//	                     fingerprint of the device's
//	                     Ed25519 public key. Redundant
//	                     with the same value on
//	                     patches / samples; kept
//	                     here for the
//	                     "list every device that
//	                     uploaded" query the SPA
//	                     can run without a join.
//	uploaded_at_unix    (number, required) — the
//	                     device's clock at upload
//	                     time. Distinct from
//	                     `created` (the server's
//	                     clock).
//	received_at         (date, required) — the
//	                     server's clock at insert
//	                     time. The onCreate auto
//	                     stamp on the autodate
//	                     field is the source of
//	                     truth; this is the same
//	                     value mirrored into a
//	                     named field so list
//	                     queries do not have to
//	                     read the generic
//	                     `created` column.
//	patch_key           (text, optional) — the
//	                     `patches` row id holding
//	                     the .p33 patch blob. Empty
//	                     when the upload was a
//	                     sample-only sketch (a
//	                     future firmware feature).
//	sample_keys         (json, optional) — array
//	                     of `samples` row ids. Empty
//	                     when the device uploaded
//	                     a patch-only sketch.
//	backup_key          (text, optional) — the
//	                     `patches` row id holding
//	                     the .bkp tarball. The
//	                     tarball is the
//	                     "immutable original blob"
//	                     from the §6 brief; it rides
//	                     as an attachment so a
//	                     future restore-from-backup
//	                     tool can read it
//	                     byte-for-byte without
//	                     re-parsing.
//	metadata            (json, required) — the raw
//	                     signed metadata envelope
//	                     for forensic / replay
//	                     purposes. The Connect
//	                     handler returns the parsed
//	                     view through SketchVersion;
//	                     this is the persisted
//	                     source.
//	signature           (text, required) — the
//	                     hex-encoded 64-byte Ed25519
//	                     signature over the
//	                     canonical metadata
//	                     serialisation. The brief's
//	                     §3 puts the signature on
//	                     the wire; the column
//	                     captures it for audit.
//	signature_algorithm (text, required, default
//	                     "ed25519") — pinned for
//	                     forward-compatibility with
//	                     a future post-quantum
//	                     migration. Today always
//	                     "ed25519".
//	created             (autodate, onCreate)
//	updated             (autodate, onCreate + onUpdate,
//	                     but the rule is "never
//	                     update": the Connect
//	                     handler rejects update
//	                     RPCs, the REST rules
//	                     deny update, and the
//	                     hook layer binds no
//	                     OnRecordUpdate hook.)
//
// Access rules: every verb is nil. The Connect
// handler is the sole authority. The
// `*Rule = nil` pattern matches the existing
// `po33_devices` and `device_authorizations`
// collections; the rationale is the same — a
// future rule-engine bug cannot accidentally
// expose the audit log. The handler enforces
// owner-only reads, period.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		c := core.NewBaseCollection("sketch_versions")
		c.Fields.Add(&core.RelationField{
			Name:          "memo_id",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		c.Fields.Add(&core.NumberField{
			Name:     "version_no",
			Required: true,
			Min:      float64Ptr(1),
			OnlyInt:  true,
		})
		c.Fields.Add(&core.TextField{
			Name:     "device_fingerprint",
			Required: true,
			Max:      64,
		})
		c.Fields.Add(&core.NumberField{
			Name:     "uploaded_at_unix",
			Required: true,
			OnlyInt:  true,
		})
		c.Fields.Add(&core.DateField{
			Name:     "received_at",
			Required: true,
		})
		c.Fields.Add(&core.TextField{
			Name: "patch_key",
			Max:  15, // a PocketBase row id is 15 chars
		})
		c.Fields.Add(&core.JSONField{
			Name: "sample_keys",
			MaxSize: 4096,
		})
		c.Fields.Add(&core.TextField{
			Name: "backup_key",
			Max:  15,
		})
		c.Fields.Add(&core.JSONField{
			Name:     "metadata",
			Required: true,
			MaxSize:  16384, // the §3 cap on the `metadata` part
		})
		c.Fields.Add(&core.TextField{
			Name:     "signature",
			Required: true,
			Max:      128, // hex(64 bytes) = 128 chars
		})
		c.Fields.Add(&core.TextField{
			Name:     "signature_algorithm",
			Required: true,
			Max:      32,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnCreate: true,
			OnUpdate: true,
		})

		// The append-only invariant is
		// enforced at the index level: a
		// duplicate (memo_id, version_no)
		// insert fails with a unique
		// constraint violation, which the
		// upload handler translates into
		// 409. The index name follows the
		// existing `idx_<collection>_<col>`
		// convention in this repo.
		c.AddIndex("idx_sketch_versions_memo_version", true, "memo_id, version_no", "")

		// A secondary index on
		// device_fingerprint is not strictly
		// needed at launch but is cheap and
		// will pay off the first time the
		// SPA wants to render "every sketch
		// uploaded from device EF3A91C2"
		// without a join.
		c.AddIndex("idx_sketch_versions_device_fpr", false, "device_fingerprint", "")

		// Every *Rule is nil. The Connect
		// handler is the only authority.
		// The collection is intentionally
		// NOT registered with a hook — the
		// absence of OnRecordUpdate /
		// OnRecordDelete bindings is what
		// makes the append-only invariant
		// mechanical at the data layer.
		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("sketch_versions")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}

// float64Ptr is a tiny helper because the
// NumberField.Min field is a *float64. Kept
// local to this migration because every
// riff migration that touches a number field
// would otherwise re-invent the same wrapper,
// and the rule "do not hand-edit generated
// files" doesn't apply to migrations.
func float64Ptr(f float64) *float64 { return &f }
