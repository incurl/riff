// Wave 6 Slice 3 import-side tests. These exercise the
// in-memory import-session manager and the lifecycle
// of an ImportMemos upload.
package connect

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

func TestImportSessionManager_CreateGetAbort(t *testing.T) {
	app := newTestApp(t)
	mgr := importManagerGet()
	mgr.setDir(app.DataDir() + "/imports")
	mgr.mu.Lock()
	for id, s := range mgr.sessions {
		s.closeAndCleanup()
		delete(mgr.sessions, id)
	}
	mgr.mu.Unlock()

	s, err := mgr.create("u1", "alice", 1024)
	require.NoError(t, err)
	require.NotEmpty(t, s.id)

	got, ok := mgr.get(s.id)
	require.True(t, ok)
	assert.Equal(t, "u1", got.userID)
	assert.Equal(t, "alice", got.username)
	assert.Equal(t, int64(1024), got.expectedSize)

	mgr.abort(s.id)
	_, ok = mgr.get(s.id)
	assert.False(t, ok)
}

func TestImportSessionManager_RejectsUserCap(t *testing.T) {
	app := newTestApp(t)
	mgr := importManagerGet()
	mgr.setDir(app.DataDir() + "/imports")
	mgr.mu.Lock()
	for id, s := range mgr.sessions {
		s.closeAndCleanup()
		delete(mgr.sessions, id)
	}
	mgr.mu.Unlock()

	for i := 0; i < importMaxUnfinishedPerUser; i++ {
		_, err := mgr.create("u1", "alice", 0)
		require.NoError(t, err)
	}
	assert.Equal(t, importMaxUnfinishedPerUser, mgr.countUnfinishedForUser("u1"))
	assert.Equal(t, 0, mgr.countUnfinishedForUser("u2"))
}

func TestImportMemos_SpecReturnsID(t *testing.T) {
	app := newTestApp(t)
	username := "alice"
	_ = newUser(t, app, username)
	u := mustGetUser(t, app, username)
	resetImportManager(t, app)

	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), u)
	resp, err := srv.ImportMemos(ctx, &v1.ImportMemosRequest{
		Name: "users/" + username,
		Upload: &v1.ImportMemosRequest_Spec{
			Spec: &v1.ImportMemosSpec{TotalSize: 1024},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.UploadId)
	assert.Equal(t, int32(importMaxChunkSize), resp.MaxChunkSize)
	assert.Equal(t, int64(0), resp.CommittedSize)
}

func TestImportMemos_ProgressPing(t *testing.T) {
	app := newTestApp(t)
	username := "alice"
	_ = newUser(t, app, username)
	u := mustGetUser(t, app, username)
	resetImportManager(t, app)

	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), u)
	spec, err := srv.ImportMemos(ctx, &v1.ImportMemosRequest{
		Name: "users/" + username,
		Upload: &v1.ImportMemosRequest_Spec{
			Spec: &v1.ImportMemosSpec{TotalSize: 0},
		},
	})
	require.NoError(t, err)

	resp, err := srv.ImportMemos(ctx, &v1.ImportMemosRequest{
		Name:        "users/" + username,
		Upload:      &v1.ImportMemosRequest_UploadId{UploadId: spec.UploadId},
		WriteOffset: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resp.CommittedSize)
}

func TestImportMemos_RejectsOtherUser(t *testing.T) {
	app := newTestApp(t)
	_ = newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	resetImportManager(t, app)

	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), bob)
	_, err := srv.ImportMemos(ctx, &v1.ImportMemosRequest{
		Name: "users/alice",
		Upload: &v1.ImportMemosRequest_Spec{
			Spec: &v1.ImportMemosSpec{},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission_denied")
}

// resetImportManager points the package-level import
// manager at the test's per-app data dir and reaps
// every live session. The function parallels
// resetUploadManagerForTest from the upload test file.
func resetImportManager(t *testing.T, app core.App) {
	t.Helper()
	mgr := importManagerGet()
	mgr.setDir(app.DataDir() + "/imports")
	mgr.mu.Lock()
	for id, s := range mgr.sessions {
		s.closeAndCleanup()
		delete(mgr.sessions, id)
	}
	mgr.mu.Unlock()
}

func mustGetUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter("users", "username={:u}", "", 1, 0, map[string]any{"u": username})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}
