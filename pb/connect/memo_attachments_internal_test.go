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

// memoAttachmentFixture inserts a patches or samples row attached to
// memoID, with a real on-disk file backing the FileField. PocketBase's
// FileField validator accepts a *filesystem.File but rejects a bare
// filename string, so we build a one-byte File via filesystem.NewFileFromBytes
// and assign it the same way the production upload pipeline does. The
// single-save form mirrors the seedPatchFile helper in
// connect_wave5_attachment_internal_test.go so the test fixture cannot
// drift from the field-level validation rules the running server enforces.
func memoAttachmentFixture(t *testing.T, app core.App, memoID, collection, filename string) *core.Record {
	t.Helper()
	col := requireCollection(t, app, collection)

	rec := core.NewRecord(col)
	rec.Set("memo", memoID)
	rec.Set("description", "test")

	// PocketBase's FileField validator runs MIME detection on the bytes
	// and rejects anything that does not match the collection's
	// MimeTypes allowlist. The patches collection only accepts
	// application/octet-stream, and the samples collection accepts
	// audio/{wav,x-wav,mpeg,mp3} or application/zip, so the byte payload
	// for the samples case starts with the RIFF/WAVE magic so the
	// detector resolves to audio/wav.
	payload := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0xfd, 0x10, 0x20, 0x30}
	if collection == "samples" {
		payload = append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), payload...)
	}
	f, err := filesystem.NewFileFromBytes(payload, filename)
	require.NoError(t, err)

	if collection == "patches" {
		rec.Set("file", f)
		rec.Set("compatible_po_version", "PO-33")
	} else {
		rec.Set("files", []*filesystem.File{f})
	}
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

	_ = memoAttachmentFixture(t, app, memoID, "patches", "a.po33")
	_ = memoAttachmentFixture(t, app, memoID, "samples", "b.wav")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), alice), &v1.SetMemoAttachmentsRequest{
		Name:        "memos/" + memoID,
		Attachments: nil,
	})
	require.NoError(t, err)

	for _, coll := range []string{"patches", "samples"} {
		rows, err := app.FindRecordsByFilter(coll, "memo={:id}", "", 0, 0, map[string]any{"id": memoID})
		require.NoError(t, err)
		assert.Empty(t, rows, "after SetMemoAttachments([]) the %s collection should be empty", coll)
	}
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

	p1 := memoAttachmentFixture(t, app, memoID, "patches", "a.po33")
	s1 := memoAttachmentFixture(t, app, memoID, "samples", "b.wav")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), alice), &v1.SetMemoAttachmentsRequest{
		Name: "memos/" + memoID,
		Attachments: []*v1.Attachment{
			{Name: "attachments/" + p1.Id, Filename: p1.GetString("file")},
			{Name: "attachments/" + s1.Id, Filename: s1.GetString("files")},
		},
	})
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter("patches", "memo={:id}", "", 0, 0, map[string]any{"id": memoID})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.Equal(t, p1.Id, rows[0].Id)

	srows, err := app.FindRecordsByFilter("samples", "memo={:id}", "", 0, 0, map[string]any{"id": memoID})
	require.NoError(t, err)
	assert.Len(t, srows, 1)
	assert.Equal(t, s1.Id, srows[0].Id)
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

	p1 := memoAttachmentFixture(t, app, memoID, "patches", "a.po33")

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

	pA := memoAttachmentFixture(t, app, memoA, "patches", "a.po33")

	_, err = srv.SetMemoAttachments(withViewer(context.Background(), alice), &v1.SetMemoAttachmentsRequest{
		Name: "memos/" + memoB,
		Attachments: []*v1.Attachment{
			{Name: "attachments/" + pA.Id},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	rows, err := app.FindRecordsByFilter("patches", "memo={:id}", "", 0, 0, map[string]any{"id": memoA})
	require.NoError(t, err)
	assert.Len(t, rows, 1, "foreign-memo rejection must not delete the original attachment")
}
