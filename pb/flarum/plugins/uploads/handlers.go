package uploads

import (
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
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

// uploadHandler serves POST /api/uploads and POST /api/fof/upload.
//
// The body is a multipart form. Upstream's extension posts its files under
// the `files` field, and A MULTIPART FIELD CAN REPEAT: dropping four files
// is one request with four parts, which is what upstream's frontend does
// and what its `UploadHandler` iterates. The single `file` part the port's
// own composer used first is still accepted, so an older client keeps
// working.
//
// The response is a JSON:API COLLECTION — `data` is an array — which is
// what upstream returns and what its frontend loops over. A single-file
// upload therefore answers an array of one.
//
// The options ride in the body the way upstream reads them, under
// `options`: `options[hidden]` and `options[shared]`. Multipart values are
// strings, so `"true"` (and `"1"`) mean true; the flat `hidden`/`shared`
// spellings are accepted too.
//
// A file may be uploaded BEFORE its post exists: the composer uploads as
// the user types, and the post is created afterwards. The file therefore
// starts unattached, and the post's `files` relationship picks it up once
// the post names it.
func uploadHandler(e *core.RequestEvent) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	// The upload permission. Upstream's `Upload` command asserts
	// `fof-upload.upload` before it stores anything, so the gate is here
	// rather than a bare "signed in" check.
	if !can(e, domain.PermissionUploadFiles) {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to upload files.")
	}

	limit := maxFileSizeBytes(e)

	// The request is a multipart form. The body is parsed with the
	// effective limit, so a malformed or hostile request cannot exhaust
	// the server's memory before the per-file check below runs.
	if err := e.Request.ParseMultipartForm(limit); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The upload could not be read.")
	}

	headers := uploadedFiles(e.Request)
	if len(headers) == 0 {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A file is required.")
	}

	hide := uploadOption(e.Request, "hidden")
	shared := uploadOption(e.Request, "shared")
	if shared && !can(e, domain.PermissionUploadSharedFiles) {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to upload to the shared library.")
	}

	// The optional post the files belong to. An absent post is fine: the
	// files are uploaded before the post exists.
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

	resources := make([]api.Resource, 0, len(headers))
	for _, header := range headers {
		record, err := saveUploadedFile(e, actor.User.ID, header, postID, hide, shared, limit)
		if err != nil {
			if errors.Is(err, errResponseWritten) {
				return nil
			}
			return err
		}
		resources = append(resources, fileResource(e, record))
	}

	return api.WriteDocument(e, http.StatusCreated, api.Document{Data: resources})
}

// errResponseWritten reports that a helper has already answered the
// request.
//
// `api.WriteError` returns the writer's own nil, so a helper that writes an
// error and returns `(nil, api.WriteError(...))` hands its caller
// `(nil, nil)` — which reads as success, and the caller then dereferences a
// nil record. The sentinel makes "already answered" distinguishable from
// "no error"
var errResponseWritten = errors.New("uploads: the response has already been written")

// saveUploadedFile stores one multipart part and returns its record.
func saveUploadedFile(e *core.RequestEvent, actorID string, header *multipart.FileHeader, postID string, hide, shared bool, limit int64) (*core.Record, error) {
	if header.Size > limit {
		_ = api.WriteError(e, http.StatusRequestEntityTooLarge, api.CodeValidationFailed, "That file is too large.")
		return nil, errResponseWritten
	}

	filename := sanitizeFilename(header.Filename)
	if filename == "" {
		_ = api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A file name is required.")
		return nil, errResponseWritten
	}

	// The media type comes from the NAME, not from the browser's
	// Content-Type: the header is whatever the client felt like
	// sending, and a client that says `image/png` about a script gets
	// a script served as an image.
	mime := mimeFromFilename(filename)

	physical := physicalName()

	collection, err := e.App.FindCollectionByNameOrId(physical)
	if err != nil {
		_ = api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
		return nil, errResponseWritten
	}

	record := core.NewRecord(collection)
	record.Set("actor", actorID)
	record.Set("filename", filename)
	record.Set("mime_type", mime)
	record.Set("size", header.Size)
	record.Set("hidden", hide)
	record.Set("shared", shared)
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
		_ = api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
		return nil, errResponseWritten
	}

	// The dimensions are read after the bytes are stored: they come
	// from the file itself, and the record has to carry them.
	if width, height, ok := imageDimensions(e, physical, record.Id, filename); ok {
		record.Set("image_width", width)
		record.Set("image_height", height)
		if err := e.App.Save(record); err != nil {
			_ = api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
			return nil, errResponseWritten
		}
	}

	return record, nil
}

// uploadedFiles returns the parts of an upload request, in order.
//
// Upstream posts a repeated `files` field. Go's multipart parser keys the
// `files[]` spelling some clients send under exactly that name, so both
// are read, and the port's own single `file` part is the third spelling.
func uploadedFiles(r *http.Request) []*multipart.FileHeader {
	var headers []*multipart.FileHeader
	if r.MultipartForm == nil {
		return headers
	}

	for _, key := range []string{"files", "files[]", "file"} {
		headers = append(headers, r.MultipartForm.File[key]...)
	}
	return headers
}

