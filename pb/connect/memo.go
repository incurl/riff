package connect

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/incurl/riff/internal/linkmeta"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// MemoServer implements the Memos MemoService. Phase 4 wires
// the methods the home feed, the post composer and the memo card
// need:
//
//	ListMemos          — feed query with visibility filtering
//	GetMemo            — single memo with visibility check
//	CreateMemo         — create + the OnRecordCreateRequest hook
//	                     auto-assigns author/space/hashtags
//	UpdateMemo         — author-only, honours the update mask
//	DeleteMemo         — author-only
//	ListMemoComments   — list memo_relations with type=COMMENT
//	CreateMemoComment  — create a comment memo + its COMMENT relation
//	ListMemoAttachments — the memo's patches and samples
//	ListMemoRelations   — the memo's REFERENCE relations
//	SetMemoRelations    — replace the memo's REFERENCE relations
//	ListMemoReactions / UpsertMemoReaction / DeleteMemoReaction
//	CreateMemoShare / ListMemoShares / DeleteMemoShare —
//	                     memo share-link CRUD; see memo_shares.go
//	GetSharedMemo      — public, no auth, resolves a share code
//	                     to a memo (memo's own visibility still applies)
//	GetLinkMetadata / BatchGetLinkMetadata — link preview metadata
//	                     from internal/linkmeta, used by the composer
//	                     to render link cards before the user posts.
//
// Every write goes through the createRecordRequest / updateRecordRequest /
// deleteRecordRequest bridge in records.go so riff's OnRecord*Request rules
// (author assignment, hashtag parsing, space-membership checks, the
// last-ADMIN invariant) run exactly as they do for a collection REST call.
// Like the REST API, they also mean the handlers must enforce collection
// rules the rule engine would otherwise apply, because a superuser save
// bypasses ListRule/UpdateRule/DeleteRule.
//
// GetLinkMetadata / BatchGetLinkMetadata wrap the linkmeta library; the
// per-server fetcher is settable for tests and defaults to a process-wide
// singleton, so the library's cache and concurrency limits cover every
// caller.
//
// Still unimplemented: memo views (per-memo view log, distinct from the
// Wave 5 saved-CEL-filters collection), webhooks, search and export.
type MemoServer struct {
	apiv1connect.UnimplementedMemoServiceHandler
	app core.App
	// linkMetaFetcher, when non-nil, is the fetcher GetLinkMetadata
	// and BatchGetLinkMetadata use. It is exported via setLinkMetaFetcher
	// so tests can swap in a transport-injected fetcher; production
	// leaves it nil and the methods fall back to the package-level
	// singleton built once via sync.Once.
	linkMetaFetcher *linkmeta.HTMLMetaFetcher
}

func NewMemoServer(app core.App) *MemoServer { return &MemoServer{app: app} }

// ListMemos returns a paginated list of memos the caller can
// read. The list rule on the memos collection already filters
// by visibility for most audiences, but the SPACE check is
// more complex than the rule engine parses; we additionally
// call CanAccessRecord per row.
func (s *MemoServer) ListMemos(ctx context.Context, req *v1.ListMemosRequest) (*v1.ListMemosResponse, error) {
	memos, err := s.app.FindCollectionByNameOrId("memos")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))

	rows, err := s.app.FindRecordsByFilter(memos, "", "-created", limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	requestInfo := requestInfoFor(ctx)

	out := make([]*v1.Memo, 0, len(rows))
	for _, r := range rows {
		ok, err := s.app.CanAccessRecord(r, requestInfo, memos.ViewRule)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		out = append(out, recordToMemo(s.app, r))
	}
	return &v1.ListMemosResponse{Memos: out, NextPageToken: ""}, nil
}

// requestInfoFor builds the RequestInfo the record rule engine reads. It
// mirrors what PocketBase constructs for an inbound HTTP request so that
// @request.auth resolves the same way.
func requestInfoFor(ctx context.Context) *core.RequestInfo {
	info := &core.RequestInfo{Method: "GET"}
	if viewer := CurrentUser(ctx); viewer != nil {
		info.Auth = viewer
	}
	return info
}

// canViewMemo reports whether the caller may read the memo. The list rule on
// the memos collection already encodes visibility, but the SPACE branch is
// more than the rule engine evaluates cheaply, so handlers re-check per row.
func canViewMemo(app core.App, ctx context.Context, rec *core.Record) bool {
	memos, err := app.FindCollectionByNameOrId("memos")
	if err != nil {
		return false
	}
	ok, err := app.CanAccessRecord(rec, requestInfoFor(ctx), memos.ViewRule)
	if err != nil {
		return false
	}
	return ok
}

