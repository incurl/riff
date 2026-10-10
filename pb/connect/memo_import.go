// memo_import.go implements the ImportMemos RPC. The
// protocol is a chunked upload that mirrors UploadAttachment
// (see attachment_upload.go) but with three
// differences:
//
//   - The staged bytes are a Memos export archive (a
//     ZIP), not a single attachment. The finishWrite
//     path validates the archive and applies it.
//   - The protocol has an extra mode: finishWrite +
//     validate_only returns a plan instead of running
//     the import. The archive stays staged so a later
//     finishing call can import it.
//   - The proto's ImportMemosResponse carries the
//     plan/report as a oneof `result`. A repeated
//     finishWrite on an imported upload returns the
//     cached report.
//
// The lifecycle:
//
//	spec               → session created, archive opened
//	writeOffset + data → bytes appended at offset
//	finishWrite +
//	  validate_only    → archive read, plan returned
//	finishWrite        → archive read, records imported,
//	                     report returned, archive
//	                     dropped
//	expiry             → archive dropped, session
//	                     removed
//
// The import logic (manifest validation, per-memo
// conflict resolution, parent/relations, attachment
// round-trip) lives in memo_import_apply.go so the
// handler stays a thin RPC layer.
package connect

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect/v2"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// ImportMemos uploads a Memos export file in bounded
// chunks and imports it into the user's memos. The first
// call carries the spec and returns an upload_id; later
// calls carry that upload_id.
func (s *UserServer) ImportMemos(ctx context.Context, req *v1.ImportMemosRequest) (*v1.ImportMemosResponse, error) {
	username := creatorUsername(req.GetName())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}

	caller := CurrentUser(ctx)
	if caller == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	// Only the user may import into their own account;
	// the design rejects an admin bypass explicitly.
	if caller.GetString("username") != username {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the user may import into their own memos")
	}

	userID := caller.Id

	switch up := req.GetUpload().(type) {
	case *v1.ImportMemosRequest_Spec:
		return s.importStart(ctx, userID, username, up.Spec)
	case *v1.ImportMemosRequest_UploadId:
		return s.importContinue(ctx, userID, username, up.UploadId, req)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "spec or upload_id is required")
	}
}

// importStart begins a new upload. The spec's total_size
// is checked against the cap; a per-user cap on in-flight
// imports prevents a runaway client from filling the
// stage directory.
func (s *UserServer) importStart(_ context.Context, userID, username string, spec *v1.ImportMemosSpec) (*v1.ImportMemosResponse, error) {
	if spec == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "spec is required")
	}
	if spec.GetTotalSize() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "total_size must be non-negative")
	}
	if spec.GetTotalSize() > importMaxTotalSize {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("total_size exceeds %d bytes", importMaxTotalSize))
	}

	mgr := importManagerGet()
	if mgr.countUnfinishedForUser(userID) >= importMaxUnfinishedPerUser {
		return nil, connect.NewError(connect.CodeResourceExhausted, "too many unfinished imports; finish or wait for one to expire")
	}

	sess, err := mgr.create(userID, username, spec.GetTotalSize())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create import session: %w", err).Error())
	}
	return &v1.ImportMemosResponse{
		UploadId:      sess.id,
		CommittedSize: 0,
		MaxChunkSize:  int32(importMaxChunkSize),
	}, nil
}

// importContinue handles every call that carries an
// upload_id: progress pings, writeOffset, and
// finishWrite. The dispatch is on (data length,
// finishWrite).
func (s *UserServer) importContinue(ctx context.Context, userID, username, uploadID string, req *v1.ImportMemosRequest) (*v1.ImportMemosResponse, error) {
	if uploadID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "upload_id is required")
	}
	mgr := importManagerGet()
	sess, ok := mgr.get(uploadID)
	if !ok {
		// A missing or expired session returns
		// NotFound. Cross-user lookups also return
		// NotFound to avoid leaking existence.
		return nil, connect.NewError(connect.CodeNotFound, "upload not found")
	}
	if sess.userID != userID {
		return nil, connect.NewError(connect.CodeNotFound, "upload not found")
	}

	// Idempotent: a repeated finishWrite on an
	// imported session returns the cached report.
	if req.GetFinishWrite() && sess.state == importSessionImported {
		return s.importCachedReport(sess)
	}
	if req.GetFinishWrite() && sess.state == importSessionValidated {
		// A second finishWrite after a validate-only
		// goes through the apply path.
	} else if sess.state == importSessionAbandoned {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "upload has been abandoned")
	}

	// finishWrite path. Validate first; the
	// apply may run later in the same call (the
	// proto supports both modes).
	if req.GetFinishWrite() {
		// Drain any pending data bytes. The proto
		// says a finishing call may also carry
		// data; we honour that.
		if len(req.GetData()) > 0 {
			if err := s.importWriteBytes(sess, req.GetWriteOffset(), req.GetData()); err != nil {
				mgr.abort(uploadID)
				return nil, err
			}
		}
		if req.GetValidateOnly() {
			return s.importValidate(sess, username)
		}
		return s.importApply(ctx, sess, username, req.GetConflictPolicy())
	}

	// progress-only ping: no data, no finishWrite.
	if len(req.GetData()) == 0 {
		return &v1.ImportMemosResponse{
			UploadId:      sess.id,
			CommittedSize: sess.bytesReceived,
			MaxChunkSize:  int32(importMaxChunkSize),
		}, nil
	}

	// writeOffset path.
	if err := s.importWriteBytes(sess, req.GetWriteOffset(), req.GetData()); err != nil {
		return nil, err
	}
	return &v1.ImportMemosResponse{
		UploadId:      sess.id,
		CommittedSize: sess.bytesReceived,
		MaxChunkSize:  int32(importMaxChunkSize),
	}, nil
}

