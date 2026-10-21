package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// UserNotification methods on UserServer. The `notifications` collection
// is owned by the recipient user (see
// pb/migrations/1710000000_create_notifications.go); we surface the rows
// as UserNotification protos, decoding the per-type payload JSON into
// the matching oneof variant.

// ListUserNotifications returns the caller's inbox. Pagination is a plain
// slice: rows are already in -created order via the index on the collection.
func (s *UserServer) ListUserNotifications(ctx context.Context, req *v1.ListUserNotificationsRequest) (*v1.ListUserNotificationsResponse, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only list your own notifications")
	}

	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	rows, err := s.app.FindRecordsByFilter("notifications", "user={:uid}", "-created", limit, offset, map[string]any{"uid": target.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.UserNotification, 0, len(rows))
	for _, r := range rows {
		n, err := recordToUserNotification(target, r)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return &v1.ListUserNotificationsResponse{Notifications: out, NextPageToken: nextPageToken(offset, limit, len(rows))}, nil
}

// UpdateUserNotification mutates a notification's status (UNREAD/ARCHIVED).
// Owner-only. The schema's updateRule already restricts to `user = @request.auth.id`
// so we can rely on the record-request bridge for the auth path.
func (s *UserServer) UpdateUserNotification(ctx context.Context, req *v1.UpdateUserNotificationRequest) (*v1.UserNotification, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.Notification == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "notification is required")
	}
	username, notifID, err := splitUserNotificationName(req.Notification.GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only update your own notifications")
	}
	rec, err := s.app.FindRecordById("notifications", notifID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "notification not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "notification not found")
	}

	switch req.Notification.GetStatus() {
	case v1.UserNotification_UNREAD:
		rec.Set("read", false)
	case v1.UserNotification_ARCHIVED:
		rec.Set("read", true)
	}
	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToUserNotification(target, rec)
}

// DeleteUserNotification removes a single notification.
func (s *UserServer) DeleteUserNotification(ctx context.Context, req *v1.DeleteUserNotificationRequest) (*emptypb.Empty, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, notifID, err := splitUserNotificationName(req.GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only delete your own notifications")
	}
	rec, err := s.app.FindRecordById("notifications", notifID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "notification not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "notification not found")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// splitUserNotificationName parses "users/{u}/notifications/{id}".
func splitUserNotificationName(name string) (username, id string, err error) {
	const prefix = "users/"
	const suffix = "/notifications/"
	if len(name) <= len(prefix)+len(suffix) {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	if !strings.HasPrefix(name, prefix) {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name must start with users/")
	}
	rest := name[len(prefix):]
	idx := strings.Index(rest, suffix)
	if idx < 0 {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name must be users/{u}/notifications/{id}")
	}
	username = rest[:idx]
	id = rest[idx+len(suffix):]
	if username == "" || id == "" {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "name is malformed")
	}
	return username, id, nil
}

// recordToUserNotification decodes the JSON payload into the matching
// oneof variant. Unknown types are reported as Type=UNSPECIFIED so the
// frontend can render a placeholder.
func recordToUserNotification(user *core.Record, rec *core.Record) (*v1.UserNotification, error) {
	n := &v1.UserNotification{
		Name:       "users/" + user.GetString("username") + "/notifications/" + rec.Id,
		CreateTime: timestampToProto(rec.GetDateTime("created")),
	}
	if rec.GetBool("read") {
		n.Status = v1.UserNotification_ARCHIVED
	} else {
		n.Status = v1.UserNotification_UNREAD
	}

	// Sender is the acting user, when present. Older rows may not record one
	// (notifications predated the sender field); we leave it blank in that case.
	rawPayload := []byte(rec.GetString("payload"))
	switch rec.GetString("type") {
	case "SPACE_INVITATION":
		n.Type = v1.UserNotification_SPACE_INVITATION
		body := &v1.UserNotification_SpaceInvitationPayload{}
		if len(rawPayload) > 0 {
			if err := protojson.Unmarshal(rawPayload, body); err != nil {
				return nil, err
			}
		}
		n.Payload = &v1.UserNotification_SpaceInvitation{SpaceInvitation: body}
	case "MEMO_COMMENT":
		n.Type = v1.UserNotification_MEMO_COMMENT
		body := &v1.UserNotification_MemoCommentPayload{}
		if len(rawPayload) > 0 {
			if err := protojson.Unmarshal(rawPayload, body); err != nil {
				return nil, err
			}
		}
		n.Payload = &v1.UserNotification_MemoComment{MemoComment: body}
	case "MEMO_MENTION":
		n.Type = v1.UserNotification_MEMO_MENTION
		body := &v1.UserNotification_MemoMentionPayload{}
		if len(rawPayload) > 0 {
			if err := protojson.Unmarshal(rawPayload, body); err != nil {
				return nil, err
			}
		}
		n.Payload = &v1.UserNotification_MemoMention{MemoMention: body}
	default:
		// Unknown type: ignore payload but don't fail.
	}

	return n, nil
}
