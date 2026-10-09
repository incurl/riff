package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// ListMemoReactions returns the reactions attached to a memo. The memo must be
// readable by the caller, otherwise reactions would leak the existence of a
// memo the caller cannot see.
func (s *MemoServer) ListMemoReactions(ctx context.Context, req *v1.ListMemoReactionsRequest) (*v1.ListMemoReactionsResponse, error) {
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

	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))
	rows, err := s.app.FindRecordsByFilter("reactions",
		"memo={:id}", "-created", limit, offset, map[string]any{"id": memoID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.Reaction, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToReaction(s.app, memoID, r))
	}
	return &v1.ListMemoReactionsResponse{Reactions: out, NextPageToken: ""}, nil
}

// UpsertMemoReaction adds the caller's reaction of the given type to a memo.
// It is idempotent on the (memo, user, reaction type) triple — the React client
// toggles one emoji at a time and re-sends the same request when the element is
// clicked twice, so a duplicate would show the reaction count twitch.
//
// A user may hold several different reaction types on one memo, which is why
// the lookup keys on the emoji as well as the user.
func (s *MemoServer) UpsertMemoReaction(ctx context.Context, req *v1.UpsertMemoReactionRequest) (*v1.Reaction, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	reactionType := req.GetReaction().GetReactionType()
	if reactionType == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "reaction_type is required")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, memo) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}

	existing, err := s.app.FindRecordsByFilter("reactions",
		"memo={:mid} && user={:uid} && emoji={:e}",
		"", 1, 0,
		map[string]any{"mid": memoID, "uid": viewerID, "e": reactionType},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if len(existing) > 0 {
		return recordToReaction(s.app, memoID, existing[0]), nil
	}

	col, err := s.app.FindCollectionByNameOrId("reactions")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(col)
	rec.Set("memo", memoID)
	rec.Set("user", viewerID)
	rec.Set("emoji", reactionType)
	// The registerReactions hook re-checks memo visibility and Space
	// membership and stamps `user` from the caller.
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToReaction(s.app, memoID, rec), nil
}

// DeleteMemoReaction withdraws one of the caller's reactions. The name is the
// full resource path memos/{memo}/reactions/{reaction} that ListMemoReactions
// handed out.
//
// Per the Space design, a reaction's creator may withdraw it even after losing
// access to the memo, so there is deliberately no parent-memo visibility check
// here — only ownership.
func (s *MemoServer) DeleteMemoReaction(ctx context.Context, req *v1.DeleteMemoReactionRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID, reactionID, ok := splitReactionName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be memos/{memo}/reactions/{reaction}")
	}
	rec, err := s.app.FindRecordById("reactions", reactionID)
	if err != nil || rec.GetString("memo") != memoID {
		return nil, connect.NewError(connect.CodeNotFound, "reaction not found")
	}
	if rec.GetString("user") != viewerID {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the reaction's creator can delete it")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// splitReactionName parses memos/{memo}/reactions/{reaction}.
func splitReactionName(name string) (memoID, reactionID string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "memos" || parts[2] != "reactions" {
		return "", "", false
	}
	if parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[3], true
}
