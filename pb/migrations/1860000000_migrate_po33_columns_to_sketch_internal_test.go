package migrations

import (
	"encoding/json"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSketchColumnAdded pins the new
// `memos.sketch` JSON column. The column must
// exist after migrations have run, and it must be
// optional (the empty `[]` / `null` shape is the
// default for a regular text memo).
func TestSketchColumnAdded(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)

	sketch := c.Fields.GetByName("sketch")
	require.NotNil(t, sketch, "memos.sketch must exist")
	_, ok := sketch.(*core.JSONField)
	require.True(t, ok, "memos.sketch must be a JSONField")
}

// TestRiffOnlyColumnsAreDropped pins the destructive
// half of the 1860xxx series. After M187 runs, the
// seven PO-33 flat columns plus `type`, `source`,
// `hidden_reason`, and `hidden_by` are all gone.
func TestRiffOnlyColumnsAreDropped(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)

	for _, name := range []string{
		"type", "bpm", "key", "sample_source",
		"difficulty", "license", "attribution",
		"video_url", "source", "hidden_reason", "hidden_by",
	} {
		assert.Nilf(t, c.Fields.GetByName(name),
			"memos.%s must be dropped", name)
	}
}

// TestPinnedColumnAdded pins the upstream Memos
// `pinned` field that the 1860xxx series adds.
func TestPinnedColumnAdded(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)

	pinned := c.Fields.GetByName("pinned")
	require.NotNil(t, pinned, "memos.pinned must exist")
	_, ok := pinned.(*core.BoolField)
	require.True(t, ok, "memos.pinned must be a BoolField")
}

// TestSketchColumnIdempotent pins the re-runnable
// property: calling RunAllMigrations twice on the
// same app must not error and must leave the
// column shape stable.
func TestSketchColumnIdempotent(t *testing.T) {
	app := freshApp(t)
	// freshApp already ran migrations once. Re-run.
	require.NoError(t, app.RunAllMigrations())

	c, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	require.NotNil(t, c.Fields.GetByName("sketch"),
		"second migration pass must not remove sketch")
	require.NotNil(t, c.Fields.GetByName("pinned"),
		"second migration pass must not remove pinned")
	for _, name := range []string{
		"type", "bpm", "key", "sample_source",
		"difficulty", "license", "attribution",
		"video_url", "source", "hidden_reason", "hidden_by",
	} {
		assert.Nilf(t, c.Fields.GetByName(name),
			"second migration pass must not re-add %s", name)
	}
}

// TestMergePo33IntoSketch exercises the backfill
// helper. The function is the load-bearing read
// step of M186; a regression here would silently
// drop user data during the upgrade. The cases:
//
//   - a row with the seven flat columns set:
//     the merge keeps every populated key.
//   - a row with only a `payload.sketch` sub-
//     object: the merge keeps the device fields
//     (model, slot_count, ...).
//   - a row with both: the flat-column values
//     win on overlapping keys (user-authored
//     metadata is the source of truth).
//   - a row with neither: the merge returns nil
//     and the migration skips the save.
func TestMergePo33IntoSketch(t *testing.T) {
	t.Run("flat columns merge", func(t *testing.T) {
		app := freshApp(t)
		rec := newMemoRecord(t, app,
			"alice", "flat cols memo")
		rec.Set("bpm", float64(120))
		rec.Set("key", "C minor")
		rec.Set("sample_source", "PO-33 K.O!")
		rec.Set("difficulty", "intermediate")
		rec.Set("license", "CC-BY-4.0")
		rec.Set("attribution", "Alice")
		rec.Set("video_url", "https://example.com")
		require.NoError(t, app.Save(rec))

		merged := mergePo33IntoSketch(rec)
		require.NotNil(t, merged)
		var got map[string]any
		require.NoError(t, json.Unmarshal(*merged, &got))
		assert.EqualValues(t, 120, got["bpm"])
		assert.Equal(t, "C minor", got["key"])
		assert.Equal(t, "PO-33 K.O!", got["sample_source"])
		assert.Equal(t, "intermediate", got["difficulty"])
		assert.Equal(t, "CC-BY-4.0", got["license"])
		assert.Equal(t, "Alice", got["attribution"])
		assert.Equal(t, "https://example.com", got["video_url"])
	})

	t.Run("payload sketch sub-object is preserved", func(t *testing.T) {
		app := freshApp(t)
		rec := newMemoRecord(t, app,
			"alice", "device upload memo")
		rec.Set("payload", json.RawMessage(
			`{"property":{},"location":{},"sketch":`+
				`{"model":"PO-33","slot_count":16,`+
				`"device_fingerprint":"EF3A91C2",`+
				`"uploaded_at_unix":1700000000}}`))
		require.NoError(t, app.Save(rec))

		merged := mergePo33IntoSketch(rec)
		require.NotNil(t, merged)
		var got map[string]any
		require.NoError(t, json.Unmarshal(*merged, &got))
		assert.Equal(t, "PO-33", got["model"])
		assert.EqualValues(t, 16, got["slot_count"])
		assert.Equal(t, "EF3A91C2", got["device_fingerprint"])
		assert.EqualValues(t, 1700000000, got["uploaded_at_unix"])
	})

	t.Run("flat column bpm overrides payload sketch bpm", func(t *testing.T) {
		app := freshApp(t)
		rec := newMemoRecord(t, app,
			"alice", "overlap memo")
		rec.Set("payload", json.RawMessage(
			`{"sketch":{"bpm":80,"model":"PO-33"}}`))
		rec.Set("bpm", float64(140))
		require.NoError(t, app.Save(rec))

		merged := mergePo33IntoSketch(rec)
		require.NotNil(t, merged)
		var got map[string]any
		require.NoError(t, json.Unmarshal(*merged, &got))
		// The user-authored `bpm` from the flat column
		// is the source of truth; it overwrites the
		// device-stamped value.
		assert.EqualValues(t, 140, got["bpm"])
		// Non-overlapping payload.sketch keys are kept.
		assert.Equal(t, "PO-33", got["model"])
	})

	t.Run("empty row returns nil", func(t *testing.T) {
		app := freshApp(t)
		rec := newMemoRecord(t, app,
			"alice", "empty memo")
		require.NoError(t, app.Save(rec))

		merged := mergePo33IntoSketch(rec)
		assert.Nil(t, merged,
			"a row with no PO-33 data must return nil")
	})

	t.Run("bpm zero is not treated as a value", func(t *testing.T) {
		// The legacy `bpm` column was a NumberField
		// with no required flag. A row written before
		// the column existed reads back as 0. The
		// backfill must not store a spurious
		// `bpm: 0` in the new column.
		app := freshApp(t)
		rec := newMemoRecord(t, app,
			"alice", "zero bpm memo")
		// bpm is left at the zero value.
		require.NoError(t, app.Save(rec))

		merged := mergePo33IntoSketch(rec)
		assert.Nil(t, merged,
			"a row with no PO-33 data and a 0 bpm must return nil")
	})
}

// newMemoRecord is a small constructor used by the
// mergePo33IntoSketch tests. It inserts a user
// (the `memos.author` relation is required; the
// PocketBase users collection has email and password
// as required text fields) and returns a fresh memo
// record.
func newMemoRecord(t *testing.T, app core.App, username, content string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	user := core.NewRecord(users)
	user.Set("username", username)
	user.Set("email", username+"@example.test")
	user.Set("password", "test-password-not-used")
	require.NoError(t, app.Save(user))

	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(memos)
	rec.Set("author", user.Id)
	rec.Set("content", content)
	rec.Set("visibility", "PRIVATE")
	return rec
}