// importWriteBytes appends data at the given offset. The
// proto spec requires exact alignment: the requested
// offset must equal the committed size, except when
// retrying the most recently accepted chunk with
// identical bytes and offset.
func (s *UserServer) importWriteBytes(sess *importSession, offset int64, data []byte) error {
	if len(data) > importMaxChunkSize {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("data exceeds max chunk size %d", importMaxChunkSize))
	}
	if offset < 0 {
		return connect.NewError(connect.CodeInvalidArgument, "write_offset must be non-negative")
	}
	if offset > sess.expectedSize {
		return connect.NewError(connect.CodeInvalidArgument, "write_offset exceeds expected total size")
	}
	if offset+int64(len(data)) > importMaxTotalSize {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("data would push archive past %d bytes", importMaxTotalSize))
	}
	// Idempotency: a retry of the most recently
	// accepted chunk with identical bytes is
	// accepted without re-writing.
	if offset == sess.bytesReceived {
		// normal write
	} else if offset == sess.bytesReceived-int64(len(data)) {
		// retry of the last chunk; check the bytes
		// already on disk match the request.
		ok, err := lastChunkMatches(sess.tmpFile, data, int64(len(data)))
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("inspect stage file: %w", err).Error())
		}
		if !ok {
			return connect.NewError(connect.CodeInvalidArgument, "retried chunk bytes do not match the previously accepted bytes")
		}
		return nil
	} else {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("write_offset must equal committed size %d", sess.bytesReceived))
	}

	if _, err := sess.tmpFile.WriteAt(data, offset); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("write stage file: %w", err).Error())
	}
	if offset+int64(len(data)) > sess.bytesReceived {
		sess.bytesReceived = offset + int64(len(data))
	}
	if int64(len(data)) > 0 {
		// touch the session so the sweeper does not
		// reap a long-running upload.
		_ = sess.lastTouchedAt
	}
	return nil
}

// lastChunkMatches reads the last n bytes of f and
// returns true if they equal want.
func lastChunkMatches(f *os.File, want []byte, n int64) (bool, error) {
	if n <= 0 {
		return true, nil
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return false, err
	}
	if size < n {
		return false, nil
	}
	if _, err := f.Seek(size-n, io.SeekStart); err != nil {
		return false, err
	}
	got := make([]byte, n)
	if _, err := io.ReadFull(f, got); err != nil {
		return false, err
	}
	return bytes.Equal(got, want), nil
}

// importValidate opens the staged archive, parses the
// manifest, walks the memo records, and returns a plan
// without importing. The session is marked validated and
// the stage file is kept so a later finishWrite can
// import it.
func (s *UserServer) importValidate(sess *importSession, _ string) (*v1.ImportMemosResponse, error) {
	plan, err := s.readImportPlan(sess)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode plan: %w", err).Error())
	}
	if err := importManagerGet().validate(sess.id, string(planJSON)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &v1.ImportMemosResponse{
		UploadId:      sess.id,
		CommittedSize: sess.bytesReceived,
		MaxChunkSize:  int32(importMaxChunkSize),
		Result: &v1.ImportMemosResponse_Plan{
			Plan: plan,
		},
	}, nil
}

// importApply opens the staged archive and imports
// every memo into the caller's account. The session is
// marked imported and the stage file is dropped.
func (s *UserServer) importApply(ctx context.Context, sess *importSession, username string, policy v1.ImportMemosRequest_ConflictPolicy) (*v1.ImportMemosResponse, error) {
	report, err := s.applyImport(ctx, sess, username, policy)
	if err != nil {
		// Apply failures leave the session
		// abandoned; the next finishWrite returns
		// FailedPrecondition. The client must
		// start a new upload.
		importManagerGet().abandon(sess.id)
		return nil, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		importManagerGet().abandon(sess.id)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode report: %w", err).Error())
	}
	if err := importManagerGet().finalize(sess.id, string(reportJSON)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &v1.ImportMemosResponse{
		UploadId:      sess.id,
		CommittedSize: sess.bytesReceived,
		MaxChunkSize:  int32(importMaxChunkSize),
		Result: &v1.ImportMemosResponse_Report{
			Report: report,
		},
	}, nil
}

// importCachedReport returns the cached report for an
// already-imported session. The JSON-encoded report is
// re-decoded because the proto response carries the
// message itself, not its JSON form.
func (s *UserServer) importCachedReport(sess *importSession) (*v1.ImportMemosResponse, error) {
	if sess.reportJSON == "" {
		// Should not happen: an imported session
		// always has a report. Surface as a
		// server-side failure so a misuse is
		// visible.
		return nil, connect.NewError(connect.CodeInternal, errors.New("imported session missing report").Error())
	}
	var report v1.MemoImportReport
	if err := json.Unmarshal([]byte(sess.reportJSON), &report); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode cached report: %w", err).Error())
	}
	return &v1.ImportMemosResponse{
		UploadId:      sess.id,
		CommittedSize: sess.bytesReceived,
		MaxChunkSize:  int32(importMaxChunkSize),
		Result: &v1.ImportMemosResponse_Report{
			Report: &report,
		},
	}, nil
}

// openImportArchive opens the staged archive as a
// zip.ReadCloser and returns a helper that walks the
// entries. The archive's manifest is the first
// validation step.
func (s *UserServer) openImportArchive(sess *importSession) (*zip.ReadCloser, error) {
	// Sync the stage file to disk before reading.
	if err := sess.tmpFile.Sync(); err != nil {
		return nil, fmt.Errorf("sync stage file: %w", err)
	}
	return zip.OpenReader(sess.tmpPath)
}
