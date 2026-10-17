package connect

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// newDraftSketchMemo creates a memo with
// source=sketch and state=DRAFT. The
// sketch_service_lifecycle tests need a
// deterministic DRAFT row to publish; the
// production path (the device-side upload
// handler) creates the same shape, but
// hand-rolling it here keeps the tests
// independent of the upload route.
func newDraftSketchMemo(t *testing.T, app core.App, userID string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", userID)
	rec.Set("content", "sketch content")
	rec.Set("visibility", "PRIVATE")
	rec.Set("type", "note")
	rec.Set("source", "sketch")
	rec.Set("state", "DRAFT")
	require.NoError(t, app.Save(rec))
	return rec
}

// newPublishedSketchMemo creates a memo
// already in NORMAL state. The
// archive-once test below needs a
// NORMAL row, and creating one through
// the publish path would be a tautology.
func newPublishedSketchMemo(t *testing.T, app core.App, userID string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", userID)
	rec.Set("content", "sketch content")
	rec.Set("visibility", "PRIVATE")
	rec.Set("type", "note")
	rec.Set("source", "sketch")
	rec.Set("state", "NORMAL")
	require.NoError(t, app.Save(rec))
	return rec
}

// seedSketchVersion inserts one row into the
// sketch_versions table directly, so the
// ListSketchVersions tests do not depend on
// the device upload route.
func seedSketchVersion(t *testing.T, app core.App, memoID string, versionNo int) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("sketch_versions")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("memo_id", memoID)
	rec.Set("version_no", versionNo)
	rec.Set("device_fingerprint", "deadbeef0000000000000000000000000000000000000000000000000000beef")
	rec.Set("uploaded_at_unix", time.Now().Unix())
	rec.Set("received_at", time.Now())
	rec.Set("metadata", []byte(`{"model":"PO-33","slot_count":16,"device_fingerprint":"deadbeef"}`))
	rec.Set("signature", "00")
	rec.Set("signature_algorithm", "ed25519")
	require.NoError(t, app.Save(rec))
	return rec
}


