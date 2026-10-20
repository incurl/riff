package connect

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/testutil"
)

// memoRecordWithPayload builds a `memos` collection + record in
// memory, with the supplied payload value already set on the
// record. The collection carries only the `payload` JSON field —
// the rest of the memos schema is irrelevant to
// memoPayloadFromRecord, which only reads the payload column.
//
// Constructing a record by hand (rather than going through
// `newTestApp` + the migration runner) keeps these tests
// independent of the rest of the schema. They are the only
// connector-side tests for Phase 3's payload plumbing that do not
// need a fully migrated test app, so they stay runnable even when
// a separate migration bug is preventing the rest of the suite
// from booting.
func memoRecordWithPayload(t *testing.T, payload any) *core.Record {
	t.Helper()
	col := core.NewBaseCollection("memos")
	col.Fields.Add(&core.JSONField{Name: "payload"})
	rec := core.NewRecord(col)
	rec.Id = "test-memo"
	rec.Set("payload", payload)
	return rec
}

// TestMemoPayloadFromRecord_DecodesJSONBytes pins the happy path:
// the hook writes the `payload` column as bytes (via
// types.JSONRaw), and the translator hands those bytes back to
// the proto envelope. HasLink/HasTaskList/Title must round-trip.
func TestMemoPayloadFromRecord_DecodesJSONBytes(t *testing.T) {
	raw := []byte(`{"property":{"has_link":true,"has_task_list":true,"has_code":false,"has_incomplete_tasks":true,"title":"Topper"},"location":{"placeholder":"","latitude":0,"longitude":0}}`)
	rec := memoRecordWithPayload(t, raw)

	got := memoPayloadFromRecord(rec)
	require.NotNil(t, got, "payload column must surface as MemoPayload")
	require.NotNil(t, got.Property, "Property sub-message must be populated")
	assert.True(t, got.Property.GetHasLink(), "HasLink must round-trip")
	assert.True(t, got.Property.GetHasTaskList(), "HasTaskList must round-trip")
	assert.False(t, got.Property.GetHasCode(), "HasCode must round-trip")
	assert.True(t, got.Property.GetHasIncompleteTasks(), "HasIncompleteTasks must round-trip")
	assert.Equal(t, "Topper", got.Property.GetTitle(), "Title must round-trip")
	require.NotNil(t, got.Location, "Location shell must be present even when empty")
	assert.Equal(t, "", got.Location.GetPlaceholder(), "empty Placeholder round-trips")
	assert.Equal(t, float64(0), got.Location.GetLatitude())
	assert.Equal(t, float64(0), got.Location.GetLongitude())
}

// TestMemoPayloadFromRecord_DecodesGoStruct covers the second
// shape the hook may hand back: a Go struct (rather than raw
// bytes) when the record was just produced by the same process.
// The translator re-marshals via JSON internally, so both inputs
// must reach the same proto output.
func TestMemoPayloadFromRecord_DecodesGoStruct(t *testing.T) {
	// Mirror the shape pb/hooks/memos.go writes.
	type propertyShape struct {
		HasLink            bool   `json:"has_link"`
		HasTaskList        bool   `json:"has_task_list"`
		HasCode            bool   `json:"has_code"`
		HasIncompleteTasks bool   `json:"has_incomplete_tasks"`
		Title              string `json:"title"`
	}
	type locationShape struct {
		Placeholder string  `json:"placeholder"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
	}
	type payloadShape struct {
		Property propertyShape `json:"property"`
		Location locationShape `json:"location"`
	}
	rec := memoRecordWithPayload(t, payloadShape{
		Property: propertyShape{
			HasLink:            true,
			HasTaskList:        false,
			HasCode:            true,
			HasIncompleteTasks: false,
			Title:              "FromGo",
		},
	})

	got := memoPayloadFromRecord(rec)
	require.NotNil(t, got)
	require.NotNil(t, got.Property)
	assert.True(t, got.Property.GetHasLink())
	assert.False(t, got.Property.GetHasTaskList())
	assert.True(t, got.Property.GetHasCode())
	assert.Equal(t, "FromGo", got.Property.GetTitle())
}

// TestMemoPayloadFromRecord_NilAndEmpty covers the no-op paths.
// A nil payload column, an empty byte slice, and the literal
// "null" all must return nil so the proto field stays unset
// (rather than serializing an empty envelope that the client
// would have to special-case).
func TestMemoPayloadFromRecord_NilAndEmpty(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		rec := memoRecordWithPayload(t, nil)
		assert.Nil(t, memoPayloadFromRecord(rec), "nil payload must map to nil MemoPayload")
	})
	t.Run("empty bytes", func(t *testing.T) {
		rec := memoRecordWithPayload(t, []byte{})
		assert.Nil(t, memoPayloadFromRecord(rec), "empty bytes must map to nil MemoPayload")
	})
	t.Run("json null", func(t *testing.T) {
		rec := memoRecordWithPayload(t, []byte("null"))
		assert.Nil(t, memoPayloadFromRecord(rec), "JSON null must map to nil MemoPayload")
	})
}

// TestRecordToMemo_PopulatesPayload is the higher-level smoke
// test: it asserts that the public translator wires the column
// through to Memo.Payload. The test builds a record in memory
// (so it does not depend on the migration layer) and verifies
// the resulting proto carries the same Property flags the
// hook would have materialised.
func TestRecordToMemo_PopulatesPayload(t *testing.T) {
	raw := []byte(`{"property":{"has_link":true,"has_task_list":false,"has_code":false,"has_incomplete_tasks":false,"title":"Hi"},"location":{}}`)

	col := core.NewBaseCollection("memos")
	col.Fields.Add(&core.JSONField{Name: "payload"})
	// The translator only reads from the record; the other
	// derived collections are nil-tolerant for empty inputs.
	rec := core.NewRecord(col)
	rec.Id = "abc"
	rec.Set("payload", raw)

	// A bootstrapped but un-migrated BaseApp is enough: the
	// translator's FindRecordsByFilter calls return errors that
	// are swallowed, so the absence of the `reactions`,
	// `memo_relations`, `patches`, and `samples` collections
	// just leaves those slices empty.
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "memos_translate_payload_test",
	})
	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed without migrations")
	testutil.DisableLogPersistence(app)
	t.Cleanup(func() { _ = app.ClearBootstrap() })

	got := recordToMemo(app, rec)
	require.NotNil(t, got)
	require.NotNil(t, got.Payload, "recordToMemo must surface MemoPayload when the column is set")
	require.NotNil(t, got.Payload.Property)
	assert.True(t, got.Payload.Property.GetHasLink(), "HasLink must flow through recordToMemo")
	assert.Equal(t, "Hi", got.Payload.Property.GetTitle(), "Title must flow through recordToMemo")
}
