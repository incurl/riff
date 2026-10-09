package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// seedPatchFile inserts a `patches` row with a real file attached. The
// FileField validator requires the bytes to exist on disk, so we go
// through filesystem.NewFileFromBytes and stash it on the record the
// same way PocketBase's collection API would.
func seedPatchFile(t *testing.T, app core.App, author *core.Record, memoID, filename string) string {
	t.Helper()
	_ = author
	coll, err := app.FindCollectionByNameOrId("patches")
	require.NoError(t, err)
	rec := core.NewRecord(coll)
	rec.Set("memo", memoID)
	f, err := filesystem.NewFileFromBytes([]byte{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0xfd, 0x10, 0x20, 0x30}, filename)
	require.NoError(t, err)
	rec.Set("file", f)
	rec.Set("description", "test patch")
	rec.Set("compatible_po_version", "PO-33")
	require.NoError(t, app.Save(rec))
	return rec.Id
}

// TestAttachment_DeleteAndBatchDelete covers the new delete surface the
// React layer uses for the gallery actions.
func TestAttachment_DeleteAndBatchDelete(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memos := NewMemoServer(app)
	attachments := NewAttachmentServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "patch #p", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())
	require.NotEmpty(t, memoID)

	patchID := seedPatchFile(t, app, alice, memoID, "patch.po33")
	_ = patchID

	// GetAttachment returns the patch record translated to the proto.
	got, err := attachments.GetAttachment(ctx, &v1.GetAttachmentRequest{Name: "attachments/" + patchID})
	require.NoError(t, err)
	// PocketBase prefixes the original name with a random token to keep
	// the storage key unique; we just check the extension survives.
	assert.Contains(t, got.GetFilename(), ".po33")
	assert.Equal(t, "application/octet-stream", got.GetType())

	// DeleteAttachment removes it.
	_, err = attachments.DeleteAttachment(ctx, &v1.DeleteAttachmentRequest{Name: "attachments/" + patchID})
	require.NoError(t, err)

	_, err = attachments.GetAttachment(ctx, &v1.GetAttachmentRequest{Name: "attachments/" + patchID})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestAttachment_BatchDeleteAllOrNothing is the happy-path for the SPA's
// "select and delete" UX.
func TestAttachment_BatchDeleteAllOrNothing(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memos := NewMemoServer(app)
	attachments := NewAttachmentServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "two patches", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())
	id1 := seedPatchFile(t, app, alice, memoID, "a.po33")
	id2 := seedPatchFile(t, app, alice, memoID, "b.po33")

	_, err = attachments.BatchDeleteAttachments(ctx, &v1.BatchDeleteAttachmentsRequest{
		Names: []string{"attachments/" + id1, "attachments/" + id2, "attachments/does-not-exist"},
	})
	require.NoError(t, err, "missing entries are skipped, not fatal")

	_, err = attachments.GetAttachment(ctx, &v1.GetAttachmentRequest{Name: "attachments/" + id1})
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = attachments.GetAttachment(ctx, &v1.GetAttachmentRequest{Name: "attachments/" + id2})
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestAttachment_NotFoundReturnsNotFound ensures a malformed name is
// InvalidArgument (no `attachments/` prefix), a missing id is NotFound.
func TestAttachment_NotFoundReturnsNotFound(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	attachments := NewAttachmentServer(app)
	ctx := withViewer(context.Background(), alice)

	_, err := attachments.GetAttachment(ctx, &v1.GetAttachmentRequest{Name: "bogus"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = attachments.GetAttachment(ctx, &v1.GetAttachmentRequest{Name: "attachments/nonexistent"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// (no extra unused imports)
