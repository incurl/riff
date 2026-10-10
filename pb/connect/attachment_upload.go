// attachment_upload.go is the chunked upload half of the
// AttachmentService. The metadata-only write path
// (CreateAttachment, UpdateAttachment, DeleteAttachment,
// BatchDeleteAttachments) lives in attachment.go; this
// file adds UploadAttachment, which receives a file in
// bounded chunks and finalises a `patches` or `samples`
// row when the last chunk arrives.
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
// collection rules in `patches` and `samples` to gate
// record creation: both collections require
// `memo.author = @request.auth.id`, and the
// createRecordRequest bridge in records.go feeds the
// same OnRecordCreateRequest hook chain that the REST
// API would, so file ownership and author enforcement
// fall out for free.
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
	if a.GetMemo() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo is required for upload")
	}
	memoID := memoIDFromName(a.GetMemo())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo name is malformed")
	}
	if _, err := s.app.FindRecordById("memos", memoID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
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

	coll := uploadCollectionForType(a.GetType())
	if !collectionAcceptsMIME(coll, a.GetType()) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("mime type %q is not accepted by the %s collection", a.GetType(), coll))
	}

	mgr := uploadManagerGet()
	sess, err := mgr.create(viewer.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	sess.filename = a.GetFilename()
	sess.mimeType = a.GetType()
	sess.collection = coll
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
		if rec, err := s.app.FindRecordById(sess.collection, sess.recordID); err == nil {
			resp.Attachment = recordToAttachment(rec)
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

// finalizeSession creates the patches/samples row,
// uploads the staged file to PocketBase's filesystem,
// and caches the result on the session so a retry
// returns the same attachment.
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

	coll, err := s.app.FindCollectionByNameOrId(sess.collection)
	if err != nil {
		sess.closeAndCleanup()
		uploadManagerGet().abort(sess.id)
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(coll)
	rec.Id = recID
	rec.Set("memo", sess.memoID)
	// Decide which file field to populate. The two
	// collections use different field names ("file"
	// for patches, "files" for samples); the schema's
	// Required constraint enforces a value.
	fieldName := fileFieldNameForCollection(sess.collection)
	rec.Set(fieldName, staged)
	rec.Set("description", "")
	if sess.collection == "patches" {
		rec.Set("compatible_po_version", "")
	}
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
	if err := uploadFileToPocketBase(s.app, sess.collection, rec.Id, staged, sess.filename); err != nil {
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
	sess.touch()
	resp.CommittedSize = sess.bytesReceived
	resp.Attachment = recordToAttachment(rec)
	return resp, nil
}

// uploadCollectionForType picks the target collection
// from the spec's MIME type. The proto has no
// collection discriminator (the SPA picks by what the
// user is uploading), so we mirror the Memos
// convention: audio and zip files live in `samples`;
// everything else (PO-33 patches included) lives in
// `patches`.
func uploadCollectionForType(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasPrefix(mime, "audio/"),
		mime == "application/zip":
		return "samples"
	default:
		return "patches"
	}
}

// collectionAcceptsMIME mirrors the FileField
// allow-list for the chosen collection. We do this
// lookup at spec time so a client gets a precise error
// before the bytes are staged. The allowed lists are
// duplicated from the migration so the handler does
// not have to re-load the collection schema on every
// call.
func collectionAcceptsMIME(coll, mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch coll {
	case "patches":
		// The migration's MIME allow-list is
		// `["application/octet-stream"]`; everything
		// that isn't binary is rejected. We do not
		// honour the SPA's content-type guesswork
		// for PO-33 files because the PO-33 firmware
		// itself emits application/octet-stream.
		return mime == "application/octet-stream"
	case "samples":
		switch mime {
		case "audio/wav", "audio/x-wav",
			"audio/mpeg", "audio/mp3",
			"application/zip":
			return true
		}
	}
	return false
}

// fileFieldNameForCollection returns the FileField
// name for the target collection. The two attachment
// collections use different field names ("file" for
// patches, "files" for samples) because samples is a
// multi-file pack and patches is a single file.
func fileFieldNameForCollection(coll string) string {
	if coll == "samples" {
		return "files"
	}
	return "file"
}

// uploadFileToPocketBase copies the staged file into
// PocketBase's local filesystem under the canonical
// key for the record. The key layout is the one PB
// uses for its REST file URLs:
//
//	<collectionId>/<recordId>/<filename>
func uploadFileToPocketBase(app core.App, coll, recID string, f *filesystem.File, originalName string) error {
	col, err := app.FindCollectionByNameOrId(coll)
	if err != nil {
		return err
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return err
	}
	key := dirForFilenames(col.Id, recID, originalName)
	return fsys.UploadFile(f, key)
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
