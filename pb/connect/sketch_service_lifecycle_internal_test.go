package connect

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// newDraftSketchMemo creates a memo with
// `sketch` populated and state=DRAFT. The
// sketch_service_lifecycle tests need a
// deterministic DRAFT row to publish; the
// production path (the device-side upload
// handler) creates the same shape, but
// hand-rolling it here keeps the tests
// independent of the upload route.
//
// The pre-1860xxx schema used a `source` column
// to mark a memo as a sketch; the 1860xxx
// series dropped that column and uses the
// presence of the `sketch` JSON column as the
// discriminator instead.
func newDraftSketchMemo(t *testing.T, app core.App, userID string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", userID)
	rec.Set("content", "sketch content")
	rec.Set("visibility", "PRIVATE")
	rec.Set("state", "DRAFT")
	rec.Set("sketch", json.RawMessage(
		`{"model":"PO-33","slot_count":16}`))
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
	rec.Set("state", "NORMAL")
	rec.Set("sketch", json.RawMessage(
		`{"model":"PO-33","slot_count":16}`))
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
	srv := NewSketchServer(app, nil)
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
	srv := NewSketchServer(app, nil)
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
	srv := NewSketchServer(app, nil)
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
	rec.Set("state", "DRAFT")
	// A text memo carries no `sketch` column,
	// which is the 1860xxx-era sketch
	// discriminator.
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
	srv := NewSketchServer(app, nil)
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
	srv := NewSketchServer(app, nil)
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
