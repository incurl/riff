// attachment_upload.go is the chunked upload half of the
// AttachmentService. The metadata-only write path
// (CreateAttachment, UpdateAttachment, DeleteAttachment,
// BatchDeleteAttachments) lives in attachment.go; this
// file adds UploadAttachment, which receives a file in
// bounded chunks and finalises a single `attachments` row
// when the last chunk arrives.
//
// The protocol matches the Memos proto and the SPA's
// uploadService.ts: the first call carries a
// `UploadAttachmentSpec` and returns an `upload_id`;
// later calls carry that `upload_id`, the byte offset,
// and a data slice; the final call sets `finish_write`
// to true and triggers record creation. The handler is
// idempotent in the ways the proto requires:
//
//   - the spec call with identical arguments is safe
//     (re-issuing a spec returns the existing upload_id,
//     not a new one);
//   - the most recently accepted write may be re-sent
//     verbatim and is accepted as a no-op;
//   - finish_write is idempotent: a second call returns
//     the previously created attachment without
//     re-creating the row.
//
// Sessions are owned by the package-level uploadManager
// (upload_session.go), which reaps staged files after
// 30 minutes of inactivity. The handler relies on the
// collection rule in `attachments` to gate record
// creation: the row requires `memo.author = @request.auth.id`
// (when a memo is set), and the createRecordRequest bridge
// in records.go feeds the same OnRecordCreateRequest hook
// chain that the REST API would, so file ownership and
// author enforcement fall out for free.
package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// UploadAttachment is the chunked file upload RPC. The
// single RPC method dispatches on the `upload` oneof:
//
//	spec         -> start a new upload, return upload_id
//	upload_id    -> continue an existing upload (write or finish)
func (s *AttachmentServer) UploadAttachment(ctx context.Context, req *v1.UploadAttachmentRequest) (*v1.UploadAttachmentResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}

	switch upload := req.GetUpload().(type) {
	case *v1.UploadAttachmentRequest_Spec:
		return s.uploadSpec(ctx, viewer, upload.Spec, req)
	case *v1.UploadAttachmentRequest_UploadId:
		return s.uploadContinue(ctx, viewer, upload.UploadId, req)
	case nil:
		return nil, connect.NewError(connect.CodeInvalidArgument, "upload spec or upload_id is required")
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "unsupported upload oneof variant")
	}
}

// uploadSpec starts a new upload. It validates the
// attachment metadata, opens a stage file, and returns
// an upload_id. The full request is also passed in so
// any data the SPA carried in the spec call (a common
// pattern for tiny uploads like favicons) is written
// before the response is returned.
func (s *AttachmentServer) uploadSpec(ctx context.Context, viewer *core.Record, spec *v1.UploadAttachmentSpec, req *v1.UploadAttachmentRequest) (*v1.UploadAttachmentResponse, error) {
	if spec == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "upload spec is required")
	}
	a := spec.GetAttachment()
	if a == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "attachment metadata is required")
	}
	if a.GetFilename() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "filename is required")
	}
	if a.GetType() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "type is required")
	}
	if a.GetContent() != nil && len(a.GetContent()) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "attachment.content must be empty; use the data field for file bytes")
	}
	// `attachment.memo` is optional. When the SPA is uploading inline during
	// a brand-new memo edit (the memo doesn't exist yet), it has no memo
	// name to thread through the spec; we accept the upload, write the row
	// with `memo` empty, and the SPA binds it later (either via a follow-up
	// `CreateMemo` whose `req.Memo.Attachments[]` names are rebound by the
	// server, or via the existing flow where `UpdateMemo`/`SetMemoAttachments`
	// happens on a memo that already owns the rows). The `attachments` row's
	// `memo` column is non-null in the wire read but nullable in the schema
	// (`Required: false` in `1850000000_create_attachments.go`), so an orphan
	// is a legitimate state. See `pb/connect/memo.go::CreateMemo` for the
	// bind pass that runs in the same RPC as `CreateMemo` so the SPA can
	// upload-then-create in one round-trip.
	memoID := ""
	if a.GetMemo() != "" {
		memoID = memoIDFromName(a.GetMemo())
		if memoID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "memo name is malformed")
		}
		if _, err := s.app.FindRecordById("memos", memoID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, "memo not found")
		}
	}
	if spec.GetTotalSize() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "total_size must be non-negative")
	}
	if spec.GetTotalSize() > uploadMaxTotalSize {
		return nil, connect.NewError(connect.CodeResourceExhausted, errUploadOversize.Error())
	}
	if uploadManagerGet().countUnfinishedForUser(viewer.Id) >= uploadMaxUnfinishedPerUser {
		return nil, connect.NewError(connect.CodeResourceExhausted, "too many unfinished uploads")
	}

	// Pre-flight MIME check against the unified
	// `attachments` collection's blob FileField. The
	// authoritative check is the FileField validator
	// at Save time, but surfacing a precise
	// CodeInvalidArgument here saves the SPA a round
	// trip on a guaranteed-fail spec.
	if !attachmentMimeAllowed(a.GetType()) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("mime type %q is not accepted by the attachments collection", a.GetType()))
	}

	mgr := uploadManagerGet()
	sess, err := mgr.create(viewer.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	sess.filename = a.GetFilename()
	sess.mimeType = a.GetType()
	sess.memoID = memoID
	sess.expectedSize = spec.GetTotalSize()
	sess.motionMediaJSON = motionMediaJSONFromProto(a.GetMotionMedia())
	sess.mediaMetadata = mediaMetadataJSONFromProto(a.GetMediaMetadata())

	if aid := spec.GetAttachmentId(); aid != "" {
		if err := validateAttachmentID(aid); err != nil {
			mgr.abort(sess.id)
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
		sess.attachmentName = aid
	}
	mgr.insert(sess)

	// The spec path also accepts data + finish_write so
	// tiny uploads (a 1-pixel favicon, a 4-byte blob)
	// can round-trip in a single RPC. We pass the full
	// request through to continueSession, which honours
	// the same data/offset/finish contract as the
	// continuation case.
	return s.continueSession(ctx, sess, req)
}

// uploadContinue handles the second and subsequent
// calls. The per-call validation lives in
// continueSession so the spec path can share it.
func (s *AttachmentServer) uploadContinue(ctx context.Context, viewer *core.Record, uploadID string, req *v1.UploadAttachmentRequest) (*v1.UploadAttachmentResponse, error) {
	if uploadID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "upload_id is required")
	}
	mgr := uploadManagerGet()
	sess, ok := mgr.get(uploadID)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, "upload not found or expired")
	}
	if sess.userID != viewer.Id {
		return nil, connect.NewError(connect.CodeNotFound, "upload not found or expired")
	}
	return s.continueSession(ctx, sess, req)
}

