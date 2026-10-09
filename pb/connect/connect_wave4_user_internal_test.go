package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestBatchGetUsers_ResolvesAndSkipsUnknown asserts the RPC the whole timeline
// depends on: every creator, mention and reaction name resolves through it, and
// a username with no user (a deleted author) is skipped rather than failing the
// whole batch.
func TestBatchGetUsers_ResolvesAndSkipsUnknown(t *testing.T) {
	app := newHookedApp(t)
	newUser(t, app, "alice")
	newUser(t, app, "bob")
	srv := NewUserServer(app)

	ctx := withViewer(context.Background(), newUser(t, app, "carol"))
	resp, err := srv.BatchGetUsers(ctx, &v1.BatchGetUsersRequest{
		Usernames: []string{"alice", "ghost", "bob", "alice", ""},
	})
	require.NoError(t, err)

	names := make([]string, 0, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		names = append(names, u.GetName())
	}
	assert.ElementsMatch(t, []string{"users/alice", "users/bob"}, names,
		"unknown and blank usernames are skipped, duplicates collapse")
}

// TestCreateUser_AdminOnlyAndSignIn asserts only an admin may provision an
// account, and that the created password works through the Connect SignIn path
// (which verifies the password_hash column, not PocketBase's own hash).
func TestCreateUser_AdminOnlyAndSignIn(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)

	_, err := users.CreateUser(withViewer(context.Background(), alice), &v1.CreateUserRequest{
		User: &v1.User{Username: "mallory", Password: "passw0rd!"},
	})
	require.Error(t, err, "a plain user must not create accounts")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	created, err := users.CreateUser(withViewer(context.Background(), admin), &v1.CreateUserRequest{
		User: &v1.User{
			Username:    "dave",
			Email:       "dave@example.com",
			Password:    "passw0rd!",
			DisplayName: "Dave",
			Description: "drummer",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "users/dave", created.GetName())
	assert.Equal(t, "Dave", created.GetDisplayName())
	assert.Equal(t, "drummer", created.GetDescription())
	assert.Equal(t, v1.User_USER, created.GetRole())

	// The password must satisfy the Connect sign-in path.
	rec, err := lookupUserByName(app, "dave")
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(rec.GetString("password_hash")), []byte("passw0rd!")))

	auth := NewAuthServer(app)
	signIn, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_PasswordCredentials_{
			PasswordCredentials: &v1.SignInRequest_PasswordCredentials{
				Username: "dave",
				Password: "passw0rd!",
			},
		},
	})
	require.NoError(t, err, "a user created over Connect must be able to sign in")
	assert.Equal(t, "users/dave", signIn.GetUser().GetName())
	assert.NotEmpty(t, signIn.GetAccessToken())
}

// TestCreateUser_RejectsAdminRole asserts the ADMIN role is refused rather than
// silently dropped, because it lives in the superuser collection.
func TestCreateUser_RejectsAdminRole(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	users := NewUserServer(app)

	_, err := users.CreateUser(withViewer(context.Background(), admin), &v1.CreateUserRequest{
		User: &v1.User{Username: "boss", Password: "passw0rd!", Role: v1.User_ADMIN},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

}

// TestUpdateUser_SelfOnly asserts a user may edit their own profile but not
// somebody else's, and that a masked update touches only the named fields.
func TestUpdateUser_SelfOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)

	_, err := srv.UpdateUser(withViewer(context.Background(), bob), &v1.UpdateUserRequest{
		User: &v1.User{Name: "users/alice", DisplayName: "Not Alice"},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	updated, err := srv.UpdateUser(withViewer(context.Background(), alice), &v1.UpdateUserRequest{
		User:       &v1.User{Name: "users/alice", DisplayName: "Alice", Description: "ignored"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Alice", updated.GetDisplayName())
	assert.Empty(t, updated.GetDescription(), "description is outside the mask")
}

// TestUpdateUser_PasswordKeepsBothStoresInSync asserts a password change over
// Connect also updates the bcrypt column the sign-in path reads.
func TestUpdateUser_PasswordKeepsBothStoresInSync(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)

	_, err := srv.UpdateUser(withViewer(context.Background(), alice), &v1.UpdateUserRequest{
		User:       &v1.User{Name: "users/alice", Password: "newpassw0rd!"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"password"}},
	})
	require.NoError(t, err)

	rec, err := lookupUserByName(app, "alice")
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(rec.GetString("password_hash")), []byte("newpassw0rd!")))

	auth := NewAuthServer(app)
	_, err = auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_PasswordCredentials_{
			PasswordCredentials: &v1.SignInRequest_PasswordCredentials{
				Username: "alice",
				Password: "newpassw0rd!",
			},
		},
	})
	assert.NoError(t, err)
}

// TestUpdateUser_RejectsAdminPromotion asserts the role field is refused.
func TestUpdateUser_RejectsAdminPromotion(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	newUser(t, app, "alice")
	srv := NewUserServer(app)

	_, err := srv.UpdateUser(withViewer(context.Background(), admin), &v1.UpdateUserRequest{
		User:       &v1.User{Name: "users/alice", Role: v1.User_ADMIN},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"role"}},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestDeleteUser_RefusesWhenContentExists asserts a user with content is not
// silently dropped, and that `force` unwinds it.
func TestDeleteUser_RefusesWhenContentExists(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	alice := newUser(t, app, "alice")
	memos := NewMemoServer(app)
	users := NewUserServer(app)
	adminCtx := withViewer(context.Background(), admin)

	_, err := memos.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "some content", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	_, err = users.DeleteUser(adminCtx, &v1.DeleteUserRequest{Name: "users/alice"})
	require.Error(t, err, "a user with memos must not be deleted without force")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = users.DeleteUser(adminCtx, &v1.DeleteUserRequest{Name: "users/alice", Force: true})
	require.NoError(t, err)

	_, err = lookupUserByName(app, "alice")
	assert.Error(t, err, "the user must be gone after a forced delete")

	remaining, err := app.FindRecordsByFilter("memos", "author={:id}", "", 0, 0, map[string]any{"id": alice.Id})
	require.NoError(t, err)
	assert.Empty(t, remaining, "the user's memos must go with them")
}

// TestDeleteUser_SelfOnly asserts a non-admin cannot delete another account,
// while deleting your own empty account is allowed.
func TestDeleteUser_SelfOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)

	_, err := srv.DeleteUser(withViewer(context.Background(), bob), &v1.DeleteUserRequest{Name: "users/alice"})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = srv.DeleteUser(withViewer(context.Background(), alice), &v1.DeleteUserRequest{Name: "users/alice"})
	require.NoError(t, err)
}
