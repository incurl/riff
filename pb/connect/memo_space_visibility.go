package connect

import (
	"context"

	"github.com/pocketbase/pocketbase/core"
)

// The `spaces` collection is private: a space is visible to its
// ACTIVE members (see migration 1930000003).
//
// A memo's own visibility is INDEPENDENT of its space's privacy — a
// PUBLIC memo in a private space stays public. But the memo's SPACE
// RELATION must not be disclosed to a non-member: the space uid is a
// URL slug, and emitting it would let anyone confirm that a private
// space exists and guess at its topic.
//
// So the converter strips the relation for a viewer who is not an
// ACTIVE member of the memo's space.

// ctxKeyActiveSpaceIDs carries the viewer's ACTIVE space ids for the
// current request.
type ctxKeyActiveSpaceIDs struct{}

// withActiveSpaceIDs returns a context carrying the ids of every
// space the viewer is an ACTIVE member of.
//
// It is installed ONCE per list request and read by every memo
// conversion, because the membership check would otherwise be a
// query per memo — and a list can carry up to maxPageSize (1000)
// rows.
func withActiveSpaceIDs(ctx context.Context, app core.App, viewerID string) context.Context {
	if viewerID == "" {
		return context.WithValue(ctx, ctxKeyActiveSpaceIDs{}, map[string]struct{}{})
	}
	return context.WithValue(ctx, ctxKeyActiveSpaceIDs{}, activeSpaceIDs(app, viewerID))
}

// activeSpaceIDs returns the ids of every space the viewer is an
// ACTIVE member of.
//
// One query per request. The membership rows are read through the
// `space_members` collection's own rule semantics (an ACTIVE row),
// which is the same condition the `spaces` collection rule uses.
func activeSpaceIDs(app core.App, viewerID string) map[string]struct{} {
	out := map[string]struct{}{}
	if viewerID == "" {
		return out
	}
	rows, err := app.FindRecordsByFilter(
		"space_members",
		"user={:uid} && status='ACTIVE'",
		"", 0, 0,
		map[string]any{"uid": viewerID},
	)
	if err != nil {
		// A failed lookup must not widen access: an empty set
		// means "not a member of anything".
		return out
	}
	for _, row := range rows {
		if id := row.GetString("space"); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

// spaceVisibleToViewer reports whether the viewer may see the
// relation to the given space.
//
// A superuser sees everything. Everybody else must be an ACTIVE
// member; the set is the one installed on the context by
// withActiveSpaceIDs.
func spaceVisibleToViewer(ctx context.Context, app core.App, spaceID string) bool {
	if spaceID == "" {
		return false
	}
	if viewer := CurrentUser(ctx); viewer != nil && viewer.IsSuperuser() {
		return true
	}
	set, ok := ctx.Value(ctxKeyActiveSpaceIDs{}).(map[string]struct{})
	if !ok {
		// The context was not prepared with the membership set
		// (e.g. a single-record read that never installed it).
		// Fall back to a direct check so the relation is not
		// silently withheld from a legitimate member.
		if viewer := CurrentUser(ctx); viewer != nil {
			return isActiveSpaceMember(app, viewer.Id, spaceID)
		}
		return false
	}
	_, ok = set[spaceID]
	return ok
}

// isActiveSpaceMember reports whether the user is an ACTIVE member
// of the space. It is the fallback path for a request that did not
// install the per-request membership set.
func isActiveSpaceMember(app core.App, userID, spaceID string) bool {
	if userID == "" || spaceID == "" {
		return false
	}
	rows, err := app.FindRecordsByFilter(
		"space_members",
		"user={:uid} && space={:sid} && status='ACTIVE'",
		"", 0, 0,
		map[string]any{"uid": userID, "sid": spaceID},
	)
	if err != nil {
		return false
	}
	return len(rows) > 0
}
