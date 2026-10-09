package connect

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// UserServer implements the Memos UserService.
//
// Phase 4 first wired the read path the feed needs (GetUser,
// ListUsers). Wave 4 adds the identity surface the React client
// exercises constantly:
//
//	BatchGetUsers — resolves every creator, mention and reaction
//	                name in the timeline back to a User. It is the
//	                single most-used RPC in the app.
//	CreateUser    — the admin "Create user" dialog and installer.
//	UpdateUser    — profile edits and the admin role/password form.
//	DeleteUser    — account deletion and admin user removal.
//
// Anything else (stats, settings, views, tokens, webhooks) returns
// Unimplemented.
type UserServer struct {
	apiv1connect.UnimplementedUserServiceHandler
	app core.App
}

func NewUserServer(app core.App) *UserServer { return &UserServer{app: app} }

// GetUser returns a single user by resource name
// ("users/{username}").
func (s *UserServer) GetUser(ctx context.Context, req *v1.GetUserRequest) (*v1.User, error) {
	username := creatorUsername(req.Name)
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	rows, err := s.app.FindRecordsByFilter("users", "username={:u}", "", 1, 0, map[string]any{"u": username})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if len(rows) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	return recordToUser(rows[0]), nil
}

// ListUsers returns a paginated list of users.
func (s *UserServer) ListUsers(ctx context.Context, req *v1.ListUsersRequest) (*v1.ListUsersResponse, error) {
	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))
	rows, err := s.app.FindRecordsByFilter(users, "", "username", limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToUser(r))
	}
	return &v1.ListUsersResponse{Users: out, NextPageToken: ""}, nil
}

// BatchGetUsers resolves usernames to users. Unknown usernames are skipped
// rather than failed: the client asks for every creator and mention at once and
// must still render memos whose author has since been deleted.
func (s *UserServer) BatchGetUsers(ctx context.Context, req *v1.BatchGetUsersRequest) (*v1.BatchGetUsersResponse, error) {
	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.User, 0, len(req.GetUsernames()))
	seen := make(map[string]struct{}, len(req.GetUsernames()))
	for _, username := range req.GetUsernames() {
		if username == "" {
			continue
		}
		if _, dup := seen[username]; dup {
			continue
		}
		seen[username] = struct{}{}

		rows, err := s.app.FindRecordsByFilter(users,
			"username={:u}", "", 1, 0, map[string]any{"u": username})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err.Error())
		}
		if len(rows) == 0 {
			continue
		}
		out = append(out, recordToUser(rows[0]))
	}
	return &v1.BatchGetUsersResponse{Users: out}, nil
}

// CreateUser creates a user. Only an admin may provision accounts, matching
// Memos' rule.
func (s *UserServer) CreateUser(ctx context.Context, req *v1.CreateUserRequest) (*v1.User, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req.User == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "user is required")
	}
	username := req.User.GetUsername()
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "username is required")
	}
	if req.GetUserId() != "" && req.GetUserId() != username {
		return nil, connect.NewError(connect.CodeInvalidArgument, "user_id must equal username")
	}
	if req.User.GetPassword() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "password is required")
	}

	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(users)
	if err := applyUserFields(rec, req.User); err != nil {
		return nil, err
	}
	if req.GetValidateOnly() {
		return recordToUser(rec), nil
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToUser(rec), nil
}

// UpdateUser updates a user's profile, and — for admins — their role and
// password. A caller may always edit their own account.
//
// The proto's `role` is projected onto PocketBase's superuser collection, which
// a `users` record can never join, so a request that promotes someone to ADMIN
// is refused rather than silently downgraded.
func (s *UserServer) UpdateUser(ctx context.Context, req *v1.UpdateUserRequest) (*v1.User, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.User == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "user is required")
	}
	username := creatorUsername(req.User.GetName())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "user name is required")
	}

	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only update your own account")
	}

	maskPaths, all := fieldMaskPaths(req.GetUpdateMask())
	targets := func(field string) bool {
		if all {
			return true
		}
		_, ok := maskPaths[field]
		return ok
	}

	if targets("role") {
		switch req.User.GetRole() {
		case v1.User_USER, v1.User_ROLE_UNSPECIFIED:
			// A `users` record is never a superuser, so this is a no-op.
		default:
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				"user rows cannot be promoted to ADMIN; create a superuser account instead")
		}
	}
	if targets("username") && req.User.GetUsername() != "" {
		target.Set("username", req.User.GetUsername())
	}
	if targets("email") {
		target.Set("email", req.User.GetEmail())
	}
	if targets("display_name") {
		target.Set("name", req.User.GetDisplayName())
	}
	if targets("description") {
		target.Set("bio", req.User.GetDescription())
	}
	if targets("password") && req.User.GetPassword() != "" {
		if err := setPassword(target, req.User.GetPassword()); err != nil {
			return nil, err
		}
	}

	if err := updateRecordRequest(ctx, s.app, target); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToUser(target), nil
}

// DeleteUser removes a user. A caller may delete their own account, or an admin
// may delete anyone.
//
// The schema points memos.author, spaces.owner, space_members.user and
// reactions.user at the record with no cascade, so a user with any of that
// attached cannot simply be dropped. Without `force` we refuse and say so; with
// `force` we unwind that content first, in one transaction, still routing the
// deletes through the record hooks so the last-ADMIN invariant holds.
func (s *UserServer) DeleteUser(ctx context.Context, req *v1.DeleteUserRequest) (*emptypb.Empty, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only delete your own account")
	}

	err = s.app.RunInTransaction(func(txApp core.App) error {
		if !req.GetForce() {
			blocked, err := userHasContent(txApp, target.Id)
			if err != nil {
				return err
			}
			if blocked {
				return errors.New("user has associated content; retry with force to delete it")
			}
		} else if err := purgeUserContent(ctx, txApp, target.Id); err != nil {
			return err
		}
		return deleteRecordRequest(ctx, txApp, target)
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// requireAdmin fails unless the caller is a superuser.
func requireAdmin(ctx context.Context) error {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if !viewer.IsSuperuser() {
		return connect.NewError(connect.CodePermissionDenied, "admin privileges required")
	}
	return nil
}
