package uploads

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// The plugin's HTTP surface.
//
// The routes mirror upstream's `fof/upload` extension, which is where
// upstream Flarum moved attachments to (upstream's v2.0.0 core has NO
// attachment support at all — the `post_attachments` table and the
// `Post->upload()` relationship are gone from the core):
//
//	POST   /api/uploads          upload a file
//	POST   /api/posts/{id}/files attach an uploaded file to a post
//	DELETE /api/files/{id}       delete a file
//	GET    /api/files            list the caller's files
//	GET    {basePath}/files/{id} the file's bytes
//
// The bytes are served by the plugin's OWN route rather than by
// PocketBase's native `/api/files/{collection}/{id}/{filename}`: that
// URL lives at the root of the listener, which the forum does not own
// (the same reason the avatar route exists), so a sub-path install
// would have the static site answer it.

// uploadHandler serves POST /api/uploads.
//
// The body is a multipart form with a `file` part and an optional
// `post` field naming the post the file belongs to.
//
// A file may be uploaded BEFORE its post exists: the composer uploads
// as the user types, and the post is created afterwards. The file
// therefore starts unattached, and the post's `files` relationship
// picks it up once the post names it.
func uploadHandler(e *core.RequestEvent) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	// The request is a multipart form. The body is parsed with a limit
	// so a malformed or hostile request cannot exhaust the server's
	// memory before the size check below gets a chance to run.
	if err := e.Request.ParseMultipartForm(MaxFileSize); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The upload could not be read.")
	}

	_, header, err := e.Request.FormFile("file")
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A file is required.")
	}

	if header.Size > MaxFileSize {
		return api.WriteError(e, http.StatusRequestEntityTooLarge, api.CodeValidationFailed, "That file is too large.")
	}

	filename := sanitizeFilename(header.Filename)
	if filename == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A file name is required.")
	}

	// The media type comes from the NAME, not from the browser's
	// Content-Type: the header is whatever the client felt like
	// sending, and a client that says `image/png` about a script gets
	// a script served as an image.
	mime := mimeFromFilename(filename)

	// The optional post the file belongs to. The file may be uploaded
	// before the post exists, so an absent post is fine.
	postID := strings.TrimSpace(e.Request.FormValue("post"))
	if postID != "" {
		post, err := e.App.FindRecordById("forum_posts", postID)
		if err != nil {
			return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That post does not exist.")
		}
		// Attaching a file to somebody else's post is not allowed:
		// the file would become readable by everyone who can read
		// the post.
		if post.GetString("user") != actor.User.ID && !actor.User.IsAdmin {
			return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to attach a file to that post.")
		}
	}

	physical := physicalName()

	collection, err := e.App.FindCollectionByNameOrId(physical)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
	}

	record := core.NewRecord(collection)
	record.Set("actor", actor.User.ID)
	record.Set("filename", filename)
	record.Set("mime_type", mime)
	record.Set("size", header.Size)
	record.Set("hidden", false)
	if postID != "" {
		record.Set("post", postID)
	}

	// The blob is set as a `filesystem.File`, which is what
	// PocketBase's FileField expects: the field stores only the file's
	// NAME, and the framework uploads the bytes and writes them under
	// the record's own files path. Handing it the bytes by hand would
	// leave the field's name empty, and the record would point at a
	// file that does not exist.
	record.Set("blob", &filesystem.File{
		Reader:       &multipartFileReader{header: header},
		Name:         filename,
		OriginalName: filename,
		Size:         header.Size,
	})

	if err := e.App.Save(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
	}

	// The dimensions are read after the bytes are stored: they come
	// from the file itself, and the record has to carry them.
	if width, height, ok := imageDimensions(e, physical, record.Id, filename); ok {
		record.Set("image_width", width)
		record.Set("image_height", height)
		if err := e.App.Save(record); err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
		}
	}

	return api.WriteDocument(e, http.StatusCreated, api.Document{Data: fileResource(e, record)})
}

