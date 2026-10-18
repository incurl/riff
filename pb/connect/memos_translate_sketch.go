package connect

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// sketchShape mirrors the JSON document the PO-33
// upload handler and the M186 backfill write into
// the `memos.sketch` JSON column.
//
// The shape carries two field groups:
//
//	device fields  — populated by the upload handler
//	                 (model, slot_count,
//	                 device_fingerprint, uploaded_at_unix,
//	                 version_no).
//	user fields    — populated by the save hook when
//	                 the user edits the sketch
//	                 (bpm, key, sample_source,
//	                 difficulty, license, attribution,
//	                 video_url).
//
// `bpm` is *int32 so a sketch without a BPM round-
// trips to protojson as `bpm: null` rather than
// `bpm: 0`. The SPA renders "—" instead of "0"
// for the null case.
//
// `version_no` is `omitempty` so a sketch row
// written by a pre-2026-10-19 upload (which
// did not set the field) decodes to 0 in
// protojson — the SPA's "v1" badge is
// suppressed for those rows because the
// counter was not in the wire shape at
// upload time. Fresh rows (the upload
// handler computes and writes the value)
// round-trip with a non-zero value.
type sketchShape struct {
	// Device fields.
	Model             string `json:"model"`
	SlotCount         int32  `json:"slot_count"`
	BPM               *int32 `json:"bpm,omitempty"`
	DeviceFingerprint string `json:"device_fingerprint"`
	UploadedAtUnix    int64  `json:"uploaded_at_unix"`
	VersionNo         int32  `json:"version_no,omitempty"`

	// User-authored PO-33 fields. The seven
	// flat columns the pre-1860xxx schema
	// carried (`bpm`, `key`, `sample_source`,
	// `difficulty`, `license`, `attribution`,
	// `video_url`) merged into these keys.
	Key          string `json:"key,omitempty"`
	SampleSource string `json:"sample_source,omitempty"`
	Difficulty   string `json:"difficulty,omitempty"`
	License      string `json:"license,omitempty"`
	Attribution  string `json:"attribution,omitempty"`
	VideoURL     string `json:"video_url,omitempty"`
}

// memoSketchFromRecord reads the `memos.sketch`
// JSON column and decodes it into the proto
// `MemoPayload.sketch` sub-message. The new top-
// level column is the source of truth (the 1860xxx
// series moved the data out of the `payload.sketch`
// sub-object).
//
// Returns nil when the column is null/empty — a
// text memo has no sketch sub-object. The check
// rejects both the nil case and the JSON `null`
// case because PocketBase returns a non-nil
// `[]byte` carrying the literal four characters
// "null" when the row was saved without the
// column populated.
func memoSketchFromRecord(rec *core.Record) *v1.SketchPayload {
	raw := rec.Get("sketch")
	if raw == nil {
		return nil
	}
	if b, ok := raw.(types.JSONRaw); ok {
		if len(b) == 0 || string(b) == "null" {
			return nil
		}
	}
	var shape sketchShape
	if err := decodePayload(raw, &shape); err != nil {
		return nil
	}
	if !sketchShapeIsPresent(&shape) {
		return nil
	}
	out := &v1.SketchPayload{
		Model:             shape.Model,
		SlotCount:         shape.SlotCount,
		DeviceFingerprint: shape.DeviceFingerprint,
		UploadedAtUnix:    shape.UploadedAtUnix,
		VersionNo:         shape.VersionNo,
	}
	// The single `BPM` field covers both the
	// device-stamped value (upload handler) and
	// the user-corrected value (save hook).
	// The upload handler and the save hook
	// never set both at once in practice, so
	// a single optional field is enough.
	if shape.BPM != nil {
		bpm := *shape.BPM
		out.Bpm = &bpm
	}
	return out
}

// sketchShapeIsPresent returns true when the shape
// carries at least one populated key. A literal
// `{}` decodes into a zero-value struct that
// memoSketchFromRecord treats as "no sketch"
// rather than "empty sketch", because a real
// sketch upload always populates at least the
// model + slot_count device fields.
//
// `version_no` is intentionally NOT in the
// presence check: a pre-2026-10-19 row that
// was back-filled into the new shape via
// the M186 migration carries no version_no
// but is still a valid sketch. VersionNo
// 0 on those rows is the discriminator the
// SPA can use to skip the "v0" badge.
func sketchShapeIsPresent(s *sketchShape) bool {
	if s == nil {
		return false
	}
	return s.Model != "" ||
		s.SlotCount != 0 ||
		s.BPM != nil ||
		s.DeviceFingerprint != "" ||
		s.UploadedAtUnix != 0 ||
		s.Key != "" ||
		s.SampleSource != "" ||
		s.Difficulty != "" ||
		s.License != "" ||
		s.Attribution != "" ||
		s.VideoURL != ""
}

// sketchPayloadFromShape was the pre-1860xxx
// path that decoded the `payload.sketch` sub-
// object. The new translator reads the top-level
// `memos.sketch` column via `memoSketchFromRecord`
// instead. The function is kept as a thin
// compatibility shim so any out-of-tree code that
// still imports the symbol (e.g. test helpers
// in a downstream fork) keeps compiling.
func sketchPayloadFromShape(in *sketchShape) *v1.SketchPayload {
	if in == nil {
		return nil
	}
	return memoSketchFromShape(in)
}

// memoSketchFromShape is the internal decoder
// the new `memoSketchFromRecord` path shares with
// the legacy shim. The shape is the same in both
// reads (the on-disk format did not change;
// only the column did); the function just
// translates the parsed struct into the proto
// envelope.
func memoSketchFromShape(in *sketchShape) *v1.SketchPayload {
	if in == nil {
		return nil
	}
	out := &v1.SketchPayload{
		Model:             in.Model,
		SlotCount:         in.SlotCount,
		DeviceFingerprint: in.DeviceFingerprint,
		UploadedAtUnix:    in.UploadedAtUnix,
		VersionNo:         in.VersionNo,
	}
	if in.BPM != nil {
		bpm := *in.BPM
		out.Bpm = &bpm
	}
	return out
}

