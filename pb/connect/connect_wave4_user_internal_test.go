package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
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

	auth := NewAuthServer(app, nil)
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

// TestCreateUser_Bootstrap asserts the SPA's installer path: an
// unauthenticated CreateUser on a fresh instance provisions a
// real superuser record regardless of the submitted role (the
// SPA always sends role=USER, but the server forces role=ADMIN
// for the bootstrap), and the resulting account can sign in
// through the Connect SignIn handler.
func TestCreateUser_Bootstrap(t *testing.T) {
	app := newHookedApp(t)
	require.True(t, needsSetup(app), "a fresh app should report needsSetup")
	users := NewUserServer(app)

	// The SPA's SignUp form always sends role=USER, even on the
	// bootstrap path. The server is responsible for forcing
	// role=ADMIN when needsSetup is true.
	created, err := users.CreateUser(context.Background(), &v1.CreateUserRequest{
		User: &v1.User{
			Username:    "admin@example.com",
			Password:    "passw0rd!",
			DisplayName: "First Admin",
			Role:        v1.User_USER,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, v1.User_ADMIN, created.GetRole(),
		"the first user is always an admin, even if the request asked for USER")
	assert.Equal(t, "users/admin@example.com", created.GetName(),
		"recordToUser keeps the resource-name stable across renames")

	// The superuser record exists and carries a bcrypt hash
	// (otherwise the SignIn path cannot verify a password from Go).
	rows, err := app.FindRecordsByFilter(core.CollectionNameSuperusers,
		"email={:e}", "", 1, 0, map[string]any{"e": "admin@example.com"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	hash := rows[0].GetString("password_hash")
	require.NotEmpty(t, hash, "createSuperuserRecord must populate password_hash")
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("passw0rd!")))

	// Bootstrap is now complete.
	assert.False(t, needsSetup(app))

	// The bootstrap admin can sign in through the same form the
	// SPA's SignIn page submits.
	auth := NewAuthServer(app, nil)
	signIn, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_PasswordCredentials_{
			PasswordCredentials: &v1.SignInRequest_PasswordCredentials{
				Username: "admin@example.com",
				Password: "passw0rd!",
			},
		},
	})
	require.NoError(t, err, "the bootstrap admin must be able to sign in")
	assert.Equal(t, v1.User_ADMIN, signIn.GetUser().GetRole())
	assert.NotEmpty(t, signIn.GetAccessToken())
}

// TestCreateUser_SelfService asserts the open-registration path
// the SPA's SignUp form uses when an instance is already set up.
func TestCreateUser_SelfService(t *testing.T) {
	app := newHookedApp(t)
	newSuperuser(t, app, "admin@example.com")
	users := NewUserServer(app)

	created, err := users.CreateUser(context.Background(), &v1.CreateUserRequest{
		User: &v1.User{
			Username:    "dave",
			Password:    "passw0rd!",
			Email:       "dave@example.com",
			DisplayName: "Dave",
			Role:        v1.User_USER,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, v1.User_USER, created.GetRole())
	assert.Equal(t, "users/dave", created.GetName())
	assert.Equal(t, "Dave", created.GetDisplayName())

	rec, err := lookupUserByName(app, "dave")
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(rec.GetString("password_hash")), []byte("passw0rd!")))
}

// TestCreateUser_RejectsSelfServiceWhenClosed asserts the
// `disallowUserRegistration` flag gates the self-service path.
// An admin can still create a user; an unauthenticated caller
// cannot.
func TestCreateUser_RejectsSelfServiceWhenClosed(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	setGeneralSetting(t, app, &v1.InstanceSetting_GeneralSetting{
		DisallowUserRegistration: true,
	})
	users := NewUserServer(app)

	// Unauthenticated: refused.
	_, err := users.CreateUser(context.Background(), &v1.CreateUserRequest{
		User: &v1.User{Username: "dave", Password: "passw0rd!", Role: v1.User_USER},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// An admin may still create a user (the "invite" flow).
	created, err := users.CreateUser(withViewer(context.Background(), admin), &v1.CreateUserRequest{
		User: &v1.User{Username: "dave", Email: "dave@example.com", Password: "passw0rd!", Role: v1.User_USER},
	})
	require.NoError(t, err, "an admin's invite path must still work when registration is closed")
	assert.Equal(t, "users/dave", created.GetName())
}

// TestCreateUser_NonAdminCannotCreateUser asserts the gate the
// old handler enforced: a regular (non-admin) user is not allowed
// to create accounts.
func TestCreateUser_NonAdminCannotCreateUser(t *testing.T) {
	app := newHookedApp(t)
	newSuperuser(t, app, "admin@example.com")
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)

	_, err := users.CreateUser(withViewer(context.Background(), alice), &v1.CreateUserRequest{
		User: &v1.User{Username: "mallory", Password: "passw0rd!", Role: v1.User_USER},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestCreateUser_AdminPromotesToSuperuser asserts the third flow:
// an existing admin uses CreateUser with role=ADMIN to provision
// another admin. The new record lands in `_superusers`, not
// `users`.
func TestCreateUser_AdminPromotesToSuperuser(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	users := NewUserServer(app)

	created, err := users.CreateUser(withViewer(context.Background(), admin), &v1.CreateUserRequest{
		User: &v1.User{
			Username: "second@example.com",
			Password: "passw0rd!",
			Role:     v1.User_ADMIN,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, v1.User_ADMIN, created.GetRole())

	rows, err := app.FindRecordsByFilter(core.CollectionNameSuperusers,
		"email={:e}", "", 1, 0, map[string]any{"e": "second@example.com"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "ADMIN role must land in _superusers")
}

// TestCreateUser_RejectsAdminRoleAfterSetup asserts the bootstrap
// path is closed once a real admin exists: an unauthenticated
// caller can no longer claim the ADMIN role, and the `users`
// collection is never the destination of an ADMIN-role request.
func TestCreateUser_RejectsAdminRoleAfterSetup(t *testing.T) {
	app := newHookedApp(t)
	newSuperuser(t, app, "admin@example.com")
	users := NewUserServer(app)

	// Unauthenticated: the bootstrap window has closed.
	_, err := users.CreateUser(context.Background(), &v1.CreateUserRequest{
		User: &v1.User{Username: "boss", Password: "passw0rd!", Role: v1.User_ADMIN},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"unauthenticated ADMIN after setup must hit the bootstrap-closed branch")

	// Confirm nothing landed in either collection.
	rows, err := app.FindRecordsByFilter("users", "username='boss' || email='boss'", "", 1, 0)
	require.NoError(t, err)
	assert.Empty(t, rows, "ADMIN role must never land in the users collection")
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

	auth := NewAuthServer(app, nil)
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
