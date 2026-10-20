package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// AttachmentServer implements the Memos AttachmentService.
//
// In riff's schema, an attachment is a row in the unified
// `attachments` collection: one row per file, with a single
// `blob` FileField, an optional `memo` relation, the additive
// PO-33 columns (`device_fingerprint_snapshot`), and the
// motion-photo / media-metadata / remote-url sidecars the
// hooks write. The proto's `Attachment` message is one
// resource per row, so the wire shape is unchanged from
// upstream Memos and the `attachments/{id}` resource name
// resolves directly to a record id.
type AttachmentServer struct {
	apiv1connect.UnimplementedAttachmentServiceHandler
	app core.App
}

func NewAttachmentServer(app core.App) *AttachmentServer {
	return &AttachmentServer{app: app}
}

// ListAttachments returns a paginated list of attachments
// from the unified `attachments` collection, filtered by the
// caller's read access.
func (s *AttachmentServer) ListAttachments(ctx context.Context, req *v1.ListAttachmentsRequest) (*v1.ListAttachmentsResponse, error) {
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))

	rows, err := s.app.FindRecordsByFilter("attachments", "", "-created", limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.Attachment, 0, len(rows))
	origin := requestOriginFromContext(ctx)
	for _, r := range rows {
		if !canAccessAttachment(s.app, ctx, r) {
			continue
		}
		if a := resourceToAttachmentWithOrigin(s.app, r, origin); a != nil {
			out = append(out, a)
		}
	}
	return &v1.ListAttachmentsResponse{Attachments: out, NextPageToken: ""}, nil
}

// canAccessAttachment returns true if the caller can read
// the attachment. A linked attachment inherits the parent
// memo's visibility (we delegate to memos.ViewRule); an
// unlinked one is visible only to its uploader, which is the
// upstream Memos contract ("an unlinked attachment is visible
// only to its uploader and admins" — see upstream
// `AttachmentService.GetAttachment` and `checkAttachmentAccess`).
//
// The uploader is the `attachments.creator` column, stamped by
// the upload handler for every row and used by the orphan-bind
// pass in `memo_bind_attachments.go` as the cross-user rebind
// gate. Superusers are admitted for both cases, mirroring
// upstream's `access.CanManageAttachment`, which admits an
// admin for a row of any uploader.
//
// An anonymous caller (viewer == nil) is NOT refused up front.
// memos.ViewRule decides: a PUBLIC memo passes its rule with no
// auth record, so the anonymous feed sees its attachments, while
// a PRIVATE one still fails. Refusing anonymous callers here used
// to make every attachment invisible to the logged-out SPA feed
// even though the same caller's file GET (`/file/attachments/...`,
// which goes through pb/api's `authorizeAttachment`) happily
// served the bytes — the API and the file surface disagreed about
// who a PUBLIC attachment was for. An unlinked row still has no
// anonymous reader: the uploader check below needs an identity.
func canAccessAttachment(app core.App, ctx context.Context, rec *core.Record) bool {
	viewer := CurrentUser(ctx)
	memoID := rec.GetString("memo")
	if memoID == "" {
		if viewer == nil {
			return false
		}
		return viewer.IsSuperuser() ||
			(rec.GetString("creator") != "" && rec.GetString("creator") == viewer.Id)
	}
	memo, err := app.FindRecordById("memos", memoID)
	if err != nil {
		return false
	}
	memos, err := app.FindCollectionByNameOrId("memos")
	if err != nil {
		return false
	}
	requestInfo := &core.RequestInfo{Method: "GET"}
	requestInfo.Auth = viewer
	ok, err := app.CanAccessRecord(memo, requestInfo, memos.ViewRule)
	if err != nil {
		return false
	}
	return ok
}

