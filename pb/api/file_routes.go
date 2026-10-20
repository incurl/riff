// file_routes.go registers riff's public file surface.
//
// The routes are:
//
//	GET /file/attachments/{uid}              -> the attachment blob
//	GET /file/attachments/{uid}/{filename}    -> the attachment blob
//	GET /file/users/{identifier}/avatar        -> a user's avatar
//	GET /o/r/{uid}                           -> the attachment blob
//
// The fourth is the upstream-Memos V0 resource URL. Upstream
// Memos serves a resource blob at `GET /o/r/{uid}`, and the
// MoeMemos iOS client builds exactly that URL from the `uid`
// field of the resource (`MemosV0Resource.path()` in
// Packages/Services/Sources/MemosV0Service/Ext.swift), falling
// back to the row name when `uid` is absent. It was not
// registered, so the request fell through to the SPA catch-all
// and answered 200 with index.html — a client that trusted the
// status code cached HTML as if it were the attachment. It
// dispatches to the same handler as the /file/... family, so
// the authorization, thumbnail, motion and share-token
// behaviour cannot diverge between the two.
//
// The first two are the URLs the SPA, the markdown renderer, and
// the share links emit (see web/src/utils/attachment.ts and
// web/src/utils/managed-attachment.ts). Before this file existed
// they were not registered at all, so they fell through to the
// SPA's catch-all route and answered 200 with index.html — the
// browser showed a broken image rather than a 404.
//
// The third mirrors the upstream-Memos file server so a Memos
// client that asks for a user's avatar does not 404 on a missing
// route.
//
// The routes are registered with GET rather than Any on purpose:
// the SPA's catch-all is registered with Any (see
// server/frontend/embed.go), and a method-less "/file/..."
// pattern would conflict with it in Go's http.ServeMux. A
// GET-only pattern is strictly more specific than the
// method-less catch-all, so the two coexist — the same reason the
// connect procedures and /mcp are registered without a method.
package api

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// mountFileRoutes registers the public /file/... surface on the
// PocketBase router.
func mountFileRoutes(r *router.Router[*core.RequestEvent], app core.App) {
	r.GET("/file/attachments/{uid}", attachmentFileHandler(app))
	r.GET("/file/attachments/{uid}/{filename}", attachmentFileHandler(app))
	r.GET("/file/users/{identifier}/avatar", userAvatarFileHandler(app))
	// The upstream-Memos V0 resource URL. See the package comment.
	r.GET("/o/r/{uid}", attachmentFileHandler(app))
}

// attachmentFileHandler resolves /file/attachments/{uid} (or
// /file/attachments/{uid}/{filename}) to a row in the unified
// `attachments` collection and streams the primary file.
//
// The UID is the PocketBase 15-char id; riff also accepts the
// upstream "name" form (the `attachments/{uid}` segment) so a
// Memos client can paste a known-good URL.
//
// The handler honours three query parameters:
//
//	?thumbnail=true  — serve a downscaled JPEG instead
//	?motion=true     — serve the MP4 embedded in a Motion Photo
//	?share_token=…   — read as a share-link bearer credential
func attachmentFileHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		uid := attachmentUIDFromRequest(e)
		if uid == "" {
			return fileNotFound(e)
		}
		rec, err := findAttachmentRecord(app, uid)
		if err != nil {
			return fileNotFound(e)
		}
		filename := rec.GetString("filename")
		if filename == "" {
			return fileNotFound(e)
		}

		// Authorization happens before any byte is read, and a
		// denial answers 404 (not 403) so the response never
		// confirms that the attachment exists.
		access := authorizeAttachment(app, e, rec)
		if !access.Allowed {
			return fileNotFound(e)
		}

		if e.Request.URL.Query().Get("motion") == "true" {
			return serveMotionClip(e, app, rec, filename)
		}
		if e.Request.URL.Query().Get("thumbnail") == "true" {
			if served := serveThumbnail(e, app, rec, filename); served {
				return nil
			}
			// The thumbnail could not be produced (an unsupported
			// format, or an image the decoder refused). Fall
			// through to the original bytes — the SPA's
			// <img onError> handler does the same.
		}

		return streamRecordFile(e, app, rec, filename, access.Public)
	}
}

// serveThumbnail writes a generated thumbnail to the response. It
// reports whether the response was completed; a false return
// means the caller must fall back to the original bytes.
func serveThumbnail(e *core.RequestEvent, app core.App, rec *core.Record, filename string) bool {
	contentType := sniffContentType(filename)
	payload := thumbnailFor(app, rec, filename, contentType)
	if payload == nil {
		return false
	}
	etag := fileEtagFor(payload)
	if writeIfNotModified(e, etag) {
		return true
	}
	e.Response.Header().Set("ETag", etag)
	return serveFileBytes(e, "image/jpeg", thumbnailFilename(filename), payload) == nil
}

