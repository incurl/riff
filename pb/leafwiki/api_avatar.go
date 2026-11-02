package leafwiki

import (
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

// errNoAvatarUpload reports a multipart body with no file part.
var errNoAvatarUpload = errors.New("leafwiki: no avatar file part")

const (
	// avatarFieldName is PocketBase's built-in file field on the `users`
	// collection. riff deliberately leaves that field alone
	// (pb/migrations/1700000001_extend_users.go), so the wiki's profile
	// picture is simply *that* file: one avatar per account, shared by the
	// Memos SPA, the admin UI and the wiki. The port does not keep a second
	// avatar store of its own.
	avatarFieldName = "avatar"

	// usersCollectionName is the collection avatars are read from. PocketBase's
	// `_superusers` collection has no avatar field, so a superuser simply has
	// no picture and the SPA falls back to the initials in AvatarFallback.
	usersCollectionName = "users"

	// maxAvatarUploadBytes mirrors the maxAvatarUploadSizeBytes value
	// /api/config advertises, so the cap the SPA shows and the cap the server
	// enforces are the same number. PocketBase's FileField enforces it too;
	// this bound only stops the body being buffered before that check runs.
	maxAvatarUploadBytes = 5 * 1024 * 1024

	// multipartOverheadBytes is the slack allowed on top of the file size for
	// the multipart envelope (boundary, headers, any extra parts).
	multipartOverheadBytes = 64 * 1024

	// avatarCacheControlImmutable is used when the request carries the SPA's
	// `?v=` cache-buster: the URL changes whenever the file does
	// (stores/avatar.ts bumps it after every upload or delete), so the response
	// can be cached hard.
	avatarCacheControlImmutable = "public, max-age=31536000, immutable"

	// avatarCacheControlRevalidate is used when there is no `?v=`: the URL is
	// then stable across avatar changes, so it must be revalidated.
	avatarCacheControlRevalidate = "no-cache"
)

// avatarHandler serves GET <prefix>/avatars/{id}.
//
// It is public and unauthenticated on purpose. The SPA's avatarUrl() documents
// the URL as "public, unauthenticated", the `users` collection's List/View
// rules are open (pb/migrations/1700000001_extend_users.go), and the wiki shows
// author names on public pages anyway — so an avatar is no more sensitive than
// the name beside it.
//
// The bytes are streamed through this route rather than redirecting to
// PocketBase's own `/api/files/users/{id}/{file}` URL, for the same reason the
// attachment surface owns `/file/...`: that native URL lives at the root of the
// riff listener, and the wiki is mounted at `/wiki` behind a reverse proxy, so
// a redirect could not be built without knowing the proxy's prefix. Serving
// here keeps every URL the SPA builds relative to the mount point.
//
// A missing record, a record with no avatar (including every superuser), or an
// avatar that was deleted all answer 404, which is what makes the SPA fall back
// to the user's initials.
func (s *Server) avatarHandler(e *core.RequestEvent) error {
	userID := pathValue(e, "id")
	if userID == "" {
		return e.NotFoundError("avatar not found", nil)
	}

	record, err := s.app.FindRecordById(usersCollectionName, userID)
	if err != nil {
		return e.NotFoundError("avatar not found", nil)
	}

	filename := record.GetString(avatarFieldName)
	if filename == "" {
		return e.NotFoundError("avatar not found", nil)
	}

	fsys, err := s.app.NewFilesystem()
	if err != nil {
		return e.InternalServerError("could not open the filesystem", nil)
	}
	defer func() { _ = fsys.Close() }()

	// The SPA adds ?v=<timestamp> after every upload or delete, so a versioned
	// URL is safe to cache hard and an unversioned one must be revalidated.
	if e.Request.URL.Query().Get("v") != "" {
		e.Response.Header().Set("Cache-Control", avatarCacheControlImmutable)
	} else {
		e.Response.Header().Set("Cache-Control", avatarCacheControlRevalidate)
	}

	// Serve sets the content type, honours Range and answers 404 for a key
	// that no longer exists.
	if err := fsys.Serve(e.Response, e.Request, avatarStorageKey(record, filename), filename); err != nil {
		return e.NotFoundError("avatar not found", nil)
	}
	return nil
}

// avatarStorageKey is the filesystem key PocketBase stores a record file under.
func avatarStorageKey(record *core.Record, filename string) string {
	return record.BaseFilesPath() + "/" + filename
}

// uploadAvatarHandler serves POST <prefix>/api/user/avatar.
//
// The upload replaces the caller's PocketBase `avatar` file, so the picture the
// wiki shows is the same one the Memos SPA and the admin UI show. Type and size
// validation is left to PocketBase's FileField (the collection defines the MIME
// allow-list and the 5 MB cap), which means there is one set of rules rather
// than a copy in this package that could drift from it.
func (s *Server) uploadAvatarHandler(e *core.RequestEvent) error {
	if _, err := s.requireWriter(e); err != nil {
		return err
	}
	record := s.currentUser(e)
	if record == nil {
		// requireWriter only lets an anonymous caller through when
		// RIFF_WIKI_PUBLIC_WRITE is set, and an anonymous caller has no
		// account to hang an avatar on.
		return e.UnauthorizedError("authentication is required to set an avatar", nil)
	}
	if record.Collection().Fields.GetByName(avatarFieldName) == nil {
		return e.BadRequestError("this account type has no avatar", nil)
	}

	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, maxAvatarUploadBytes+multipartOverheadBytes)
	header, err := avatarUploadPart(e)
	if err != nil {
		return e.BadRequestError("an avatar file is required", nil)
	}

	file, err := filesystem.NewFileFromMultipart(header)
	if err != nil {
		return e.BadRequestError("invalid avatar upload", nil)
	}

	record.Set(avatarFieldName, file)
	if err := s.app.Save(record); err != nil {
		// PocketBase's FileField reports size/MIME violations as a validation
		// error, which is the caller's mistake, not a server fault.
		return e.BadRequestError(err.Error(), nil)
	}

	return e.JSON(http.StatusOK, avatarResponseFromRecord(record))
}

