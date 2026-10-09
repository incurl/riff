package connect

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestampToProto converts a types.DateTime (or zero) to a
// timestamppb.Timestamp. Returns nil for zero values so the
// proto field is omitted in JSON serialization.
func timestampToProto(dt types.DateTime) *timestamppb.Timestamp {
	if dt.IsZero() {
		return nil
	}
	return timestamppb.New(dt.Time())
}

// paginationFromRequest translates Memos' page-size/offset
// pair into PocketBase's limit/offset. Memos uses 0-indexed
// page numbers; we translate to limit/offset (page * perPage).
// perPage is clamped to [1, 100] to prevent abuse.
func paginationFromRequest(page, perPage int) (limit, offset int) {
	if page < 0 {
		page = 0
	}
	if perPage < 1 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	return perPage, page * perPage
}

// lookupUsername resolves a user id to a username. Returns ""
// on miss.
func lookupUsername(app core.App, userID string) string {
	if userID == "" {
		return ""
	}
	rec, err := app.FindRecordById("users", userID)
	if err != nil {
		return ""
	}
	return rec.GetString("username")
}

// lookupSpaceUID resolves a space id to its uid slug.
func lookupSpaceUID(app core.App, spaceID string) string {
	if spaceID == "" {
		return ""
	}
	rec, err := app.FindRecordById("spaces", spaceID)
	if err != nil {
		return ""
	}
	return rec.GetString("uid")
}

// fieldMaskPaths splits a FieldMask into the set of paths it names.
// It returns all=true when the update should touch every supported
// field: either the mask is absent/empty, or it carries the "*"
// catch-all that AIP-134 uses for a full replace.
//
// Callers treat a nil mask as "apply what the request populated",
// which is what the Memos React client sends for targeted edits.
func fieldMaskPaths(mask *fieldmaskpb.FieldMask) (paths map[string]struct{}, all bool) {
	if mask == nil || len(mask.GetPaths()) == 0 {
		return map[string]struct{}{}, true
	}
	paths = make(map[string]struct{}, len(mask.GetPaths()))
	for _, p := range mask.GetPaths() {
		if p == "*" {
			return map[string]struct{}{}, true
		}
		paths[p] = struct{}{}
	}
	return paths, false
}
