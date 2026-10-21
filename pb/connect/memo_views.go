package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// MemoView methods on UserServer. A MemoView is a named CEL filter the
// user has saved; we model each as a row in the `memo_views` collection
// (user, title, filter, icon) and gate reads/writes to the owner.

// ListMemoViews returns all saved views owned by the parent user. Any
// authenticated user can list their own views; admins can list anyone's.
func (s *UserServer) ListMemoViews(ctx context.Context, req *v1.ListMemoViewsRequest) (*v1.ListMemoViewsResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username := creatorUsername(req.GetParent())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "parent is required")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only list your own views")
	}

	rows, err := s.app.FindRecordsByFilter("memo_views", "user={:uid}", "", 0, 0, map[string]any{"uid": target.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.MemoView, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToMemoView(target, r))
	}
	return &v1.ListMemoViewsResponse{MemoViews: out}, nil
}

// GetMemoView returns a single view by name.
func (s *UserServer) GetMemoView(ctx context.Context, req *v1.GetMemoViewRequest) (*v1.MemoView, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, viewID, err := splitMemoViewName(req.GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only read your own views")
	}
	rec, err := s.app.FindRecordById("memo_views", viewID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo view not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "memo view not found")
	}
	return recordToMemoView(target, rec), nil
}

// CreateMemoView creates a new view. The owning user is taken from the
// parent in the request, not from the body — bodies without a parent
// are rejected.
func (s *UserServer) CreateMemoView(ctx context.Context, req *v1.CreateMemoViewRequest) (*v1.MemoView, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username := creatorUsername(req.GetParent())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "parent is required")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only create views for yourself")
	}
	if req.MemoView == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo_view is required")
	}
	if req.MemoView.GetTitle() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "title is required")
	}
	if req.MemoView.GetFilter() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "filter is required")
	}
	if err := validateMemoViewFilter(s.app, req.MemoView.GetFilter()); err != nil {
		return nil, err
	}
	if req.GetValidateOnly() {
		// The filter compiled against the same schema ListMemos uses,
		// so the SPA's "Validate" button now answers the question it
		// asks. Before this, any string was accepted and a view that
		// could not be evaluated 400ed every feed that selected it.
		return req.MemoView, nil
	}

	coll, err := s.app.FindCollectionByNameOrId("memo_views")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(coll)
	rec.Set("user", target.Id)
	rec.Set("title", req.MemoView.GetTitle())
	rec.Set("filter", req.MemoView.GetFilter())
	setMemoViewIcon(rec, req.MemoView.GetIcon())
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	view := recordToMemoView(target, rec)
	view.Name = "users/" + target.GetString("username") + "/views/" + rec.Id
	return view, nil
}

// UpdateMemoView updates the title/filter/icon. Owner-only. An absent
// update_mask applies every populated field (Memos convention).
func (s *UserServer) UpdateMemoView(ctx context.Context, req *v1.UpdateMemoViewRequest) (*v1.MemoView, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.MemoView == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo_view is required")
	}
	username, viewID, err := splitMemoViewName(req.MemoView.GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only update your own views")
	}
	rec, err := s.app.FindRecordById("memo_views", viewID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo view not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "memo view not found")
	}

	paths, all := fieldMaskPaths(req.GetUpdateMask())
	touch := func(field string) bool {
		if all {
			return true
		}
		_, ok := paths[field]
		return ok
	}

	if touch("title") && req.MemoView.GetTitle() != "" {
		rec.Set("title", req.MemoView.GetTitle())
	}
	if touch("filter") && req.MemoView.GetFilter() != "" {
		if err := validateMemoViewFilter(s.app, req.MemoView.GetFilter()); err != nil {
			return nil, err
		}
		rec.Set("filter", req.MemoView.GetFilter())
	}
	if touch("icon") {
		setMemoViewIcon(rec, req.MemoView.GetIcon())
	}

	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToMemoView(target, rec), nil
}

// DeleteMemoView removes a view owned by the caller.
func (s *UserServer) DeleteMemoView(ctx context.Context, req *v1.DeleteMemoViewRequest) (*emptypb.Empty, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, viewID, err := splitMemoViewName(req.GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only delete your own views")
	}
	rec, err := s.app.FindRecordById("memo_views", viewID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo view not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "memo view not found")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// splitMemoViewName parses "users/{u}/views/{id}" into (username, id).
func splitMemoViewName(name string) (username, id string, err error) {
	const prefix = "users/"
	const suffix = "/views/"
	if len(name) <= len(prefix)+len(suffix) {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	if !strings.HasPrefix(name, prefix) {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name must start with users/")
	}
	rest := name[len(prefix):]
	idx := strings.Index(rest, suffix)
	if idx < 0 {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name must be users/{u}/views/{id}")
	}
	username = rest[:idx]
	id = rest[idx+len(suffix):]
	if username == "" || id == "" {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name is malformed")
	}
	return username, id, nil
}

// recordToMemoView converts a `memo_views` row into the proto.
func recordToMemoView(user *core.Record, rec *core.Record) *v1.MemoView {
	view := &v1.MemoView{
		Name:   "users/" + user.GetString("username") + "/views/" + rec.Id,
		Title:  rec.GetString("title"),
		Filter: rec.GetString("filter"),
	}
	switch rec.GetString("icon_kind") {
	case "emoji":
		view.Icon = &v1.MemoView_Icon{Value: &v1.MemoView_Icon_Emoji{Emoji: rec.GetString("icon_value")}}
	case "lucide":
		view.Icon = &v1.MemoView_Icon{Value: &v1.MemoView_Icon_Lucide{Lucide: rec.GetString("icon_value")}}
	}
	return view
}

// validateMemoViewFilter compiles a saved view's filter with the same
// field schema ListMemos uses. A view whose filter cannot compile is
// rejected at write time: otherwise it is stored happily and every
// feed request that selects it answers CodeInvalidArgument, which the
// user reads as a broken page rather than a bad saved filter.
func validateMemoViewFilter(app core.App, filter string) error {
	if _, _, err := memoCELFilter(app, filter); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, "filter is invalid: "+err.Error())
	}
	return nil
}

// setMemoViewIcon applies the proto's icon oneof to the record.
func setMemoViewIcon(rec *core.Record, icon *v1.MemoView_Icon) {
	if icon == nil {
		rec.Set("icon_kind", "")
		rec.Set("icon_value", "")
		return
	}
	switch v := icon.GetValue().(type) {
	case *v1.MemoView_Icon_Emoji:
		rec.Set("icon_kind", "emoji")
		rec.Set("icon_value", v.Emoji)
	case *v1.MemoView_Icon_Lucide:
		rec.Set("icon_kind", "lucide")
		rec.Set("icon_value", v.Lucide)
	default:
		rec.Set("icon_kind", "")
		rec.Set("icon_value", "")
	}
}
