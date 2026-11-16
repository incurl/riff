package flarum

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/stretchr/testify/require"
)

// avatarPNG is a 1x1 PNG, small enough to inline.
var avatarPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

// setAvatar attaches PocketBase's built-in `avatar` file to a user.
//
// The file is the SAME one the Memos SPA and the admin UI use: the forum
// keeps no avatar store of its own.
func setAvatar(t *testing.T, app core.App, record *core.Record) {
	t.Helper()

	file, err := filesystem.NewFileFromBytes(avatarPNG, "avatar.png")
	require.NoError(t, err)

	record.Set("avatar", file)
	require.NoError(t, app.Save(record), "the avatar should save")
}

// TestAvatar_URLIsTheModulesOwnRoute pins the URL the frontend is given.
//
// PocketBase's own `/api/files/users/{id}/{file}` URL lives at the root
// of the listener, which the forum does not own (and a reverse proxy in
// front of a sub-path install puts the static site there), so the forum
// streams the bytes through its own mount point.
func TestAvatar_URLIsTheModulesOwnRoute(t *testing.T) {
	app, handler := newTestForum(t)

	withoutAvatar := testUser(t, app, "plain")
	recorder := do(t, handler, http.MethodGet, "/forum/api/users/"+withoutAvatar.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes := attributesOf(t, recorder)
	require.Empty(t, attributes["avatarUrl"], "an account with no picture has no URL")
	require.Equal(t, false, attributes["hasUploadedAvatar"])

	withAvatar := testUser(t, app, "photo")
	setAvatar(t, app, withAvatar)

	recorder = do(t, handler, http.MethodGet, "/forum/api/users/"+withAvatar.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes = attributesOf(t, recorder)
	require.Equal(t, "/forum/avatars/"+withAvatar.Id, attributes["avatarUrl"],
		"the avatar URL should be the module's own route")
	require.Equal(t, true, attributes["hasUploadedAvatar"])
}

// TestAvatar_RouteServesPocketBasesFile pins the bytes.
func TestAvatar_RouteServesPocketBasesFile(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "photo")
	setAvatar(t, app, user)

	recorder := do(t, handler, http.MethodGet, "/forum/avatars/"+user.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "image/png", recorder.Header().Get("Content-Type"))
	require.Equal(t, avatarPNG, recorder.Body.Bytes(), "the route should serve the stored bytes")
}

// TestAvatar_RouteAnswers404WithoutAPicture pins the fallback.
//
// A 404 is what makes the frontend render the user's initials, so it is
// the contract, not an accident.
func TestAvatar_RouteAnswers404WithoutAPicture(t *testing.T) {
	app, handler := newTestForum(t)

	withoutAvatar := testUser(t, app, "plain")

	cases := []struct {
		name string
		id   string
	}{
		{"an account with no picture", withoutAvatar.Id},
		{"an unknown account", "nosuchuser123456"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := do(t, handler, http.MethodGet, "/forum/avatars/"+testCase.id, "", nil)
			require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
		})
	}
}

// TestAvatar_RouteIsPublic pins that an anonymous caller can load one.
//
// The forum shows author names on public pages, so a picture is no more
// sensitive than the name beside it — the same reasoning the wiki's
// avatar route records.
func TestAvatar_RouteIsPublic(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "photo")
	setAvatar(t, app, user)

	recorder := do(t, handler, http.MethodGet, "/forum/avatars/"+user.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, "no credentials are needed")
}

// attributesOf reads the attributes of a single-resource document.
func attributesOf(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")
	return attributes
}
