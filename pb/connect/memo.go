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
	// ftsSearch routes memo content search through the SQLite FTS5
	// index (see memo_fts.go) instead of the LIKE scan. Set from
	// Profile.FTS via SetFTSSearch; off by default so an instance only
	// changes search behaviour when an operator asks for it.
	ftsSearch bool
}

func NewMemoServer(app core.App) *MemoServer { return &MemoServer{app: app} }

// SetFTSSearch enables or disables the FTS5-backed content search for
// this server. It mirrors setLinkMetaFetcher: a setter rather than a
// constructor argument, because NewMemoServer is called from a dozen
// test files and a variadic option there would be noise in every one.
func (s *MemoServer) SetFTSSearch(enabled bool) { s.ftsSearch = enabled }

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
	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}

	// `filter` is the AIP-132 CEL expression the SPA sends (see
	// web/src/hooks/useMemoFilters.ts). It used to be discarded,
	// which made every filter — space, creator, tag, search,
	// visibility, calendar range — a no-op that silently returned
	// the unfiltered feed.
	filterSQL, filterArgs, err := memoCELFilter(s.app, req.GetFilter())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	// `state` is a separate request field, not part of the CEL
	// expression; the SPA sets it on the Archived page and leaves
	// it unset (STATE_UNSPECIFIED) on the home feed.
	if st := req.GetState(); st != v1.State_STATE_UNSPECIFIED {
		stateSQL, stateArgs, err := memoStateFilter(st)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
		filterSQL, filterArgs = joinFilters(filterSQL, filterArgs, stateSQL, stateArgs)
	}

	// With FTS on, a *positive* content-match term is served by the
	// index instead of the LIKE scan: the index narrows rows and
	// tokenises them, which LIKE cannot do (no index, ASCII-only case
	// folding). The LIKE clause is then dropped, because keeping both
	// would shrink the result set — LIKE cannot match the folded
	// spelling FTS just matched, so the two disagree.
	//
	// collectTextMatches never reports a negated term, so `!contains`
	// and friends keep their LIKE clause and stay correct.
	ftsQuery := ""
	if s.ftsSearch {
		filterSQL, filterArgs, ftsQuery = memoFTSRewrite(
			req.GetFilter(), filterSQL, filterArgs,
		)
	}

	rows, err := s.findMemos(memos, filterSQL, limit, offset, filterArgs, ftsQuery)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	requestInfo := requestInfoFor(ctx)

	// Install the viewer's ACTIVE space ids ONCE for the whole
	// page. The converter strips the space relation from a memo
	// whose space the viewer is not a member of, and a per-memo
	// membership query would be an N+1 on a page that can carry
	// up to maxPageSize (1000) rows.
	ctx = withActiveSpaceIDs(ctx, s.app, currentUserID(ctx))

	out := make([]*v1.Memo, 0, len(rows))
	for _, r := range rows {
		ok, err := s.app.CanAccessRecord(r, requestInfo, memos.ViewRule)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		// Skip the upload stubs the V0 (Memos 0.21)
		// resource path creates. A stub is an
		// implementation detail of the upload, not a
		// memo the user wrote, and it must not appear in
		// the SPA feed as an empty memo (see
		// `pb/connect/memo_stub.go`).
		if IsStubMemoContent(r.GetString("content")) {
			continue
		}
		out = append(out, recordToMemo(ctx, s.app, r))
	}
	return &v1.ListMemosResponse{Memos: out, NextPageToken: nextPageToken(offset, limit, len(rows))}, nil
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
	return recordToMemo(ctx, s.app, rec), nil
}