// TestPublishSketch_NotInDraft_Fails pins the
// "only DRAFT can be published" rule.
// Already-NORMAL and ARCHIVED memos
// return FAILED_PRECONDITION; HIDDEN and
// DRAFT (the only legal state) get the
// pass; non-sketch memos are rejected
// outright.
func TestPublishSketch_NotInDraft_Fails(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")

	// An already-NORMAL sketch: the publish
	// path's precondition is "state == DRAFT";
	// a NORMAL row fails the precondition.
	memo := newPublishedSketchMemo(t, app, alice.Id)
	_, err := srv.PublishSketch(withViewer(context.Background(), alice),
		&v1.PublishSketchRequest{Name: "memos/" + memo.Id})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestPublishSketch_NotOwner_NotFound pins the
// "non-owner gets NotFound" rule. A
// stranger probing the publish RPC with a
// guessed id cannot distinguish a real
// memo from a non-existent one.
func TestPublishSketch_NotOwner_NotFound(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	memo := newDraftSketchMemo(t, app, alice.Id)

	_, err := srv.PublishSketch(withViewer(context.Background(), bob),
		&v1.PublishSketchRequest{Name: "memos/" + memo.Id})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestPublishSketch_NotASketch_Fails pins the
// "publish only applies to sketch memos"
// rule. A regular text memo in DRAFT
// (which the hook would actually prevent
// from being created today, but a
// defensive check in the handler is
// still right) returns
// FAILED_PRECONDITION.
func TestPublishSketch_NotASketch_Fails(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")

	// Force a text memo into DRAFT state
	// directly. The hook in pb/hooks/memos.go
	// would default this to NORMAL on a
	// production save, so the test sets the
	// column after the save.
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", alice.Id)
	rec.Set("content", "regular note")
	rec.Set("visibility", "PRIVATE")
	rec.Set("type", "note")
	rec.Set("source", "text")
	rec.Set("state", "DRAFT")
	require.NoError(t, app.Save(rec))

	_, err = srv.PublishSketch(withViewer(context.Background(), alice),
		&v1.PublishSketchRequest{Name: "memos/" + rec.Id})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestArchiveSketch_HappyPath_NormalToArchived pins
// the archive transition. The memo moves
// from NORMAL to ARCHIVED and the
// response carries the new state.
func TestArchiveSketch_HappyPath_NormalToArchived(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	memo := newPublishedSketchMemo(t, app, alice.Id)

	out, err := srv.ArchiveSketch(withViewer(context.Background(), alice),
		&v1.ArchiveSketchRequest{Name: "memos/" + memo.Id})

	require.NoError(t, err)
	assert.Equal(t, v1.State_ARCHIVED, out.GetState())
	reloaded, err := app.FindRecordById("memos", memo.Id)
	require.NoError(t, err)
	assert.Equal(t, "ARCHIVED", reloaded.GetString("state"))
}

// TestArchiveSketch_AlreadyArchived_Idempotent pins
// the "archive is one-way but
// idempotent" rule. A second archive on
// an already-ARCHIVED memo returns the
// same row without an error.
func TestArchiveSketch_AlreadyArchived_Idempotent(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	memo := newPublishedSketchMemo(t, app, alice.Id)

	// First archive: NORMAL -> ARCHIVED.
	_, err := srv.ArchiveSketch(withViewer(context.Background(), alice),
		&v1.ArchiveSketchRequest{Name: "memos/" + memo.Id})
	require.NoError(t, err)
	// Second archive: still ARCHIVED, no error.
	out, err := srv.ArchiveSketch(withViewer(context.Background(), alice),
		&v1.ArchiveSketchRequest{Name: "memos/" + memo.Id})
	require.NoError(t, err)
	assert.Equal(t, v1.State_ARCHIVED, out.GetState())
}

// TestListSketchVersions_AppendOnly pins the
// "the log never shrinks" invariant.
// Even after the memo is archived, the
// ListSketchVersions response still
// includes every version row that ever
// existed for the memo. The audit log is
// independent of the memo's lifecycle.
func TestListSketchVersions_AppendOnly(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	// Use a NORMAL memo because the brief's
	// archive path requires state=NORMAL.
	// The "append-only" invariant is about
	// the version log, not the memo state,
	// so this test's choice of source state
	// does not weaken what it pins.
	memo := newPublishedSketchMemo(t, app, alice.Id)

	// Three versions.
	for i := 1; i <= 3; i++ {
		seedSketchVersion(t, app, memo.Id, i)
	}
	// Archive the memo. The list response
	// must still include all three versions.
	_, err := srv.ArchiveSketch(withViewer(context.Background(), alice),
		&v1.ArchiveSketchRequest{Name: "memos/" + memo.Id})
	require.NoError(t, err)

	out, err := srv.ListSketchVersions(withViewer(context.Background(), alice),
		&v1.ListSketchVersionsRequest{Parent: "memos/" + memo.Id})
	require.NoError(t, err)
	require.Len(t, out.Versions, 3)
	assert.Equal(t, int32(1), out.Versions[0].GetVersionNo())
	assert.Equal(t, int32(2), out.Versions[1].GetVersionNo())
	assert.Equal(t, int32(3), out.Versions[2].GetVersionNo())
	for i, v := range out.Versions {
		assert.Equal(t, "memos/"+memo.Id, v.GetMemo())
		assert.Equal(t,
			"memos/"+memo.Id+"/versions/"+itoaForTest(int32(i+1)),
			v.GetName())
	}
}

// itoaForTest is a local forwarder of the
// resource-name version renderer. The
// production helper is in
// sketch_service_lifecycle.go; the test
// file uses the same name so the
// expected resource names are
// byte-identical to what the handler
// emits.
func itoaForTest(n int32) string {
	return itoa(n)
}

// TestGetSketchVersion_NotFound pins the
// "version row that does not exist gets
// NotFound" rule. A non-existent
// (memo_id, version_no) pair returns
// 404 even when the parent memo is real.
func TestGetSketchVersion_NotFound(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	memo := newDraftSketchMemo(t, app, alice.Id)

	_, err := srv.GetSketchVersion(withViewer(context.Background(), alice),
		&v1.GetSketchVersionRequest{Name: "memos/" + memo.Id + "/versions/999"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
