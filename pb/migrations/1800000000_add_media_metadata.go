package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addMediaMetadataField attaches a `media_metadata` JSON text column to the
// `patches` and `samples` collections. The column is populated by the
// AttachmentService.UploadAttachment RPC from the
// `UploadAttachmentSpec.attachment.media_metadata` proto field (which the
// SPA extracts at file-ingest time, before the chunked upload starts).
//
// The column shape is the protojson encoding of the `MediaMetadata` message
// declared in `proto/api/v1/attachment_service.proto`. The converter
// `pb/connect/attachment_motion_media.go` reads it back to populate the
// `Attachment.media_metadata` proto field on the response, so the value
// survives the round-trip without re-extraction.
//
// We pick a Text field (max 8192 bytes) rather than a JSON field so the
// migration stays PocketBase-portable — `Text` round-trips as a plain
// string in every supported DB driver, while `core.JSONField` semantics
// drift between SQLite/Postgres/MySQL.
//
// The migration is idempotent: it skips collections that already have the
// column, and the down-migration is a no-op when the column never landed.
func init() {
	migrations.Register(func(txApp core.App) error {
		for _, name := range []string{"patches", "samples"} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if col.Fields.GetByName("media_metadata") != nil {
				continue
			}
			col.Fields.Add(&core.TextField{
				Name:     "media_metadata",
				Required: false,
				Max:      8192,
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
				continue
			}
			if col.Fields.GetByName("media_metadata") == nil {
				continue
			}
			col.Fields.RemoveByName("media_metadata")
			_ = txApp.Save(col)
		}
		return nil
	})
}