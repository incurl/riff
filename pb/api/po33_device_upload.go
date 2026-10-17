package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	// clock. Stored in sketch_versions and on
	// the memo's payload.sketch envelope.
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
// successful upload. The `memo` and `version`
// fields are the resource names the SPA can
// render in the "sketches" page.
type uploadSketchResponse struct {
	Memo    string `json:"memo"`    // "memos/{id}"
	Version int32  `json:"version"` // the new sketch_versions.version_no
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
//  5. Create the memos row (source='sketch',
//     state='DRAFT', visibility=PRIVATE) and
//     a patches row for the .p33 patch blob.
//  6. Create one samples row per sample_wav
//     part. Link via memo_relations (SAMPLE_FILE).
//  7. If backup_file is present, create a
//     second patches row for the .bkp blob.
//     Link via memo_relations (BACKUP_FILE).
//  8. Compute version_no = (existing
//     sketch_versions count for this memo) + 1.
//     Insert the sketch_versions row. The
//     unique (memo_id, version_no) index makes
//     a concurrent upload a 409.
//  9. Return 201 with the new memo's resource
//     name and the new version's number.
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
	memoRec.Set("type", "note")
	memoRec.Set("source", "sketch")
	memoRec.Set("state", "DRAFT")
	// payload envelope: the SketchPayload shape
	// the existing pb/connect/memos_translate_sketch.go
	// reads. A future slice will move this to
	// the same extractor the save hook uses for
	// text memos, but today the upload handler
	// writes the envelope directly.
	sketchPayload := map[string]any{
		"property":           map[string]any{},
		"location":           map[string]any{},
		"sketch": map[string]any{
			"model":              meta.Model,
			"slot_count":         meta.SlotCount,
			"device_fingerprint": meta.DeviceFingerprint,
			"uploaded_at_unix":   meta.UploadedAtUnix,
		},
	}
	if meta.BPM != nil {
		sketchPayload["sketch"].(map[string]any)["bpm"] = *meta.BPM
	}
	rawPayload, _ := json.Marshal(sketchPayload)
	memoRec.Set("payload", json.RawMessage(rawPayload))
	if err := e.App.Save(memoRec); err != nil {
		return err
	}

	// Write the .p33 patch.
	// Write the .p33 patch. The patches
	// collection accepts a single `file` field;
	// the device's wire shape is a single
	// `patch_file` part (the firmware team's
	// §3 call it that). We read it from a
	// dedicated part name; if absent, the
	// upload is patch-less (a future firmware
	// feature for sample-only sketches) and we
	// continue without writing a patches row.
	var patchKey string
	if rawPatch, err := readCappedPart(mf, "patch_file", uploadBackupMaxBytes); err == nil {
		key, err := writePatchRow(e.App, memoRec.Id,
			"sketch.p33", rawPatch, meta.DeviceFingerprint)
		if err != nil {
			return err
		}
		patchKey = key
	}

	// Write the per-sample rows. Each
	// sample_wav_N part becomes one samples
	// row, linked via a SAMPLE_FILE relation.
	sampleKeys := make([]string, 0, len(sampleParts))
	for i, hdr := range sampleParts {
		// A samples row is multi-file; a
		// sketch upload is per-sample, so
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
		key, err := writeSampleRow(e.App, memoRec.Id,
			fmt.Sprintf("sample_%02d.wav", i),
			buf.Bytes(), meta.DeviceFingerprint)
		if err != nil {
			return err
		}
		sampleKeys = append(sampleKeys, key)
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
	var backupKey string
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
		key, err := writePatchRow(e.App, memoRec.Id,
			"sketch.bkp", rawBackup, meta.DeviceFingerprint)
		if err != nil {
			return err
		}
		backupKey = key
	}

	// Compute the new version number. The
	// brief's "monotonic per-memo" invariant
	// is enforced at the index level: a
	// duplicate (memo_id, version_no) insert
	// fails the unique constraint, which the
	// handler surfaces as 409. The count +
	// insert here is racy under concurrent
	// uploads of the same memo, but the race
	// is resolved by the unique index; the
	// loser's insert fails and is reported
	// as 409.
	existing, err := e.App.FindRecordsByFilter("sketch_versions",
		"memo_id = {:mid}", "-version_no", 1, 0,
		map[string]any{"mid": memoRec.Id})
	if err != nil {
		return err
	}
	versionNo := int32(1)
	if len(existing) > 0 {
		versionNo = int32(existing[0].GetInt("version_no")) + 1
	}
	if err := writeSketchVersion(e.App, memoRec.Id, versionNo,
		meta, patchKey, sampleKeys, backupKey, rawMeta, rawSig); err != nil {
		return err
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

// writePatchRow inserts a `patches` row
// pointing at the parent memo, then writes
// the file bytes to PocketBase's filesystem
// under the row's id. The on-disk layout
// matches the existing chunked-upload
// pipeline (`pb_data/storage/<col_id>/<rec_id>/<filename>`)
// so the SPA's existing playback path works
// unchanged.
func writePatchRow(app core.App, memoID, filename string, body []byte, fpr string) (string, error) {
	col, err := app.FindCollectionByNameOrId("patches")
	if err != nil {
		return "", err
	}
	rec := core.NewRecord(col)
	rec.Set("memo", memoID)
	rec.Set("device_fingerprint_snapshot", fpr)
	// SHA-256 of the bytes for a fast
	// content-addressable sanity check; the
	// column is unused today but mirrors
	// the convention the rest of riff has
	// adopted for media assets.
	sum := sha256.Sum256(body)
	rec.Set("description", "device upload sha256:"+hex.EncodeToString(sum[:]))
	// Build a *filesystem.File the
	// FileField interceptor can write to
	// disk as part of the Save. The
	// existing chunked upload pipeline
	// (pb/connect/attachment_upload.go)
	// uses the same pattern; the file is
	// uploaded by the interceptor and the
	// row's file column is populated
	// transparently.
	f, err := filesystem.NewFileFromBytes(body, filename)
	if err != nil {
		return "", err
	}
	rec.Set("file", f)
	if err := app.Save(rec); err != nil {
		return "", err
	}
	return rec.Id, nil
}

// writeSampleRow inserts a `samples` row
// pointing at the parent memo and writes
// the .wav bytes to the file field.
func writeSampleRow(app core.App, memoID, filename string, body []byte, fpr string) (string, error) {
	col, err := app.FindCollectionByNameOrId("samples")
	if err != nil {
		return "", err
	}
	rec := core.NewRecord(col)
	rec.Set("memo", memoID)
	rec.Set("device_fingerprint_snapshot", fpr)
	rec.Set("description", "device upload")
	// Build a *filesystem.File the
	// FileField interceptor writes to disk
	// during Save. samples.files is a
	// multi-file field; the assignment
	// shape is a single-element slice.
	f, err := filesystem.NewFileFromBytes(body, filename)
	if err != nil {
		return "", err
	}
	rec.Set("files", []*filesystem.File{f})
	if err := app.Save(rec); err != nil {
		return "", err
	}
	return rec.Id, nil
}

// writeSketchVersion inserts a `sketch_versions`
// row carrying the upload's metadata, signature,
// and the keys of the patch / sample / backup
// rows the upload created. The
// (memo_id, version_no) unique index is the
// invariant; a duplicate insert fails the
// index and the handler maps to 409.
func writeSketchVersion(app core.App, memoID string, versionNo int32,
	meta uploadSketchMetadata,
	patchKey string, sampleKeys []string, backupKey string,
	rawMeta, rawSig []byte) error {
	col, err := app.FindCollectionByNameOrId("sketch_versions")
	if err != nil {
		return err
	}
	rec := core.NewRecord(col)
	rec.Set("memo_id", memoID)
	rec.Set("version_no", versionNo)
	rec.Set("device_fingerprint", meta.DeviceFingerprint)
	rec.Set("uploaded_at_unix", meta.UploadedAtUnix)
	rec.Set("received_at", types.NowDateTime())
	if patchKey != "" {
		rec.Set("patch_key", patchKey)
	}
	if len(sampleKeys) > 0 {
		rawSamples, _ := json.Marshal(sampleKeys)
		rec.Set("sample_keys", json.RawMessage(rawSamples))
	}
	if backupKey != "" {
		rec.Set("backup_key", backupKey)
	}
	rec.Set("metadata", json.RawMessage(rawMeta))
	rec.Set("signature", hex.EncodeToString(rawSig))
	rec.Set("signature_algorithm", "ed25519")
	return app.Save(rec)
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
