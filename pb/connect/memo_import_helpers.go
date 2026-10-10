// memo_import_helpers.go owns the small adapter helpers
// the import pipeline needs but the rest of the
// package does not.
package connect

import (
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/types"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// typesDateTime wraps a time.Time in the PocketBase
// DateTime struct. The struct's `t` field is
// unexported, so callers have to use this helper
// which goes through the public ParseDateTime API.
func typesDateTime(t time.Time) types.DateTime {
	dt, _ := types.ParseDateTime(t)
	return dt
}

// timestamppbFromTime wraps time.Time in a proto
// Timestamp.
func timestamppbFromTime(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

// uploadAttachmentBytes writes the file bytes to
// PocketBase's filesystem under the canonical key for
// the record. The key layout is the one PB uses for its
// REST file URLs:
//
//	<collectionId>/<recordId>/<filename>
//
// The function is the byte-array analogue of
// uploadFileToPocketBase (see attachment_upload.go);
// the import pipeline uses it because the staged
// archive holds raw bytes, not a filesystem.File.
func uploadAttachmentBytes(app core.App, coll, recID string, data []byte, originalName string) error {
	if originalName == "" {
		originalName = "upload.bin"
	}
	c, err := app.FindCollectionByNameOrId(coll)
	if err != nil {
		return err
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return err
	}
	f, err := filesystem.NewFileFromBytes(data, originalName)
	if err != nil {
		return err
	}
	key := dirForFilenames(c.Id, recID, originalName)
	if err := fsys.UploadFile(f, key); err != nil {
		return err
	}
	return nil
}
