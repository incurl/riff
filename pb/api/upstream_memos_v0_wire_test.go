package api

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/incurl/riff/proto/gen/api/v1"
)

// These tests pin the V0 response shaper against the
// shapes protojson *actually* emits.
//
// The shaper is a `map[string]any` translator: it reads
// the connect handler's JSON body field by field. Every
// field it reads has a proto type, and protojson's
// encoding of that type is not the JSON type you would
// guess:
//
//   - `google.protobuf.Timestamp` becomes an RFC 3339
//     *string* (`"2025-09-20T00:10:00Z"`), NOT the
//     `{"seconds": N}` message shape.
//   - `int64` becomes a *string* (`"15"`), because a
//     64-bit integer can exceed the precision of an
//     IEEE-754 double. (This is the JSON mapping rule
//     for 64-bit integral types.)
//
// The previous tests hand-wrote `{"seconds": 1700000000}`
// and a numeric `size`, which protojson never produces —
// so they passed while the live surface returned
// `createdTs: 0` (epoch) and `size: 0` for every row.
// Each test below therefore builds its input by running a
// real proto message through `protojson.Marshal` and then
// decoding it the way the handler decodes it, so the
// fixture cannot drift from the wire again.

// TestV0ProtojsonShapes pins the exact JSON encoding the
// shaper has to cope with, so a future proto field-type
// change surfaces here rather than as a silently-zeroed
// V0 response.
func TestV0ProtojsonShapes(t *testing.T) {
	ts := timestamppb.New(time.Unix(1700000000, 0).UTC())
	memoName := "memos/abc123"

	raw, err := protojson.Marshal(&apiv1.Attachment{
		Name:       "attachments/g6c5uwcilr1wk0j",
		Filename:   "photo.jpeg",
		Type:       "image/jpeg",
		Size:       15,
		Memo:       &memoName,
		CreateTime: ts,
	})
	require.NoError(t, err, "marshal Attachment")

	var body map[string]any
	require.NoError(t, protojson.Unmarshal(raw, &body),
		"decode Attachment the way the handler does")

	// A Timestamp is a string, not `{"seconds": N}`.
	assert.IsType(t, "", body["createTime"],
		"protojson must emit createTime as an RFC 3339 string; "+
			"if this changed the shaper needs a new branch")
	// An int64 is a string, not a float64.
	assert.IsType(t, "", body["size"],
		"protojson must emit int64 size as a JSON string")

	// Both must survive the shaper's readers.
	created, err := protoTimestampToUnix(body["createTime"])
	require.NoError(t, err)
	assert.EqualValues(t, 1700000000, created,
		"an RFC 3339 createTime must decode to Unix seconds")

	assert.EqualValues(t, 15, protoInt64(body["size"]),
		"a stringified int64 size must decode to the byte count")
}

