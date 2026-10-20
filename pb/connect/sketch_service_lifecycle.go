package connect

import (
	"context"
	"regexp"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// memoResourceNamePattern is the resource-name
// shape `memos/{memo_id}`. The {memo_id} segment
// is the PocketBase row id (15 alphanumeric
// chars). Re-validating the segment here is the
// same defensive pattern the device handler
// uses for `po33-devices/{id}` — a name like
// `memos/../../etc/passwd` is rejected before
// the DB ever sees it.
var memoResourceNamePattern = regexp.MustCompile(`^memos/([A-Za-z0-9]{15})$`)

// isSketchMemo reports whether the memo is a
// PO-33 sketch upload. The pre-1860xxx schema
// carried this on a `source` column
// (`source == "sketch"`); the 1860xxx series
// dropped that column and folded the same
// signal into the presence of a non-null
// `sketch` JSON column.
//
// PocketBase returns the column as a
// non-nil `types.JSONRaw` (a named-type
// wrapper around []byte) containing the
// literal four characters "null" when the
// row was saved without the column
// populated. `rec.Get("sketch") != nil` is
// therefore not enough; the helper also has
// to reject the JSON-null case explicitly.
// The empty `[]byte` is the "key absent"
// case (a row read from a collection that
// does not have the column at all).
func isSketchMemo(rec *core.Record) bool {
	v := rec.Get("sketch")
	if v == nil {
		return false
	}
	if b, ok := v.(types.JSONRaw); ok {
		if len(b) == 0 || string(b) == "null" {
			return false
		}
	}
	return true
}

// PublishSketch flips a sketch memo from DRAFT
// to NORMAL. The caller must be the memo's
// author. A memo that is not in DRAFT is rejected
// with FAILED_PRECONDITION. The same hook that
// gates visibility on state (see
// pb/hooks/memos.go) lets a sketch become PUBLIC
// only after it has been published; this is the
// owner-side counterpart.
//
// The handler does not write any audit log — a
// publish is a lifecycle event, not an upload.
// The device-side upload handler is the only
// authority on "which patch bytes live on disk".
func (s *SketchServer) PublishSketch(ctx context.Context, req *v1.PublishSketchRequest) (*v1.Memo, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" || !isMemoIDShape(memoID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be memos/{id}")
	}
	rec, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("author") != viewer.Id {
		// A non-owner gets NotFound rather than
		// Forbidden so a stranger cannot enumerate
		// sketch memo ids by probing the publish
		// RPC.
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !isSketchMemo(rec) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"only sketch memos have a publish lifecycle")
	}
	if rec.GetString("state") != "DRAFT" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"memo is not in DRAFT state")
	}
	rec.Set("state", "NORMAL")
	if err := s.app.Save(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToMemo(ctx, s.app, rec), nil
}

// ArchiveSketch flips a sketch memo from NORMAL
// to ARCHIVED. The caller must be the memo's
// author. Archive is one-way: a re-publish is
// not supported. Re-uploads of the same sketch
// overwrite the memo's attachment blobs but do
// not change the ARCHIVED state — the
// user-facing memo is archived.
//
// Why one-way: the brief's §6 says "never
// silently modify the original blob", and the
// ARCHIVED state is the user-visible signal
// that a memo is done. A re-publish would
// make the same memo visible in the feed
// again, which is a different kind of edit
// than the lifecycle we want. If a user
// re-activates a sketch by accident, the
// right answer is a new memo + a new upload.
func (s *SketchServer) ArchiveSketch(ctx context.Context, req *v1.ArchiveSketchRequest) (*v1.Memo, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" || !isMemoIDShape(memoID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be memos/{id}")
	}
	rec, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("author") != viewer.Id {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !isSketchMemo(rec) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"only sketch memos have an archive lifecycle")
	}
	if rec.GetString("state") == "ARCHIVED" {
		// Idempotent: archiving an already-archived
		// memo returns the same memo the client
		// already has, with no error. The SPA
		// then renders the same ARCHIVED chip.
		return recordToMemo(ctx, s.app, rec), nil
	}
	if rec.GetString("state") != "NORMAL" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"only NORMAL memos can be archived")
	}
	rec.Set("state", "ARCHIVED")
	if err := s.app.Save(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToMemo(ctx, s.app, rec), nil
}

// isMemoIDShape reports whether s looks like a
// 15-character PocketBase row id. Used by the
// lifecycle handlers to reject names like
// "memos/../../etc/passwd" before the DB
// ever sees them. The shape check is in
// addition to the prefix check the shared
// memoIDFromName helper already does; both
// guards are cheap and the extra defence
// makes the handler resilient to a future
// caller passing a hand-built resource name.
func isMemoIDShape(s string) bool {
	if len(s) != 15 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}
