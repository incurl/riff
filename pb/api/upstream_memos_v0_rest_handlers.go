// This file holds the request-time handlers for the
// V0 (Memos 0.21) legacy REST surface at `/api/v1/...`.
// Every V0 path goes through a `custom` handler that
// does the V0→V1 request shaping, dispatches through
// the connect mux, and then shapes the V1 response
// into the V0 wire format.
//
// The shape helpers live in `upstream_memos_v0_convert.go`;
// this file is just the per-route glue that wires the
// helpers into the request/response cycle. The V0
// handlers are similar in spirit to the V1 wrap-flavour
// `connectProxyWrapHandler`; the V0 paths just need
// both directions of shaping, not just one.
//
// Handler naming convention: `v0*Handler` is a
// closure factory that returns a `*core.RequestEvent`
// handler bound to a `core.App` (the helpers need the
// app for multipart parsing on the upload path).
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"

	"github.com/incurl/riff/pb/connect"
)

// The V0 handlers do their own request shaping
// (calling the V0 convert helpers inline) and
// dispatch, so there is no central
// `v0BodyWrappers` registry to maintain. The
// helpers in `upstream_memos_v0_convert.go` are
// pure functions; the handlers wire them into the
// request/response cycle.

// ============================================================
// V0 user
// ============================================================

// v0UserMeHandler serves `GET /api/v1/user/me`. The V0
// client treats this as the identity bootstrap; the
// returned `id: Long` is the owner identity for every
// other V0 call.
//
// The V1 connect handler returns
// `GetCurrentUserResponse{user: User}`; the V0 shaper
// in `v0ShapeUser` converts the V1 `User` to a V0
// `MemosV0User`. The shaper reads the user's
// `memoVisibility` and `telegramChatId` settings from
// the `userSettingList` field; the connect handler
// populates this from the `instance_settings` rows
// scoped to the caller (see
// `pb/connect/users.go::GetCurrentUser`).
func v0UserMeHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectAuthGetCurrentUserProcedure,
			[]byte("{}"),
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		shaped, err := v0ShapeUser(v1Body)
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"shape V0 user: "+err.Error())
		}
		return e.JSON(http.StatusOK, shaped)
	}
}

// ============================================================
// V0 memos
// ============================================================

