package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addAttachmentCreatorColumn records the uploader of an attachment
// row even when the spec did not name a `memo` — i.e. an "orphan"
// upload during a brand-new memo edit. The bind pass in
// `pb/connect/memo_bind_attachments.go` uses the column to refuse
// a cross-user rebind (without it, anyone who happened to learn
// an attachment id could re-bind the row onto their own memo,
// silently stealing the bytes).
//
// The column is null when the upload filled the row via the
// classic path (spec.attachment.memo set) — the original parent
// memo's author is already the authoritative visibility owner via
// the `memo.author = …` rule. The column is non-null when the
// upload lands without a memo; the bind pass promotes `memo` from
// null to a real id and the column stays as the uploader fingerprint.
//
// Idempotency: the migration is a no-op when the column already
// exists (the prior install ran it), and it skips entirely on a
// fresh install where the `attachments` collection has not been
// created yet (the create migration owns the schema).
func init() {
	migrations.Register(func(txApp core.App) error {
		col, err := txApp.FindCollectionByNameOrId("attachments")
		if err != nil {
			return nil
		}
		if col.Fields.GetByName("creator") != nil {
			return nil
		}
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		col.Fields.Add(&core.RelationField{
			Name:         "creator",
			CollectionId: users.Id,
			Required:     false,
			MaxSelect:    1,
			MinSelect:    0,
			// No cascade: orphan uploads outlive a deleted user (the
			// row gets garbage collected by `attachments.memo` cascade
			// once it is bound).
		})
		return txApp.Save(col)
	}, nil)
}
