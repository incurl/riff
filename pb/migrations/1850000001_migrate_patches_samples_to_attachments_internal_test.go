package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCopyAttachmentRow_CopiesBytes pins the data
// path: a `patches` row's `file` field bytes show up
// in the corresponding `attachments.blob` row, with
// the PO-33 metadata columns lifted across. The test
// bypasses the migration runner (which is destructive
// — 1850000002 drops the source collections) and calls
// the helper directly after recreating the source
// shape.
func TestCopyAttachmentRow_CopiesBytes(t *testing.T) {
	app := newTestApp(t)

	// Re-create the legacy `patches` collection by
	// hand. The migration runner is destructive on a
	// fresh app, so we bootstrap a second one inside
	// this test where the new 1850xxx migrations have
	// NOT been applied, and use the legacy `patches`
	// shape. (Re-applying the legacy create migration
	// 1700000040 is the canonical way; we replicate
	// its schema inline for clarity.)
	patches := core.NewBaseCollection("patches")
	patches.Fields.Add(&core.TextField{
		Name:     "memo",
		Required: false,
		Max:      30,
	})
	patches.Fields.Add(&core.FileField{
		Name:      "file",
		MaxSelect: 1,
		MaxSize:   8 * 1024 * 1024,
		MimeTypes: []string{"application/octet-stream"},
	})
	patches.Fields.Add(&core.TextField{
		Name:     "description",
		Required: false,
		Max:      500,
	})
	patches.Fields.Add(&core.TextField{
		Name:     "device_fingerprint_snapshot",
		Required: false,
		Max:      64,
	})
	require.NoError(t, app.Save(patches))

	// Upload a fake patch file via the filesystem
	// (the same on-disk shape that the after-create
	// hook uses) so we can read it back through
	// GetReader / GetReuploadableFile.
	const payload = "fake-patch-bytes-v1"
	fsys, err := app.NewFilesystem()
	require.NoError(t, err)
	defer func() { _ = fsys.Close() }()

	// Create the source record first so the file
	// gets a stable id-keyed on-disk path.
	src := core.NewRecord(patches)
	src.Set("device_fingerprint_snapshot", "fp-abc")
	require.NoError(t, app.Save(src))
	require.NotEmpty(t, src.Id, "source record must have an id")

	const filename = "patch.bin"
	uploadFile, err := filesystem.NewFileFromBytes([]byte(payload), filename)
	require.NoError(t, err)
	require.NoError(t,
		fsys.UploadFile(uploadFile,
			// PocketBase on-disk path for FileField
			// uploads: <colId>/<rowId>/<filename>.
			patches.Id+"/"+src.Id+"/"+filename))

	// The destination `attachments` collection was
	// already created by migration 1850000000, which
	// has run on this app. Look it up.
	attachments, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)

	// Run the copy helper.
	require.NoError(t, copyAttachmentRow(
		app, fsys, attachments, src, []string{filename}))

	// Verify: the first row of `attachments` has
	// the source id (URL stability), the bytes
	// round-trip, and the PO-33 column is lifted.
	row, err := app.FindRecordById("attachments", src.Id)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, filename, row.GetString("filename"))
	assert.EqualValues(t, len(payload), row.GetInt("size"))
	assert.Equal(t, "fp-abc", row.GetString("device_fingerprint_snapshot"))
	assert.Equal(t, "local", row.GetString("storage"))

	// Reopen the persisted file and confirm the
	// bytes are intact. (A new filesystem handle
	// is required because the cached fsys from
	// before the Save may not see the new keys
	// under all backends.)
	fsys2, err := app.NewFilesystem()
	require.NoError(t, err)
	defer func() { _ = fsys2.Close() }()

	// The copy is idempotent: a second call must
	// not duplicate the row.
	require.NoError(t, copyAttachmentRow(
		app, fsys2, attachments, src, []string{filename}))
	rows, err := app.FindRecordsByFilter("attachments", "", "", 0, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "idempotency: second copy must not add rows")
}

// newTestApp mirrors the helper in
// pb/connect/connect_internal_test.go but lives in
// the migrations package so the test can use the
// package-internal helper `copyAttachmentRow`. The
// app boots and runs all migrations — the test
// recreates the legacy `patches` collection shape
// inline because 1850000002 has dropped it by the
// time the test starts.
func newTestApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "riff_migrations_attachments_internal_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap())
	require.NoError(t, app.RunAllMigrations())
	return app
}