// v0MemoListHandler serves `GET /api/v1/memo`. The V0
// response is a top-level array of `MemosV0Memo` (no
// envelope); the V1 connect handler returns
// `ListMemosResponse{memos: [...]}`. The shaper
// `v0ShapeMemoList` walks the V1 list and emits the
// V0 flat shapes.
//
// The list handler also populates the per-request
// `v0IDMap` so subsequent V0 PATCH/DELETE requests
// on the same connection can resolve the V0 Long id
// without an extra round trip. The map is stashed on
// the request context; in practice every V0 PATCH
// or DELETE has to re-list (the context is
// per-request, not per-connection), but the
// `populateV0IDMapFromV1List` helper is still
// useful for the rare case where the V0 client
// uses HTTP/1.1 keep-alive and the connect mux
// preserves the upstream context.
func v0MemoListHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body, err := v0MemoListToV1ListMemos(nil, nil, e.Request.URL.Query())
		if err != nil {
			return crudError(e, http.StatusBadRequest, "shape V0 list: "+err.Error())
		}
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectMemoListMemosProcedure,
			body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		// Populate the per-request V0 id map
		// so subsequent V0 writes can resolve
		// the V0 Long id to a V1 resource
		// name. (In practice the map is
		// per-request, so the write handlers
		// re-list, but the stash is here for
		// the keep-alive case.)
		idMap := newV0IDMap()
		if err := populateV0IDMapFromV1List(idMap, v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"populate V0 id map: "+err.Error())
		}
		e.Request = e.Request.WithContext(withV0IDMap(e.Request.Context(), idMap))

		shaped, err := v0ShapeMemoList(v1Body, requestOriginFromEvent(e))
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"shape V0 memo list: "+err.Error())
		}
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0MemoAllListHandler serves `GET /api/v1/memo/all`,
// the admin / directory view. The V0 client uses this
// to list *every* memo on the instance, not just the
// caller's. The V1 connect handler doesn't have an
// admin-list RPC; the V0 shaper falls back to a
// regular `ListMemos` with no filter and a large
// page size, which is the same shape the V1 admin
// view has today.
func v0MemoAllListHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		// Force a no-filter list with a large
		// page size. The V0 client treats this
		// as "all memos" and doesn't paginate.
		body := []byte(`{"page_size":1000}`)
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectMemoListMemosProcedure,
			body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		shaped, err := v0ShapeMemoList(v1Body, requestOriginFromEvent(e))
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"shape V0 memo list: "+err.Error())
		}
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0MemoCreateHandler serves `POST /api/v1/memo`. The
// V0 body is the inner `MemosV0Memo` shape (no
// envelope); the V1 connect handler expects
// `CreateMemoRequest{memo: Memo}`. The shaper
// `v0MemoCreateToV1CreateMemo` strips the V0-only
// fields and translates the visibility string.
//
// A create may carry `resourceList`. The V0 client that uploads-then-creates
// (rather than uploads, creates, then PATCHes) names its already-uploaded
// resources here, so the handler binds them onto the memo it just created —
// the same `v0LinkResourcesToMemo` pass the PATCH path uses. Before this, the
// shaper dropped `resourceList` on the floor and the files were stranded.
func v0MemoCreateHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body, err := io.ReadAll(e.Request.Body)
		if err != nil {
			return crudError(e, http.StatusBadRequest,
				"could not read V0 create body: "+err.Error())
		}
		// Read the resource ids before dispatch: the V1 `Memo` proto has no
		// `resourceList` field, so the shaper strips it and the binding has
		// to happen here.
		resourceIDs := v0ResourceListIDsFromBody(body)
		v1body, err := v0MemoCreateToV1CreateMemo(body, nil, nil)
		if err != nil {
			return crudError(e, http.StatusBadRequest,
				"shape V0 create: "+err.Error())
		}
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectMemoCreateMemoProcedure,
			v1body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		if len(resourceIDs) > 0 {
			name, _ := v1Body["name"].(string)
			memoID := strings.TrimPrefix(name, "memos/")
			if memoID == "" {
				return crudError(e, http.StatusInternalServerError,
					"connect response carried no memo name to bind the uploaded resources to")
			}
			viewerID := resolveV0ViewerID(e)
			if viewerID == "" {
				return crudError(e, http.StatusUnauthorized,
					"authentication required for V0 resource link")
			}
			if err := v0LinkResourcesToMemo(e.App, viewerID, memoID, resourceIDs); err != nil {
				return crudError(e, http.StatusBadRequest,
					"link V0 resources to memo: "+err.Error())
			}
			// The memo proto the client gets back must show the files it just
			// asked for, or the next edit's `resourceList` would be built from
			// an empty list and the client could drop them again.
			v1Body, err = v0ShapeSingleMemoAttachments(e.App, e, memoID, v1Body)
			if err != nil {
				return crudError(e, http.StatusInternalServerError,
					"shape V0 memo attachments: "+err.Error())
			}
		}
		shaped, err := v0ShapeSingleMemo(v1Body, requestOriginFromEvent(e))
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"shape V0 memo: "+err.Error())
		}
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0MemoOrganizerHandler serves
// `POST /api/v1/memo/{id}/organizer`. The V0 client
// uses this verb-style route to toggle the `pinned`
// flag; the V1 path is a PATCH with an update mask.
// The shaper produces a V1 `UpdateMemoRequest` from
// the path variable and the query/body `pinned` bool.
//
// The V0 `{id}` is a numeric Long; the V1
// `UpdateMemoRequest.memo.name` is a resource name
// `memos/<15-char-id>`. The handler resolves the
// V0 id to a V1 name via a per-request
// `ListMemos` round trip; see `v0ResolveMemoName`.
func v0MemoOrganizerHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		idStr := e.Request.PathValue("id")
		v0ID, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return crudError(e, http.StatusBadRequest,
				"V0 memo id is not a Long: "+idStr)
		}
		v1Name, rerr := v0ResolveMemoName(e, v0ID)
		if rerr != nil {
			return crudError(e, http.StatusInternalServerError,
				"resolve V0 memo id: "+rerr.Error())
		}
		if v1Name == "" {
			return crudError(e, http.StatusNotFound,
				"V0 memo id not found: "+idStr)
		}
		v1ID := strings.TrimPrefix(v1Name, "memos/")
		path := map[string]string{"id": v1ID}
		body, err := io.ReadAll(e.Request.Body)
		if err != nil {
			body = nil
		}
		v1body, err := v0MemoOrganizerToV1UpdateMemo(body, path, e.Request.URL.Query())
		if err != nil {
			if we, ok := err.(*wrapErr); ok {
				return crudError(e, we.status, we.message)
			}
			return crudError(e, http.StatusBadRequest,
				"shape V0 organizer: "+err.Error())
		}
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectMemoUpdateMemoProcedure,
			v1body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		shaped, err := v0ShapeSingleMemo(v1Body, requestOriginFromEvent(e))
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"shape V0 memo: "+err.Error())
		}
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0MemoPatchHandler serves `PATCH /api/v1/memo/{id}`.
// The V0 body shape echoes the numeric `id`; the V1
// path injects the resource name from the URL. The
// shaper `v0MemoPatchToV1UpdateMemo` strips the
// redundant `id`, maps `rowStatus` to V1 `state`, and
// maps the V0 visibility string to the V1 enum.
//
// The V0 `{id}` is a numeric Long; the handler
// resolves it to a V1 resource name before
// dispatching the connect call (see
// `v0ResolveMemoName`).
func v0MemoPatchHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		idStr := e.Request.PathValue("id")
		v0ID, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return crudError(e, http.StatusBadRequest,
				"V0 memo id is not a Long: "+idStr)
		}
		v1Name, rerr := v0ResolveMemoName(e, v0ID)
		if rerr != nil {
			return crudError(e, http.StatusInternalServerError,
				"resolve V0 memo id: "+rerr.Error())
		}
		if v1Name == "" {
			return crudError(e, http.StatusNotFound,
				"V0 memo id not found: "+idStr)
		}
		v1ID := strings.TrimPrefix(v1Name, "memos/")
		path := map[string]string{"id": v1ID}
		body, err := io.ReadAll(e.Request.Body)
		if err != nil {
			return crudError(e, http.StatusBadRequest,
				"could not read V0 patch body: "+err.Error())
		}
		// The V0 client links an uploaded resource to its
		// memo through the `resourceList` field of the
		// PATCH body. The V1 `Memo` proto has no such
		// field, so the shaper cannot carry it — the
		// binding is done here instead (see
		// `v0LinkResourcesToMemo`).
		resourceIDs := v0ResourceListIDsFromBody(body)
		if len(resourceIDs) > 0 {
			viewerID := resolveV0ViewerID(e)
			if viewerID == "" {
				return crudError(e, http.StatusUnauthorized,
					"authentication required for V0 resource link")
			}
			if err := v0LinkResourcesToMemo(e.App, viewerID, v1ID, resourceIDs); err != nil {
				return crudError(e, http.StatusBadRequest,
					"link V0 resources to memo: "+err.Error())
			}
		}
		v1body, err := v0MemoPatchToV1UpdateMemo(body, path, nil)
		if err != nil {
			if we, ok := err.(*wrapErr); ok {
				return crudError(e, we.status, we.message)
			}
			return crudError(e, http.StatusBadRequest,
				"shape V0 patch: "+err.Error())
		}
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectMemoUpdateMemoProcedure,
			v1body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		// The memo proto the client gets back must show the files
		// the PATCH just bound, or the client's own `resourceList`
		// bookkeeping stays empty and the next edit — which rebuilds
		// that list from this response — could drop them again. The
		// V1 `UpdateMemo` response carries only the memo's own
		// columns, so the attachment list is read back from the store
		// here, the same pass the create path uses.
		if len(resourceIDs) > 0 {
			v1Body, err = v0ShapeSingleMemoAttachments(e.App, e, v1ID, v1Body)
			if err != nil {
				return crudError(e, http.StatusInternalServerError,
					"shape V0 memo attachments: "+err.Error())
			}
		}
		shaped, err := v0ShapeSingleMemo(v1Body, requestOriginFromEvent(e))
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"shape V0 memo: "+err.Error())
		}
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0MemoDeleteHandler serves
// `DELETE /api/v1/memo/{id}`. The V0 client sends an
// empty body; the shaper produces a V1
// `DeleteMemoRequest{name: "memos/{id}"}` from the
// path. The V1 connect handler returns an empty body
// (`google.protobuf.Empty`); the V0 shaper emits an
// empty `{}` so the Kotlin client deserialises
// cleanly.
//
// The V0 `{id}` is a numeric Long; the handler
// resolves it via `v0ResolveMemoName` before
// dispatching the connect call.
func v0MemoDeleteHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		idStr := e.Request.PathValue("id")
		v0ID, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return crudError(e, http.StatusBadRequest,
				"V0 memo id is not a Long: "+idStr)
		}
		v1Name, rerr := v0ResolveMemoName(e, v0ID)
		if rerr != nil {
			return crudError(e, http.StatusInternalServerError,
				"resolve V0 memo id: "+rerr.Error())
		}
		if v1Name == "" {
			return crudError(e, http.StatusNotFound,
				"V0 memo id not found: "+idStr)
		}
		// Pass the V1 id (not the full
		// resource name); the helper
		// concatenates "memos/" + id.
		v1ID := strings.TrimPrefix(v1Name, "memos/")
		path := map[string]string{"id": v1ID}
		v1body, err := v0MemoDeleteToV1DeleteMemo(nil, path, nil)
		if err != nil {
			if we, ok := err.(*wrapErr); ok {
				return crudError(e, we.status, we.message)
			}
			return crudError(e, http.StatusBadRequest,
				"shape V0 delete: "+err.Error())
		}
		_, status, err := v0DispatchConnect(
			e,
			memosConnectMemoDeleteMemoProcedure,
			v1body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		// V0 client expects 200 with empty body
		// (the legacy Memos 0.21 server
		// returns 200 + `{}`).
		return e.JSON(http.StatusOK, map[string]any{})
	}
}