// TestV0ShapeMemoCarriesCreationMetadata is the direct
// regression test for "MoeMemos is not getting memos
// metadata like date of creation": a memo built from a
// real protojson body must report a non-zero createdTs.
func TestV0ShapeMemoCarriesCreationMetadata(t *testing.T) {
	creator := "users/msmup3uy44r8xjz"
	created := timestamppb.New(time.Unix(1700000000, 0).UTC())
	updated := timestamppb.New(time.Unix(1700000042, 0).UTC())

	raw, err := protojson.Marshal(&apiv1.Memo{
		Name:       "memos/wdq85xegrxio2cs",
		Creator:    &creator,
		Content:    "hello from V0",
		CreateTime: created,
		UpdateTime: updated,
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, protojson.Unmarshal(raw, &body))

	shaped, err := v0ShapeMemo(body)
	require.NoError(t, err)

	assert.EqualValues(t, 1700000000, shaped["createdTs"],
		"V0 memo must carry its creation time as Unix seconds")
	assert.EqualValues(t, 1700000042, shaped["updatedTs"],
		"V0 memo must carry its update time as Unix seconds")
	assert.NotZero(t, shaped["createdTs"],
		"a memo with a real createTime must never surface as the epoch")
}

// TestV0ShapeMemoTimestampsZeroTimestamp guards the
// "epoch 1970" symptom from coming back through a
// different door: a memo genuinely created at the epoch
// is indistinguishable from a decode failure, so the
// shaper must still produce a *valid* (not NaN/negative)
// value rather than a garbage one.
func TestV0ShapeMemoTimestampsZeroTimestamp(t *testing.T) {
	shaped, err := v0ShapeMemo(map[string]any{
		"name":       "memos/wdq85xegrxio2cs",
		"content":    "no timestamps at all",
		"visibility": "PUBLIC",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 0, shaped["createdTs"],
		"a missing createTime degrades to 0, not a panic or a bogus date")
}

// TestV0ShapeResourceCarriesMetadata pins the resource
// side of the same bug: `size` (int64 → JSON string) and
// `memoId` (a resource name → a V0 Long) were both
// dropped on the floor, so MoeMemos saw zero-byte files
// with no owning memo.
func TestV0ShapeResourceCarriesMetadata(t *testing.T) {
	creator := "users/msmup3uy44r8xjz"
	memoName := "memos/wdq85xegrxio2cs"
	created := timestamppb.New(time.Unix(1700000000, 0).UTC())

	raw, err := protojson.Marshal(&apiv1.Attachment{
		Name:       "attachments/g6c5uwcilr1wk0j",
		Filename:   "photo.jpeg",
		Type:       "image/jpeg",
		Size:       4096,
		Memo:       &memoName,
		Creator:    &creator,
		CreateTime: created,
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, protojson.Unmarshal(raw, &body))

	shaped := v0ShapeResource(body)

	assert.EqualValues(t, 4096, shaped["size"],
		"V0 resource must report its real byte count")
	assert.Equal(t, "image/jpeg", shaped["type"])
	assert.EqualValues(t, 1700000000, shaped["createdTs"],
		"V0 resource must carry its creation time")

	// `memoId` used to be hardcoded to 0.
	memoV0ID := v0IDFromV1("wdq85xegrxio2cs")
	assert.EqualValues(t, memoV0ID, shaped["memoId"],
		"V0 resource must name the memo it is bound to")
	assert.NotZero(t, shaped["memoId"],
		"a bound resource must not report memoId 0")
}

// TestV0ShapeResourceUnlinkedMemoIDIsZero keeps the fix
// honest: an upload that has not been bound to a memo yet
// genuinely has no owner, and 0 is the truthful answer
// (it is the shaper's "missing" sentinel).
func TestV0ShapeResourceUnlinkedMemoIDIsZero(t *testing.T) {
	raw, err := protojson.Marshal(&apiv1.Attachment{
		Name:     "attachments/g6c5uwcilr1wk0j",
		Filename: "photo.jpeg",
		Type:     "image/jpeg",
		Size:     12,
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, protojson.Unmarshal(raw, &body))

	shaped := v0ShapeResource(body)
	assert.EqualValues(t, 0, shaped["memoId"],
		"an unlinked upload has no owning memo and must report 0")
}

// TestV0ShapeResourceUpdatedTsFallsBackToCreated covers
// the `Attachment` proto's missing `update_time`: V0
// clients sort resources by `updatedTs`, so a hard 0
// pushes every file to the epoch.
func TestV0ShapeResourceUpdatedTsFallsBackToCreated(t *testing.T) {
	created := timestamppb.New(time.Unix(1700000000, 0).UTC())
	raw, err := protojson.Marshal(&apiv1.Attachment{
		Name:       "attachments/g6c5uwcilr1wk0j",
		Filename:   "photo.jpeg",
		Size:       12,
		CreateTime: created,
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, protojson.Unmarshal(raw, &body))

	shaped := v0ShapeResource(body)
	assert.EqualValues(t, 1700000000, shaped["updatedTs"],
		"updatedTs must fall back to createTime, not the epoch")
}

// TestProtoInt64Coercions pins the reader the resource
// shaper relies on across every JSON shape it can be fed:
// a real protojson string, a `json.Number` from a
// `UseNumber` decode, and a float64 from a plain decode.
func TestProtoInt64Coercions(t *testing.T) {
	assert.EqualValues(t, 15, protoInt64("15"), "protojson string form")
	assert.EqualValues(t, 15, protoInt64(float64(15)), "float64 form")
	assert.EqualValues(t, 15, protoInt64(int64(15)), "native int64 form")
	assert.EqualValues(t, 0, protoInt64(nil), "absent value")
	assert.EqualValues(t, 0, protoInt64("not-a-number"), "garbage value")
}

// TestProtoTimestampCoercions pins both accepted
// timestamp shapes: the RFC 3339 string protojson emits,
// and the structured form the legacy hand-written tests
// pass in.
func TestProtoTimestampCoercions(t *testing.T) {
	got, err := protoTimestampToUnix("2023-11-14T22:13:20Z")
	require.NoError(t, err)
	assert.EqualValues(t, 1700000000, got)

	// Sub-second precision is truncated: V0 timestamps are
	// whole seconds.
	got, err = protoTimestampToUnix("2023-11-14T22:13:20.123456789Z")
	require.NoError(t, err)
	assert.EqualValues(t, 1700000000, got)

	// The structured shape stays supported for the
	// hand-built bodies the older unit tests feed in.
	got, err = protoTimestampToUnix(map[string]any{"seconds": float64(1700000000)})
	require.NoError(t, err)
	assert.EqualValues(t, 1700000000, got)

	// Garbage degrades to 0 rather than failing the list.
	got, err = protoTimestampToUnix("not-a-timestamp")
	require.NoError(t, err)
	assert.EqualValues(t, 0, got)

	got, err = protoTimestampToUnix(nil)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got)
}