// CreateMemo inserts a new memo. The OnRecordCreateRequest hook in
// pb/hooks/memos.go takes over from here: it stamps the author from the
// authenticated caller, parses hashtags into `tags`, and validates the
// visibility/space combination (including active Space membership).
func (s *MemoServer) CreateMemo(ctx context.Context, req *v1.CreateMemoRequest) (*v1.Memo, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
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

	// Bind any attachment ids the SPA named on the request to this newly
	// created memo. The SPA uploads files *before* the memo exists (because
	// the unified `attachments` collection used to require `attachment.memo`
	// to point at a memo that already existed); the spec now accepts
	// memo-less uploads (see `attachment_upload.go::uploadSpec`) and the
	// upload finishes with the row's `memo` column empty. This pass rewires
	// every named attachment to the new memo so the file becomes a child of
	// the memo that was just created. We only re-bind the caller's own
	// orphan rows to prevent a stranger from pivoting their attachment onto
	// someone else's memo — the `attachments.memo` column null check is the
	// authoritative "still orphan" test because bound rows keep a non-empty
	// `memo`. Attachments that already belong to another memo are rejected
	// as `INVALID_ARGUMENT` rather than silently moved (matches
	// `SetMemoAttachments` policy).
	if len(req.Memo.GetAttachments()) > 0 {
		if err := bindOrphanAttachmentsToMemo(ctx, s.app, rec.Id, viewer.Id, req.Memo.GetAttachments()); err != nil {
			// A failed binding pass leaves the memo in place but orphans
			// the attachments. The SPA decides whether to retry or surface
			// the error; deleting the memo would discard content the user
			// already saw, so we err out instead.
			return nil, err
		}
	}

	return recordToMemo(ctx, s.app, rec), nil
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
	// `pinned` is in the V0 (Memos 0.21)
	// organizer path; the V0 client
	// toggles it via
	// `POST /api/v1/memo/{id}/organizer`.
	// The V1 connect handler used to drop
	// it on the floor; the V0 surface
	// (see `pb/api/upstream_memos_v0_rest.go`)
	// relies on the field being honoured
	// here. The `memos.pinned` column is
	// a PocketBase bool; the setter
	// coerces JSON booleans to a real
	// bool (see `core.Record.Set`).
	if targets("pinned") {
		rec.Set("pinned", req.Memo.GetPinned())
	}
	if targets("space") && req.Memo.Space != nil {
		spaceID, err := resolveSpaceNameToID(s.app, req.Memo.GetSpace())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, "space not found")
		}
		rec.Set("space", spaceID)
	}

	// `attachments` is the V1 mask entry that the SPA sets when the user adds
	// new files to an existing memo by drag-and-drop. The SPA uploads the
	// file (now allowed to be memo-less — see `attachment_upload.go::uploadSpec`)
	// and then names the resulting `Attachment.name` on the patch. We bind
	// those orphan rows to this memo inside the same RPC so the SPA does not
	// need a separate `SetMemoAttachments` round-trip. Attachments that
	// already belong to a *different* memo are rejected as `INVALID_ARGUMENT`
	// rather than silently moved, mirroring `SetMemoAttachments`'s cross-memo
	// guard.
	if targets("attachments") {
		if err := bindOrphanAttachmentsToMemo(ctx, s.app, id, viewerID, req.Memo.GetAttachments()); err != nil {
			return nil, err
		}
	}

	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err.Error())
	}
	return recordToMemo(ctx, s.app, rec), nil
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

	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
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
		out = append(out, recordToMemo(ctx, s.app, comment))
	}
	return &v1.ListMemoCommentsResponse{Memos: out, NextPageToken: nextPageToken(offset, limit, len(rels))}, nil
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
	viewer := CurrentUser(ctx)
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

		// Mirror `CreateMemo`: rebind any orphan attachments the SPA
		// just uploaded (its inline-image uploads land first as
		// orphans because the memo doesn't exist yet — see
		// `pb/connect/memo_bind_attachments.go`). The rebind runs in
		// the same transaction as the comment-creation so a rejected
		// target cannot leave a half-bound memo behind. The viewer
		// identity is required for the cross-user rebind guard; both
		// `CreateMemo` and `CreateMemoComment` feed from the same
		// helper.
		if len(req.Comment.GetAttachments()) > 0 {
			if err := bindOrphanAttachmentsToMemo(ctx, txApp, rec.Id, viewer.Id, req.Comment.GetAttachments()); err != nil {
				return err
			}
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
	return recordToMemo(ctx, s.app, comment), nil
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
	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	// The attachment set is assembled in memory (one memo's patches and
	// samples), so the page is a slice of it rather than a query offset.
	all := memoAttachments(ctx, s.app, id)
	if offset > len(all) {
		offset = len(all)
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[offset:end]
	return &v1.ListMemoAttachmentsResponse{
		Attachments:   page,
		NextPageToken: nextPageToken(offset, limit, len(page)),
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
