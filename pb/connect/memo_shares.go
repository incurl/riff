package connect

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"

	"github.com/incurl/riff/internal/random"
)

// shareCodeLength is the size of the opaque bearer token we
// generate for each share. 22 chars from the
// [0-9A-Za-z] alphabet is 131 bits of entropy, well past any
// practical guessing attack and well inside URL length limits.
// The migration's TextField pattern accepts 22–32 so a future
// change can grow the code without a migration.
const shareCodeLength = 22

// shareCodeMaxAttempts caps the code-collision retry loop. The
// collection unique index makes the chance of collision
// astronomically small (1 in 2^131 per try), but we still
// bound the loop so a corrupted RNG can't loop forever.
const shareCodeMaxAttempts = 5

// MemoServer now also serves the four MemoShare RPCs:
//
//	CreateMemoShare   — mint a share link for a memo the caller
//	                    authored. Body supplies an optional
//	                    expire_time; the code is generated.
//	ListMemoShares    — list the share links for one memo.
//	                    Memo author or admin only.
//	DeleteMemoShare   — revoke a share link. Memo author or
//	                    admin only. Idempotent.
//	GetSharedMemo     — public, no auth. Resolves a code to the
//	                    memo, but the memo's own visibility still
//	                    applies (a private memo shared by link
//	                    stays private to the holder of the link,
//	                    who by definition is a non-member).
//
// The handlers are the only authority for "who can manage
// shares"; the migration sets every *Rule to nil so the rule
// engine cannot be tricked into letting a wrong caller through
// the collection REST API.

// CreateMemoShare inserts a memo_shares row. The caller must be
// the memo's author or an instance administrator; we delegate
// the admin check to the next call (a future change can plug
// in a real admin role) by allowing superuser-equivalent callers
// through, and otherwise requiring the memo's author.
func (s *MemoServer) CreateMemoShare(ctx context.Context, req *v1.CreateMemoShareRequest) (*v1.MemoShare, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetParent())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "parent is required")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canManageMemoShares(ctx, memo) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the memo's author or an admin can create a share")
	}

	code, err := generateShareCode()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	col, err := s.app.FindCollectionByNameOrId("memo_shares")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	// Retry on the (astronomically unlikely) unique-index
	// collision. A 5-attempt cap turns ~2^131 entropy into a
	// ~5·2^-131 chance of giving up, which is below the noise
	// floor for any plausible system.
	var rec *core.Record
	for attempt := 0; attempt < shareCodeMaxAttempts; attempt++ {
		rec = core.NewRecord(col)
		rec.Set("memo", memoID)
		rec.Set("code", code)
		rec.Set("created_by", viewerID)
		if exp := req.GetMemoShare().GetExpireTime(); exp != nil {
			rec.Set("expire_time", exp.AsTime())
		}
		if err := createRecordRequest(ctx, s.app, rec); err == nil {
			return recordToMemoShare(rec), nil
		} else if !strings.Contains(err.Error(), "validation_failed") && !strings.Contains(err.Error(), "duplicate") {
			return nil, connect.NewError(connect.CodeInternal, err.Error())
		}
		code, err = generateShareCode()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err.Error())
		}
	}
	return nil, connect.NewError(connect.CodeInternal, "could not allocate a unique share code")
}

// ListMemoShares returns every share row for a memo. Memo
// author or admin only.
func (s *MemoServer) ListMemoShares(ctx context.Context, req *v1.ListMemoSharesRequest) (*v1.ListMemoSharesResponse, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetParent())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "parent is required")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canManageMemoShares(ctx, memo) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the memo's author or an admin can list shares")
	}

	rows, err := s.app.FindRecordsByFilter("memo_shares",
		"memo={:id}", "-created", 0, 0,
		map[string]any{"id": memoID},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.MemoShare, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToMemoShare(r))
	}
	return &v1.ListMemoSharesResponse{MemoShares: out}, nil
}

