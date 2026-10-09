package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addMotionMediaField attaches a `motion_media` JSON column to the
// `patches` and `samples` collections. The field is populated by
// the motion-photo detection hook in `pb/hooks/motion_media.go`
// after a record is created; the converter in
// `pb/connect/attachment.go` reads it back to populate the
// `Attachment.motion_media` proto field.
//
// The column shape is a JSON object with the same fields the
// `MotionMedia` proto carries:
//
//	{
//	  "family": "ANDROID_MOTION_PHOTO",
//	  "role":   "PHOTO_AND_VIDEO",
//	  "group_id": "...",
//	  "presentation_timestamp_us": 1234567,
//	  "has_embedded_video": true
//	}
//
// The field is OPTIONAL: only JPEGs that are recognised
// Motion Photos get a value. WAVs and other non-photo
// uploads keep the field null and the proto's `motion_media`
// is unset on the response.
//
// We pick a Text field (max 8192 bytes) rather than a JSON
// field so the migration stays PocketBase-portable —
// `Text` round-trips as a plain string in every supported
// DB driver, while `core.JSONField` semantics drift between
// SQLite/Postgres/MySQL.
func init() {
	migrations.Register(func(txApp core.App) error {
		for _, name := range []string{"patches", "samples"} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if col.Fields.GetByName("motion_media") != nil {
				continue
			}
			col.Fields.Add(&core.TextField{
				Name:     "motion_media",
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
				return err
			}
			if col.Fields.GetByName("motion_media") == nil {
				continue
			}
			col.Fields.RemoveByName("motion_media")
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	})
}
