package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachmentsCollectionCreated pins the new
// `attachments` collection that replaces `patches` and
// `samples` (the `patches` + `samples` split was PO-33-only;
// upstream Memos stores every attachment in a single table).
//
// Every field on the new collection is asserted, the
// `memo` cascade is verified, and the
// `device_fingerprint_snapshot` / `compatible_po_version` /
// `motion_media` / `media_metadata` / `remote_url` additive
// columns are pinned (PO-33-only metadata that generic
// Memos clients ignore).
func TestAttachmentsCollectionCreated(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	require.NotNil(t, c)

	wantFields := []string{
		"memo", "blob", "filename", "mime_type", "size",
		"storage", "external_link", "description",
		"device_fingerprint_snapshot", "compatible_po_version",
		"motion_media", "media_metadata", "remote_url",
	}
	for _, f := range wantFields {
		assert.NotNilf(t, c.Fields.GetByName(f), "missing field %q", f)
	}

	// The `memo` relation must cascade on delete.
	memo := c.Fields.GetByName("memo").(*core.RelationField)
	assert.True(t, memo.CascadeDelete, "memo cascade must be true")

	// The `blob` field must be a single-file FileField
	// (not multi), matching upstream Memos's resource
	// shape.
	blob := c.Fields.GetByName("blob").(*core.FileField)
	assert.Equal(t, 1, blob.MaxSelect, "blob must be a single-file field")
	assert.Contains(t, blob.MimeTypes, "application/octet-stream",
		"blob must accept octet-stream (PO-33 patch uploads)")

	// Visibility rules inherit from the parent memo.
	assert.NotNil(t, c.ListRule)
	assert.NotNil(t, c.ViewRule)
	assert.NotNil(t, c.CreateRule)
	assert.NotNil(t, c.UpdateRule)
	assert.NotNil(t, c.DeleteRule)
}

// TestPatchesAndSamplesAreDropped pins the destructive
// final step: after migrations have run, the old
// `patches` and `samples` collections are gone.
func TestPatchesAndSamplesAreDropped(t *testing.T) {
	app := freshApp(t)
	for _, name := range []string{"patches", "samples"} {
		_, err := app.FindCollectionByNameOrId(name)
		assert.Errorf(t, err,
			"collection %q must be gone after the unified migration", name)
	}
}

// TestMigrationsAreIdempotent_Attachments pins the
// re-runnable property: calling RunAllMigrations twice
// on the same app must not error and must leave the
// collection shape stable.
func TestMigrationsAreIdempotent_Attachments(t *testing.T) {
	app := freshApp(t)
	// freshApp already ran migrations once. Re-run.
	require.NoError(t, app.RunAllMigrations())

	c, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotNil(t, c.Fields.GetByName("blob"),
		"second migration pass must not remove fields")
}