// resourceToAttachment converts an `attachments` record to an
// Attachment proto. The `filename` column stores the
// user-supplied name; the `external_link` is the
// PocketBase file URL the SPA can stream.
//
// `memo`, `size`, and `creator` are the three OUTPUT_ONLY /
// OPTIONAL fields the upstream Memos `Attachment` contract
// promises and that the V0 `MemosV0Resource` shaper reads
// (`pb/api/upstream_memos_v0_convert.go::v0ShapeResource`).
// They are populated here so both surfaces see the same row:
//
//   - `memo` is `memos/{id}` or empty for an unlinked upload.
//     Upstream declares it `OPTIONAL` and treats an unlinked
//     attachment as a first-class state (see
//     `checkAttachmentAccess`: "For unlinked attachments, only
//     the creator can access"), so the empty case is the
//     signal, not a missing value.
//   - `size` mirrors the `size` column the upload handler
//     writes (`attachment_upload.go`: `rec.Set("size", ...)`).
//     The V0 client reads `size` to render the attachment's
//     byte count; leaving it zero shows every upload as empty.
//   - `creator` is `users/{id}` of the **uploader**, which is
//     the `creator` column. It is deliberately not the linked
//     memo's author — upstream documents the same distinction
//     ("The user who uploaded the attachment, which may differ
//     from the creator of the linked memo"). The column is
//     only stamped on unlinked rows, so a bound row falls back
//     to its memo's author to keep the field populated.
// MemoAttachmentsAsProto returns the attachment rows currently bound to
// `memoID`, in the wire shape the V0 REST shaper needs.
//
// `pb/api`'s V0 handlers bind resources onto a memo *after* the V1 connect
// call that created it has already returned its response body. That body was
// shaped from the memo as it existed before the bind, so its `attachments`
// list is stale — empty. The client would then build its next `resourceList`
// from what it was told it owns, see no files, and drop them on the next
// edit. Re-deriving the list from the store after the bind closes that loop.
//
// This is exported rather than duplicated in `pb/api` because the
// record→proto attachment conversion (`resourceToAttachment`, including the
// `/file/...` URL and the uploader-vs-memo-author distinction) is `pb/connect`'s
// to own; a second copy in the shaper would be free to drift from it.
//
// This variant returns `externalLink` as a relative path — the only caller
// is `pb/api`'s V0 stub-bind flow, which has no request in scope when the
// row needs to be presented to the connect server (the request event has
// already returned the response by then). For request-aware callers use
// `MemoAttachmentsAsProtoWithOrigin` instead.
func MemoAttachmentsAsProto(app core.App, memoID string) []*v1.Attachment {
	return memoAttachmentsWithOrigin(app, memoID, "")
}

// MemoAttachmentsAsProtoWithOrigin is the request-aware form of
// MemoAttachmentsAsProto. It threads the user-facing origin stashed
// on `ctx` through to every emitted attachment so the
// `externalLink` is absolute — see the request-origin comment in
// connect.go for why the connect handlers want this.
func MemoAttachmentsAsProtoWithOrigin(ctx context.Context, app core.App, memoID string) []*v1.Attachment {
	origin := requestOriginFromContext(ctx)
	return memoAttachmentsWithOrigin(app, memoID, origin)
}

// MemoAttachmentsAsProtoWithOriginString is the same form for callers
// that have a string origin (e.g. an upstream REST handler
// computing the origin via `requestOriginFromEvent`) but no
// `context.Context`. Useful for code paths in `pb/api` that
// derive the origin from a `*core.RequestEvent` rather than a
// connect-side interceptor.
func MemoAttachmentsAsProtoWithOriginString(app core.App, memoID, origin string) []*v1.Attachment {
	return memoAttachmentsWithOrigin(app, memoID, origin)
}

func memoAttachmentsWithOrigin(app core.App, memoID, origin string) []*v1.Attachment {
	out := []*v1.Attachment{}
	rows, err := app.FindRecordsByFilter("attachments",
		"memo={:id}", "-created", 0, 0, map[string]any{"id": memoID})
	if err != nil {
		return out
	}
	for _, r := range rows {
		if a := resourceToAttachmentWithOrigin(app, r, origin); a != nil {
			out = append(out, a)
		}
	}
	return out
}

// resourceToAttachment converts one `attachments` row into the V1 wire
// shape.
func resourceToAttachment(app core.App, rec *core.Record) *v1.Attachment {
	return resourceToAttachmentWithOrigin(app, rec, "")
}

// resourceToAttachmentWithOrigin is the request-aware form of
// resourceToAttachment. When `origin` is non-empty, `externalLink`
// is built as an absolute URL with that origin (used by every
// Connect RPC handler); when it is empty, `externalLink` falls
// back to the relative-path form `buildFileURL` returns (used by
// record hooks and batch code paths that have no request in
// scope).
//
// The absolute form is what the MoeMemos iOS client's host
// comparison (see request-origin comment in connect.go) needs to
// pass — a relative URL has `host == nil` and the comparison is
// always false, so the bearer is dropped before `/file/...` ever
// sees it.
func resourceToAttachmentWithOrigin(app core.App, rec *core.Record, origin string) *v1.Attachment {
	if rec == nil {
		return nil
	}
	filename := rec.GetString("filename")
	a := &v1.Attachment{
		Name:         "attachments/" + rec.Id,
		Filename:     filename,
		ExternalLink: buildAttachmentExternalLink(rec.Collection().Name, rec.Id, filename, origin),
		Type:         guessMime(filename, "application/octet-stream"),
		Size:         int64(rec.GetInt("size")),
	}
	// The stored `mime_type` is authoritative when present: it is
	// what the upload handler detected from the bytes, whereas
	// `guessMime` only pattern-matches the filename. Keep the
	// filename guess as the fallback for rows written before the
	// column existed.
	if mt := rec.GetString("mime_type"); mt != "" {
		a.Type = mt
	}
	if memoID := rec.GetString("memo"); memoID != "" {
		name := "memos/" + memoID
		a.Memo = &name
	}
	a.CreateTime = timestampToProto(rec.GetDateTime("created"))
	// `creator` is the uploader. Unlinked rows carry it directly;
	// bound rows only have `memo.author`, which is the closest
	// truthful answer for a row the caller already owns.
	if creatorID := rec.GetString("creator"); creatorID != "" {
		a.Creator = "users/" + creatorID
	} else if app != nil {
		if memoID := rec.GetString("memo"); memoID != "" {
			if memo, err := app.FindRecordById("memos", memoID); err == nil {
				if authorID := memo.GetString("author"); authorID != "" {
					a.Creator = "users/" + authorID
				}
			}
		}
	}
	if mm := readMotionMedia(rec); mm != nil {
		a.MotionMedia = mm
	}
	if md := mediaMetadataFromRecord(rec); md != nil {
		a.MediaMetadata = md
	}
	return a
}