// uploadOption reads one of the upload's options.
//
// Upstream reads `options.<name>` from the parsed body. A multipart form
// arrives as `options[<name>]` (PHP parses the bracket into a nested
// array; Go keeps the literal key), and the bare `<name>` is accepted for
// a hand-written client.
func uploadOption(r *http.Request, name string) bool {
	for _, key := range []string{"options[" + name + "]", "options." + name, name} {
		switch strings.ToLower(strings.TrimSpace(r.FormValue(key))) {
		case "true", "1", "on", "yes":
			return true
		}
	}
	return false
}

// maxFileSizeBytes returns the effective upload limit, in bytes.
//
// The setting is upstream's `fof-upload.maxFileSize`, in KIBIBYTES. The
// collection's own field maximum is the CEILING: a larger setting cannot
// make PocketBase's FileField accept more, so the effective limit is the
// smaller of the two — otherwise the field's own rejection would surface
// as an opaque 400 instead of this handler's 413.
func maxFileSizeBytes(e *core.RequestEvent) int64 {
	limit := int64(MaxFileSize)

	value, err := api.SettingValue(e, SettingMaxFileSize, nil)
	if err != nil {
		return limit
	}
	setting, ok := api.SettingString(value)
	if !ok {
		return limit
	}

	kib, err := strconv.ParseInt(strings.TrimSpace(setting), 10, 64)
	if err != nil || kib <= 0 {
		return limit
	}

	if bytes := kib * 1024; bytes < limit {
		return bytes
	}
	return limit
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

// deleteFileHandler serves DELETE /api/files/{id} and
// DELETE /api/fof/upload/delete/{uuid}.
//
// The frontend sends the first as a POST with an `X-HTTP-Method-Override`
// header, because it rewrites every method other than GET and POST (see
// the core's `Application.request`). The routes are registered for both
// verbs and the effective method is resolved here.
//
// The permission is upstream's `FilePolicy::delete()`, which the
// `canDelete` attribute asks too: a shared file needs
// `upload-shared-files`, anybody's file needs `deleteUserUploads`, and the
// uploader's own needs `deleteOwnUploads` as well.
func deleteFileHandler(e *core.RequestEvent) error {
	if err := api.RequireMethod(e, http.MethodDelete); err != nil {
		return api.WriteError(e, http.StatusMethodNotAllowed, api.CodeValidationFailed, "The method is not allowed for this route.")
	}

	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	physical := physicalName()

	record, err := findFile(e.App, physical, fileIDParam(e))
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That file does not exist.")
	}

	if !mayDeleteFile(e, record) {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to delete that file.")
	}

	// The uploader's own file is the actor's to delete, but the row is
	// only deletable while the account still exists; the record's actor
	// relation cascades, so nothing else has to be cleaned up here.
	if err := e.App.Delete(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be deleted.")
	}

	// A bodyless 204, which is what upstream's `DeleteFileController`
	// answers. `WriteDocument` would serialise `{"data":null}`.
	return api.WriteEmpty(e, http.StatusNoContent)
}

