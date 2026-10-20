package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddAttachmentCreatorColumn_AddsRelation pins the new
// `creator` column that the `UploadAttachment` server uses to
// stamp orphan uploads (the row's `memo` is empty until the SPA
// rebinds the row via `CreateMemo`/`UpdateMemo` — see
// `pb/connect/memo_bind_attachments.go`). The migration is
// idempotent: re-running it on a partial install must be a no-op
// rather than fail with "field already exists".
func TestAddAttachmentCreatorColumn_AddsRelation(t *testing.T) {
	app := freshApp(t)
	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)

	field := col.Fields.GetByName("creator")
	require.NotNil(t, field, "creator column must exist on the attachments collection")

	rel, ok := field.(*core.RelationField)
	require.True(t, ok, "creator must be a relation field, got %T", field)
	assert.Equal(t, 1, rel.MaxSelect, "creator must be a single-user relation")
	assert.Equal(t, 0, rel.MinSelect, "creator must be optional (the column stays null when the upload supplied a memo)")
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	assert.Equal(t, users.Id, rel.CollectionId, "creator must point at the users collection")
}

// TestAddAttachmentCreatorColumn_IsIdempotent pins the
// re-runnable property: re-running migrations on an already
// migrated app leaves the column shape stable.
func TestAddAttachmentCreatorColumn_IsIdempotent(t *testing.T) {
	app := freshApp(t)
	require.NoError(t, app.RunAllMigrations(), "re-running migrations must not fail when the column already exists")

	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	field := col.Fields.GetByName("creator")
	require.NotNil(t, field, "creator column must still be present after re-running migrations")
}