// recordToAttachment is the package-wide entry point for
// converting an `attachments` record to a proto. It is a
// thin alias for resourceToAttachment kept for callers
// that previously switched on collection name.
func recordToAttachment(app core.App, rec *core.Record) *v1.Attachment {
	return resourceToAttachment(app, rec)
}

// recordToAttachmentWithOrigin is the ctx-aware form of
// `recordToAttachment`, used by Connect RPC handlers that
// have a request in scope. The origin is the user-facing
// `scheme://host[:port]` the caller addressed — see
// `requestOriginFromContext`. With an empty origin (no
// interceptor ran, or the request landed outside Connect) the
// caller falls back to the relative-path form.
func recordToAttachmentWithOrigin(ctx context.Context, app core.App, rec *core.Record) *v1.Attachment {
	return resourceToAttachmentWithOrigin(app, rec, requestOriginFromContext(ctx))
}

// buildFileURL composes the file URL for a record.
//
// The URL points at riff's own `/file/...` surface (see
// `pb/api/file_routes.go`), NOT at PocketBase's native
// `/api/files/...` route. The native route only evaluates the
// collection's `ViewRule` when the file field is marked
// `Protected` (see `apis/file.go::download`), and even then it
// wants a short-lived *file token* rather than the caller's
// bearer token — so a native URL could not be read by the
// author of a PRIVATE memo.
//
// The `/file/...` surface authorizes against the same
// `ViewRule` (the memo-visibility rule) using the caller's
// normal PocketBase token, and additionally supports
// `?thumbnail=true`, `?motion=true`, and `?share_token=…`.
//
// The shape mirrors the native route
// (`/file/{collection}/{id}/{filename}`) so the two are
// recognisable; the `attachments` collection is the only one
// with a file field on the public surface.
//
// `buildFileURL` returns a *relative* path. That is the right
// shape for callers with no request in scope (record hooks,
// batch jobs, share-link composers that know the SPA's base)
// and matches every existing consumer. Callers that have a
// request in scope MUST go through `buildAttachmentExternalLink`
// instead — see the request-origin comment in connect.go for
// why a relative URL fails the MoeMemos iOS host comparison
// that gates the bearer token.
func buildFileURL(collection, recordID, filename string) string {
	if filename == "" {
		return ""
	}
	return "/file/" + collection + "/" + recordID + "/" + filename
}

// buildAttachmentExternalLink is the request-aware form of
// `buildFileURL`. With an empty `origin` it is equivalent to
// `buildFileURL`; with a non-empty `origin` (the user-facing
// `https://host[:port]` the inbound request addressed) it
// prefixes that origin to the relative path, producing an
// absolute URL that the MoeMemos iOS client's host-equality
// auth check (`request.url?.host == hostURL.host`) matches.
//
// The origin MUST come from the same inbound request the
// V1/V0 client addressed — `e.OriginalRequest().Host` plus
// `X-Forwarded-Proto` for the connect path, the request event
// for the REST path. Two requests behind the same caddy
// proxy can carry different hosts (the SPA at `riff.example`,
// a community subtree at `riff.example/community/` — same
// origin here, but a second instance behind a different host
// header would diverge) and a per-request origin keeps the URL
// honest. Tests can pass a fixed origin string.
func buildAttachmentExternalLink(collection, recordID, filename, origin string) string {
	if filename == "" {
		return ""
	}
	rel := buildFileURL(collection, recordID, filename)
	if origin == "" {
		return rel
	}
	return origin + rel
}

// guessMime maps a filename extension to a MIME type. Falls
// back to the provided default.
func guessMime(filename, fallback string) string {
	return GuessMimeType(filename, fallback)
}

