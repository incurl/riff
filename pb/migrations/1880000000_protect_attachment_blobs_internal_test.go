package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachmentBlobFieldIsProtected pins the leak fix.
//
// PocketBase's native file server
// (`GET /api/files/{collection}/{recordId}/{filename}`) only
// evaluates the collection's `ViewRule` when the file field is
// marked `Protected` (see `apis/file.go::download`). The
// `attachments.ViewRule` is the memo-visibility rule, so an
// unprotected `blob` field let anybody download a PRIVATE
// memo's attachment.
//
// A fresh install must therefore create the field protected.
func TestAttachmentBlobFieldIsProtected(t *testing.T) {
	app := newTestApp(t)

	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)

	field := col.Fields.GetByName("blob")
	require.NotNil(t, field, "the attachments collection must have a blob field")

	fileField, ok := field.(*core.FileField)
	require.True(t, ok, "the blob field must be a FileField, got %T", field)
	assert.True(t, fileField.Protected,
		"the blob field must be Protected so the native file server evaluates the ViewRule")
}

// TestProtectAttachmentBlobsMigrationIsIdempotent runs the
// migration twice: the second run must be a no-op rather than
// an error, and the field must still be protected.
func TestProtectAttachmentBlobsMigrationIsIdempotent(t *testing.T) {
	app := newTestApp(t)

	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	fileField, ok := col.Fields.GetByName("blob").(*core.FileField)
	require.True(t, ok)
	require.True(t, fileField.Protected, "precondition: the field starts protected")

	// The migration is registered through `migrations.Register`
	// and already ran during `RunAllMigrations`. Running the
	// app's migration list a second time must not fail and
	// must not flip the flag back.
	require.NoError(t, app.RunAllMigrations())

	col, err = app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	fileField, ok = col.Fields.GetByName("blob").(*core.FileField)
	require.True(t, ok)
	assert.True(t, fileField.Protected,
		"the blob field must still be protected after a second migration run")
}
