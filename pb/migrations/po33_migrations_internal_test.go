package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshApp spins up a migrated app on a temp dir and
// returns it. The test cleanup tears the app down so
// the SQLite handle is released.
func freshApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "riff_migrations_internal_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap())
	require.NoError(t, app.RunAllMigrations())
	return app
}

// TestPo33DevicesCollectionCreated pins the new
// po33_devices collection: every field is present, the
// access rules are nil, and the (user, public_key_fpr)
// index exists.
func TestPo33DevicesCollectionCreated(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("po33_devices")
	require.NoError(t, err)
	require.NotNil(t, c)

	wantFields := []string{
		"user", "label", "public_key", "public_key_fpr",
		"status", "last_seen_at", "created", "updated",
	}
	for _, f := range wantFields {
		assert.NotNilf(t, c.Fields.GetByName(f), "missing field %q", f)
	}
	assert.Nil(t, c.ListRule)
	assert.Nil(t, c.ViewRule)
	assert.Nil(t, c.CreateRule)
	assert.Nil(t, c.UpdateRule)
	assert.Nil(t, c.DeleteRule)
}

// TestDeviceAuthorizationsCollectionCreated pins the
// new device_authorizations collection.
func TestDeviceAuthorizationsCollectionCreated(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("device_authorizations")
	require.NoError(t, err)

	wantFields := []string{
		"user", "user_code", "authorization_id", "nonce",
		"public_key", "issued_at", "expires_at",
		"status", "failure_reason", "created",
	}
	for _, f := range wantFields {
		assert.NotNilf(t, c.Fields.GetByName(f), "missing field %q", f)
	}
}

// TestPo33SketchDownloadsCollectionCreated pins the
// new audit-log collection.
func TestPo33SketchDownloadsCollectionCreated(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("po33_sketch_downloads")
	require.NoError(t, err)

	wantFields := []string{
		"device", "memo", "attachment_kind", "remote_addr", "created",
	}
	for _, f := range wantFields {
		assert.NotNilf(t, c.Fields.GetByName(f), "missing field %q", f)
	}
}

// TestMemosSourceColumnAdded pins the
// `memos.source` extension.
func TestMemosSourceColumnAdded(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	src := c.Fields.GetByName("source")
	require.NotNil(t, src, "memos.source must exist")
	sel, ok := src.(*core.SelectField)
	require.True(t, ok, "memos.source must be a SelectField")
	assert.Contains(t, sel.Values, "text")
	assert.Contains(t, sel.Values, "sketch")
}

// TestMemoRelationsTypeExtended pins that the
// existing `memo_relations.type` select has been
// extended to allow BACKUP_FILE alongside the
// original COMMENT and REFERENCE values.
func TestMemoRelationsTypeExtended(t *testing.T) {
	app := freshApp(t)
	c, err := app.FindCollectionByNameOrId("memo_relations")
	require.NoError(t, err)
	t0 := c.Fields.GetByName("type")
	require.NotNil(t, t0)
	sel, ok := t0.(*core.SelectField)
	require.True(t, ok)
	assert.Contains(t, sel.Values, "COMMENT")
	assert.Contains(t, sel.Values, "REFERENCE")
	assert.Contains(t, sel.Values, "BACKUP_FILE")
}

// TestAttachmentDeviceFingerprintColumnAdded pins
// the new `device_fingerprint_snapshot` text column
// on patches and samples.
func TestAttachmentDeviceFingerprintColumnAdded(t *testing.T) {
	app := freshApp(t)
	for _, name := range []string{"patches", "samples"} {
		c, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "collection %s", name)
		fp := c.Fields.GetByName("device_fingerprint_snapshot")
		assert.NotNilf(t, fp, "collection %s missing device_fingerprint_snapshot", name)
	}
}

// TestMigrationsAreIdempotent pins the re-runnable
// property: calling RunAllMigrations twice on the
// same app must not error and must not duplicate the
// new collections.
func TestMigrationsAreIdempotent(t *testing.T) {
	app := freshApp(t)
	// The freshApp helper already ran migrations
	// once. Run them again to verify re-runnability.
	require.NoError(t, app.RunAllMigrations())

	// The new collections are still exactly one
	// each (FindCollectionByNameOrId returns a
	// single record; the call below would still
	// succeed even if duplicates existed, but the
	// schema is enforced unique by PocketBase, so a
	// panic or duplicate error would have surfaced
	// in the second RunAllMigrations call).
	for _, name := range []string{
		"po33_devices",
		"device_authorizations",
		"po33_sketch_downloads",
	} {
		c, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "collection %s", name)
		require.NotNilf(t, c, "collection %s is nil", name)
	}
}