// ============================================================
// V0 resources (a.k.a. attachments)
// ============================================================

// v0ResourceListHandler serves `GET /api/v1/resource`.
// The V0 response is a top-level array of
// `MemosV0Resource` (no envelope); the V1 connect
// handler returns `ListAttachmentsResponse{attachments: [...]}`
// and the shaper walks the V1 list.
func v0ResourceListHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body, err := v0ResourceListToV1ListAttachments(nil, nil, e.Request.URL.Query())
		if err != nil {
			return crudError(e, http.StatusBadRequest,
				"shape V0 resource list: "+err.Error())
		}
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectAttachmentListAttachmentsProcedure,
			body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		shaped := v0ShapeResourceList(v1Body, requestOriginFromEvent(e))
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0ResourceUploadHandler serves
// `POST /api/v1/resource/blob`. The V0 client uploads
// a single `multipart/form-data` part named `file`.
// The V1 path uses the chunked `UploadAttachment`
// procedure (`spec` → `writeOffset` → `finishWrite`);
// the V0 path is a single-shot upload. The handler
// buffers the file in memory (the V0 client never
// uploads more than a few megabytes — the Memos 0.21
// server had a 32 MB cap), constructs a synthetic V1
// `spec` + a single `writeOffset` that delivers the
// full buffer, and dispatches the chunked procedure.
//
// The V0 client also accepts the response as a
// single `MemosV0Resource` JSON object; the chunked
// procedure returns `{attachment: Attachment}` on
// `finishWrite`, which the V0 shaper converts to the
// V0 resource shape.
func v0ResourceUploadHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		// 1. Parse the multipart form. The V0
		//    client uses a `file` part; the
		//    part can have any name, but
		//    `file` is the documented one.
		if err := e.Request.ParseMultipartForm(64 << 20); err != nil {
			return crudError(e, http.StatusBadRequest,
				"could not parse V0 multipart form: "+err.Error())
		}
		// Walk the multipart form for the
		// first file part. The V0 client names
		// it `file`, but we accept any name
		// for forward-compat (some forks use
		// `data`).
		var (
			fileHdr   *multipart.FileHeader
			partValue string
		)
		for name, fhs := range e.Request.MultipartForm.File {
			if len(fhs) == 0 {
				continue
			}
			fileHdr = fhs[0]
			// Some V0 clients post a sibling
			// `name` field for the attachment
			// display name. Default to the
			// filename.
			if vs, ok := e.Request.MultipartForm.Value[name]; ok && len(vs) > 0 {
				partValue = vs[0]
			}
			break
		}
		if fileHdr == nil {
			return crudError(e, http.StatusBadRequest,
				"V0 upload missing 'file' part")
		}
		if partValue == "" {
			partValue = fileHdr.Filename
		}
		f, err := fileHdr.Open()
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"could not open V0 file part: "+err.Error())
		}
		defer f.Close()
		buf, err := io.ReadAll(f)
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"could not read V0 file part: "+err.Error())
		}
		// 2. Build the synthetic chunked
		//    `UploadAttachment` request. The
		//    V1 procedure accepts a single
		//    `spec` followed by one
		//    `writeOffset` carrying the full
		//    buffer, followed by a
		//    `finishWrite`. We dispatch each
		//    sub-call as a separate connect
		//    request, threading the upload_id
		//    through.
		//
		// The V1 chunked upload treats
		// `spec.attachment.memo` as OPTIONAL
		// (upstream `Attachment.memo` is
		// `OPTIONAL`, and riff's upload handler
		// accepts an empty memo as an
		// "unlinked upload"). The V0 client
		// uploads before it knows the memo
		// id, so we deliberately send no memo
		// on the spec: the row lands unlinked
		// with the `creator` column stamped
		// with the uploader, and the follow-up
		// `PATCH /api/v1/memo/{id}` binds it
		// via `resourceList` (see
		// `v0LinkResourcesToMemo`).
		//
		// This path used to create a hidden stub
		// memo to satisfy a since-relaxed gate.
		// The stub was a phantom memo row for
		// every upload: it had to be cleaned up
		// by the follow-up PATCH, and any
		// client that uploaded without
		// patching left it in the `memos`
		// table forever — the visible symptom of
		// "my post became two memos, one empty
		// with the file on it". Uploading
		// unlinked removes the phantom row
		// entirely and matches upstream.
		//
		// The viewer id is read from the
		// `e.Auth` record (PocketBase's
		// auth middleware populates this
		// from the Bearer token before the
		// handler runs) and falls back to a
		// direct bearer-token resolve for
		// tests / non-middleware paths.
		if resolveV0ViewerID(e) == "" {
			return crudError(e, http.StatusUnauthorized,
				"authentication required for V0 resource upload")
		}

		// The part header is only as good as the client that
		// wrote it: Go's `multipart.CreateFormFile` and
		// several Kotlin upload helpers label every part
		// `application/octet-stream`, and a stale header
		// would pin the row's `mime_type` to a useless
		// value forever. When the header is missing or is the
		// generic octet-stream placeholder, fall back to the
		// filename-derived type — the same table the V1 read
		// path uses for a row with no stored `mime_type`.
		contentType := fileHdr.Header.Get("Content-Type")
		if contentType == "" || contentType == "application/octet-stream" {
			contentType = connect.GuessMimeType(partValue, "application/octet-stream")
		}
		// The V0 path is a single-shot
		// upload: the file is small
		// (the V0 client never uploads
		// more than a few MB), so we
		// collapse the spec + write +
		// finish into a single
		// `UploadAttachment` call that
		// carries the full buffer in the
		// `data` field and sets
		// `finish_write = true`. The V1
		// connect handler supports this
		// shape for small uploads (see
		// `attachment_upload.go::uploadSpec`,
		// "tiny uploads can round-trip
		// in a single RPC").
		spec, err := jsonMarshal(map[string]any{
			"spec": map[string]any{
				"attachment": map[string]any{
					"filename": partValue,
					"type":     contentType,
					// No `memo`: the V0 client uploads
					// before it knows the memo id. The
					// row lands unlinked with `creator`
					// stamped; the follow-up PATCH
					// binds it via `resourceList`.
				},
				"total_size": int64(len(buf)),
			},
			"write_offset": int64(0),
			"data":         buf,
			"finish_write": true,
		})
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"could not build V0 upload spec: "+err.Error())
		}
		specResp, status, err := v0DispatchConnect(
			e,
			memosConnectAttachmentUploadAttachmentProcedure,
			spec,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(specResp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect spec was not JSON: "+err.Error())
		}
		att, _ := v1Body["attachment"].(map[string]any)
		if att == nil {
			att = v1Body
		}
		shaped := v0ShapeResource(att, requestOriginFromEvent(e))
		return e.JSON(http.StatusOK, shaped)
	}
}

