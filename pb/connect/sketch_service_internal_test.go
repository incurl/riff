package connect

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// newSketchMemo creates a memo with source=sketch
// and a payload.sketch sub-object. The memo is
// authored by the given user.
func newSketchMemo(t *testing.T, app core.App, userID, content string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", userID)
	rec.Set("content", content)
	rec.Set("visibility", "PRIVATE")
	rec.Set("type", "note")
	rec.Set("source", "sketch")
	// payload as a struct so the path mirrors
	// what the save hook writes.
	payload, err := types.JSONRaw(`{"property":{"title":"Sketch"},"location":{},"sketch":{"model":"PO-33","slot_count":16,"device_fingerprint":"abc","uploaded_at_unix":1730000000}}`).MarshalJSON()
	require.NoError(t, err)
	rec.Set("payload", json.RawMessage(payload))
	require.NoError(t, app.Save(rec))
	return rec
}

// TestListMySketches_EmptyByDefault pins the
// read-path happy state: a fresh user has no
// sketch memos.
func TestListMySketches_EmptyByDefault(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")

	out, err := srv.ListMySketches(withViewer(context.Background(), alice), &v1.ListMySketchesRequest{})
	require.NoError(t, err)
	require.NotNil(t, out.Memos)
	assert.Empty(t, out.Memos)
}

// TestListMySketches_OnlyMineWithSourceSketch pins
// the filter: alice's text notes do not surface
// (the memo's source must be "sketch"), and bob's
// sketch does not surface to alice.
func TestListMySketches_OnlyMineWithSourceSketch(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	// Alice has one sketch, one text note. Only
	// the sketch should surface.
	newSketchMemo(t, app, alice.Id, "sketch content")
	{
		col, err := app.FindCollectionByNameOrId("memos")
		require.NoError(t, err)
		note := core.NewRecord(col)
		note.Set("author", alice.Id)
		note.Set("content", "regular note")
		note.Set("visibility", "PRIVATE")
		note.Set("type", "note")
		note.Set("source", "text")
		require.NoError(t, app.Save(note))
	}
	// Bob has a sketch that should not surface.
	newSketchMemo(t, app, bob.Id, "bob sketch")

	out, err := srv.ListMySketches(withViewer(context.Background(), alice), &v1.ListMySketchesRequest{})
	require.NoError(t, err)
	require.Len(t, out.Memos, 1)
	assert.Equal(t, "sketch content", out.Memos[0].GetContent())
	require.NotNil(t, out.Memos[0].GetPayload().GetSketch())
	assert.Equal(t, "PO-33", out.Memos[0].GetPayload().GetSketch().GetModel())
}

// TestListMySketches_RequiresAuth pins the
// unauthenticated path.
func TestListMySketches_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app)
	alice := newUser(t, app, "alice")
	newSketchMemo(t, app, alice.Id, "sketch")

	_, err := srv.ListMySketches(context.Background(), &v1.ListMySketchesRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