// GuessMimeType maps a filename's extension to a MIME
// type, falling back to `fallback` for an unknown or
// absent extension. It is the exported form of the
// same table `guessMime` uses, so the V0 REST upload
// path can label a row when the multipart part header
// is missing or carries the generic
// `application/octet-stream` placeholder (which both
// Go's `multipart.CreateFormFile` and several Kotlin
// upload helpers emit).
func GuessMimeType(filename, fallback string) string {
	idx := lastDot(filename)
	if idx < 0 {
		return fallback
	}
	ext := filename[idx+1:]
	switch ext {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "pdf":
		return "application/pdf"
	case "zip":
		return "application/zip"
	case "po33":
		return "application/octet-stream"
	default:
		return fallback
	}
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// findAttachmentRecord resolves "attachments/{id}" to a
// (collection, record) pair. The id is the PocketBase
// record id; the unified `attachments` collection is the
// only place an attachment row can live.
func findAttachmentRecord(app core.App, name string) (collection string, rec *core.Record, err error) {
	id := attachmentIDFromName(name)
	if id == "" {
		return "", nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	r, err := app.FindRecordById("attachments", id)
	if err != nil {
		return "", nil, connect.NewError(connect.CodeNotFound, "attachment not found")
	}
	return "attachments", r, nil
}

// attachmentIDFromName extracts the record id from "attachments/{id}".
func attachmentIDFromName(name string) string {
	const prefix = "attachments/"
	if len(name) <= len(prefix) || !strings.HasPrefix(name, prefix) {
		return ""
	}
	return name[len(prefix):]
}

// GetAttachment returns a single attachment by resource name.
func (s *AttachmentServer) GetAttachment(ctx context.Context, req *v1.GetAttachmentRequest) (*v1.Attachment, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	_, rec, err := findAttachmentRecord(s.app, req.GetName())
	if err != nil {
		return nil, err
	}
	if !canAccessAttachment(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "attachment not found")
	}
	return recordToAttachmentWithOrigin(ctx, s.app, rec), nil
}

// DeleteAttachment removes a single attachment by resource name.
func (s *AttachmentServer) DeleteAttachment(ctx context.Context, req *v1.DeleteAttachmentRequest) (*emptypb.Empty, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	_, rec, err := findAttachmentRecord(s.app, req.GetName())
	if err != nil {
		return nil, err
	}
	if !canAccessAttachment(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "attachment not found")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// BatchDeleteAttachments removes a list of attachments. Each name is
// resolved independently; not-found entries are skipped so the SPA
// can rely on partial-failure semantics.
func (s *AttachmentServer) BatchDeleteAttachments(ctx context.Context, req *v1.BatchDeleteAttachmentsRequest) (*emptypb.Empty, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	for _, name := range req.GetNames() {
		_, rec, err := findAttachmentRecord(s.app, name)
		if err != nil {
			continue
		}
		if !canAccessAttachment(s.app, ctx, rec) {
			continue
		}
		if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
		}
	}
	return &emptypb.Empty{}, nil
}

// CreateAttachment registers metadata for an attachment. The actual
// file upload goes through PocketBase's collection API or
// UploadAttachment. We create a single `attachments` row; the proto
// has no collection discriminator, and riff's admin UI sets the
// `memo`/`filename`/etc. via the request.
func (s *AttachmentServer) CreateAttachment(ctx context.Context, req *v1.CreateAttachmentRequest) (*v1.Attachment, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	a := req.GetAttachment()
	if a == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "attachment is required")
	}
	if a.GetFilename() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "filename is required")
	}
	if a.GetType() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "type is required")
	}
	coll, err := s.app.FindCollectionByNameOrId("attachments")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(coll)
	if req.GetAttachmentId() != "" {
		rec.Id = req.GetAttachmentId()
	}
	rec.Set("filename", a.GetFilename())
	rec.Set("size", 0)
	rec.Set("storage", "local")
	if memoName := a.GetMemo(); memoName != "" {
		if memoID := memoIDFromName(memoName); memoID != "" {
			rec.Set("memo", memoID)
		}
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToAttachmentWithOrigin(ctx, s.app, rec), nil
}

// UpdateAttachment is a metadata round-trip. The proto's optional
// fields are mostly presentation hints; the underlying schema stores
// `filename`, `size`, `storage`, and the PO-33 / motion-photo
// sidecars. We accept the call as a no-op for fields we do not
// mutate, so the SPA can persist without a 501.
func (s *AttachmentServer) UpdateAttachment(ctx context.Context, req *v1.UpdateAttachmentRequest) (*v1.Attachment, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.GetAttachment() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "attachment is required")
	}
	_, rec, err := findAttachmentRecord(s.app, req.GetAttachment().GetName())
	if err != nil {
		return nil, err
	}
	if !canAccessAttachment(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "attachment not found")
	}
	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return recordToAttachmentWithOrigin(ctx, s.app, rec), nil
}