// GetMemo returns a single memo by id, enforcing visibility.
func (s *MemoServer) GetMemo(ctx context.Context, req *v1.GetMemoRequest) (*v1.Memo, error) {
	id := memoIDFromName(req.GetName())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	rec, err := s.app.FindRecordById("memos", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	return recordToMemo(s.app, rec), nil
}

// CreateMemo inserts a new memo. The OnRecordCreateRequest hook in
// pb/hooks/memos.go takes over from here: it stamps the author from the
// authenticated caller, parses hashtags into `tags`, and validates the
// visibility/space combination (including active Space membership).
func (s *MemoServer) CreateMemo(ctx context.Context, req *v1.CreateMemoRequest) (*v1.Memo, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.Memo == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo is required")
	}
	memos, err := s.app.FindCollectionByNameOrId("memos")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(memos)
	rec.Set("content", req.Memo.GetContent())
	if v := visibilityFromProto(req.Memo.GetVisibility()); v != "" {
		rec.Set("visibility", v)
	} else {
		rec.Set("visibility", "PRIVATE")
	}
	if req.Memo.Space != nil {
		spaceID, err := resolveSpaceNameToID(s.app, req.Memo.GetSpace())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, "space not found")
		}
		rec.Set("space", spaceID)
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToMemo(s.app, rec), nil
}

// UpdateMemo updates a memo's content, visibility and/or space. Only the
// author may update; the collection's UpdateRule expresses the same
// constraint, but our superuser save bypasses rules, so it is enforced here.
//
// update_mask restricts which fields the request may touch. An absent or "*"
// mask applies every field the request populated, which is what the React
// client sends when it does not build an explicit mask.
func (s *MemoServer) UpdateMemo(ctx context.Context, req *v1.UpdateMemoRequest) (*v1.Memo, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.Memo == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo is required")
	}
	id := memoIDFromName(req.Memo.GetName())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo name is required")
	}
	rec, err := s.app.FindRecordById("memos", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("author") != viewerID {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the author can update a memo")
	}

	paths, all := fieldMaskPaths(req.GetUpdateMask())
	targets := func(field string) bool {
		if all {
			return true
		}
		_, ok := paths[field]
		return ok
	}

	if targets("content") && req.Memo.GetContent() != "" {
		rec.Set("content", req.Memo.GetContent())
	}
	if targets("visibility") && req.Memo.GetVisibility() != v1.Visibility_VISIBILITY_UNSPECIFIED {
		if v := visibilityFromProto(req.Memo.GetVisibility()); v != "" {
			rec.Set("visibility", v)
		}
	}
	if targets("space") && req.Memo.Space != nil {
		spaceID, err := resolveSpaceNameToID(s.app, req.Memo.GetSpace())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, "space not found")
		}
		rec.Set("space", spaceID)
	}

	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err.Error())
	}
	return recordToMemo(s.app, rec), nil
}

// DeleteMemo removes a memo. Only the author may delete; the
// OnRecordDeleteRequest hook owns any cascade.
func (s *MemoServer) DeleteMemo(ctx context.Context, req *v1.DeleteMemoRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	id := memoIDFromName(req.GetName())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	rec, err := s.app.FindRecordById("memos", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("author") != viewerID {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the author can delete a memo")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// ListMemoComments returns the comments (memo_relations with
// type=COMMENT) for a given memo. The parent must be readable by the
// caller, otherwise a private memo's replies could be enumerated.
func (s *MemoServer) ListMemoComments(ctx context.Context, req *v1.ListMemoCommentsRequest) (*v1.ListMemoCommentsResponse, error) {
	parentID := memoIDFromName(req.GetName())
	if parentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	parent, err := s.app.FindRecordById("memos", parentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, parent) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}

	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))
	order := req.GetOrderBy()
	if order == "" {
		order = "created"
	}
	rels, err := s.app.FindRecordsByFilter("memo_relations",
		"related_memo={:pid} && type='COMMENT'",
		order, limit, offset,
		map[string]any{"pid": parentID},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.Memo, 0, len(rels))
	for _, r := range rels {
		comment, err := s.app.FindRecordById("memos", r.GetString("memo"))
		if err != nil {
			continue
		}
		if !canViewMemo(s.app, ctx, comment) {
			continue
		}
		out = append(out, recordToMemo(s.app, comment))
	}
	return &v1.ListMemoCommentsResponse{Memos: out, NextPageToken: ""}, nil
}