// v0ResourceDeleteHandler serves
// `DELETE /api/v1/resource/{id}`. The V0 path uses
// a numeric Long id; the V1 connect handler reads
// a resource name `attachments/{id}`. The handler
// resolves the V0 Long id via `v0ResolveAttachmentName`,
// which lists the caller's attachments and matches
// the V0 id. On a miss the handler surfaces 404.
func v0ResourceDeleteHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		idStr := e.Request.PathValue("id")
		v0ID, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return crudError(e, http.StatusBadRequest,
				"V0 resource id is not a Long: "+idStr)
		}
		v1Name, rerr := v0ResolveAttachmentName(e, v0ID)
		if rerr != nil {
			return crudError(e, http.StatusInternalServerError,
				"resolve V0 resource id: "+rerr.Error())
		}
		if v1Name == "" {
			return crudError(e, http.StatusNotFound,
				"V0 resource id not found: "+idStr)
		}
		body, err := jsonMarshal(map[string]any{"name": v1Name})
		if err != nil {
			return crudError(e, http.StatusInternalServerError,
				"could not build V0 delete body: "+err.Error())
		}
		_, status, err := v0DispatchConnect(
			e,
			memosConnectAttachmentDeleteAttachmentProcedure,
			body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		return e.JSON(http.StatusOK, map[string]any{})
	}
}

