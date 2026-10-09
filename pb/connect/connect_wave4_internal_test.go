// Wave 4 tests. Like connect_internal_test.go these live in package
// connect so they can build an authenticated context by writing the
// unexported ctxKeyAuthRecord key directly.
//
// The central behaviour under test is the record-request bridge in
// records.go: before Wave 4 the handlers called app.Save directly,
// which skipped every OnRecord*Request rule in pb/hooks. The schema
// marks memos.author and spaces.owner Required, so CreateMemo and
// CreateSpace did not merely lose their business rules — they failed
// outright. These tests register the real hooks and assert the rules
// run.
package connect

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/hooks"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// newHookedApp bootstraps a migrated app with riff's production hooks
// registered, which is what the running server has.
func newHookedApp(t *testing.T) core.App {
	t.Helper()
	app := newTestApp(t)
	hooks.Register(app)
	return app
}

// newSuperuser creates a record in the superusers collection, the only
// way to hold the ADMIN role in riff's schema.
func newSuperuser(t *testing.T, app core.App, email string) *core.Record {
	t.Helper()

	superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	require.NoError(t, err)

	rec := core.NewRecord(superusers)
	rec.Set("email", email)
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	return rec
}

// mustCreateSpace creates a space through the handler so the creator's
// ADMIN membership comes along, then returns the space's record id.
func mustCreateSpace(t *testing.T, app core.App, srv *SpaceServer, ctx context.Context, title string) string {
	t.Helper()

	space, err := srv.CreateSpace(ctx, &v1.CreateSpaceRequest{Space: &v1.Space{Title: title}})
	require.NoError(t, err)
	require.NotEmpty(t, space.GetName())

	rec, err := findSpaceByUID(app, spaceUIDFromName(space.GetName()))
	require.NoError(t, err)
	return rec.Id
}

// membershipRow loads the space_members row for a (space, user) pair.
func membershipRow(t *testing.T, app core.App, spaceID, userID string) *core.Record {
	t.Helper()

	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid}", "", 1, 0,
		map[string]any{"sid": spaceID, "uid": userID})
	require.NoError(t, err)
	require.Len(t, rows, 1, "expected exactly one membership row")
	return rows[0]
}

// memoRecordByID reloads a memo row by record id.
func memoRecordByID(t *testing.T, app core.App, id string) *core.Record {
	t.Helper()
	rec, err := app.FindRecordById("memos", id)
	require.NoError(t, err)
	return rec
}
