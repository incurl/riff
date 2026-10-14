package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// UserStats methods on UserServer. The proto asks for memo counts per
// type, per-tag counts, and the timestamps that drive the activity
// heatmap. riff's `memos.type` column only distinguishes "note" from
// "patch", so the per-type counters are mostly zero; the heatmap uses
// the timestamps verbatim, which is the data the React client actually
// renders.
//
// Auth model: align with upstream Memos's `ListUserStats` /
// `GetUserStats` (renamed in upstream, still `ListAllUserStats` here
// for wire compatibility with the existing SPA hook). Any caller —
// authenticated or anonymous — is allowed. The per-memo visibility
// filter (`memos.ViewRule` evaluated per row via `CanAccessRecord`)
// decides which memos contribute to the per-user tally. Anonymous
// callers therefore see only authors with at least one PUBLIC memo,
// and authenticated callers see PUBLIC + PROTECTED + their own
// PRIVATE + the SPACE memos of any space they actively belong to.
// The optional `filter` is accepted for proto compatibility but is
// not yet translated to a PocketBase filter clause; the React client
// never sends one.

// GetUserStats returns the named user's stats, filtered to memos the
// caller can read. Anonymous callers are accepted; the visibility
// filter narrows the response to PUBLIC memos of the named user.
func (s *UserServer) GetUserStats(ctx context.Context, req *v1.GetUserStatsRequest) (*v1.UserStats, error) {
	username := creatorUsername(req.GetName())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}

	stats, err := computeUserStatsForViewer(s.app, ctx, target.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	stats.Name = "users/" + target.GetString("username") + "/stats"
	return stats, nil
}

// ListAllUserStats returns one UserStats row per user with at least
// one memo the caller can read. The auth contract is "any caller";
// the response is filtered by per-memo visibility so a non-admin or
// anonymous caller only sees authors they would have any business
// seeing in the directory view. The `State` field is accepted for
// proto compatibility but riff's `memos` collection has no
// `row_status` column, so the ARCHIVED branch from upstream does
// not apply; this implementation always treats the request as
// NORMAL.
func (s *UserServer) ListAllUserStats(ctx context.Context, _ *v1.ListAllUserStatsRequest) (*v1.ListAllUserStatsResponse, error) {
	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rows, err := s.app.FindRecordsByFilter(users, "", "", 0, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.UserStats, 0, len(rows))
	for _, r := range rows {
		stats, err := computeUserStatsForViewer(s.app, ctx, r.Id)
		if err != nil {
			continue
		}
		// Skip authors the caller cannot see at all — that is, users
		// whose memos are all PROTECTED/PRIVATE/SPACE and not visible
		// to this viewer. The empty `total_memo_count` is the
		// "no visible memos" signal.
		if stats.GetTotalMemoCount() == 0 {
			continue
		}
		stats.Name = "users/" + r.GetString("username") + "/stats"
		out = append(out, stats)
	}
	return &v1.ListAllUserStatsResponse{Stats: out}, nil
}

// computeUserStatsForViewer is the per-caller wrapper around
// computeUserStats that drops memos the caller cannot read. It uses
// the same `memos.ViewRule` the ListMemos handler relies on, so the
// visibility contract is identical to the rest of the read API:
// anonymous callers see only PUBLIC memos, authenticated callers see
// PUBLIC + PROTECTED + their own PRIVATE + SPACE memos of any space
// they actively belong to. The returned `TotalMemoCount` reflects
// only the visible subset, so callers can use it as a "no visible
// memos" sentinel in directory views.
func computeUserStatsForViewer(app core.App, ctx context.Context, userID string) (*v1.UserStats, error) {
	rows, err := app.FindRecordsByFilter("memos", "author={:uid}", "-created", 0, 0, map[string]any{"uid": userID})
	if err != nil {
		return nil, err
	}

	memos, err := app.FindCollectionByNameOrId("memos")
	if err != nil {
		return nil, err
	}
	requestInfo := requestInfoFor(ctx)

	tagCount := map[string]int32{}
	createdTimestamps := make([]*timestamppb.Timestamp, 0, len(rows))
	updatedTimestamps := make([]*timestamppb.Timestamp, 0, len(rows))
	todoCount := int32(0)
	visible := 0
	for _, r := range rows {
		ok, err := app.CanAccessRecord(r, requestInfo, memos.ViewRule)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		visible++
		for _, t := range r.GetStringSlice("tags") {
			tagCount[t]++
		}
		// Heuristic: count memos that look like todo lists. The frontend
		// renders `[ ]` and `[x]` lines as tasks; we don't parse markdown
		// here, but a non-empty `[ ]` substring is a reasonable proxy.
		if containsTodoMarker(r.GetString("content")) {
			todoCount++
		}
		createdTimestamps = append(createdTimestamps, timestampToProto(r.GetDateTime("created")))
		updatedTimestamps = append(updatedTimestamps, timestampToProto(r.GetDateTime("updated")))
	}

	return &v1.UserStats{
		MemoTypeStats: &v1.UserStats_MemoTypeStats{
			TodoCount: todoCount,
		},
		TagCount:              tagCount,
		TotalMemoCount:        int32(visible),
		MemoCreatedTimestamps: createdTimestamps,
		MemoUpdatedTimestamps: updatedTimestamps,
	}, nil
}

// containsTodoMarker reports whether the memo content carries at least
// one Markdown task-list marker (`[ ]` or `[x]`).
func containsTodoMarker(content string) bool {
	return strings.Contains(content, "[ ]") ||
		strings.Contains(content, "[x]") ||
		strings.Contains(content, "[X]")
}