// ============================================================
// V0 tags
// ============================================================

// v0TagListHandler serves `GET /api/v1/tag`. The V0
// response is a top-level array of strings; the V1
// connect handler doesn't have a "list all tags" RPC,
// so the shaper walks the recent memos and
// de-duplicates.
func v0TagListHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body, err := v0TagListToV1ListMemosForTags(nil, nil, nil)
		if err != nil {
			return crudError(e, http.StatusBadRequest,
				"shape V0 tag list: "+err.Error())
		}
		resp, status, err := v0DispatchConnect(
			e,
			memosConnectMemoListMemosProcedure,
			body,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		if status >= 400 {
			return nil
		}
		var v1Body map[string]any
		if err := json.Unmarshal(resp, &v1Body); err != nil {
			return crudError(e, http.StatusInternalServerError,
				"connect response was not JSON: "+err.Error())
		}
		shaped := v0ShapeTagList(v1Body)
		return e.JSON(http.StatusOK, shaped)
	}
}

// ============================================================
// Internal helpers
// ============================================================

// v0DispatchConnect is the V0-shaped cousin of
// `connectProxyHandler`. It performs the same
// request-rebuild + dispatch + response-copy dance,
// but the caller passes a pre-shaped V1 body (the V0
// handler has already translated the V0 body into
// the V1 wire format) and the caller does the V0
// response shaping itself. The wrapper is here so
// every V0 handler can dispatch the same way: build
// the upstream request, call the connect mux, copy
// the response back, and return the body bytes.
//
// `pathVars` and `query` are unused in this
// implementation; they're reserved for future
// V0 path-inject wrappers (none today).
func v0DispatchConnect(
	e *core.RequestEvent,
	procedure string,
	body []byte,
	_ map[string]string,
	_ map[string][]string,
) ([]byte, int, error) {
	mux := connectProxyMux()
	if mux == nil {
		e.Response.Header().Set("Content-Type", "application/json")
		e.Response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(e.Response,
			`{"code":13,"message":"connect proxy mux not installed"}`)
		return nil, http.StatusServiceUnavailable, nil
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	upstream, err := http.NewRequestWithContext(
		e.Request.Context(),
		http.MethodPost,
		procedure,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, 0, errors.Wrap(err, "build V0 upstream request")
	}
	upstream.Header = e.Request.Header.Clone()
	upstream.URL.RawQuery = e.Request.URL.RawQuery
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "application/json")
	// Mirror the Host into the header map so the connect-side
	// `WithAuth` interceptor, which reads `info.RequestHeader()`
	// (and so does NOT see `req.Host` directly), can derive the
	// user-facing origin for absolute `externalLink`s. Without
	// this every V0 connect-dispatch response carries a
	// relative `/file/...` URL and the iOS MoeMemos client's
	// host-equality auth check drops the bearer — see
	// `pb/connect/connect.go::ctxKeyRequestOrigin`.
	upstream.Header.Set("Host", e.Request.Host)
	upstream.Host = e.Request.Host
	// Strip the inbound Accept-Encoding (e.g. the
	// gzip caddy forwards from MoeMemos) so the
	// connect server returns plain JSON we can
	// json.Unmarshal. Without this, the response
	// body is gzip-compressed and the V0 shaper
	// throws "invalid character '\\x1f'" because
	// 0x1f 0x8b is the gzip magic number. The V1
	// pass-through path doesn't trip on this
	// because it io.Copy's the body verbatim and
	// lets caddy handle the decompression.
	upstream.Header.Del("Accept-Encoding")
	if e.Auth != nil {
		upstream = upstream.WithContext(contextWithViewer(upstream.Context(), e.Auth))
	}
	resp := newConnectProxyResponseWriter()
	mux.ServeHTTP(resp, upstream)
	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	if status >= 400 {
		// Forward the connect error body
		// unchanged, with the upstream status
		// and headers. The connect-go error
		// envelope (`{code, message}`) is the
		// Kotlin client's expected shape, so
		// the caller does not need to shape
		// anything here.
		for k, vv := range resp.headers {
			for _, v := range vv {
				e.Response.Header().Add(k, v)
			}
		}
		if ct := e.Response.Header().Get("Content-Type"); ct == "" {
			e.Response.Header().Set("Content-Type", "application/json")
		}
		e.Response.WriteHeader(status)
		_, _ = e.Response.Write(resp.body.Bytes())
		return resp.body.Bytes(), status, nil
	}
	return resp.body.Bytes(), status, nil
}