// deleteAvatarHandler serves DELETE <prefix>/api/user/avatar.
func (s *Server) deleteAvatarHandler(e *core.RequestEvent) error {
	if _, err := s.requireWriter(e); err != nil {
		return err
	}
	record := s.currentUser(e)
	if record == nil {
		return e.UnauthorizedError("authentication is required to remove an avatar", nil)
	}
	if record.Collection().Fields.GetByName(avatarFieldName) == nil {
		return e.BadRequestError("this account type has no avatar", nil)
	}

	record.Set(avatarFieldName, nil)
	if err := s.app.Save(record); err != nil {
		return e.InternalServerError(err.Error(), nil)
	}

	return e.JSON(http.StatusOK, avatarResponseFromRecord(record))
}

// avatarUploadPart returns the uploaded file, accepting the field name the SPA
// uses ("file") as well as the field name the avatar is stored under, so a
// hand-written curl or an older client keeps working.
func avatarUploadPart(e *core.RequestEvent) (*multipart.FileHeader, error) {
	for _, name := range []string{"file", avatarFieldName} {
		if _, header, err := e.Request.FormFile(name); err == nil {
			return header, nil
		}
	}
	// Fall back to the first file part under any name: the shape of the
	// multipart envelope is not something the SPA contract fixes.
	if err := e.Request.ParseMultipartForm(maxAvatarUploadBytes + multipartOverheadBytes); err == nil {
		for _, headers := range e.Request.MultipartForm.File {
			if len(headers) > 0 {
				return headers[0], nil
			}
		}
	}
	return nil, errNoAvatarUpload
}

// avatarResponse is the body both mutation handlers return. `filename` is the
// stored file name, which the SPA does not need (it caches by ?v=) but an
// operator debugging an upload does.
type avatarResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
}

func avatarResponseFromRecord(record *core.Record) avatarResponse {
	return avatarResponse{ID: record.Id, Filename: strings.TrimSpace(record.GetString(avatarFieldName))}
}
