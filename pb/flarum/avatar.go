package flarum

import (
	"github.com/pocketbase/pocketbase/core"
)

const (
	// avatarFieldName is PocketBase's built-in file field on the `users`
	// collection. riff deliberately leaves that field alone
	// (pb/migrations/1700000001_extend_users.go), so the forum's profile
	// picture is simply *that* file: one avatar per account, shared with
	// the Memos SPA, the admin UI and the wiki. The forum keeps no avatar
	// store of its own.
	avatarFieldName = "avatar"

	// avatarUsersCollection is the collection avatars are read from.
	// PocketBase's `_superusers` collection has no avatar field, so a
	// superuser has no picture and the frontend falls back to initials.
	avatarUsersCollection = "users"
)

// avatarHandler serves GET {basePath}/avatars/{id}.
//
// The route exists because the frontend needs a URL it can put in an
// `<img src>`: PocketBase keeps the file itself, and its own
// `/api/files/users/{id}/{file}` URL lives at the root of the riff
// listener. The forum is mounted at a prefix (and behind a reverse proxy
// under a sub-path layout), so that root URL would be answered by
// whatever owns the root — the static site, in the `/community/` layout.
// Streaming the bytes here keeps every URL the frontend builds relative
// to the mount point, which is the same reason the wiki and the
// attachment surface own their own file routes.
//
// It is public and unauthenticated on purpose, matching the wiki's
// avatar route: the `users` collection's List and View rules are open
// (pb/migrations/1700000001_extend_users.go), and the forum shows author
// names on public pages anyway, so an avatar is no more sensitive than
// the name beside it.
//
// A missing record, an account with no avatar (including every
// superuser) and a deleted avatar all answer 404, which is what makes the
// frontend fall back to the user's initials.
func (m *Module) avatarHandler(e *core.RequestEvent) error {
	userID := e.Request.PathValue("id")
	if userID == "" {
		return e.NotFoundError("avatar not found", nil)
	}

	record, err := m.app.FindRecordById(avatarUsersCollection, userID)
	if err != nil {
		return e.NotFoundError("avatar not found", nil)
	}

	filename := record.GetString(avatarFieldName)
	if filename == "" {
		return e.NotFoundError("avatar not found", nil)
	}

	fsys, err := m.app.NewFilesystem()
	if err != nil {
		return e.InternalServerError("could not open the filesystem", nil)
	}
	defer func() { _ = fsys.Close() }()

	// The frontend appends `?v=<timestamp>` after an avatar change, so a
	// versioned URL is safe to cache hard and an unversioned one has to be
	// revalidated.
	if e.Request.URL.Query().Get("v") != "" {
		e.Response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		e.Response.Header().Set("Cache-Control", "no-cache")
	}

	// Serve sets the content type, honours Range, and answers 404 for a
	// key that no longer exists.
	key := record.BaseFilesPath() + "/" + filename
	if err := fsys.Serve(e.Response, e.Request, key, filename); err != nil {
		return e.NotFoundError("avatar not found", nil)
	}
	return nil
}
