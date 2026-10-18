// Wave 6 Slice 3 tests: the ExportMemos and
// ImportMemos round-trip. The tests pin the manifest
// shape, the safe-filename mapping, the JSON
// round-trip, and the end-to-end archive read after
// a fresh import.
package connect

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

func TestSafeFilename_Ascii(t *testing.T) {
	assert.Equal(t, "file.wav", safeFilename("file.wav"))
	assert.Equal(t, "_CON", safeFilename("CON"))
	assert.Equal(t, "_CON.txt", safeFilename("CON.txt"))
	assert.Equal(t, "file", safeFilename(""))
}

func TestSafeFilename_ReplacesPathSeps(t *testing.T) {
	assert.Equal(t, "sub_file.wav", safeFilename("sub/file.wav"))
	assert.Equal(t, "win_file.wav", safeFilename("win\\file.wav"))
	assert.Equal(t, "name_file.bin", safeFilename("name:file.bin"))
}

func TestSafeFilename_TrimsAndDots(t *testing.T) {
	assert.Equal(t, "file.wav", safeFilename("  file.wav  "))
	assert.Equal(t, "file.wav", safeFilename("...file.wav"))
	assert.Equal(t, "___", safeFilename("///"))
}

func TestGuessMimeFromFilename(t *testing.T) {
	cases := map[string]string{
		"foo.wav":    "audio/wav",
		"FOO.MP3":    "audio/mpeg",
		"x.zip":      "application/zip",
		"x.bin":      "application/octet-stream",
		"photo.jpg":  "image/jpeg",
		"photo.jpeg": "image/jpeg",
		"photo.png":  "image/png",
		"screen.pdf": "application/pdf",
	}
	for in, want := range cases {
		assert.Equal(t, want, guessMimeFromFilename(in), in)
	}
}

func TestExportMemos_EmptyArchiveRoundtrip(t *testing.T) {
	app := newTestApp(t)
	username := "alice"
	newUser(t, app, username)

	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), func() *core.Record {
		t.Helper()
		rows, err := app.FindRecordsByFilter("users", "username={:u}", "", 1, 0, map[string]any{"u": username})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0]
	}())

	resp, err := srv.ExportMemos(ctx, &v1.ExportMemosRequest{Name: "users/" + username})
	require.NoError(t, err)
	require.Equal(t, memoExportMediaType, resp.ContentType)
	require.NotEmpty(t, resp.Data)

	zr, err := zip.NewReader(bytes.NewReader(resp.Data), int64(len(resp.Data)))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"manifest.json"}, names)
}

func TestExportMemos_RejectsOtherUser(t *testing.T) {
	app := newTestApp(t)
	_ = newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), bob)
	_, err := srv.ExportMemos(ctx, &v1.ExportMemosRequest{Name: "users/alice"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission_denied")
}

func TestExportMemos_RequiresAuth(t *testing.T) {
	app := newTestApp(t)
	srv := NewUserServer(app)
	_, err := srv.ExportMemos(context.Background(), &v1.ExportMemosRequest{Name: "users/alice"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthenticated")
}

// TestExportMemos_RoundtripWithMemo creates a memo, exports it, and
// confirms the resulting archive contains the manifest plus the
// memo record and content file. The full ImportMemos round-trip
// lives in memo_import_internal_test.go.
func TestExportMemos_RoundtripWithMemo(t *testing.T) {
	app := newTestApp(t)
	username := "alice"
	newUser(t, app, username)
	u := mustGetUser(t, app, username)

	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(memos)
	rec.Set("author", u.Id)
	rec.Set("content", "hello world")
	rec.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(rec))

	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), u)
	resp, err := srv.ExportMemos(ctx, &v1.ExportMemosRequest{Name: "users/" + username})
	require.NoError(t, err)

	zr, err := zip.NewReader(bytes.NewReader(resp.Data), int64(len(resp.Data)))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "manifest.json")
	assert.Contains(t, names, "memos/"+rec.Id+".json")
	assert.Contains(t, names, "memos/"+rec.Id+".md")
}

func TestExportMemos_InvalidName(t *testing.T) {
	app := newTestApp(t)
	u := newUser(t, app, "alice")
	srv := NewUserServer(app)
	ctx := withViewer(context.Background(), u)
	_, err := srv.ExportMemos(ctx, &v1.ExportMemosRequest{Name: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_argument")
}