// hideFileHandler serves PATCH /api/fof/upload/hide.
//
// It is upstream's media-manager switch: `{"uuid": "...", "hidden": true}`
// hides a file from its owner's listing (and from the shared library for a
// caller who may only read it). Hiding a SHARED file moves it between the
// public and private stores upstream; here it is the same flag on the same
// row, because the port has one storage.
func hideFileHandler(e *core.RequestEvent) error {
	if err := api.RequireMethod(e, http.MethodPatch); err != nil {
		return api.WriteError(e, http.StatusMethodNotAllowed, api.CodeValidationFailed, "The method is not allowed for this route.")
	}

	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	var body struct {
		UUID   string `json:"uuid"`
		Hidden *bool  `json:"hidden"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The request body could not be read.")
	}

	uuid := strings.TrimSpace(body.UUID)
	if uuid == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A file is required.")
	}

	physical := physicalName()

	record, err := findFile(e.App, physical, uuid)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That file does not exist.")
	}

	if !mayHideFile(e, record) {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to hide that file.")
	}

	hidden := true
	if body.Hidden != nil {
		hidden = *body.Hidden
	}
	record.Set("hidden", hidden)

	if err := e.App.Save(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The file could not be saved.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: fileResource(e, record)})
}

// fileIDParam returns the id a file route names.
//
// The port's own routes use `{id}` (`/api/files/{id}`) and upstream's use
// `{uuid}` (`/api/fof/upload/delete/{uuid}`, `/api/fof/download/{uuid}`);
// the value is the same record id either way, because this port uses the
// record's id as the file's uuid.
func fileIDParam(e *core.RequestEvent) string {
	if id := e.Request.PathValue("id"); id != "" {
		return id
	}
	return e.Request.PathValue("uuid")
}

// listFilesHandler serves GET /api/files, GET /api/fof/uploads and
// GET /api/fof/upload/shared-files.
//
// It is upstream's `FileResource` INDEX endpoint, and it answers the same
// query the extension's media manager sends:
//
//	filter[user]     whose uploads to list (default: the caller)
//	filter[shared]   the shared library instead
//	sort             `-createdAt` (default) or `createdAt`/`id`
//	page[offset]     the window
//	page[limit]      20 by default, 50 at most (upstream's `paginate(20, 50)`)
//
// The scope rules are upstream's `FileResource::scope()`:
//
//   - the caller's own listing is narrowed to `hidden = false` AND
//     `shared = false`, so a file hidden from the media manager and a
//     shared file never appear among its uploads;
//   - another account's listing needs `viewUserUploads`;
//   - the shared listing needs `access-shared-files` (or the stronger
//     `upload-shared-files`), and a caller who may only READ the library
//     does not see its hidden entries.
//
// The sort default differs from upstream's `-id` on purpose: PocketBase
// ids are random, not sequential, so `-id` would be an arbitrary order.
// `-created_at` is the order upstream's `-id` produces.
func listFilesHandler(e *core.RequestEvent) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}

	physical := physicalName()
	collection, err := e.App.FindCollectionByNameOrId(physical)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The files could not be loaded.")
	}

	// The shared listing is the same endpoint with `filter[shared]=1`;
	// upstream's `/fof/upload/shared-files` route is a literal alias
	// that sets it.
	shared := queryFlag(e, "filter[shared]") || strings.HasSuffix(e.Request.URL.Path, "/shared-files")

	clauses := make([]string, 0, 3)
	params := map[string]any{}

	if shared {
		if !can(e, domain.PermissionAccessSharedFiles) && !can(e, domain.PermissionUploadSharedFiles) {
			return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to see the shared files.")
		}

		clauses = append(clauses, "shared = true")
		if !can(e, domain.PermissionUploadSharedFiles) {
			clauses = append(clauses, "hidden = false")
		}
	} else {
		userID := strings.TrimSpace(e.Request.URL.Query().Get("filter[user]"))
		if userID == "" {
			userID = actor.User.ID
		}
		if userID != actor.User.ID && !can(e, domain.PermissionViewUserUploads) {
			return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to see that account's files.")
		}

		clauses = append(clauses, "actor = {:actor}", "hidden = false", "shared = false")
		params["actor"] = userID
	}

	// THE TWO READERS SPEAK DIFFERENT DIALECTS, and the join has to
	// match each one. `FindRecordsByFilter` parses PocketBase's FILTER
	// grammar, where the conjunction is `&&`; `CountRecords` receives a
	// dbx expression, whose text goes to the driver as written, where the
	// conjunction is SQL's `AND`. Joined the wrong way round, a listing
	// with two clauses answers 500 and a listing with one works — which
	// is exactly how this was found.
	filter := strings.Join(clauses, " && ")
	where := strings.Join(clauses, " AND ")

	// The window. Upstream's index paginates 20 at a time with a hard
	// maximum of 50, so a client cannot ask for the whole library.
	limit, offset := filePageWindow(e)

	total, err := e.App.CountRecords(collection, dbx.NewExp(where, dbx.Params(params)))
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The files could not be loaded.")
	}

	records, err := e.App.FindRecordsByFilter(collection, filter, fileSort(e), limit, offset, params)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The files could not be loaded.")
	}

	resources := make([]api.Resource, 0, len(records))
	for _, record := range records {
		resources = append(resources, fileResource(e, record))
	}

	meta, links := api.Paginate(e, int(total), limit, offset)

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resources, Meta: meta, Links: links})
}

// filePageWindow reads the listing's pagination window.
//
// Upstream's `FileResource` declares `->paginate(20, 50)`: twenty records
// a page, fifty at most.
func filePageWindow(e *core.RequestEvent) (limit, offset int) {
	limit, offset = 20, 0

	query := e.Request.URL.Query()
	if raw := query.Get("page[limit]"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if raw := query.Get("page[offset]"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = value
		}
	}
	if limit > 50 {
		limit = 50
	}
	return limit, offset
}

// fileSort reads the listing's sort.
//
// Upstream sorts by `id` or `createdAt`. `-id` is NOT the default here:
// PocketBase ids are random, so `-id` is an arbitrary order, and
// `-created_at` is the order upstream's `-id` produces on a sequential
// key.
func fileSort(e *core.RequestEvent) string {
	switch strings.TrimSpace(e.Request.URL.Query().Get("sort")) {
	case "createdAt", "created_at":
		return "created_at"
	case "-createdAt", "-created_at":
		return "-created_at"
	case "id":
		return "id"
	case "-id":
		return "-id"
	default:
		return "-created_at"
	}
}

// queryFlag reports whether a query parameter is a truthy flag.
func queryFlag(e *core.RequestEvent, key string) bool {
	switch strings.ToLower(strings.TrimSpace(e.Request.URL.Query().Get(key))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
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

	record, err := findFile(e.App, physical, fileIDParam(e))
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
