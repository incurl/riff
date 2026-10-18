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

// memoAttachmentFixture inserts a row in the unified
// `attachments` collection attached to memoID, with a
// real on-disk file backing the FileField. PocketBase's
// FileField validator accepts a *filesystem.File but
// rejects a bare filename string, so we build a one-byte
// File via filesystem.NewFileFromBytes and assign it the
// same way the production upload pipeline does. The
// single-save form mirrors the seedPatchFile helper in
// connect_wave5_attachment_internal_test.go so the test
// fixture cannot drift from the field-level validation
// rules the running server enforces.
func memoAttachmentFixture(t *testing.T, app core.App, memoID, collection, filename string) *core.Record {
	t.Helper()
	_ = collection // unified `attachments` only; kept for the call site readability
	col := requireCollection(t, app, "attachments")

	rec := core.NewRecord(col)
	rec.Set("memo", memoID)
	rec.Set("filename", filename)
	rec.Set("size", int64(len("test-bytes")))
	rec.Set("storage", "local")

	// PocketBase's FileField validator runs MIME
	// detection on the bytes and rejects anything
	// outside the unified `attachments` collection's
	// `blob` MimeTypes allow-list. The collection
	// accepts a wide range of file types, so a small
	// non-magic byte payload is fine for the unit
	// tests; the production upload pipeline is what
	// actually exercises the field's MIME rules.
	f, err := filesystem.NewFileFromBytes([]byte("test-bytes"), filename)
	require.NoError(t, err)
	rec.Set("blob", f)
	require.NoError(t, app.Save(rec))
	return rec
}

// requireCollection fetches a collection and fails the test if it is
// missing. The whole connect package assumes the riff schema is migrated,
// so the only branch worth asserting is the error path.
func requireCollection(t *testing.T, app core.App, name string) *core.Collection {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(name)
	require.NoError(t, err, "collection %q must exist", name)
	return col
}

// TestSetMemoAttachments_ReplaceClears pins the headline behaviour:
// passing an empty list clears every attachment on the memo.
func TestSetMemoAttachments_ReplaceClears(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	_ = memoAttachmentFixture(t, app, memoID, "attachments", "a.po33")
	_ = memoAttachmentFixture(t, app, memoID, "attachments", "b.wav")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), alice), &v1.SetMemoAttachmentsRequest{
		Name:        "memos/" + memoID,
		Attachments: nil,
	})
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter("attachments", "memo={:id}", "", 0, 0, map[string]any{"id": memoID})
	require.NoError(t, err)
	assert.Empty(t, rows, "after SetMemoAttachments([]) the attachments collection should be empty")
}

// TestSetMemoAttachments_PassesThroughSuppliedSet asserts the
// "replace, not append" semantics on the trivial happy path: keeping the
// supplied ids leaves the kept rows alone. We sign in as alice because
// she is the memo's author.
func TestSetMemoAttachments_PassesThroughSuppliedSet(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	p1 := memoAttachmentFixture(t, app, memoID, "attachments", "a.po33")
	s1 := memoAttachmentFixture(t, app, memoID, "attachments", "b.wav")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), alice), &v1.SetMemoAttachmentsRequest{
		Name: "memos/" + memoID,
		Attachments: []*v1.Attachment{
			{Name: "attachments/" + p1.Id},
			{Name: "attachments/" + s1.Id},
		},
	})
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter("attachments", "memo={:id}", "", 0, 0, map[string]any{"id": memoID})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
	got := map[string]string{}
	for _, r := range rows {
		got[r.Id] = r.GetString("filename")
	}
	assert.Equal(t, "a.po33", got[p1.Id])
	assert.Equal(t, "b.wav", got[s1.Id])
}

// TestSetMemoAttachments_AuthorOnly guards the author check: a non-author
// is rejected with CodePermissionDenied, an unauthenticated caller with
// CodeUnauthenticated.
func TestSetMemoAttachments_AuthorOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	p1 := memoAttachmentFixture(t, app, memoID, "attachments", "a.po33")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), bob), &v1.SetMemoAttachmentsRequest{
		Name: "memos/" + memoID,
		Attachments: []*v1.Attachment{
			{Name: "attachments/" + p1.Id},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = srv.SetMemoAttachments(context.Background(), &v1.SetMemoAttachmentsRequest{
		Name: "memos/" + memoID,
		Attachments: []*v1.Attachment{
			{Name: "attachments/" + p1.Id},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestSetMemoAttachments_RejectsForeignMemo guards the cross-memo
// rejection: an attachment that belongs to memo A cannot be rebound to
// memo B by SetMemoAttachments on B.
func TestSetMemoAttachments_RejectsForeignMemo(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	createdA, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "A", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoA := memoIDFromName(createdA.GetName())
	createdB, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "B", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoB := memoIDFromName(createdB.GetName())

	pA := memoAttachmentFixture(t, app, memoA, "attachments", "a.po33")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), alice), &v1.SetMemoAttachmentsRequest{
		Name: "memos/" + memoB,
		Attachments: []*v1.Attachment{
			{Name: "attachments/" + pA.Id},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	rows, err := app.FindRecordsByFilter("attachments", "memo={:id}", "", 0, 0, map[string]any{"id": memoA})
	require.NoError(t, err)
	assert.Len(t, rows, 1, "foreign-memo rejection must not delete the original attachment")
}
