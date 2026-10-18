package connect

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoRecordWithSketch builds a `memos` collection +
// record in memory, with the supplied sketch value
// already set on the record. The collection carries
// only the `sketch` JSON field — the rest of the
// memos schema is irrelevant to memoSketchFromRecord
// / memoPayloadFromRecord, which only read the
// sketch column. (The 1860xxx series moved the
// sketch data out of the `payload.sketch` sub-object
// and into a dedicated `memos.sketch` JSON column;
// the test mirrors that.)
func memoRecordWithSketch(t *testing.T, sketch any) *core.Record {
	t.Helper()
	col := core.NewBaseCollection("memos")
	col.Fields.Add(&core.JSONField{Name: "sketch"})
	rec := core.NewRecord(col)
	rec.Id = "test-memo"
	rec.Set("sketch", sketch)
	return rec
}

// TestMemoPayloadFromRecord_SketchBranch pins the
// happy path: a `sketch` JSON column on the row
// round-trips into the MemoPayload.Sketch proto
// envelope. Every field must be present and equal.
//
// The 1860xxx series moved the sketch data out
// of the `payload.sketch` sub-object and into a
// dedicated top-level column. The test now writes
// the sketch to the new column and verifies the
// translator populates `MemoPayload.Sketch` from
// it.
func TestMemoPayloadFromRecord_SketchBranch(t *testing.T) {
	raw := []byte(`{"model":"PO-33","slot_count":16,"bpm":120,"device_fingerprint":"ef3a91c2","uploaded_at_unix":1730000000}`)
	rec := memoRecordWithSketch(t, raw)

	sk := memoSketchFromRecord(rec)
	require.NotNil(t, sk, "sketch column must surface as MemoPayload.Sketch")
	assert.Equal(t, "PO-33", sk.GetModel())
	assert.Equal(t, int32(16), sk.GetSlotCount())
	require.NotNil(t, sk.Bpm, "bpm must round-trip")
	assert.Equal(t, int32(120), *sk.Bpm)
	assert.Equal(t, "ef3a91c2", sk.GetDeviceFingerprint())
	assert.Equal(t, int64(1730000000), sk.GetUploadedAtUnix())
}

// TestMemoPayloadFromRecord_NoSketch pins the
// "non-sketch memo" path: a memo with no sketch
// column returns nil from memoSketchFromRecord
// so the proto MemoPayload.Sketch stays unset.
// This is the common case today: every existing
// text memo predates the PO-33 binding flow.
func TestMemoPayloadFromRecord_NoSketch(t *testing.T) {
	rec := memoRecordWithSketch(t, nil)
	assert.Nil(t, memoSketchFromRecord(rec),
		"sketch must be nil when the column is missing")
}

// TestMemoPayloadFromRecord_SketchWithNullBpm pins
// the optional-BPM path: a sketch without a BPM
// reports `bpm = nil` so the SPA renders "—"
// rather than "0". The `*int32` JSON tag and the
// `*int32` proto3 oneof are wired through; a
// missing key produces a nil pointer on both
// sides.
func TestMemoPayloadFromRecord_SketchWithNullBpm(t *testing.T) {
	raw := []byte(`{"model":"PO-33","slot_count":16,"device_fingerprint":"abc","uploaded_at_unix":1}`)
	rec := memoRecordWithSketch(t, raw)

	sk := memoSketchFromRecord(rec)
	require.NotNil(t, sk)
	assert.Equal(t, "PO-33", sk.GetModel())
	assert.Nil(t, sk.Bpm, "Bpm must be nil when the JSON omits the key")
	// GetBpm() returns the proto3 zero-value (0)
	// for a nil pointer, distinct from a pointer
	// that explicitly points to 0. The SPA is
	// expected to use Bpm == nil as the test.
	assert.Equal(t, int32(0), sk.GetBpm())
}

// TestMemoPayloadFromRecord_NullSketchColumn pins
// the "sketch column is JSON null" path: a row
// saved without the column populated reads back
// as a non-nil `types.JSONRaw` containing the
// literal four characters "null". The translator
// must reject that shape the same way it rejects
// a nil column.
func TestMemoPayloadFromRecord_NullSketchColumn(t *testing.T) {
	rec := memoRecordWithSketch(t, []byte("null"))
	assert.Nil(t, memoSketchFromRecord(rec),
		"JSON null must map to nil sketch")
}

// TestSketchPayloadFromShape_NilIsNil pins the
// pure-converter contract: a nil input returns a
// nil output so the caller can pass through the
// "sketch key absent" case without a special
// branch.
func TestSketchPayloadFromShape_NilIsNil(t *testing.T) {
	assert.Nil(t, sketchPayloadFromShape(nil))
}

// TestMemoPayloadFromRecord_VersionNoRoundTrip pins
// the 2026-10-19 addition of `version_no` to
// the `sketch` JSON column. A row with a
// non-zero `version_no` reads back as
// `SketchPayload.version_no`; a pre-2026-10-19
// row that omits the key reads back as 0 (the
// SPA's "no v-badge" discriminator). The
// `omitempty` tag on `VersionNo` in sketchShape
// is the load-bearing bit: without it, an
// older row would marshal to `version_no: 0`
// and overwrite a missing key with a 0.
func TestMemoPayloadFromRecord_VersionNoRoundTrip(t *testing.T) {
	raw := []byte(`{"model":"PO-33","slot_count":16,"device_fingerprint":"abc","uploaded_at_unix":1,"version_no":7}`)
	rec := memoRecordWithSketch(t, raw)
	sk := memoSketchFromRecord(rec)
	require.NotNil(t, sk)
	assert.Equal(t, int32(7), sk.GetVersionNo(),
		"version_no from the sketch JSON column must surface on the proto")
}

// TestMemoPayloadFromRecord_NoVersionNoIsZero pins
// the legacy-row path: a sketch row that was
// written before 2026-10-19 (or by a back-fill
// migration that did not synthesise a value)
// has no `version_no` key in the JSON. The
// translator must read 0 (not panic, not
// surface a sentinel) so the SPA can render
// "no v-badge" for that case.
func TestMemoPayloadFromRecord_NoVersionNoIsZero(t *testing.T) {
	raw := []byte(`{"model":"PO-33","slot_count":16,"device_fingerprint":"abc","uploaded_at_unix":1}`)
	rec := memoRecordWithSketch(t, raw)
	sk := memoSketchFromRecord(rec)
	require.NotNil(t, sk, "a legacy row without version_no is still a valid sketch")
	assert.Equal(t, int32(0), sk.GetVersionNo(),
		"missing version_no key must read back as 0")
}