// errV0UpstreamNotFound is the sentinel returned
// by v0ResolveMemoName / v0ResolveAttachmentName
// when the upstream list call returned a non-2xx
// status (typically 401/403, when the V0 caller
// is unauthenticated or forbidden). The connect
// error body is already on e.Response; the
// per-request list-and-match loop bails out
// without trying to shape a V0 id.
var errV0UpstreamNotFound = errors.New("v0Resolve*: upstream list returned an error status")

// resolveV0ViewerID returns the caller's user
// record id, or the empty string if no
// authenticated caller. The PocketBase auth
// middleware populates `e.Auth` from the
// `Authorization: Bearer <token>` header on the
// request; the V0 handlers also accept the same
// header and fall back to a direct bearer-token
// resolve when the middleware didn't run
// (notably in the unit tests, which call
// `mux.ServeHTTP` directly).
//
// Two token shapes are accepted: the
// PocketBase-native JWT (resolved by
// `FindAuthRecordByToken`) and the riff-issued
// `memos_pat_*` personal access token. The
// `memos_pat_*` branch is the common case for
// headless clients; without it, the V0 upload
// (and any other V0 write) would 401 on a
// PAT-only caller.
//
// The PAT lookup mirrors the production
// `connect.resolvePersonalAccessToken` helper
// (which is unexported and package-private) but
// is inlined here so the V0 surface can resolve
// PATs without a circular import. The logic is
// intentionally minimal: prefix check, bcrypt
// compare, and the user-id read. The
// `last_used_at` stamp that the production
// helper applies is omitted; the V0 handlers
// don't read it.
func resolveV0ViewerID(e *core.RequestEvent) string {
	if e.Auth != nil {
		return e.Auth.Id
	}
	auth := e.Request.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	token := auth[len(prefix):]
	if token == "" {
		return ""
	}
	if strings.HasPrefix(token, "memos_pat_") {
		return resolveV0PatOwner(e, token)
	}
	rec, err := e.App.FindAuthRecordByToken("users", token)
	if err != nil || rec == nil {
		return ""
	}
	return rec.Id
}

