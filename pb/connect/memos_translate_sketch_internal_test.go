package connect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoPayloadFromRecord_SketchBranch pins the
// happy path: a `payload` JSON column that includes
// a `sketch` sub-object round-trips into the
// MemoPayload.Sketch proto envelope. Every field
// must be present and equal.
func TestMemoPayloadFromRecord_SketchBranch(t *testing.T) {
	raw := []byte(`{"property":{"title":"Sketch"},"location":{"placeholder":"","latitude":0,"longitude":0},"sketch":{"model":"PO-33","slot_count":16,"bpm":120,"device_fingerprint":"ef3a91c2","uploaded_at_unix":1730000000}}`)
	rec := memoRecordWithPayload(t, raw)

	got := memoPayloadFromRecord(rec)
	require.NotNil(t, got)
	require.NotNil(t, got.Sketch, "sketch sub-object must surface as MemoPayload.Sketch")
	assert.Equal(t, "PO-33", got.Sketch.GetModel())
	assert.Equal(t, int32(16), got.Sketch.GetSlotCount())
	require.NotNil(t, got.Sketch.Bpm, "bpm must round-trip")
	assert.Equal(t, int32(120), *got.Sketch.Bpm)
	assert.Equal(t, "ef3a91c2", got.Sketch.GetDeviceFingerprint())
	assert.Equal(t, int64(1730000000), got.Sketch.GetUploadedAtUnix())
}

// TestMemoPayloadFromRecord_NoSketch pins the
// "non-sketch memo" path: a payload that does not
// include a `sketch` key returns a MemoPayload
// with the Sketch field unset. This is the common
// case today: every existing memo predates the
// PO-33 binding flow.
func TestMemoPayloadFromRecord_NoSketch(t *testing.T) {
	raw := []byte(`{"property":{"title":"Hello"},"location":{"placeholder":"","latitude":0,"longitude":0}}`)
	rec := memoRecordWithPayload(t, raw)

	got := memoPayloadFromRecord(rec)
	require.NotNil(t, got)
	assert.Nil(t, got.Sketch, "sketch must be nil when the JSON omits the key")
}

// TestMemoPayloadFromRecord_SketchWithNullBpm pins
// the optional-BPM path: a sketch without a BPM
// reports `bpm = nil` so the SPA renders "—"
// rather than "0". The `*int32` JSON tag and the
// `*int32` proto3 oneof are wired through; a
// missing key produces a nil pointer on both
// sides.
func TestMemoPayloadFromRecord_SketchWithNullBpm(t *testing.T) {
	raw := []byte(`{"property":{"title":"Sketch"},"location":{},"sketch":{"model":"PO-33","slot_count":16,"device_fingerprint":"abc","uploaded_at_unix":1}}`)
	rec := memoRecordWithPayload(t, raw)

	got := memoPayloadFromRecord(rec)
	require.NotNil(t, got)
	require.NotNil(t, got.Sketch)
	assert.Equal(t, "PO-33", got.Sketch.GetModel())
	assert.Nil(t, got.Sketch.Bpm, "Bpm must be nil when the JSON omits the key")
	// GetBpm() returns the proto3 zero-value (0)
	// for a nil pointer, distinct from a pointer
	// that explicitly points to 0. The SPA is
	// expected to use Bpm == nil as the test.
	assert.Equal(t, int32(0), got.Sketch.GetBpm())
}

// TestSketchPayloadFromShape_NilIsNil pins the
// pure-converter contract: a nil input returns a
// nil output so the caller can pass through the
// "sketch key absent" case without a special
// branch.
func TestSketchPayloadFromShape_NilIsNil(t *testing.T) {
	assert.Nil(t, sketchPayloadFromShape(nil))
}
