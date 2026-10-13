package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// UserServer now also serves the four
// UserService.LinkedIdentity RPCs:
//
//	ListLinkedIdentities   — every linked identity owned
//	                        by the user named in `parent`.
//	                        Non-admin callers may only
//	                        list their own.
//	GetLinkedIdentity      — a single row by its resource
//	                        name. The caller must own it.
//	CreateLinkedIdentity   — exchange an OAuth
//	                        authorisation code through the
//	                        named IdP and store the
//	                        resulting (user, idp, oauth-id)
//	                        tuple. The raw code is consumed
//	                        in-flight; only the resolved
//	                        provider id is persisted.
//	DeleteLinkedIdentity   — unlink. The caller must own
//	                        the row.
//
// The handlers are the only authority for "who can manage
// linked identities"; the migration sets every *Rule to
// nil so the PocketBase rule engine cannot be tricked
// into letting a wrong caller through the collection REST
// API.

// splitLinkedIdentityName parses
// "users/{user}/linkedIdentities/{li}" into its
// (username, id) segments. The boolean is false when the
// name is malformed.
func splitLinkedIdentityName(name string) (string, string, bool) {
	const prefix = "users/"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	username := rest[:slash]
	segments := rest[slash+1:]
	const segs = "linkedIdentities/"
	if !strings.HasPrefix(segments, segs) {
		return "", "", false
	}
	id := segments[len(segs):]
	if username == "" || id == "" {
		return "", "", false
	}
	return username, id, true
}

// ListLinkedIdentities returns every linked identity
// owned by the user named in `parent`. A non-admin caller
// may only list their own links; the auth check runs
// before the DB scan so a wrong parent never leaks
// another user's row count.
func (s *UserServer) ListLinkedIdentities(ctx context.Context, req *v1.ListLinkedIdentitiesRequest) (*v1.ListLinkedIdentitiesResponse, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only list your own linked identities")
	}

	rows, err := s.app.FindRecordsByFilter("linked_identities",
		"user={:uid}", "-created", 0, 0, map[string]any{"uid": target.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.LinkedIdentity, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToLinkedIdentity(s.app, r))
	}
	return &v1.ListLinkedIdentitiesResponse{LinkedIdentities: out}, nil
}

// GetLinkedIdentity returns a single row by its resource
// name. The caller must own it; an admin can read any.
func (s *UserServer) GetLinkedIdentity(ctx context.Context, req *v1.GetLinkedIdentityRequest) (*v1.LinkedIdentity, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, id, ok := splitLinkedIdentityName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be users/{user}/linkedIdentities/{linked_identity}")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	rec, err := s.app.FindRecordById("linked_identities", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "linked identity not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "linked identity not found")
	}
	if rec.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only read your own linked identities")
	}
	return recordToLinkedIdentity(s.app, rec), nil
}

// CreateLinkedIdentity exchanges the supplied OAuth code
// through the named IdP and persists the resulting
// (user, idp, provider_id) tuple. The caller must be the
// target user; the only thing that distinguishes this
// from a SignIn sso_credentials call is the missing
// auto-provision + session-mint: the user is already
// authenticated, so the handler only needs to verify
// the code and store the link.
//
// The OAuth code-exchange is the same code path the
// SignIn sso_credentials branch runs; we re-use the
// resolveIdpAndExchange helper from auth_sso.go to keep
// the two paths in lockstep.
func (s *UserServer) CreateLinkedIdentity(ctx context.Context, req *v1.CreateLinkedIdentityRequest) (*v1.LinkedIdentity, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only link to your own account")
	}
	if req.GetCode() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "code is required")
	}
	if req.GetRedirectUri() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "redirect_uri is required")
	}

	_, idpRec, providerName, oauthUser, err := resolveIdpAndExchange(ctx, s.app, req.GetIdpName(), req.GetRedirectUri(), req.GetCode(), req.GetCodeVerifier())
	if err != nil {
		return nil, err
	}
	if oauthUser == nil || oauthUser.Id == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "OAuth2 provider returned no user id")
	}

	// Idempotency: a duplicate (user, idp) insert fails the
	// unique index. We catch that failure and return the
	// existing row, so the SPA's "link" button is safe to
	// double-click.
	col, err := s.app.FindCollectionByNameOrId("linked_identities")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(col)
	rec.Set("user", target.Id)
	rec.Set("idp", idpRec.Id)
	rec.Set("provider_name", providerName)
	rec.Set("provider_id", oauthUser.Id)
	if oauthUser.Email != "" {
		rec.Set("extern_uid", oauthUser.Email)
	} else if oauthUser.Username != "" {
		rec.Set("extern_uid", oauthUser.Username)
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		if existing, ferr := s.app.FindFirstRecordByFilter("linked_identities",
			"user={:u} && idp={:p}", map[string]any{"u": target.Id, "p": idpRec.Id}); ferr == nil && existing != nil {
			return recordToLinkedIdentity(s.app, existing), nil
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToLinkedIdentity(s.app, rec), nil
}

// DeleteLinkedIdentity revokes a link. The caller must own
// the row; an admin can revoke anyone's.
func (s *UserServer) DeleteLinkedIdentity(ctx context.Context, req *v1.DeleteLinkedIdentityRequest) (*emptypb.Empty, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, id, ok := splitLinkedIdentityName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be users/{user}/linkedIdentities/{linked_identity}")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	rec, err := s.app.FindRecordById("linked_identities", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "linked identity not found")
	}
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeNotFound, "linked identity not found")
	}
	if rec.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only delete your own linked identities")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// recordToLinkedIdentity converts a `linked_identities`
// row into its proto shape. The IdP slug is recovered
// from the `idp` relation so the proto's `idp_name` field
// is `identity-providers/{idp}`.
func recordToLinkedIdentity(app core.App, rec *core.Record) *v1.LinkedIdentity {
	if rec == nil {
		return nil
	}
	username := lookupUsername(app, rec.GetString("user"))
	idpSlug := lookupIdentityProviderSlug(app, rec.GetString("idp"))
	return &v1.LinkedIdentity{
		Name:      "users/" + username + "/linkedIdentities/" + rec.Id,
		IdpName:   "identity-providers/" + idpSlug,
		ExternUid: rec.GetString("extern_uid"),
	}
}

// lookupIdentityProviderSlug resolves an IdP record id to
// its slug (the `name` column). It is a thin local alias
// so the linked-identity module does not depend on a
// future internal IdP package.
func lookupIdentityProviderSlug(app core.App, idpID string) string {
	if app == nil || idpID == "" {
		return ""
	}
	rec, err := app.FindRecordById("identity_providers", idpID)
	if err != nil || rec == nil {
		return ""
	}
	return rec.GetString("name")
}
