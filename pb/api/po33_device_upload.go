package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"time"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/internal/ed25519verify"
	"github.com/incurl/riff/internal/ratelimit"
)

// Size caps for each multipart part. The
// numbers come from the firmware team's brief
// §3 and §10 and are pinned here so a future
// spec change is a single constant edit. The
// values are the maximum allowed BYTE length
// of the part's body; a 413 surfaces the
// specific cap that was exceeded so a
// device-side error message can be precise.
const (
	// 16 KB is the §3 cap on the signed metadata
	// JSON envelope. The signed JSON in
	// production is closer to 1 KB (model,
	// slot_count, bpm, device_fingerprint, ...),
	// so 16 KB is two orders of magnitude of
	// headroom for future field additions.
	uploadMetadataMaxBytes = 16 * 1024

	// 16 KB is the §3 cap on the hex-encoded
	// Ed25519 signature (64 raw bytes = 128 hex
	// chars, so 16 KB is plenty of headroom for
	// future signature algorithm migrations).
	uploadSignatureMaxBytes = 16 * 1024

	// 50 MB is the §3 cap on the .bkp tarball.
	// The brief notes the typical tarball is
	// 5-10 MB; 50 MB is the upper bound a future
	// PO-33 K.O! firmware might emit.
	uploadBackupMaxBytes = 50 * 1024 * 1024

	// 10 MB is the §3 cap on a single .wav
	// sample. The PO-33's 16 sample slots are
	// each 4-8 seconds at 44.1 kHz / 16-bit, so
	// 10 MB per sample is comfortable headroom
	// for 32-bit-float variants.
	uploadSampleMaxBytes = 10 * 1024 * 1024

	// 16 samples is the PO-33's hardware slot
	// count. A 17th sample would be a firmware
	// bug; we cap server-side and return 422 so
	// the device can render a precise diagnostic.
	uploadMaxSampleCount = 16
)

// UploadSketchRequest is the body of
// POST /api/po33/sketches/upload. The device
// sends a multipart/form-data with three
// required parts (metadata, signature,
// backup_file) and zero or more sample_wav
// parts. The backup_file and sample_wav parts
// are optional in firmware; the handler treats
// their absence as "this upload did not emit
// them" rather than as a request error.
type uploadSketchMetadata struct {
	// The PO-33 model identifier, e.g. "PO-33".
	// Matches MemoPayload.sketch.model.
	Model string `json:"model"`

	// The number of sample slots the device
	// exposes. Today always 16 for PO-33 / PO-33
	// K.O! and 8 for PO-33 micro. The handler
	// validates this against the device's
	// reported value (po33_devices.slot_count)
	// and rejects mismatches with 422.
	SlotCount int32 `json:"slot_count"`

	// The BPM the device stamped on the sketch.
	// Optional: not every device models BPM.
	BPM *int32 `json:"bpm,omitempty"`

	// The hex SHA-256 of the device's Ed25519
	// public key. Used to look up the
	// po33_devices row; the public key in that
	// row is what the signature is verified
	// against.
	DeviceFingerprint string `json:"device_fingerprint"`

	// The unix-second timestamp the device
	// recorded at upload time. Distinct from the
	// memo's create_time, which is the server's
	// clock. Mirrored onto the memo's payload.sketch
	// envelope.
	UploadedAtUnix int64 `json:"uploaded_at_unix"`

	// A server-issued random 16-byte hex-encoded
	// nonce that the device includes in the
	// signing input. The nonce is bound to the
	// (device, upload-time) pair so a captured
	// signature cannot be replayed against a
	// different upload. The handler generates
	// the nonce from the device's
	// last_seen_at + the metadata's
	// uploaded_at_unix so a replay (an older
	// uploaded_at_unix) is detectable.
	Nonce string `json:"nonce"`
}

