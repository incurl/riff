package connect

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// SetMemoAttachments replaces the full set of attachments on a memo with the
// provided list. It is a replace, not an append: an empty list clears every
// attachment from the memo. Idempotent.
//
// The proto accepts any "attachments/{id}" the SPA is holding; the handler
// resolves each id to a row in the unified `attachments` collection and
// keeps only the rows whose `memo` is the target. An attachment that belongs
// to a different memo is rejected — the rule on the attachments collection
// is `memo.author = @request.auth.id`, and a save with a new memo would
// either fail the rule (if the new memo's author is not the caller) or
// silently move the file out from under another memo (if the author happens
// to match). Both outcomes are worse than a clean error, so we forbid moves
// here. The SPA only ever sends attachments it already knows belong to the
// memo being edited, so the restriction is invisible in practice.
//
// Only the memo's author may rewrite the set; the rule engine has no
// per-row equivalent because attachments inherit visibility from their
// parent memo rather than the other way round.
//
// The whole rewrite runs in a single transaction so a rejected target
// cannot leave the memo with its attachments already stripped.
func (s *MemoServer) SetMemoAttachments(ctx context.Context, req *v1.SetMemoAttachmentsRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if memo.GetString("author") != viewerID {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the author can set a memo's attachments")
	}

	// Resolve every supplied attachment id to a (collection, record) pair,
	// rejecting ids the SPA cannot have legitimately produced (empty name,
	// non-existent row, cross-memo move).
	wanted := make([]*core.Record, 0, len(req.GetAttachments()))
	seen := make(map[string]struct{}, len(req.GetAttachments()))
	for _, att := range req.GetAttachments() {
		_, rec, err := findAttachmentRecord(s.app, att.GetName())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "attachment not found: "+att.GetName())
		}
		// Reject attachments that belong to a different memo. The SPA only
		// sends ids from the memo's current attachment list, so a foreign
		// memo is always a client bug.
		ownerMemo := rec.GetString("memo")
		if ownerMemo != memoID {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				"attachment belongs to a different memo: "+att.GetName())
		}
		// Dedup within the request — the SPA never sends duplicates, but a
		// defensive dedup here means a stray duplicate cannot produce two
		// pointer entries in the rewrite loop below.
		if _, dup := seen[rec.Id]; dup {
			continue
		}
		seen[rec.Id] = struct{}{}
		wanted = append(wanted, rec)
	}

	wantedIDs := make(map[string]struct{}, len(wanted))
	for _, r := range wanted {
		wantedIDs[r.Id] = struct{}{}
	}

	if err := s.app.RunInTransaction(func(txApp core.App) error {
		// Delete every existing attachment on this memo that is not in the
		// requested set. The unified `attachments` collection has a single
		// `memo` relation, so a single filter is the complete superset.
		existing, err := txApp.FindRecordsByFilter("attachments",
			"memo={:id}", "", 0, 0, map[string]any{"id": memoID})
		if err != nil {
			return err
		}
		for _, r := range existing {
			if _, keep := wantedIDs[r.Id]; keep {
				continue
			}
			if err := deleteRecordRequest(ctx, txApp, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if cErr, ok := err.(*connect.Error); ok {
			return nil, cErr
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &emptypb.Empty{}, nil
}
