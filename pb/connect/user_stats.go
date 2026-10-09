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

// GetUserStats returns the named user's stats. The caller must be the
// user themselves or an admin. The optional `filter` is accepted for
// proto compatibility but is not yet translated to a PocketBase filter
// clause; the React client never sends one.
func (s *UserServer) GetUserStats(ctx context.Context, req *v1.GetUserStatsRequest) (*v1.UserStats, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username := creatorUsername(req.GetName())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only read your own stats")
	}

	stats, err := computeUserStats(s.app, target.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	stats.Name = "users/" + target.GetString("username") + "/stats"
	return stats, nil
}

// ListAllUserStats returns one UserStats row per user. Admin-only because
// the response is the full member directory.
func (s *UserServer) ListAllUserStats(ctx context.Context, _ *v1.ListAllUserStatsRequest) (*v1.ListAllUserStatsResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "admin privileges required")
	}

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
		stats, err := computeUserStats(s.app, r.Id)
		if err != nil {
			continue
		}
		stats.Name = "users/" + r.GetString("username") + "/stats"
		out = append(out, stats)
	}
	return &v1.ListAllUserStatsResponse{Stats: out}, nil
}

// computeUserStats walks the user's memos in memory and tallies tag
// counts and timestamps. This is fine while the count is small
// (single-instance, single-user feed); a periodic heatmap cache would
// be the right next step if profiling shows it matters.
func computeUserStats(app core.App, userID string) (*v1.UserStats, error) {
	rows, err := app.FindRecordsByFilter("memos", "author={:uid}", "-created", 0, 0, map[string]any{"uid": userID})
	if err != nil {
		return nil, err
	}

	tagCount := map[string]int32{}
	createdTimestamps := make([]*timestamppb.Timestamp, 0, len(rows))
	updatedTimestamps := make([]*timestamppb.Timestamp, 0, len(rows))
	todoCount := int32(0)
	for _, r := range rows {
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
		TotalMemoCount:        int32(len(rows)),
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
