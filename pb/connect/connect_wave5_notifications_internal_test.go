package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestUserNotification_ListUpdateDelete walks the inbox. We seed rows
// directly through app.Save because notifications are server-only
// (CreateRule=nil), so the wave-4 hooks are the only production path.
func TestUserNotification_ListUpdateDelete(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)

	seedNotification(t, app, alice.Id, "SPACE_INVITATION", &v1.UserNotification_SpaceInvitationPayload{
		Role:  v1.SpaceMember_ADMIN,
		State: v1.UserNotification_SpaceInvitationPayload_PENDING,
	})
	seedNotification(t, app, alice.Id, "MEMO_COMMENT", &v1.UserNotification_MemoCommentPayload{
		Memo:        "memos/abc",
		RelatedMemo: "memos/xyz",
		MemoSnippet: "hi",
	})

	ctx := withViewer(context.Background(), alice)

	list, err := users.ListUserNotifications(ctx, &v1.ListUserNotificationsRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, list.GetNotifications(), 2, "both seeded rows surface")

	// Update one to ARCHIVED.
	target := list.GetNotifications()[0].GetName()
	updated, err := users.UpdateUserNotification(ctx, &v1.UpdateUserNotificationRequest{
		Notification: &v1.UserNotification{
			Name:   target,
			Status: v1.UserNotification_ARCHIVED,
		},
		UpdateMask: nil,
	})
	require.NoError(t, err)
	assert.Equal(t, v1.UserNotification_ARCHIVED, updated.GetStatus())

	// Delete the other.
	_, err = users.DeleteUserNotification(ctx, &v1.DeleteUserNotificationRequest{
		Name: list.GetNotifications()[1].GetName(),
	})
	require.NoError(t, err)

	// List now returns one row, archived.
	list, err = users.ListUserNotifications(ctx, &v1.ListUserNotificationsRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, list.GetNotifications(), 1)
	assert.Equal(t, v1.UserNotification_ARCHIVED, list.GetNotifications()[0].GetStatus())
}

// TestUserNotification_CrossUserDenied asserts only the recipient can
// see/update/delete their notifications.
func TestUserNotification_CrossUserDenied(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	users := NewUserServer(app)

	seedNotification(t, app, alice.Id, "MEMO_COMMENT", &v1.UserNotification_MemoCommentPayload{
		MemoSnippet: "x",
	})

	bobCtx := withViewer(context.Background(), bob)

	_, err := users.ListUserNotifications(bobCtx, &v1.ListUserNotificationsRequest{Parent: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	list, err := users.ListUserNotifications(withViewer(context.Background(), alice), &v1.ListUserNotificationsRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, list.GetNotifications(), 1)

	_, err = users.UpdateUserNotification(bobCtx, &v1.UpdateUserNotificationRequest{
		Notification: &v1.UserNotification{
			Name:   list.GetNotifications()[0].GetName(),
			Status: v1.UserNotification_ARCHIVED,
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// seedNotification inserts a notifications row directly through the
// PocketBase DAO. The collection is server-only via CreateRule=nil, so
// we bypass the rule engine — this is the same path the production
// hooks would take if they existed.
func seedNotification(t *testing.T, app core.App, userID, kind string, payload proto.Message) {
	t.Helper()
	coll, err := app.FindCollectionByNameOrId("notifications")
	require.NoError(t, err)
	rec := core.NewRecord(coll)
	rec.Set("user", userID)
	rec.Set("type", kind)
	raw, err := protojson.Marshal(payload)
	require.NoError(t, err)
	rec.Set("payload", string(raw))
	rec.Set("read", false)
	require.NoError(t, app.Save(rec))
}