// resolveV0PatOwner looks up the `users` record
// that owns a `memos_pat_*` token. The token's
// bcrypt digest is stored on the
// `personal_access_tokens` collection; we scan
// the (small) table and compare. The owner is
// the `user` field on the matching row.
func resolveV0PatOwner(e *core.RequestEvent, token string) string {
	col, err := e.App.FindCollectionByNameOrId("personal_access_tokens")
	if err != nil {
		return ""
	}
	rows, err := e.App.FindAllRecords(col)
	if err != nil {
		return ""
	}
	for _, row := range rows {
		hash := row.GetString("token_hash")
		if hash == "" {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)); err != nil {
			continue
		}
		ownerID := row.GetString("user")
		if ownerID == "" {
			return ""
		}
		return ownerID
	}
	return ""
}

// v0ResolveMemoName is the V0-write helper. It
// resolves a V0 Long id (the `{id}` path variable
// in PATCH/DELETE/organizer) to a V1 resource name
// (`memos/<15-char-id>`) by listing the caller's
// memos and matching the V0 id. The list is cheap
// (caller-scoped, bounded by the caller's memo
// count) and the resolution is local to this
// request. Returns the empty string if the V0 id
// is unknown; the caller surfaces a 404.
func v0ResolveMemoName(e *core.RequestEvent, v0ID int64) (string, error) {
	// First, try the per-request map (the V0
	// list handler in the same request may
	// have populated it). PocketBase's
	// `http.Request.Context()` is fresh per
	// request, so in practice the map is
	// always empty here; the keep-alive case
	// is the only one that benefits.
	if v1 := resolveV0IDForRequest(e.Request.Context(), v0ID); v1 != "" {
		return v1, nil
	}
	// Fall through to a fresh list.
	//
	// The request's query string is deliberately NOT
	// forwarded: `?pinned=true` on an organizer or
	// patch call is a leftover from the client's
	// list request, and now that ListMemos honours
	// the CEL filter an unpinned memo would drop out
	// of this lookup. An id resolution has to consider
	// every memo the caller can see.
	body, err := v0MemoListToV1ListMemos(nil, nil, nil)
	if err != nil {
		return "", err
	}
	resp, status, err := v0DispatchConnect(
		e,
		memosConnectMemoListMemosProcedure,
		body,
		nil,
		nil,
	)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		// The connect server already wrote the
		// error body to e.Response; the caller
		// cannot shape a V0 memo name, so
		// signal the miss with an empty string
		// and a non-nil error so the caller's
		// list-and-match loop bails out.
		return "", errV0UpstreamNotFound
	}
	var v1Body map[string]any
	if err := json.Unmarshal(resp, &v1Body); err != nil {
		return "", err
	}
	memos, _ := v1Body["memos"].([]any)
	for _, raw := range memos {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		v1id := strings.TrimPrefix(name, "memos/")
		if v1id == name {
			continue
		}
		if v0IDFromV1(v1id) == v0ID {
			return name, nil
		}
	}
	return "", nil
}

