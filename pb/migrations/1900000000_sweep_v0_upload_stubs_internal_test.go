package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustCreateStubUser creates the user that owns the phantom rows below.
func mustCreateStubUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("username", username)
	rec.Set("email", username+"@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	return rec
}

// mustCreateStubMemo writes a `memos` row that mimics what the retired
// `v0CreateStubMemo` produced: the zero-width-space content sentinel and the
// `{"_v0_stub":true}` payload marker.
func mustCreateStubMemo(t *testing.T, app core.App, authorID string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("content", legacyStubMemoContent)
	rec.Set("visibility", "PRIVATE")
	rec.Set("author", authorID)
	rec.Set("payload", legacyStubPayload)
	require.NoError(t, app.Save(rec))
	return rec
}

// mustAttachFile writes an `attachments` row bound to `memoID`, the way the
// retired upload path left a user's file on a stub.
func mustAttachFile(t *testing.T, app core.App, memoID string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("filename", "photo.jpeg")
	rec.Set("type", "image/jpeg")
	rec.Set("memo", memoID)
	require.NoError(t, app.Save(rec))
	return rec
}

// TestSweepV0UploadStubs_DeletesStubsAndPreservesFiles is the core
// guarantee: the phantom memo rows are gone, but the user's uploaded bytes
// survive.
//
// This is the destructive edge the migration has to get right.
// `attachments.memo` is declared `CascadeDelete: true`, so deleting a stub
// that still holds a file would take the file with it. The sweep therefore
// unlinks each attachment (clearing `memo`, stamping `creator` with the
// stub's author) BEFORE deleting the stub, leaving an ordinary unlinked
// attachment the user can re-attach to a real memo.
func TestSweepV0UploadStubs_DeletesStubsAndPreservesFiles(t *testing.T) {
	app := freshApp(t)

	alice := mustCreateStubUser(t, app, "alice")

	stubWithFile := mustCreateStubMemo(t, app, alice.Id)
	held := mustAttachFile(t, app, stubWithFile.Id)

	emptyStub := mustCreateStubMemo(t, app, alice.Id)

	require.NoError(t, sweepV0UploadStubs(app))

	// The stubs are gone.
	for _, id := range []string{stubWithFile.Id, emptyStub.Id} {
		_, err := app.FindRecordById("memos", id)
		assert.Error(t, err,
			"stub memo %s must be deleted by the sweep", id)
	}

	// The file survived, unlinked, attributed to the uploader.
	rec, err := app.FindRecordById("attachments", held.Id)
	require.NoError(t, err,
		"the user's upload must NOT be cascade-deleted along with the stub")
	assert.Empty(t, rec.GetString("memo"),
		"the surviving attachment must be unlinked so no phantom memo owns it")
	assert.Equal(t, alice.Id, rec.GetString("creator"),
		"the surviving attachment must be attributed to the stub's author so it can be re-attached")
	assert.Equal(t, "photo.jpeg", rec.GetString("filename"),
		"the attachment's own fields must be untouched")
}

// TestSweepV0UploadStubs_LeavesRealMemosAlone pins the detection
// boundary. Both markers must match for a row to be treated as a stub: the
// content sentinel on its own is theoretically something a user could have
// submitted as a memo body, and the payload marker on its own is a
// user-writable JSON column. A real memo, and a sentinel-content memo
// without the payload marker, both survive.
func TestSweepV0UploadStubs_LeavesRealMemosAlone(t *testing.T) {
	app := freshApp(t)

	alice := mustCreateStubUser(t, app, "alice")

	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)

	// A genuinely real memo.
	real := core.NewRecord(memos)
	real.Set("content", "a real memo about a zero width space")
	real.Set("visibility", "PUBLIC")
	real.Set("author", alice.Id)
	require.NoError(t, app.Save(real))

	// A memo whose content IS the sentinel but which carries no stub
	// payload — not written by the stub creator, so not ours to delete.
	sentinelOnly := core.NewRecord(memos)
	sentinelOnly.Set("content", legacyStubMemoContent)
	sentinelOnly.Set("visibility", "PRIVATE")
	sentinelOnly.Set("author", alice.Id)
	require.NoError(t, app.Save(sentinelOnly))

	require.NoError(t, sweepV0UploadStubs(app))

	_, err = app.FindRecordById("memos", real.Id)
	assert.NoError(t, err, "a real memo must survive the sweep")

	_, err = app.FindRecordById("memos", sentinelOnly.Id)
	assert.NoError(t, err,
		"a sentinel-content memo without the stub payload marker must survive: the sweep requires both markers")
}

// TestSweepV0UploadStubs_IsIdempotent pins the re-runnable property. The
// sweep is a data migration, not a schema one, so it must be safe on the
// second pass — it finds no sentinel rows and does nothing.
func TestSweepV0UploadStubs_IsIdempotent(t *testing.T) {
	app := freshApp(t)

	alice := mustCreateStubUser(t, app, "alice")
	stub := mustCreateStubMemo(t, app, alice.Id)
	held := mustAttachFile(t, app, stub.Id)

	require.NoError(t, sweepV0UploadStubs(app))
	require.NoError(t, sweepV0UploadStubs(app),
		"a second sweep pass must be a no-op")

	rec, err := app.FindRecordById("attachments", held.Id)
	require.NoError(t, err,
		"a second sweep pass must not cascade-delete the file the first pass unlinked")
	assert.Empty(t, rec.GetString("memo"))
}