// attachFileHandler serves POST /api/posts/{id}/files.
//
// It attaches an already-uploaded file to a post. The upload and the
// attach are separate steps, because the composer uploads as the user
// types and only learns the post's id when the post is saved.
func attachFileHandler(e *core.RequestEvent) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	postID := e.Request.PathValue("id")
	if postID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A post is required.")
	}

	var body struct {
		FileID string `json:"fileId"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The request body could not be read.")
	}

	fileID := strings.TrimSpace(body.FileID)
	if fileID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A file is required.")
	}

	physical := physicalName()

	post, err := e.App.FindRecordById("forum_posts", postID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That post does not exist.")
	}

	record, err := findFile(e.App, physical, fileID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That file does not exist.")
	}

	// Only the uploader may attach their own file.
	if record.GetString("actor") != actor.User.ID && !actor.User.IsAdmin {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to attach that file.")
	}

	// Attaching a file to somebody else's post is not allowed either.
	if post.GetString("user") != actor.User.ID && !actor.User.IsAdmin {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to attach a file to that post.")
	}

	record.Set("post", postID)
	if err := e.App.Save(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be attached.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: fileResource(e, record)})
}

// deleteFileHandler serves DELETE /api/files/{id}.
//
// The frontend sends this as a POST with an `X-HTTP-Method-Override`
// header, because it rewrites every method other than GET and POST
// (see the core's `Application.request`). The route is registered for
// both verbs and the effective method is resolved here.
func deleteFileHandler(e *core.RequestEvent) error {
	if err := api.RequireMethod(e, http.MethodDelete); err != nil {
		return api.WriteError(e, http.StatusMethodNotAllowed, api.CodeValidationFailed, "The method is not allowed for this route.")
	}

	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	physical := physicalName()

	record, err := findFile(e.App, physical, e.Request.PathValue("id"))
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That file does not exist.")
	}

	// The uploader may delete their own file, and so may an
	// administrator. Upstream's `FileResource` has the same two
	// actors: the policy answers `canDelete` for the uploader and for
	// a moderator.
	if record.GetString("actor") != actor.User.ID && !actor.User.IsAdmin {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to delete that file.")
	}

	if err := e.App.Delete(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be deleted.")
	}

	return api.WriteDocument(e, http.StatusNoContent, api.Document{})
}

// listFilesHandler serves GET /api/files.
//
// It lists the caller's own files — upstream's `FileResource` scope
// does the same for the actor's uploads.
func listFilesHandler(e *core.RequestEvent) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}

	physical := physicalName()

	records, err := filesForActor(e.App, physical, actor.User.ID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The files could not be loaded.")
	}

	resources := make([]api.Resource, 0, len(records))
	for _, record := range records {
		resources = append(resources, fileResource(e, record))
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resources})
}

// showFileHandler serves GET /api/files/{id}.
//
// It is the JSON:API view of a file: the frontend needs the name, the
// size and the URL to render the attachment, and the post's `files`
// relationship carries only the identifier.
//
// The file must be one the caller may read, and the rule is the same
// one the byte route applies: a file is as readable as the post it is
// attached to, and an unattached file belongs to its uploader alone.
func showFileHandler(e *core.RequestEvent) error {
	physical := physicalName()

	record, err := findFile(e.App, physical, e.Request.PathValue("id"))
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That file does not exist.")
	}

	if !mayReadFile(e, record) {
		// The denial is a 404, not a 403, so the response never
		// confirms that the file exists.
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That file does not exist.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: fileResource(e, record)})
}

// ServeFileHandler serves GET {basePath}/files/{id}.
//
// It is exported because the MODULE registers the route: the module
// owns the base path, and the route has to sit next to the avatars
// rather than under `/api`.
//
// The bytes are streamed through the forum's own route, because
// PocketBase's native file URL lives at the root of the listener,
// which the forum does not own.
//
// A file is readable by anyone who may read the post it is attached
// to. An UNATTACHED file is readable by its uploader alone: it has no
// post to inherit a visibility from, and it is not yet part of the
// conversation.
//
// A denial answers 404 rather than 403, so the response never
// confirms that a file exists.
func ServeFileHandler(e *core.RequestEvent) error {
	physical := physicalName()

	record, err := findFile(e.App, physical, e.Request.PathValue("id"))
	if err != nil {
		return e.NotFoundError("file not found", nil)
	}

	if !mayReadFile(e, record) {
		return e.NotFoundError("file not found", nil)
	}

	filename := record.GetString("blob")
	if filename == "" {
		return e.NotFoundError("file not found", nil)
	}

	fsys, err := e.App.NewFilesystem()
	if err != nil {
		return e.InternalServerError("could not open the filesystem", nil)
	}
	defer func() { _ = fsys.Close() }()

	// The content type comes from the record, not from the file's
	// extension at serve time: the record holds the type the upload
	// decided on.
	contentType := record.GetString("mime_type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	e.Response.Header().Set("Content-Type", contentType)

	// An attachment is not executable content: an HTML or SVG file
	// served inline could run against the forum's origin. Only the
	// inline-safe types are rendered, and everything else is
	// downloaded — the same rule the memo attachment surface applies
	// (`pb/api/file_serve.go::inlineFileContentTypes`).
	if isInlineContentType(contentType) {
		e.Response.Header().Set("Content-Disposition", "inline")
	} else {
		e.Response.Header().Set("Content-Disposition", "attachment; filename=\""+record.GetString("filename")+"\"")
	}

	key := record.BaseFilesPath() + "/" + filename
	if err := fsys.Serve(e.Response, e.Request, key, filename); err != nil {
		return e.NotFoundError("file not found", nil)
	}
	return nil
}

// inlineFileContentTypes are the content types the file server is
// willing to render in the browser.
//
// Everything else is served with `Content-Disposition: attachment`, so
// the browser downloads it instead of executing it against the forum's
// origin. The list is the same one the memo attachment surface uses
// (`pb/api/file_serve.go::inlineFileContentTypes`): the file's NAME is
// attacker-controlled, so an `.html` or `.svg` upload must never be
// rendered inline.
//
// SVG is deliberately absent: an SVG is a document, and a document
// served from the forum's origin can carry script.
var inlineFileContentTypes = map[string]struct{}{
	"image/png":       {},
	"image/jpeg":      {},
	"image/gif":       {},
	"image/webp":      {},
	"image/bmp":       {},
	"image/tiff":      {},
	"video/webm":      {},
	"video/mp4":       {},
	"video/quicktime": {},
	"audio/mpeg":      {},
	"audio/mp3":       {},
	"audio/wav":       {},
	"audio/x-wav":     {},
	"audio/aac":       {},
	"audio/flac":      {},
	"audio/ogg":       {},
	"application/pdf": {},
}

// isInlineContentType reports whether the content type may be rendered
// by the browser. See inlineFileContentTypes.
func isInlineContentType(contentType string) bool {
	_, ok := inlineFileContentTypes[contentType]
	return ok
}

// mayReadFile reports whether the caller may read a file.
//
// The rule is the post's own: a file is as readable as the post it is
// attached to. An unattached file belongs to its uploader alone.
func mayReadFile(e *core.RequestEvent, record *core.Record) bool {
	postID := record.GetString("post")
	if postID == "" {
		// An unattached file is the uploader's alone.
		actor, err := api.CurrentActor(e)
		if err != nil {
			return false
		}
		return actor.User.ID == record.GetString("actor")
	}

	post, err := e.App.FindRecordById("forum_posts", postID)
	if err != nil {
		return false
	}

	discussionID := post.GetString("discussion")
	if discussionID == "" {
		return false
	}

	// The forum's own visibility rule: the caller may see the forum
	// at all, the discussion is not hidden (unless the caller may
	// moderate), and the post is not hidden (unless the caller may
	// moderate).
	if !mayViewDiscussion(e, discussionID) {
		return false
	}

	if post.GetBool("is_hidden") {
		allowed, err := api.Can(e, domain.PermissionHidePost, api.TypePost, postID)
		if err != nil || !allowed {
			return false
		}
	}

	return true
}

// mayViewDiscussion reports whether the caller may read a discussion.
//
// It mirrors the core's `showDiscussionHandler`: a hidden discussion
// is invisible to everyone but a moderator.
func mayViewDiscussion(e *core.RequestEvent, discussionID string) bool {
	discussion, err := e.App.FindRecordById("forum_discussions", discussionID)
	if err != nil {
		return false
	}

	if discussion.GetBool("is_hidden") {
		allowed, err := api.Can(e, domain.PermissionHideDiscussion, api.TypeDiscussion, discussionID)
		if err != nil || !allowed {
			return false
		}
	}

	// The forum has to be visible at all.
	allowed, err := api.Can(e, domain.PermissionViewForum, api.TypeDiscussion, discussionID)
	if err != nil {
		return false
	}
	return allowed
}

// sanitizeFilename reduces an uploaded file's name to something safe.
//
// The name is used as a download's `Content-Disposition` filename and
// as the storage key's leaf, so a name carrying a path separator or a
// control character is reduced to its leaf and to printable
// characters.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)

	// The leaf only: a name may not name a directory.
	if slash := strings.LastIndexAny(name, "/\\"); slash >= 0 {
		name = name[slash+1:]
	}

	var builder strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			// A control character.
			continue
		case r == '"' || r == '\\':
			// A quote would break out of the Content-Disposition
			// header's quoted string.
			builder.WriteRune('_')
		default:
			builder.WriteRune(r)
		}
	}

	cleaned := strings.TrimSpace(builder.String())
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return ""
	}
	return cleaned
}

// physicalName is the plugin's collection name.
//
// It is a constant rather than a registry lookup because the plugin's
// collection is created by its own migration, which runs before the
// registry is reachable from a request. It follows the convention the
// Tags plugin uses: the forum's reserved prefix plus the plugin's
// entity name.
func physicalName() string { return physicalPrefix + filesCollection }

// physicalPrefix is the forum's reserved collection prefix.
const physicalPrefix = "forum_"
