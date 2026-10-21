package connect

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// ListMemoRelations returns the relations a memo owns (rows whose `memo`
// column points at it). The memo must be readable by the caller.
func (s *MemoServer) ListMemoRelations(ctx context.Context, req *v1.ListMemoRelationsRequest) (*v1.ListMemoRelationsResponse, error) {
	memoID := memoIDFromName(req.GetName())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, memo) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}

	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	rows, err := s.app.FindRecordsByFilter("memo_relations",
		"memo={:id}", "-created", limit, offset, map[string]any{"id": memoID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.MemoRelation, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToMemoRelation(s.app, r))
	}
	return &v1.ListMemoRelationsResponse{Relations: out, NextPageToken: nextPageToken(offset, limit, len(rows))}, nil
}

// SetMemoRelations replaces the full set of REFERENCE relations a memo owns.
// It is a replace, not an append: an empty list clears them.
//
// Only the memo's author may rewrite them, because a relation is an assertion
// the memo's author makes about their own content. COMMENT relations are
// rejected — replies are created by CreateMemoComment so the comment memo and
// its relation stay consistent.
//
// The delete-then-create pair runs in one transaction so a rejected target
// cannot leave the memo with its relations already stripped.
func (s *MemoServer) SetMemoRelations(ctx context.Context, req *v1.SetMemoRelationsRequest) (*emptypb.Empty, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "only the author can set a memo's relations")
	}

	// Validate the whole request before touching stored rows.
	targets := make([]string, 0, len(req.GetRelations()))
	for _, rel := range req.GetRelations() {
		if rel.GetType() == v1.MemoRelation_COMMENT {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				"COMMENT relations are created with CreateMemoComment, not SetMemoRelations")
		}
		targetID := memoIDFromName(rel.GetRelatedMemo().GetName())
		if targetID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "every relation needs a related_memo")
		}
		if targetID == memoID {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a memo cannot relate to itself")
		}
		targets = append(targets, targetID)
	}

	err = s.app.RunInTransaction(func(txApp core.App) error {
		existing, err := txApp.FindRecordsByFilter("memo_relations",
			"memo={:id} && type='REFERENCE'", "", 0, 0, map[string]any{"id": memoID})
		if err != nil {
			return err
		}
		for _, rel := range existing {
			if err := deleteRecordRequest(ctx, txApp, rel); err != nil {
				return err
			}
		}

		col, err := txApp.FindCollectionByNameOrId("memo_relations")
		if err != nil {
			return err
		}
		for _, targetID := range targets {
			rel := core.NewRecord(col)
			rel.Set("memo", memoID)
			rel.Set("related_memo", targetID)
			rel.Set("type", "REFERENCE")
			// The hook re-checks that the memo's author is the caller and
			// that the target memo is readable.
			if err := createRecordRequest(ctx, txApp, rel); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &emptypb.Empty{}, nil
}