// DeleteMemoShare removes a share row. Idempotent: a missing
// row is treated as success so the Memos client can safely
// retry a delete after a network blip.
func (s *MemoServer) DeleteMemoShare(ctx context.Context, req *v1.DeleteMemoShareRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID, code, ok := splitMemoShareName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canManageMemoShares(ctx, memo) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the memo's author or an admin can revoke a share")
	}
	rows, err := s.app.FindRecordsByFilter("memo_shares",
		"memo={:mid} && code={:c}", "", 1, 0,
		map[string]any{"mid": memoID, "c": code},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if len(rows) == 0 {
		return &emptypb.Empty{}, nil
	}
	if err := deleteRecordRequest(ctx, s.app, rows[0]); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// GetSharedMemo resolves a public share code to the memo it
// grants access to. The handler is unauthenticated: the
// withAuth interceptor never rejects a request without a
// token, and currentUserID is intentionally never called here.
// A share is a bearer token, not a session.
//
// Memo visibility is still enforced: a private memo shared by
// link stays private to the holder of the link (who, by
// definition, has the link and therefore passes canViewMemo's
// author check). A SPACE-visibility memo is not shareable: a
// non-member with a code would still fail canViewMemo, so
// GetSharedMemo returns NotFound rather than leaking the
// memo's existence. This matches the design doc rule that
// "share is read-only, scope-bound access" — the link does
// not elevate the bearer above what they could otherwise see.
func (s *MemoServer) GetSharedMemo(ctx context.Context, req *v1.GetSharedMemoRequest) (*v1.Memo, error) {
	token := req.GetShareToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "share_token is required")
	}
	rows, err := s.app.FindRecordsByFilter("memo_shares",
		"code={:c}", "", 1, 0,
		map[string]any{"c": token},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if len(rows) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, "share not found")
	}
	share := rows[0]

	// Expiry check. An unset expire_time means "never
	// expires"; GetDateTime returns a zero value for empty
	// columns, which the IsZero() guard handles.
	if dt := share.GetDateTime("expire_time"); !dt.IsZero() && dt.Time().Before(time.Now()) {
		return nil, connect.NewError(connect.CodeNotFound, "share expired")
	}

	memo, err := s.app.FindRecordById("memos", share.GetString("memo"))
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, memo) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	return recordToMemo(s.app, memo), nil
}

// canManageMemoShares reports whether the caller can create,
// list, or delete share links for memo. The authorisation rule
// mirrors the Memos 0.31 design: the memo's author is the
// owner of the share surface, and an instance admin (a
// PocketBase superuser) can manage any memo's shares.
//
// PocketBase's Connect handlers execute as the superuser, so
// the record's own rule set never applies. The author check is
// authoritative for non-admin callers. An admin caller is
// detected by comparing CurrentUser to the PocketBase
// superuser record; we go through the same `IsAdmin` helper
// the wave-4 admin paths use.
func canManageMemoShares(ctx context.Context, memo *core.Record) bool {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return false
	}
	if viewer.Id == memo.GetString("author") {
		return true
	}
	// PocketBase's superuser is recorded separately from the
	// `users` collection. The wave-4 admin path checks
	// `viewer.IsSuperuser()`. Apply the same predicate.
	if viewer.IsSuperuser() {
		return true
	}
	return false
}

// recordToMemoShare converts a memo_shares record to the
// v1.MemoShare proto. The `name` segment is the share's code,
// which the frontend embeds directly in the share URL. The
// memo's id appears only for completeness; the client uses
// the `parent` it sent on the create call, not the echoed name.
func recordToMemoShare(rec *core.Record) *v1.MemoShare {
	if rec == nil {
		return nil
	}
	memoID := rec.GetString("memo")
	code := rec.GetString("code")
	out := &v1.MemoShare{
		Name: memoShareName(memoID, code),
	}
	if created := rec.GetDateTime("created"); !created.IsZero() {
		out.CreateTime = timestamppb.New(created.Time())
	}
	if exp := rec.GetDateTime("expire_time"); !exp.IsZero() {
		out.ExpireTime = timestamppb.New(exp.Time())
	}
	return out
}

// memoShareName returns the canonical resource name for a
// share, used both in the wire format and as the lookup key
// in DeleteMemoShare.
func memoShareName(memoID, code string) string {
	return "memos/" + memoID + "/shares/" + code
}

// splitMemoShareName parses memos/{memo}/shares/{share}.
func splitMemoShareName(name string) (memoID, code string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "memos" || parts[2] != "shares" {
		return "", "", false
	}
	if parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

// generateShareCode returns a fresh URL-safe token. Wraps
// internal/random so tests can substitute a deterministic
// source if needed.
var generateShareCode = func() (string, error) {
	return random.String(shareCodeLength)
}