// CreateMemoComment creates a reply. A comment is an independent memo joined to
// its context memo by one immutable COMMENT relation, so the two writes run in
// a single transaction: a comment that lost its relation would be unreachable
// from the thread it answers.
//
// The comment inherits the parent's visibility and Space, which keeps a reply
// from ever being more visible than the memo it replies to — and satisfies the
// OnRecordCreateRequest membership check when the parent is SPACE-visible.
func (s *MemoServer) CreateMemoComment(ctx context.Context, req *v1.CreateMemoCommentRequest) (*v1.Memo, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	parentID := memoIDFromName(req.GetName())
	if parentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	if req.Comment == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "comment is required")
	}
	parent, err := s.app.FindRecordById("memos", parentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, parent) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}

	var comment *core.Record
	err = s.app.RunInTransaction(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		rec := core.NewRecord(memos)
		rec.Set("content", req.Comment.GetContent())
		rec.Set("visibility", parent.GetString("visibility"))
		if spaceID := parent.GetString("space"); spaceID != "" {
			rec.Set("space", spaceID)
		}
		if err := createRecordRequest(ctx, txApp, rec); err != nil {
			return err
		}

		rels, err := txApp.FindCollectionByNameOrId("memo_relations")
		if err != nil {
			return err
		}
		rel := core.NewRecord(rels)
		rel.Set("memo", rec.Id)
		rel.Set("related_memo", parentID)
		rel.Set("type", "COMMENT")
		if err := createRecordRequest(ctx, txApp, rel); err != nil {
			return err
		}
		comment = rec
		return nil
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToMemo(s.app, comment), nil
}

// ListMemoAttachments returns the attachments hanging off a memo. In riff's
// schema an attachment is a PO-33 patch or a sample pack, both of which store
// their files in their own collection, so the result is the union of the two.
func (s *MemoServer) ListMemoAttachments(ctx context.Context, req *v1.ListMemoAttachmentsRequest) (*v1.ListMemoAttachmentsResponse, error) {
	id := memoIDFromName(req.GetName())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	rec, err := s.app.FindRecordById("memos", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if !canViewMemo(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	return &v1.ListMemoAttachmentsResponse{
		Attachments:   memoAttachments(s.app, id),
		NextPageToken: "",
	}, nil
}

// memoIDFromName extracts the record id from a Memos-style
// resource name like "memos/{id}".
func memoIDFromName(name string) string {
	const prefix = "memos/"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return ""
	}
	return name[len(prefix):]
}

// creatorUsername extracts the username from "users/{name}".
func creatorUsername(s string) string {
	const prefix = "users/"
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func resolveSpaceNameToID(app core.App, name string) (string, error) {
	uid := name
	const prefix = "spaces/"
	if len(uid) > len(prefix) && uid[:len(prefix)] == prefix {
		uid = uid[len(prefix):]
	}
	rows, err := app.FindRecordsByFilter("spaces", "uid={:u}", "", 1, 0, map[string]any{"u": uid})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errors.New("space not found")
	}
	return rows[0].Id, nil
}

// lookupUserByName resolves a resource-name username (the bit after
// "users/") to a PocketBase record. The proto treats both the
// `users` collection and the `_superusers` collection as the same
// logical "user" (a superuser has role=ADMIN via `recordToUser`'s
// `IsSuperuser()` check, and a superuser's email becomes its
// resource name when `username` is empty), so we fall through to
// `_superusers` when `users` returns no match.
//
// The PocketBase installer placeholder
// (`__pbinstaller@example.com`) is excluded from the fall-through
// so a fresh instance without a real admin does not surface the
// placeholder as if it were a real account. This mirrors
// `AuthServer.findByIdentity` and the installer-aware lookups in
// `instance.go::findFirstAdmin` / `needsSetup`.
func lookupUserByName(app core.App, username string) (*core.Record, error) {
	rows, err := app.FindRecordsByFilter("users", "username={:u}", "", 1, 0, map[string]any{"u": username})
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		return rows[0], nil
	}

	superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return nil, err
	}
	rows, err = app.FindRecordsByFilter(superusers,
		"email={:u} && email != {:installer}", "", 1, 0,
		map[string]any{"u": username, "installer": core.DefaultInstallerEmail},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("user not found")
	}
	return rows[0], nil
}
