package hooks

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/motionphoto"
)

// motionMediaLog is the slog logger used by the auto-detect
// path. Pulled out so the tests can substitute a discard
// handler. The default is the package-level slog default.
var motionMediaLog = slog.Default()

// motionMediaMaxFileSize caps the JPEG blob we'll read for
// detection. The unified `attachments` collection's
// `blob` field is the only place user files land, and
// the `motion_media` column is meaningless for non-JPEG
// entries, so a 16 MB ceiling here is comfortably above
// the largest possible JPEG we'll encounter. Past that,
// a file is unlikely to be a valid Motion Photo and
// we'd rather skip it than risk OOMing on a malicious
// upload.
const motionMediaMaxFileSize = 16 * 1024 * 1024

// motionMediaSkipExtensions is the set of filename
// extensions we skip without any disk read. A well-
// meaning client might label a JPEG with the ".heic"
// extension (Samsung's container) or the ".png"
// extension, and the detector's regex would still
// light up if the JPEG SOI marker were present
// inside; rather than risk false positives, we skip
// any upload whose name has a non-JPEG extension.
var motionMediaSkipExtensions = map[string]struct{}{
	".heic": {}, ".heif": {}, ".png": {}, ".gif": {},
	".webp": {}, ".bmp": {}, ".tiff": {}, ".tif": {},
	".wav": {}, ".mp3": {}, ".aac": {}, ".flac": {},
	".ogg": {}, ".m4a": {}, ".zip": {}, ".pdf": {},
}

// registerMotionMedia subscribes the JPEG-Motion-Photo
// detector to the unified `attachments` collection's
// after-create hook. Fires only on create, not on
// update: the blob is immutable from riff's perspective
// once written, and re-running detection on every save
// would waste a JPEG read for rows whose file did not
// change.
//
// The hook is bound with OnRecordAfterCreateSuccess (not
// the request hooks) so a save that fails validation
// never runs the detector — we only spend CPU on
// records that actually committed.
func registerMotionMedia(app core.App) {
	bind := func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		// Detached goroutine so a slow disk read does
		// not stall the API response. Failures are
		// logged and dropped; the memo save is the
		// primary path, this is best-effort.
		go detectMotionMedia(e.App, e.Record)
		return nil
	}
	app.OnRecordAfterCreateSuccess("attachments").BindFunc(bind)
}

// detectMotionMedia reads the file on the record,
// runs motionphoto.DetectJPEG, and writes the JSON
// payload to the `motion_media` column. The detector
// is also given a reader-based variant in
// motionphoto.DetectJPEGReader that we don't use
// here because the file fits comfortably in memory
// (16 MB cap above the field's own MaxSize).
func detectMotionMedia(app core.App, rec *core.Record) {
	if rec == nil {
		return
	}
	filename := rec.GetString("filename")
	if filename == "" {
		return
	}
	if !shouldInspectForMotionPhoto(filename) {
		return
	}
	// Compose the file key the PocketBase filesystem
	// understands: <collectionId>/<recordId>/<filename>.
	key := filepath.ToSlash(filepath.Join(rec.Collection().Id, rec.Id, filename))
	fsys, err := app.NewFilesystem()
	if err != nil {
		motionMediaLog.Warn("motion-media: cannot open filesystem", "err", err.Error())
		return
	}
	reader, err := fsys.GetReader(key)
	if err != nil {
		motionMediaLog.Warn("motion-media: cannot open file", "key", key, "err", err.Error())
		return
	}
	defer func() { _ = reader.Close() }()
	// Bounded read: don't trust the file to be as small
	// as the field's MaxSize claims. io.LimitReader
	// caps the allocation; if the file is bigger, the
	// excess is ignored and we treat the file as not-a-
	// motion-photo.
	blob, err := io.ReadAll(io.LimitReader(reader, motionMediaMaxFileSize))
	if err != nil {
		motionMediaLog.Warn("motion-media: read error", "key", key, "err", err.Error())
		return
	}
	detection := motionphoto.DetectJPEG(blob)
	if detection == nil {
		// Not a motion photo. The column stays nil
		// and the proto's motion_media is unset on
		// the response. Nothing to write.
		return
	}
	// Compose the JSON payload matching the proto's
	// MotionMedia shape. `family` is hardcoded to
	// ANDROID_MOTION_PHOTO because the only Motion
	// Photo family the current detector recognises
	// is the Google GCamera one. A future wave can
	// add iOS Live Photo detection and switch on
	// `family` here.
	payload := map[string]any{
		"family":                    "ANDROID_MOTION_PHOTO",
		"role":                      "CONTAINER",
		"presentation_timestamp_us": detection.PresentationTimestampUs,
		"has_embedded_video":        true,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		motionMediaLog.Warn("motion-media: marshal error", "err", err.Error())
		return
	}
	rec.Set("motion_media", string(encoded))
	if err := app.Save(rec); err != nil {
		motionMediaLog.Warn("motion-media: save error", "record", rec.Id, "err", err.Error())
	}
}

// shouldInspectForMotionPhoto is a quick pre-filter
// that skips non-image uploads before any disk read.
// It looks at the filename's extension only; a
// JPEG with a misleading extension will still be
// passed to the detector (which is the right call
// — the SOI marker check is the source of truth).
func shouldInspectForMotionPhoto(filename string) bool {
	if filename == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return true // unknown extension: let the detector decide
	}
	if _, skip := motionMediaSkipExtensions[ext]; skip {
		return false
	}
	return true
}