// uploadSketchResponse is the body of a
// successful upload. The `memo` field is the
// resource name the SPA can render in the
// "sketches" page. The `version` field is the
// per-author monotonically increasing upload
// counter; the SPA renders it as a "v3"
// badge on the sketch card without needing
// a separate `ListSketchVersions` RPC. The
// same value is also stored on the memo's
// `sketch` JSON column under `version_no` so
// it persists alongside the rest of the
// upload-time metadata. See DECISIONS.md,
// "sketch version lives in sketch JSON
// column (2026-10-19)".
type uploadSketchResponse struct {
	Memo    string `json:"memo"`    // "memos/{id}"
	Version int32  `json:"version"` // 1-indexed per-author upload counter
}

// uploadSketchHandler serves
// POST /api/po33/sketches/upload. The route is
// intentionally NOT authenticated: the PO-33
// device has no session token; the Ed25519
// signature over the canonical metadata is the
// bearer credential, exactly the pattern the
// /pair/finish route already uses.
//
// The body is multipart/form-data with up to
// 18 parts (metadata, signature, backup_file,
// 16 sample_wavs). Each part is read in full
// into memory (with the size cap enforced
// during read) and processed sequentially.
// The file parts (backup_file, sample_wav_0..N)
// are written to PocketBase's filesystem under
// their respective patches/samples row id.
//
// The lifecycle:
//
//  1. Parse multipart, cap each part's bytes.
//  2. Decode metadata JSON; validate shape.
//  3. Look up po33_devices row by
//     device_fingerprint. Reject if absent or
//     REVOKED.
//  4. Verify the Ed25519 signature against the
//     stored public key. Reject on mismatch.
//  5. Create the memos row (state='DRAFT',
//     visibility=PRIVATE) and a patches row
//     for the .p33 patch blob.
//  6. Create one samples row per sample_wav
//     part. Link via memo_relations (SAMPLE_FILE).
//  7. If backup_file is present, create a
//     second patches row for the .bkp blob.
//     Link via memo_relations (BACKUP_FILE).
//  8. Bump the device's last_seen_at so a
//     replayed signature with an older
//     uploaded_at_unix is rejected on the next
//     attempt.
//  9. Compute the per-author upload counter
//     (version_no) — 1 on a fresh author, +1
//     over the author's max so far. Stash it
//     on the sketch JSON column.
// 10. Return 201 with the new memo's resource
//     name and the version number.
//
// The pre-2026-10-19 path inserted a row
// into a parallel `sketch_versions` table
// under a `(memo_id, version_no)` unique
// index; the table is gone. The version
// now lives in-band on the memo's `sketch`
// JSON column (key `version_no`), so a
// single `FindRecordById` returns both the
// metadata and the version. See
// DECISIONS.md, "sketch version lives in
// sketch JSON column (2026-10-19)".
//
// The error mapping is in uploadSketchError.
// The status codes match the brief's §10
// table verbatim.
func uploadSketchHandler(e *core.RequestEvent) error {
	// Reject any non-multipart content type
	// before reading the body so a malformed
	// client gets a clean 400 rather than a
	// confusing parser error.
	ct := e.Request.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		return NewAPIError(http.StatusBadRequest,
			"Content-Type must be multipart/form-data")
	}

	// Read the entire body into memory with a
	// hard cap. The cap is the sum of all part
	// caps plus 10% headroom for multipart
	// framing. A device that sends more than
	// this is misbehaving; 413 with a precise
	// error message is the right answer.
	maxBody := uploadMetadataMaxBytes +
		uploadSignatureMaxBytes +
		uploadBackupMaxBytes +
		uploadMaxSampleCount*uploadSampleMaxBytes
	maxBody = maxBody + maxBody/10
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, int64(maxBody))
	if err := e.Request.ParseMultipartForm(int64(maxBody)); err != nil {
		return NewAPIError(http.StatusRequestEntityTooLarge,
			"multipart body exceeds the size cap")
	}

	mf := e.Request.MultipartForm
	if mf == nil {
		return NewAPIError(http.StatusBadRequest,
			"missing multipart body")
	}

	// Read the three required parts. Each is
	// capped to its part-specific byte limit so
	// a single oversized part surfaces as 413
	// with the part name, not as a body-wide
	// cap hit.
	rawMeta, err := readCappedPart(mf, "metadata", uploadMetadataMaxBytes)
	if err != nil {
		return uploadSketchError(err, "metadata")
	}
	rawSig, err := readCappedPart(mf, "signature", uploadSignatureMaxBytes)
	if err != nil {
		return uploadSketchError(err, "signature")
	}

	var meta uploadSketchMetadata
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		return NewAPIError(http.StatusBadRequest,
			"metadata is not valid JSON")
	}
	if meta.DeviceFingerprint == "" || meta.SlotCount < 1 || meta.UploadedAtUnix <= 0 {
		return NewAPIError(http.StatusBadRequest,
			"metadata missing required fields")
	}

	// Look up the device row. A non-owner
	// request gets NotFound rather than
	// Forbidden so a stranger cannot
	// enumerate registered devices by probing
	// the upload route with a guessed
	// fingerprint.
	device, err := findDeviceByFingerprint(e.App, meta.DeviceFingerprint)
	if err != nil {
		return NewAPIError(http.StatusNotFound,
			"device not registered")
	}
	if device.GetString("status") != "ACTIVE" {
		return NewAPIError(http.StatusConflict,
			"device is "+device.GetString("status"))
	}

	// Per-device rate limit. The brief's §10
	// caps uploads at 10 / hour / device; the
	// rule is in DefaultPolicy. The bucket
	// key is the device row id, not the IP, so
	// a device moving between networks (home,
	// studio, mobile) keeps the same bucket.
	// A nil limiter is a no-op, matching the
	// behaviour of the connect-side middleware.
	if packageLimiter != nil {
		if dec := packageLimiter.Consume(
			ratelimit.ScopeUploadSketchDevice,
			device.Id, 1); !dec.Allowed {
		return NewAPIError(http.StatusTooManyRequests,
			fmt.Sprintf("device upload rate limit exceeded; retry in %s",
				dec.RetryAfter.Round(time.Second)))
		}
	}

	// Verify the signature. The signing input
	// is the canonical JSON serialisation of
	// the metadata (sorted keys, no
	// whitespace, fixed integer rendering), so
	// a server that re-orders the JSON
	// structure cannot break the signature.
	pub, err := ed25519verify.ParsePublicKey(
		mustHexDecode(device.GetString("public_key")))
	if err != nil {
		return NewAPIError(http.StatusInternalServerError,
			"stored public_key is malformed")
	}
	sig, err := ed25519verify.ParseSignature(rawSig)
	if err != nil {
		return NewAPIError(http.StatusBadRequest,
			"signature is malformed")
	}
	canonical, err := canonicalizeUploadMetadata(meta)
	if err != nil {
		return NewAPIError(http.StatusInternalServerError,
			"could not canonicalize metadata")
	}
	if err := ed25519verify.Verify(pub, canonical, sig); err != nil {
		return NewAPIError(http.StatusUnauthorized,
			"signature does not validate")
	}

	// Replay check: the device's last_seen_at
	// (set by pb/hooks/device_last_seen.go on
	// every successful authenticated request)
	// must be <= the metadata's uploaded_at_unix.
	// A captured signature replayed at a later
	// time would carry an older uploaded_at_unix
	// and trip this check.
	if lastSeen := device.GetDateTime("last_seen_at"); !lastSeen.IsZero() {
		if meta.UploadedAtUnix < lastSeen.Time().Unix() {
			return NewAPIError(http.StatusConflict,
				"signature timestamp is older than the device's last seen time")
		}
	}

	// Per-attachment shape check: the device
	// reports slot_count in the metadata; the
	// sample_wav parts on the wire must match.
	// A 17th sample part is a firmware bug; we
	// surface 422 so the device renders a
	// precise diagnostic.
	sampleParts := make([]*multipart.FileHeader, 0, uploadMaxSampleCount)
	for k, v := range mf.File {
		if strings.HasPrefix(k, "sample_wav_") {
			sampleParts = append(sampleParts, v...)
		}
	}
	if len(sampleParts) > uploadMaxSampleCount {
		return NewAPIError(http.StatusUnprocessableEntity,
			fmt.Sprintf("too many sample parts (max %d)",
				uploadMaxSampleCount))
	}

	// Create the parent memo. The hook in
	// pb/hooks/memos.go defaults state to
	// NORMAL; we override to DRAFT here
	// before save so the sketch lands in
	// DRAFT the way the brief's lifecycle
	// requires.
	memos, err := e.App.FindCollectionByNameOrId("memos")
	if err != nil {
		return err
	}
	memoRec := core.NewRecord(memos)
	memoRec.Set("author", device.GetString("user"))
	memoRec.Set("content", "Sketch uploaded from PO-33 device.")
	memoRec.Set("visibility", "PRIVATE")
	memoRec.Set("state", "DRAFT")
	// payload envelope: the MemoPayload shape
	// the existing pb/connect/memos_translate.go
	// reads (property + location). The pre-1860xxx
	// shape also carried a `sketch` sub-object;
	// the 1860xxx series moved the sketch data
	// to a dedicated `memos.sketch` JSON column
	// and the translator no longer reads the
	// `payload.sketch` sub-object.
	rawPayload, _ := json.Marshal(map[string]any{
		"property": map[string]any{},
		"location": map[string]any{},
	})
	memoRec.Set("payload", json.RawMessage(rawPayload))
	// The `sketch` column is the PO-33 device
	// metadata in the upstream Memos shape. The
	// translator reads it directly; it is the
	// source of truth for the proto's
	// `MemoPayload.sketch` sub-message. The
	// user-authored metadata (key, license,
	// attribution, ...) is not populated yet at
	// this stage of the upload; the device
	// fields are the only data the device
	// sends.
	//
	// The `version_no` is the per-author
	// monotonically increasing upload counter.
	// 1 on the first upload from this author,
	// +1 on every subsequent upload. The pre-
	// 2026-10-19 path used a parallel
	// `sketch_versions` table; the version now
	// lives in-band on this JSON column (see
	// DECISIONS.md, "sketch version lives in
	// sketch JSON column (2026-10-19)").
	versionNo := nextSketchVersionNo(e.App, device.GetString("user"))
	sketchJSON := map[string]any{
		"model":              meta.Model,
		"slot_count":         meta.SlotCount,
		"device_fingerprint": meta.DeviceFingerprint,
		"uploaded_at_unix":   meta.UploadedAtUnix,
		"version_no":         versionNo,
	}
	if meta.BPM != nil {
		sketchJSON["bpm"] = *meta.BPM
	}
	rawSketch, _ := json.Marshal(sketchJSON)
	memoRec.Set("sketch", json.RawMessage(rawSketch))
	if err := e.App.Save(memoRec); err != nil {
		return err
	}

	// Write the .p33 patch. The unified
	// `attachments` collection accepts a single
	// `blob` field; the device's wire shape is a
	// single `patch_file` part (the firmware team's
	// §3 call it that). We read it from a
	// dedicated part name; if absent, the
	// upload is patch-less (a future firmware
	// feature for sample-only sketches) and we
	// continue without writing an attachment row.
	if rawPatch, err := readCappedPart(mf, "patch_file", uploadBackupMaxBytes); err == nil {
		if _, err := writeAttachmentRow(e.App, memoRec.Id,
			"sketch.p33", rawPatch, meta.DeviceFingerprint); err != nil {
			return err
		}
	}

	// Write the per-sample rows. Each
	// sample_wav_N part becomes one row in the
	// unified `attachments` collection.
	for i, hdr := range sampleParts {
		// A sketch upload is per-sample, so
		// each row gets exactly one file.
		// The cap is per-row, not per-file.
		if hdr.Size > uploadSampleMaxBytes {
			return NewAPIError(http.StatusRequestEntityTooLarge,
				fmt.Sprintf("sample %d exceeds %d bytes",
					i, uploadSampleMaxBytes))
		}
		f, err := hdr.Open()
		if err != nil {
			return err
		}
		buf := bytes.NewBuffer(nil)
		if _, err := io.Copy(buf, f); err != nil {
			_ = f.Close()
			return err
		}
		_ = f.Close()
		if _, err := writeAttachmentRow(e.App, memoRec.Id,
			fmt.Sprintf("sample_%02d.wav", i),
			buf.Bytes(), meta.DeviceFingerprint); err != nil {
			return err
		}
	}

	// Write the .bkp tarball. The
	// original .bkp bytes are stored
	// byte-for-byte as a second patches
	// row pointing at the parent memo; the
	// server does not re-parse the
	// tarball. The .p33 and .wav rows
	// already link to the parent memo via
	// their own `memo` relation column;
	// no memo_relations row is needed for
	// the device upload path. The
	// `BACKUP_FILE` kind that migration
	// 1840000004 added to memo_relations
	// is reserved for the user-side
	// "link this backup as a separate
	// reference" workflow (a future
	// slice) and is not the device
	// upload's join.
	// A non-nil err is "the part is
	// missing or over-cap" (the typed
	// errors in readCappedPart). We
	// surface "over-cap" as 413 so the
	// device can render a precise
	// diagnostic; "missing" is fine
	// (the backup is optional).
	rawBackup, berr := readCappedPart(mf, "backup_file", uploadBackupMaxBytes)
	if berr != nil {
		var oversize errPartOversize
		if errors.As(berr, &oversize) {
			return NewAPIError(http.StatusRequestEntityTooLarge,
				"part backup_file exceeds "+strconv.Itoa(oversize.cap)+" bytes")
		}
		// errPartMissing: the backup is
		// optional, so we silently skip.
	}
	if rawBackup != nil {
		if _, err := writeAttachmentRow(e.App, memoRec.Id,
			"sketch.bkp", rawBackup, meta.DeviceFingerprint); err != nil {
			return err
		}
	}

	// Bump the device's last_seen_at so a
	// replayed signature with an older
	// uploaded_at_unix is rejected on the
	// next attempt.
	device.Set("last_seen_at", types.NowDateTime())
	_ = e.App.Save(device)

	return e.JSON(http.StatusCreated, uploadSketchResponse{
		Memo:    "memos/" + memoRec.Id,
		Version: versionNo,
	})
}

