package connect

import (
	"encoding/base64"
	"strconv"

	"github.com/pkg/errors"
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

// defaultPageSize and maxPageSize are the AIP-132 bounds the protos
// document: 50 when page_size is unset, coerced down to 1000 above
// that.
const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

// pageWindow resolves an AIP-132 `page_size` / `page_token` pair into
// PocketBase's limit/offset. The token is opaque to clients: it is the
// base64url form of the result-set offset the previous page ended at.
func pageWindow(perPage int32, token string) (limit, offset int, err error) {
	limit = int(perPage)
	if limit < 1 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	offset, err = pageOffset(token)
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

// pageOffset decodes a page token into a result-set offset. An empty
// token is the first page. Anything that is not a non-negative integer
// under the base64url encoding is an error: silently starting at zero
// would hand the client the first page again on every call, which is
// the failure mode the token exists to prevent.
func pageOffset(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, errors.New("invalid page token")
	}
	offset, convErr := strconv.Atoi(string(raw))
	if convErr != nil || offset < 0 {
		return 0, errors.New("invalid page token")
	}
	return offset, nil
}

// encodePageToken is the inverse of pageOffset.
func encodePageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// nextPageToken returns the token that resumes after (offset, limit),
// or "" when the page was not full.
//
// The signal is the number of rows the query returned, not the number
// of items that survived the per-row access checks or the stub filter:
// a full page whose rows were all filtered out still has successors,
// and stopping there would silently truncate the feed.
func nextPageToken(offset, limit, fetched int) string {
	if limit < 1 || fetched < limit {
		return ""
	}
	return encodePageToken(offset + limit)
}

// lookupUsername resolves a user id to a username. Returns ""
// on miss.
//
// The result is memoised in the app's identity cache — see
// identity_cache.go. Every list path that renders a name goes through
// here, and PocketBase has no record cache, so without this each row
// costs an extra point query.
func lookupUsername(app core.App, userID string) string {
	return identityCacheFor(app).username(app, userID)
}

// lookupSpaceUID resolves a space id to its uid slug.
//
// Memoised in the app's identity cache, like lookupUsername.
func lookupSpaceUID(app core.App, spaceID string) string {
	return identityCacheFor(app).spaceUID(app, spaceID)
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