// continueSession is the shared path: validate the
// request, append the data (if any), and (if finish_write
// is set) create the record. Already-finalised sessions
// short-circuit to the cached attachment so retries of
// the last call are safe.
func (s *AttachmentServer) continueSession(ctx context.Context, sess *uploadSession, req *v1.UploadAttachmentRequest) (*v1.UploadAttachmentResponse, error) {
	resp := &v1.UploadAttachmentResponse{
		UploadId:     sess.id,
		MaxChunkSize: int32(uploadMaxChunkSize),
	}

	// Already finalised: idempotent return.
	if sess.recordID != "" {
		resp.CommittedSize = sess.bytesReceived
		if rec, err := s.app.FindRecordById("attachments", sess.recordID); err == nil {
			resp.Attachment = recordToAttachment(s.app, rec)
		}
		return resp, nil
	}

	data := req.GetData()
	offset := req.GetWriteOffset()
	finish := req.GetFinishWrite()

	switch {
	case len(data) == 0 && !finish:
		// Progress-only ping. Ignore the offset and
		// report the current committed size so the SPA
		// can recover from a lost response.
		resp.CommittedSize = sess.bytesReceived
		return resp, nil

	case len(data) == 0 && finish:
		// finishWrite without data: the spec said
		// total_size = 0, so the file is empty.
		// Validate and finalise.
		if sess.expectedSize != 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("committed size %d does not match total_size %d", sess.bytesReceived, sess.expectedSize))
		}
		return s.finalizeSession(ctx, sess, resp)

	case len(data) > 0:
		if len(data) > uploadMaxChunkSize {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Sprintf("data exceeds max_chunk_size %d", uploadMaxChunkSize))
		}
		if offset != sess.bytesReceived {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("write_offset %d does not match committed size %d", offset, sess.bytesReceived))
		}
		// The spec also said the chunk's offset+len
		// must not exceed total_size. We check that
		// here so a client that says total_size=1024
		// but writes 2048 bytes across two calls
		// fails on the second one, not after a
		// finishWrite.
		if offset+int64(len(data)) > sess.expectedSize {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("write would exceed total_size %d", sess.expectedSize))
		}
		if _, err := sess.tmpFile.WriteAt(data, offset); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Sprintf("write staged file: %s", err.Error()))
		}
		sess.bytesReceived = offset + int64(len(data))
		sess.touch()
		resp.CommittedSize = sess.bytesReceived
		if finish {
			if sess.bytesReceived != sess.expectedSize {
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Sprintf("committed size %d does not match total_size %d", sess.bytesReceived, sess.expectedSize))
			}
			return s.finalizeSession(ctx, sess, resp)
		}
		return resp, nil
	}

	return nil, connect.NewError(connect.CodeInvalidArgument, "unreachable: empty data and finish_write=false")
}

