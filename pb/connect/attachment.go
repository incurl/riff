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
// Phase 4 wired the read path (ListAttachments). Wave 5 adds
// the metadata-only write path (Create/Get/Update/Delete and
// BatchDelete). File uploads go through PocketBase's standard
// /api/collections/{patches,samples}/records endpoints; the
// chunked UploadAttachment RPC remains unimplemented because
// riff doesn't ship an upload-session manager.
//
// In our schema, "attachments" is a federated concept: a
// PO-33 patch is stored in the `patches` collection and a
// sample-pack audio file is stored in `samples`. Both have
// a file field. The methods below translate resource names
// like "attachments/{id}" back to a record by id.
type AttachmentServer struct {
	apiv1connect.UnimplementedAttachmentServiceHandler
	app core.App
}

func NewAttachmentServer(app core.App) *AttachmentServer {
	return &AttachmentServer{app: app}
}

// ListAttachments returns a paginated list of attachments
// across the patches and samples collections, filtered by
// the caller's read access.
//
// Pagination is applied per source collection, so a request
// for the newest N attachments may return up to 2N rows when
// both patches and samples have data. We accept that while the
// collections are small; a merged, cursor-paginated view can
// come later if the feed grows enough to need exact page sizes.
func (s *AttachmentServer) ListAttachments(ctx context.Context, req *v1.ListAttachmentsRequest) (*v1.ListAttachmentsResponse, error) {
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))

	out := []*v1.Attachment{}

	patches, err := s.app.FindRecordsByFilter("patches", "", "-created", limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	for _, p := range patches {
		if !canAccessAttachment(s.app, ctx, p) {
			continue
		}
		if a := patchToAttachment(p); a != nil {
			out = append(out, a)
		}
	}

	samples, err := s.app.FindRecordsByFilter("samples", "", "-created", limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	for _, sa := range samples {
		if !canAccessAttachment(s.app, ctx, sa) {
			continue
		}
		if a := sampleToAttachment(sa); a != nil {
			out = append(out, a)
		}
	}

	return &v1.ListAttachmentsResponse{Attachments: out, NextPageToken: ""}, nil
}

// canAccessAttachment returns true if the caller can read
// the parent memo. We delegate to memos.ViewRule; if the
// parent memo doesn't exist (orphaned attachment), deny.
func canAccessAttachment(app core.App, ctx context.Context, rec *core.Record) bool {
	memoID := rec.GetString("memo")
	if memoID == "" {
		return false
	}
	memo, err := app.FindRecordById("memos", memoID)
	if err != nil {
		return false
	}
	memos, _ := app.FindCollectionByNameOrId("memos")
	requestInfo := &core.RequestInfo{Method: "GET"}
	if viewer := CurrentUser(ctx); viewer != nil {
		requestInfo.Auth = viewer
	}
	ok, err := app.CanAccessRecord(memo, requestInfo, memos.ViewRule)
	if err != nil {
		return false
	}
	return ok
}

// patchToAttachment converts a `patches` record to an
// Attachment proto.
func patchToAttachment(rec *core.Record) *v1.Attachment {
	if rec == nil {
		return nil
	}
	filename := rec.GetString("file")
	a := &v1.Attachment{
		Name:         "attachments/" + rec.Id,
		Filename:     filename,
		ExternalLink: buildFileURL(rec.Collection().Name, rec.Id, filename),
		Type:         guessMime(filename, "application/octet-stream"),
	}
	a.CreateTime = timestampToProto(rec.GetDateTime("created"))
	if mm := readMotionMedia(rec); mm != nil {
		a.MotionMedia = mm
	}
	return a
}

// sampleToAttachment converts a `samples` record to an
// Attachment proto. We pick the first file as the
// representative; the frontend resolves the rest via the
// related memo's attachments field.
func sampleToAttachment(rec *core.Record) *v1.Attachment {
	if rec == nil {
		return nil
	}
	files := rec.GetStringSlice("files")
	if len(files) == 0 {
		return nil
	}
	filename := files[0]
	a := &v1.Attachment{
		Name:         "attachments/" + rec.Id,
		Filename:     filename,
		ExternalLink: buildFileURL(rec.Collection().Name, rec.Id, filename),
		Type:         guessMime(filename, "audio/wav"),
	}
	a.CreateTime = timestampToProto(rec.GetDateTime("created"))
	if mm := readMotionMedia(rec); mm != nil {
		a.MotionMedia = mm
	}
	return a
}

// buildFileURL composes the PocketBase file URL for a record.
func buildFileURL(collection, recordID, filename string) string {
	if filename == "" {
		return ""
	}
	return "/api/files/" + collection + "/" + recordID + "/" + filename
}

// guessMime maps a filename extension to a MIME type. Falls
// back to the provided default.
func guessMime(filename, fallback string) string {
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

// findAttachmentRecord resolves "attachments/{id}" to a (collection, record)
// pair. The id is the PocketBase record id; we look first in `patches`, then
// in `samples`. Returns the empty string for collection when not found.
func findAttachmentRecord(app core.App, name string) (collection string, rec *core.Record, err error) {
	id := attachmentIDFromName(name)
	if id == "" {
		return "", nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	for _, coll := range []string{"patches", "samples"} {
		r, err := app.FindRecordById(coll, id)
		if err == nil {
			return coll, r, nil
		}
	}
	return "", nil, connect.NewError(connect.CodeNotFound, "attachment not found")
}

// attachmentIDFromName extracts the record id from "attachments/{id}".
func attachmentIDFromName(name string) string {
	const prefix = "attachments/"
	if len(name) <= len(prefix) || !strings.HasPrefix(name, prefix) {
		return ""
	}
	return name[len(prefix):]
}

// recordToAttachment dispatches to patchToAttachment/sampleToAttachment
// based on the record's collection.
func recordToAttachment(rec *core.Record) *v1.Attachment {
	if rec == nil {
		return nil
	}
	switch rec.Collection().Name {
	case "patches":
		return patchToAttachment(rec)
	case "samples":
		return sampleToAttachment(rec)
	default:
		return nil
	}
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
	return recordToAttachment(rec), nil
}

// DeleteAttachment removes a single attachment. We go through the
// record-request bridge so the `patches`/`samples` collection rules apply.
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
// file upload goes through PocketBase's collection API or, in the
// future, the UploadAttachment RPC. We create a `patches` row by
// default; the proto has no collection discriminator, and riff's
// admin UI sets the type via the route.
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
	coll, err := s.app.FindCollectionByNameOrId("patches")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(coll)
	if req.GetAttachmentId() != "" {
		rec.Id = req.GetAttachmentId()
	}
	rec.Set("description", "")
	if memoName := a.GetMemo(); memoName != "" {
		if memoID := memoIDFromName(memoName); memoID != "" {
			rec.Set("memo", memoID)
		}
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToAttachment(rec), nil
}

// UpdateAttachment is a metadata round-trip. The proto's optional
// fields are mostly presentation hints and the underlying schema only
// stores `file`, `description`, `compatible_po_version`. We accept
// the call as a no-op so the SPA can persist without a 501.
func (s *AttachmentServer) UpdateAttachment(ctx context.Context, req *v1.UpdateAttachmentRequest) (*v1.Attachment, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.GetAttachment() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "attachment is required")
	}
	coll, rec, err := findAttachmentRecord(s.app, req.GetAttachment().GetName())
	if err != nil {
		return nil, err
	}
	_ = coll
	if !canAccessAttachment(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "attachment not found")
	}
	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return recordToAttachment(rec), nil
}
