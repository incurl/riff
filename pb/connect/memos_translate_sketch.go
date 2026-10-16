package connect

import (
	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// sketchShape mirrors the JSON the memos save hook
// writes into the `memos.payload.sketch` sub-object
// when a PO-33 device uploads a sketch. The shape
// is the proto SketchPayload serialised by hand
// (not via protojson) because the existing
// payloadShape is plain JSON without protojson
// quoting, and a future migration to protojson can
// change the wire shape without breaking the
// on-disk format.
type sketchShape struct {
	Model             string `json:"model"`
	SlotCount         int32  `json:"slot_count"`
	BPM               *int32 `json:"bpm,omitempty"`
	DeviceFingerprint string `json:"device_fingerprint"`
	UploadedAtUnix    int64  `json:"uploaded_at_unix"`
}

// sketchPayloadFromShape turns a decoded sketchShape
// into the proto envelope. Returns nil when the
// input is nil so the caller can pass through the
// empty-skeleton case without a special branch.
//
// The `optional int32 bpm` proto3 wrapper maps to
// the `*int32` Go pointer so a sketch without a BPM
// reports `bpm = null` rather than `bpm = 0`. The
// SPA renders "—" instead of "0" in that case.
func sketchPayloadFromShape(in *sketchShape) *v1.SketchPayload {
	if in == nil {
		return nil
	}
	out := &v1.SketchPayload{
		Model:             in.Model,
		SlotCount:         in.SlotCount,
		DeviceFingerprint: in.DeviceFingerprint,
		UploadedAtUnix:    in.UploadedAtUnix,
	}
	if in.BPM != nil {
		bpm := *in.BPM
		out.Bpm = &bpm
	}
	return out
}