// v0ResolveAttachmentName is the
// V0-resource-delete helper. It mirrors
// v0ResolveMemoName but for the attachments
// collection. The V0 resource id is a Long;
// the V1 `Attachment.name` is
// `attachments/<15-char-id>`.
func v0ResolveAttachmentName(e *core.RequestEvent, v0ID int64) (string, error) {
	body, err := v0ResourceListToV1ListAttachments(nil, nil, e.Request.URL.Query())
	if err != nil {
		return "", err
	}
	resp, status, err := v0DispatchConnect(
		e,
		memosConnectAttachmentListAttachmentsProcedure,
		body,
		nil,
		nil,
	)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		// See the matching block in
		// v0ResolveMemoName: the connect
		// error body is already on
		// e.Response; the caller cannot
		// shape a V0 attachment name, so
		// bail with an empty name and a
		// non-nil error.
		return "", errV0UpstreamNotFound
	}
	var v1Body map[string]any
	if err := json.Unmarshal(resp, &v1Body); err != nil {
		return "", err
	}
	atts, _ := v1Body["attachments"].([]any)
	for _, raw := range atts {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		v1id := strings.TrimPrefix(name, "attachments/")
		if v1id == name {
			continue
		}
		if v0IDFromV1(v1id) == v0ID {
			return name, nil
		}
	}
	return "", nil
}