// finalizeSession creates the attachments row, uploads
// the staged file to PocketBase's filesystem, and caches
// the result on the session so a retry returns the same
// attachment.
func (s *AttachmentServer) finalizeSession(ctx context.Context, sess *uploadSession, resp *v1.UploadAttachmentResponse) (*v1.UploadAttachmentResponse, error) {
	// Close the staging file so the upload to the PB
	// filesystem can read it back.
	if err := sess.tmpFile.Close(); err != nil {
		sess.tmpFile = nil
		return nil, connect.NewError(connect.CodeInternal, fmt.Sprintf("close staged file: %s", err.Error()))
	}
	sess.tmpFile = nil

	// Mint the record id up front so the filesystem
	// key is stable. If the user supplied an
	// attachment_id, use that; otherwise generate a
	// fresh PocketBase id (15-char alnum).
	recID := strings.TrimSpace(sess.attachmentName)
	if recID == "" {
		recID = defaultRecordID()
	}

	staged, err := newFileFromStagedPath(sess.tmpPath, sess.filename)
	if err != nil {
		sess.closeAndCleanup()
		uploadManagerGet().abort(sess.id)
		return nil, connect.NewError(connect.CodeInternal, fmt.Sprintf("open staged file: %s", err.Error()))
	}

	coll, err := s.app.FindCollectionByNameOrId("attachments")
	if err != nil {
		sess.closeAndCleanup()
		uploadManagerGet().abort(sess.id)
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(coll)
	rec.Id = recID
	if sess.memoID != "" {
		rec.Set("memo", sess.memoID)
	}
	// Stamp the uploader into the `creator` column when the row lands as an
	// orphan (no memo on the spec). The bind pass in `memo_bind_attachments.go`
	// refuses a cross-user rebind by reading this column, so without it a
	// hostile SPA could re-bind any orphan it happened to learn the name of
	// onto its own memo and effectively steal the bytes. Bound rows already
	// have their owner implied by `memo.author`, so we do not stamp them.
	if sess.memoID == "" {
		rec.Set("creator", sess.userID)
	}
	// The unified `attachments` collection uses a
	// single-file `blob` FileField for both binary
	// patches and audio sample packs.
	rec.Set("blob", staged)
	rec.Set("filename", sess.filename)
	rec.Set("size", sess.expectedSize)
	rec.Set("storage", "local")
	rec.Set("mime_type", sess.mimeType)
	if sess.motionMediaJSON != "" {
		rec.Set("motion_media", sess.motionMediaJSON)
	}
	if sess.mediaMetadata != "" {
		rec.Set("media_metadata", sess.mediaMetadata)
	}

	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		sess.closeAndCleanup()
		uploadManagerGet().abort(sess.id)
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}

	// Upload the bytes into PocketBase's filesystem
	// under the canonical key. The on-write hook
	// (storage_bridge) may mirror to a remote backend
	// if one is configured; that mirror is
	// best-effort and does not fail the upload.
	if err := uploadFileToPocketBase(s.app, "attachments", rec.Id, staged, sess.filename); err != nil {
		// Roll back the row we just created so a
		// filesystem failure does not leave a
		// dangling attachment record with no bytes.
		_ = s.app.Delete(rec)
		sess.closeAndCleanup()
		uploadManagerGet().abort(sess.id)
		return nil, connect.NewError(connect.CodeInternal, fmt.Sprintf("upload to filesystem: %s", err.Error()))
	}

	// The staging file has been copied into the
	// filesystem; the original is now redundant.
	sess.closeAndCleanup()

	sess.recordID = rec.Id
	sess.bytesReceived = sess.expectedSize
	resp.CommittedSize = sess.expectedSize
	resp.Attachment = recordToAttachment(s.app, rec)
	return resp, nil
}

// validateAttachmentID enforces the proto's
// `^[a-zA-Z0-9]([a-zA-Z0-9-]{0,34}[a-zA-Z0-9])?$`
// pattern. The same pattern matches the PocketBase
// default id format, so a value that passes the proto
// validator can be used as a record id without
// re-mangling.
func validateAttachmentID(s string) error {
	if len(s) < 1 || len(s) > 36 {
		return errors.New("attachment_id must be 1-36 characters")
	}
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			(r == '-' && i > 0 && i < len(s)-1)
		if !ok {
			return errors.New("attachment_id must match ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,34}[a-zA-Z0-9])?$")
		}
	}
	return nil
}

