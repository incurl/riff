package connect

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"

	"github.com/incurl/riff/internal/random"
)

// UserServer now also serves the three
// UserService.PersonalAccessToken RPCs:
//
//	ListPersonalAccessTokens   — every PAT owned by
//	                             the user named in
//	                             `parent`. Non-admin
//	                             callers may only list
//	                             their own tokens.
//	CreatePersonalAccessToken  — mint a fresh PAT for
//	                             the user named in
//	                             `parent`. Returns the
//	                             raw token exactly once;
//	                             subsequent reads only
//	                             see the metadata row.
//	DeletePersonalAccessToken  — revoke a token by its
//	                             resource name. The
//	                             caller must own it.
//
// The handlers are the only authority for "who can manage
// personal access tokens"; the migration sets every *Rule
// to nil so the PocketBase rule engine cannot be tricked
// into letting a wrong caller through the collection REST
// API.

// personalAccessTokenPrefix is the only thing the SPA's
// CreateAccessTokenDialog uses to recognise a riff-issued
// token. The curl example in the React component is
// `memos_pat_...`, so we keep that shape for parity with
// the upstream Memos client. Operators can grep logs for
// the prefix when they want to find leaked credentials.
const personalAccessTokenPrefix = "memos_pat_"

// personalAccessTokenRandomBytes is the entropy in the
// raw token, before base64url encoding. 24 bytes yields
// 32 base64url characters (no padding), which keeps the
// raw token at 41 characters total (8-char prefix + 32
// chars) — long enough to be unguessable in practice.
const personalAccessTokenRandomBytes = 24

// mintPersonalAccessToken returns a fresh raw token. The
// caller is expected to bcrypt it before persisting.
// Exposed via the package-level mintPersonalAccessToken
// var so tests can substitute a deterministic source.
var mintPersonalAccessToken = func() (string, error) {
	raw, err := random.String(personalAccessTokenRandomBytes)
	if err != nil {
		return "", err
	}
	return personalAccessTokenPrefix + raw, nil
}

// splitPersonalAccessTokenName parses
// "users/{user}/personalAccessTokens/{token}" into its
// (username, token-id) segments. The boolean is false
// when the name is malformed.
func splitPersonalAccessTokenName(name string) (string, string, bool) {
	const prefix = "users/"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	// The user segment ends at the first "/"; the rest
	// must be "personalAccessTokens/{id}".
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	username := rest[:slash]
	segments := rest[slash+1:]
	const segs = "personalAccessTokens/"
	if !strings.HasPrefix(segments, segs) {
		return "", "", false
	}
	id := segments[len(segs):]
	if username == "" || id == "" {
		return "", "", false
	}
	return username, id, true
}

// ListPersonalAccessTokens returns every PAT owned by the
// user named in `parent`. A non-admin caller may only list
// their own tokens; the auth check runs before the DB scan
// so a wrong parent never leaks another user's row count.
//
// The list is sorted newest-first; the SPA sorts in JS
// too, but a stable server order keeps the React render
// cost bounded.
func (s *UserServer) ListPersonalAccessTokens(ctx context.Context, req *v1.ListPersonalAccessTokensRequest) (*v1.ListPersonalAccessTokensResponse, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only list your own personal access tokens")
	}

	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	rows, err := s.app.FindRecordsByFilter("personal_access_tokens",
		"user={:uid}", "-created_at", limit, offset, map[string]any{"uid": target.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.PersonalAccessToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToPersonalAccessToken(s.app, r))
	}
	return &v1.ListPersonalAccessTokensResponse{PersonalAccessTokens: out, NextPageToken: nextPageToken(offset, limit, len(rows))}, nil
}

// CreatePersonalAccessToken mints a fresh PAT for the user
// named in `parent`. The raw token is returned exactly
// once in the response; the row stores only its bcrypt
// hash. `expires_in_days == 0` means "never expires",
// matching the SPA's "0 = never" picker. A negative value
// is rejected as InvalidArgument; a value greater than
// ~100 years is clamped to that to prevent overflow
// shenanigans.
func (s *UserServer) CreatePersonalAccessToken(ctx context.Context, req *v1.CreatePersonalAccessTokenRequest) (*v1.CreatePersonalAccessTokenResponse, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only mint your own personal access tokens")
	}

	days := int(req.GetExpiresInDays())
	if days < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "expires_in_days must be >= 0")
	}
	if days > 36500 {
		days = 36500
	}

	raw, err := mintPersonalAccessToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "failed to generate token: "+err.Error())
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "failed to hash token: "+err.Error())
	}

	col, err := s.app.FindCollectionByNameOrId("personal_access_tokens")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(col)
	rec.Set("user", target.Id)
	if req.GetDescription() != "" {
		rec.Set("description", req.GetDescription())
	}
	rec.Set("token_hash", string(hash))
	if days > 0 {
		rec.Set("expires_at", typesDateTime(time.Now().Add(time.Duration(days)*24*time.Hour)))
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}

	return &v1.CreatePersonalAccessTokenResponse{
		PersonalAccessToken: recordToPersonalAccessToken(s.app, rec),
		Token:               raw,
	}, nil
}

// DeletePersonalAccessToken revokes a token. Only the
// owning user (or an admin) can revoke; the resource name
// is parsed to recover the user/parent pair so a request
// for someone else's token is rejected without ever
// reading the row.
func (s *UserServer) DeletePersonalAccessToken(ctx context.Context, req *v1.DeletePersonalAccessTokenRequest) (*emptypb.Empty, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, id, ok := splitPersonalAccessTokenName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be users/{user}/personalAccessTokens/{token}")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	rec, err := s.app.FindRecordById("personal_access_tokens", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "personal access token not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "personal access token not found")
	}
	if rec.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only delete your own personal access tokens")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// recordToPersonalAccessToken converts a
// `personal_access_tokens` row into its proto shape. The
// `token_hash` column is intentionally not surfaced: the
// raw token is only ever returned on Create.
func recordToPersonalAccessToken(app core.App, rec *core.Record) *v1.PersonalAccessToken {
	if rec == nil {
		return nil
	}
	username := lookupUsername(app, rec.GetString("user"))
	out := &v1.PersonalAccessToken{
		Name:        "users/" + username + "/personalAccessTokens/" + rec.Id,
		Description: rec.GetString("description"),
	}
	if created := rec.GetDateTime("created_at"); !created.IsZero() {
		out.CreatedAt = timestamppb.New(created.Time())
	}
	if lastUsed := rec.GetDateTime("last_used_at"); !lastUsed.IsZero() {
		out.LastUsedAt = timestamppb.New(lastUsed.Time())
	}
	if expires := rec.GetDateTime("expires_at"); !expires.IsZero() {
		out.ExpiresAt = timestamppb.New(expires.Time())
	}
	return out
}
