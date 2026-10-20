package hooks

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/motionphoto"
)

// motionMediaLog is the slog logger used by the auto-detect
// path. Pulled out so the tests can substitute a discard
// handler. The default is the package-level slog default.
var motionMediaLog = slog.Default()

// motionMediaStates holds the per-app detection state.
//
// The wait group must be PER APP, not package-level: a
// package-level `sync.WaitGroup` cannot be waited on
// twice concurrently, and two apps tearing down at the
// same time would panic with "sync: WaitGroup is
// reused before previous Wait has returned".
//
// `gate` closes the same hole for a single app: `Add` runs
// per attachment upload and can overlap the teardown wait,
// and `Add` on a wait group that is mid-`Wait` panics the
// process. See memo_links.go for the full rationale; the
// same pattern is used in both files.
type motionMediaState struct {
	workers sync.WaitGroup

	gate          sync.Mutex
	gateAccepting bool
}

// begin accepts a new detection worker iff teardown has
// not started, and returns false once it has. A caller
// that gets false must run its goroutine untracked.
func (s *motionMediaState) begin() bool {
	s.gate.Lock()
	defer s.gate.Unlock()
	if !s.gateAccepting {
		return false
	}
	s.workers.Add(1)
	return true
}

var motionMediaStates = map[core.App]*motionMediaState{}

// motionMediaStatesMu guards motionMediaStates. The map
// is written from the after-create hook and read from
// the teardown hook; two apps running concurrently would
// otherwise race on the map itself.
var motionMediaStatesMu sync.Mutex

// motionMediaStateFor returns the per-app state, creating
// it on first use.
func motionMediaStateFor(app core.App) *motionMediaState {
	motionMediaStatesMu.Lock()
	defer motionMediaStatesMu.Unlock()
	st := motionMediaStates[app]
	if st == nil {
		st = &motionMediaState{gateAccepting: true}
		motionMediaStates[app] = st
	}
	return st
}

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
		//
		// The worker is tracked so the OnTerminate hook
		// can wait for it: an untracked goroutine can
		// still be writing into the data directory when
		// the app terminates, which breaks the cleanup
		// of a test's temp dir.
		st := motionMediaStateFor(e.App)
		tracked := st.begin()
		// Snapshot `e.App` and `e.Record` into locals before
		// launching the goroutine. Reading them *inside* the
		// goroutine is a data race: PocketBase's
		// after-create chain re-assigns `e.App`, `e.Context`
		// and `e.Type` on the shared `RecordEvent` on its way
		// back up the stack
		// (`core.syncRecordEventWithModelEvent`, bound at
		// `core/record_model.go:118`), and that write can land
		// after this handler returns — concurrently with the
		// goroutine's read. The race detector flags exactly this
		// pair under `go test -race ./pb/api/`.
		//
		// The record pointer is captured the same way for the
		// same reason: `syncModelEventWithRecordEvent` writes
		// back into the same event on the way down.
		app, rec := e.App, e.Record
		go func() {
			if tracked {
				defer st.workers.Done()
			}
			defer func() {
				// A panic in the detector must not take
				// the process down; the save already
				// committed and the payload is
				// best-effort.
				if r := recover(); r != nil {
					motionMediaLog.Warn("motion-media: detector panicked",
						"panic", r)
				}
			}()
			detectMotionMedia(app, rec)
		}()
		return nil
	}
	app.OnRecordAfterCreateSuccess("attachments").BindFunc(bind)

	// Wait for the in-flight detectors before the app
	// tears down. Without this a goroutine can recreate a
	// file in the data directory after the cleanup ran,
	// and the cleanup fails with "directory not empty".
	//
	// The wait is bound to `OnBootstrapClear`, not
	// `OnTerminate`: `ClearBootstrap` (which is what a
	// test's cleanup calls) triggers `OnBootstrapClear`
	// but NOT `OnTerminate`, so a wait on the latter
	// would never run in a test — and the test is exactly
	// where the teardown race bites. `OnBootstrapClear`
	// fires on a real shutdown too, right before the
	// data directory is removed.
	app.OnBootstrapClear().BindFunc(func(e *core.BootstrapEvent) error {
		motionMediaStatesMu.Lock()
		st := motionMediaStates[app]
		delete(motionMediaStates, app)
		motionMediaStatesMu.Unlock()
		if st != nil {
			// Close the gate BEFORE waiting, so an upload
			// that lands mid-teardown declines to `Add`
			// rather than panicking on a wait group that is
			// already in `Wait`.
			st.gate.Lock()
			st.gateAccepting = false
			st.gate.Unlock()
			st.workers.Wait()
		}
		return e.Next()
	})
}

// WaitMotionMediaWorkers blocks until every in-flight
// motion-media detection goroutine for `app` has finished.
// The hook fires detection as a detached goroutine so a slow
// disk read does not stall the API response; tests that read
// the row right after `UploadAttachment` finishes need this
// sync to avoid racing against the hook's `rec.Set("motion_media", ...)`
// write under `-race`. Production code does not need to call
// this — `OnBootstrapClear` waits for the workers anyway.
func WaitMotionMediaWorkers(app core.App) {
	motionMediaStatesMu.Lock()
	st := motionMediaStates[app]
	motionMediaStatesMu.Unlock()
	if st != nil {
		st.workers.Wait()
	}
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