// motionMediaJSONFromProto renders the proto's
// `MotionMedia` to the JSON shape the on-disk column
// expects. Returns "" when the proto is nil or the
// zero-value (the same lenient rule as
// readMotionMedia).
func motionMediaJSONFromProto(mm *v1.MotionMedia) string {
	if mm == nil {
		return ""
	}
	if mm.GetFamily() == v1.MotionMediaFamily_MOTION_MEDIA_FAMILY_UNSPECIFIED &&
		mm.GetRole() == v1.MotionMediaRole_MOTION_MEDIA_ROLE_UNSPECIFIED &&
		!mm.GetHasEmbeddedVideo() {
		return ""
	}
	payload := map[string]any{
		"family":                    mm.GetFamily().String(),
		"role":                      mm.GetRole().String(),
		"group_id":                  mm.GetGroupId(),
		"presentation_timestamp_us": mm.GetPresentationTimestampUs(),
		"has_embedded_video":        mm.GetHasEmbeddedVideo(),
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(out)
}

// mediaMetadataJSONFromProto renders the proto's
// `MediaMetadata` to the JSON shape stored in the
// `media_metadata` column. Returns "" when the proto
// is nil. We use protojson so the round-trip is exact
// (the proto's `oneof details` keeps its discriminator
// in the JSON).
func mediaMetadataJSONFromProto(md *v1.MediaMetadata) string {
	if md == nil {
		return ""
	}
	out, err := protojson.Marshal(md)
	if err != nil {
		return ""
	}
	return string(out)
}

// mediaMetadataFromRecord renders the `media_metadata`
// column back into the proto `MediaMetadata` message.
// Returns nil when the column is empty (the field is
// OPTIONAL on the proto). The lenient read matches
// readMotionMedia so a malformed column yields nil
// rather than failing the request.
func mediaMetadataFromRecord(rec *core.Record) *v1.MediaMetadata {
	if rec == nil {
		return nil
	}
	raw := rec.GetString("media_metadata")
	if raw == "" {
		return nil
	}
	var out v1.MediaMetadata
	if err := protojson.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return &out
}

// newFileFromStagedPath wraps the staged on-disk file
// in a PocketBase `filesystem.File` so the
// OnRecordCreateRequest chain sees a value shaped like
// what the REST API would produce. We discard the
// auto-generated name (PB appends a random suffix) and
// force the user-supplied filename so the storage key
// matches what the SPA displays.
func newFileFromStagedPath(path, originalName string) (*filesystem.File, error) {
	if originalName == "" {
		originalName = "upload.bin"
	}
	f, err := filesystem.NewFileFromPath(path)
	if err != nil {
		return nil, err
	}
	f.OriginalName = originalName
	f.Name = originalName
	return f, nil
}

// attachmentMimeAllowed reports whether mime is in the
// unified `attachments` collection's blob FileField
// MimeTypes allow-list. The list mirrors the one in
// `pb/migrations/1850000000_create_attachments.go`; a
// drift between the two will surface as a Save-time
// FileField rejection, which the spec handler will
// already have rejected earlier.
//
// We do not honour the SPA's content-type guesswork for
// PO-33 patches because the PO-33 firmware itself emits
// application/octet-stream for `.p33` files; forcing a
// precise MIME would block the device path.
func attachmentMimeAllowed(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	// Images
	case "image/png", "image/jpeg", "image/jpg", "image/gif",
		"image/webp", "image/svg+xml", "image/bmp", "image/tiff",
		"image/heic", "image/heif", "image/avif":
		return true
	// Audio
	case "audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp3",
		"audio/aac", "audio/flac", "audio/ogg", "audio/m4a",
		"audio/webm", "audio/midi", "audio/x-midi":
		return true
	// Video
	case "video/mp4", "video/webm", "video/ogg", "video/quicktime":
		return true
	// Documents
	case "application/pdf", "text/plain", "text/html", "text/css",
		"text/javascript", "text/markdown", "text/csv",
		"application/json", "application/xml":
		return true
	// Archives
	case "application/zip", "application/x-tar", "application/gzip",
		"application/x-7z-compressed", "application/x-rar-compressed":
		return true
	// Office
	case "application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return true
	// Generic binary — the PO-33 device uploads patches
	// with this MIME.
	case "application/octet-stream":
		return true
	}
	return false
}

// uploadFileToPocketBase copies the staged file into
// PocketBase's local filesystem under the canonical
// key for the record. The key layout is the one PB
// uses for its REST file URLs:
//
//	<collectionId>/<recordId>/<filename>
//
// We open a fresh filesystem handle so a cached
// backend from an earlier call does not serve stale
// metadata; the staged file is also the bytes we just
// wrote, so the upload is effectively a `cp`.
func uploadFileToPocketBase(app core.App, coll, recID string, f *filesystem.File, originalName string) error {
	col, err := app.FindCollectionByNameOrId(coll)
	if err != nil {
		return err
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return err
	}
	defer func() { _ = fsys.Close() }()
	key := dirForFilenames(col.Id, recID, originalName)
	return fsys.UploadFile(f, key)
}