// serveMotionClip writes the MP4 embedded in a Motion Photo to the
// response.
//
// A file that is not a Motion Photo answers 404: the SPA asked
// for a motion clip, and serving the still frame would look like
// a video that never plays.
func serveMotionClip(e *core.RequestEvent, app core.App, rec *core.Record, filename string) error {
	fs, err := app.NewFilesystem()
	if err != nil {
		return fileError(e, http.StatusInternalServerError, "filesystem unavailable")
	}
	defer fs.Close()

	reader, err := fs.GetReader(attachmentFileKey(rec, filename))
	if err != nil {
		return fileNotFound(e)
	}
	defer reader.Close()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return fileNotFound(e)
	}

	clip := extractMotionClip(raw)
	if clip == nil {
		return fileNotFound(e)
	}

	etag := fileEtagFor(clip)
	if writeIfNotModified(e, etag) {
		return nil
	}
	e.Response.Header().Set("ETag", etag)
	return serveFileBytes(e, "video/mp4", motionClipName(filename), clip)
}

// thumbnailFilename returns the filename a generated thumbnail is
// served under. The extension is always .jpg because the
// generator always encodes JPEG.
func thumbnailFilename(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if base == "" {
		base = "thumbnail"
	}
	return base + ".jpg"
}

// attachmentUIDFromRequest extracts the attachment id from the
// request path.
//
// The path is one of:
//
//	/file/attachments/{uid}
//	/file/attachments/{uid}/{filename}
//	/memos/api/file/attachments/{uid}
//	/memos/api/file/attachments/{uid}/{filename}
//	/o/r/{uid}
//
// The first four share the "attachments/" segment, so the prefix
// is trimmed by locating that segment rather than by matching a
// fixed prefix. The uid is additionally accepted in the upstream
// "name" form ("attachments/{uid}").
//
// The last is the upstream-Memos V0 resource URL. It has no
// "attachments" segment — it is `/o/r/{uid}`, two opaque path
// segments that name the same row by its V0 `uid` — so it is
// matched explicitly. Both forms resolve to the same
// `attachments` record; the V0 client picks one or the other
// depending on whether the resource carried a `uid` or only a
// name.
func attachmentUIDFromRequest(e *core.RequestEvent) string {
	parts := strings.Split(strings.Trim(e.Request.URL.Path, "/"), "/")
	for i, part := range parts {
		// The V0 form: /o/r/{uid}.
		if part == "o" && i+2 < len(parts) && parts[i+1] == "r" {
			return parts[i+2]
		}
		if part != "attachments" {
			continue
		}
		if i+1 >= len(parts) {
			return ""
		}
		uid := strings.TrimPrefix(parts[i+1], "attachments/")
		return uid
	}
	return ""
}

// userAvatarFileHandler resolves
// /file/users/{identifier}/avatar. The riff users schema has an
// `avatar` FileField (a single image), so the handler streams it.
//
// The identifier is a username, an email address, or a record id —
// the same three forms the upstream Memos avatar route accepts.
func userAvatarFileHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		identifier := e.Request.PathValue("identifier")
		if identifier == "" {
			identifier = userAvatarIdentifierFromPath(e)
		}
		if identifier == "" {
			return fileError(e, http.StatusNotFound, "user not found")
		}
		rec, err := findUserByIdentifier(app, identifier)
		if err != nil {
			return fileError(e, http.StatusNotFound, "user not found")
		}
		filenames := rec.GetStringSlice("avatar")
		if len(filenames) == 0 {
			return fileError(e, http.StatusNotFound, "avatar not found")
		}
		// An avatar is a public profile picture — it is shown
		// next to the user's name in every memo list — so it is
		// served with a cacheable Cache-Control.
		return streamRecordFile(e, app, rec, filenames[0], true)
	}
}

// userAvatarIdentifierFromPath extracts the user identifier from
// the request path. It is the fallback for the /memos/api/file/...
// route family, where the PocketBase path value is not populated
// because the route is registered with a different pattern.
func userAvatarIdentifierFromPath(e *core.RequestEvent) string {
	parts := strings.Split(strings.Trim(e.Request.URL.Path, "/"), "/")
	for i, part := range parts {
		if part != "users" {
			continue
		}
		if i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
