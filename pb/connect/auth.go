package connect

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// AuthServer implements the Memos AuthService.
//
// The generated v2-style handler signatures are plain
// (context.Context, *v1.XRequest) -> (*v1.XResponse, error),
// not the v1 connect.Request[T] / connect.Response[T] wrappers.
type AuthServer struct {
	apiv1connect.UnimplementedAuthServiceHandler
	app core.App
}

func NewAuthServer(app core.App) *AuthServer { return &AuthServer{app: app} }

// SignIn authenticates a user and returns a token. We support
// password-based credentials only; SSO is delegated to
// IdentityProviderService (currently Unimplemented).
func (s *AuthServer) SignIn(ctx context.Context, req *v1.SignInRequest) (*v1.SignInResponse, error) {
	pw := req.GetPasswordCredentials()
	if pw == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "SSO sign-in is not implemented")
	}

	rec, err := s.findByIdentity(pw.Username)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "invalid credentials")
	}

	// PocketBase v0.40 doesn't have a direct Authenticate
	// method on Record; we use the auth collection's
	// password check by attempting to call
	// app.NewAuthToken via a static auth check. The
	// simplest portable path is the dao-level check.
	if err := s.verifyPassword(rec, pw.Password); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "invalid credentials")
	}

	tok, err := rec.NewAuthToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "token generation failed")
	}

	return &v1.SignInResponse{
		User:                 recordToUser(rec),
		AccessToken:          tok,
		AccessTokenExpiresAt: timestamppb.New(time.Now().Add(14 * 24 * time.Hour)),
	}, nil
}

// SignOut terminates the current session. PocketBase v0.40
// tokens are stateless; the SDK discards the token client-side.
func (s *AuthServer) SignOut(_ context.Context, _ *v1.SignOutRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// RefreshToken exchanges a still-valid access token for a new
// one. The current user's record is in the context (set by
// the auth interceptor).
func (s *AuthServer) RefreshToken(ctx context.Context, _ *v1.RefreshTokenRequest) (*v1.RefreshTokenResponse, error) {
	rec := CurrentUser(ctx)
	if rec == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no valid session")
	}
	tok, err := rec.NewAuthToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "token regeneration failed")
	}
	return &v1.RefreshTokenResponse{
		AccessToken: tok,
		ExpiresAt:   timestamppb.New(time.Now().Add(14 * 24 * time.Hour)),
	}, nil
}

// GetCurrentUser returns the authenticated user's info.
func (s *AuthServer) GetCurrentUser(ctx context.Context, _ *v1.GetCurrentUserRequest) (*v1.GetCurrentUserResponse, error) {
	rec := CurrentUser(ctx)
	if rec == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no valid session")
	}
	return &v1.GetCurrentUserResponse{User: recordToUser(rec)}, nil
}

// findByIdentity locates a user record by username OR email.
func (s *AuthServer) findByIdentity(identity string) (*core.Record, error) {
	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, err
	}
	rows, err := s.app.FindRecordsByFilter(users,
		"username={:id} || email={:id}", "", 1, 0,
		map[string]any{"id": identity},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("user not found")
	}
	return rows[0], nil
}

// verifyPassword checks a password against a user's stored
// bcrypt hash. The hash lives in the `password_hash` field
// (added in migration 1710000001) so we can verify without
// calling PocketBase's internal auth endpoint in a loop.
//
// Migration strategy for existing users: PocketBase's
// built-in `password` field still works for the standard
// /api/collections/users/auth-with-password endpoint; new
// users created via the Connect CreateUser path get a
// `password_hash` set. Users created via the admin UI need
// to re-set their password through the user-update RPC to
// populate the hash. (Documented in DECISIONS.md.)
func (s *AuthServer) verifyPassword(rec *core.Record, password string) error {
	if password == "" {
		return errors.New("empty password")
	}
	hash := rec.GetString("password_hash")
	if hash == "" {
		return errors.New("no password set on this account")
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