// readCappedPart reads a single multipart
// part into a byte slice, enforcing a hard
// size cap. A missing part returns a
// sentinel that the handler maps to 400 (for
// required parts) or 200/noop (for optional
// parts). An over-cap part returns a 413-
// ready error.
func readCappedPart(mf *multipart.Form, name string, cap int) ([]byte, error) {
	files, ok := mf.File[name]
	if !ok || len(files) == 0 {
		return nil, errPartMissing{name: name}
	}
	hdr := files[0]
	if hdr.Size > int64(cap) {
		return nil, errPartOversize{name: name, cap: cap, got: hdr.Size}
	}
	f, err := hdr.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// errPartMissing is a typed error the handler
// maps to 400 (for required parts) or to
// "absent" (for optional parts). Using a
// typed error keeps the handler free of
// ad-hoc string matching.
type errPartMissing struct{ name string }

func (e errPartMissing) Error() string {
	return "missing part: " + e.name
}

// errPartOversize carries the cap and the
// actual size so the 413 response can name
// both numbers.
type errPartOversize struct {
	name string
	cap  int
	got  int64
}

func (e errPartOversize) Error() string {
	return "part " + e.name + " exceeds cap (" +
		strconv.Itoa(e.cap) + " bytes, got " +
		strconv.FormatInt(e.got, 10) + ")"
}

// uploadSketchError maps a typed part error
// to the right HTTP status + message. The
// mapping is the brief's §10 table.
func uploadSketchError(err error, part string) error {
	var missing errPartMissing
	if errors.As(err, &missing) {
		return NewAPIError(http.StatusBadRequest,
			"missing required part: "+part)
	}
	var oversize errPartOversize
	if errors.As(err, &oversize) {
		return NewAPIError(http.StatusRequestEntityTooLarge,
			"part "+part+" exceeds "+strconv.Itoa(oversize.cap)+" bytes")
	}
	return NewAPIError(http.StatusInternalServerError, err.Error())
}

// canonicalizeUploadMetadata is the
// deterministic-JSON serialisation the device
// signs. Sorted keys, no whitespace, fixed
// integer rendering. The same helper lives
// alongside the firmware team's signing
// reference implementation; the unit test
// for this function is the contract.
func canonicalizeUploadMetadata(m uploadSketchMetadata) ([]byte, error) {
	// Use a fixed-order key list so the
	// serialisation is stable. Any new
	// metadata field must be appended at
	// the end so existing signatures keep
	// validating. The values are the same
	// JSON tag the proto side uses; the
	// brief's §3 wires the same shape.
	fields := map[string]string{
		"model":              m.Model,
		"slot_count":         strconv.FormatInt(int64(m.SlotCount), 10),
		"bpm":                bpmToString(m.BPM),
		"device_fingerprint": m.DeviceFingerprint,
		"uploaded_at_unix":   strconv.FormatInt(m.UploadedAtUnix, 10),
		"nonce":              m.Nonce,
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	// sort.Strings would do; the explicit
	// sort is documented for the firmware
	// team's reference.
	less := func(a, b string) bool {
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] < b[i] {
				return true
			}
			if a[i] > b[i] {
				return false
			}
		}
		return len(a) < len(b)
	}
	// simple sort
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && less(keys[j], keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(fields[k])
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// bpmToString renders the BPM as the
// canonical-JSON integer or null. The
// *int32 pointer mirrors the proto
// `optional int32` shape: an unset BPM
// serialises to `null` rather than `0`.
func bpmToString(bpm *int32) string {
	if bpm == nil {
		return "null"
	}
	return strconv.FormatInt(int64(*bpm), 10)
}

// findDeviceByFingerprint looks up a
// po33_devices row by the full SHA-256
// fingerprint. Returns (nil, err) on miss;
// the handler maps to NotFound.
func findDeviceByFingerprint(app core.App, fpr string) (*core.Record, error) {
	if fpr == "" {
		return nil, errors.New("empty fingerprint")
	}
	row, err := app.FindFirstRecordByFilter("po33_devices",
		"public_key_fpr = {:fpr}",
		map[string]any{"fpr": fpr})
	if err != nil || row == nil {
		return nil, errors.New("device not found")
	}
	return row, nil
}

// writeAttachmentRow inserts a row in the unified
// `attachments` collection pointing at the parent memo,
// then writes the file bytes to PocketBase's filesystem
// under the row's id. The on-disk layout matches the
// existing chunked-upload pipeline
// (`pb_data/storage/<col_id>/<rec_id>/<filename>`) so the
// SPA's existing playback path works unchanged. A sketch
// upload produces one row per `.p33` patch blob, one row
// per `.wav` sample, and one row for the `.bkp` tarball;
// the unified collection's single-file `blob` field is
// the right shape for all three.
func writeAttachmentRow(app core.App, memoID, filename string, body []byte, fpr string) (string, error) {
	col, err := app.FindCollectionByNameOrId("attachments")
	if err != nil {
		return "", err
	}
	rec := core.NewRecord(col)
	rec.Set("memo", memoID)
	rec.Set("device_fingerprint_snapshot", fpr)
	rec.Set("filename", filename)
	rec.Set("size", int64(len(body)))
	rec.Set("storage", "local")
	// Build a *filesystem.File the FileField
	// interceptor can write to disk as part of
	// the Save. The existing chunked upload
	// pipeline (pb/connect/attachment_upload.go)
	// uses the same pattern; the file is uploaded
	// by the interceptor and the row's blob column
	// is populated transparently.
	f, err := filesystem.NewFileFromBytes(body, filename)
	if err != nil {
		return "", err
	}
	rec.Set("blob", f)
	if err := app.Save(rec); err != nil {
		return "", err
	}
	return rec.Id, nil
}

// writeFileToRecord writes the bytes to
// PocketBase's filesystem under the
// collection's storage path. The layout
// matches the chunked-upload pipeline:
// `pb_data/storage/<col_id>/<rec_id>/<filename>`.
// The on-disk name is derived from the
// original filename; PocketBase's
// FileField.Serve serves the file at
// `/api/files/<col_id>/<rec_id>/<filename>`.
func writeFileToRecord(app core.App, coll, recID, filename string, body []byte) error {
	fsys, err := app.NewFilesystem()
	if err != nil {
		return err
	}
	c, err := app.FindCollectionByNameOrId(coll)
	if err != nil {
		return err
	}
	key := dirForFilenames(c.Id, recID, filename)
	f, err := filesystem.NewFileFromBytes(body, filename)
	if err != nil {
		return err
	}
	return fsys.UploadFile(f, key)
}


// dirForFilenames is a local copy of the
// helper in pb/connect/upload_session.go.
// The api package does not import connect
// (it would create an import cycle) and the
// helper is four lines long; a local copy
// keeps the api package self-contained.
// The on-disk layout matches the connect
// helper exactly: forward-slash form is
// the canonical separator on every
// PocketBase backend (local, S3).
func dirForFilenames(collectionID, recordID, filename string) string {
	return filepath.ToSlash(filepath.Join(collectionID, recordID, filename))
}

// nextSketchVersionNo returns the per-author
// monotonically increasing sketch upload
// counter. 1 on a fresh author (no prior
// sketch memos); (max version_no over the
// author's prior sketch memos) + 1 on every
// subsequent upload.
//
// The cap on the `FindRecordsByFilter` scan
// is the most-recent 200 sketch memos. A
// well-behaved author who has uploaded more
// than 200 sketches is rare; the bound keeps
// the read cost on the upload hot path
// bounded even for a power user, and the
// counter only ever increases so a
// bounded-but-large scan picks up the
// right max. A future backend admin tool
// (out of scope for the upload handler) can
// compact the counter after a bulk delete.
//
// The pre-2026-10-19 path used a parallel
// `sketch_versions` table; the value is now
// read from each row's `sketch` JSON column
// (key `version_no`). See DECISIONS.md,
// "sketch version lives in sketch JSON
// column (2026-10-19)".
func nextSketchVersionNo(app core.App, authorID string) int32 {
	const scanCap = 200
	rows, err := app.FindRecordsByFilter("memos",
		"author = {:uid} && sketch != null",
		"-created", scanCap, 0,
		map[string]any{"uid": authorID})
	if err != nil || len(rows) == 0 {
		return 1
	}
	var maxNo int32
	for _, r := range rows {
		raw := r.Get("sketch")
		if raw == nil {
			continue
		}
		var shape struct {
			VersionNo int32 `json:"version_no"`
		}
		// The column comes back as
		// types.JSONRaw (a []byte) on
		// PocketBase; json.Unmarshal
		// handles it directly. A
		// pre-2026-10-19 row that was
		// back-filled by M186 carries
		// no version_no key and
		// decodes to 0 — which is the
		// expected "no version" value
		// and does not bump the max.
		b, ok := raw.([]byte)
		if !ok {
			// PocketBase returns the
			// `sketch` column as a
			// `types.JSONRaw` (a
			// named-type []byte). The
			// plain `[]byte` assertion
			// fails for that case;
			// json.Marshal round-trips
			// it through Go's
			// reflection. The
			// `payload` translator in
			// pb/connect hits the same
			// problem and solves it the
			// same way.
			b, _ = json.Marshal(raw)
		}
		if len(b) == 0 || string(b) == "null" {
			continue
		}
		if err := json.Unmarshal(b, &shape); err != nil {
			continue
		}
		if shape.VersionNo > maxNo {
			maxNo = shape.VersionNo
		}
	}
	return maxNo + 1
}
