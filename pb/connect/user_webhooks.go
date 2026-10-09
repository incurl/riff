package connect

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"

	"github.com/incurl/riff/internal/webhook"
)

// UserServer now also serves the five
// UserService.UserWebhook RPCs:
//
//	ListUserWebhooks          — list the caller's own
//	                            webhooks (or any user's,
//	                            when the caller is an
//	                            instance admin).
//	CreateUserWebhook         — mint a new webhook for
//	                            the user named in
//	                            `parent`. Auto-generates
//	                            a Standard Webhooks
//	                            "whsec_<base64>" signing
//	                            secret if the request
//	                            does not supply one.
//	UpdateUserWebhook         — patch url, display_name
//	                            or signing_secret. The
//	                            update_mask follows the
//	                            Memos 0.31 convention:
//	                            empty means "apply what
//	                            the request populated".
//	DeleteUserWebhook         — revoke a webhook. The
//	                            cascade in the migration
//	                            removes any queued
//	                            deliveries.
//	GetUserWebhookSigningSecret — reveal the raw
//	                            signing secret, in the
//	                            same form the receiver
//	                            will see in the
//	                            webhook-signature header.
//
// The handlers are the only authority for "who can
// manage webhooks"; the migration sets every *Rule to
// nil so the rule engine cannot be tricked into
// letting a wrong caller through the collection REST
// API. PocketBase's password field is not used here
// (see the migration's file comment); the secret is a
// plain TextField, INPUT_ONLY in the proto, and
// returned only on the dedicated RPC.

// ListUserWebhooks returns every webhook owned by the
// user named in `parent`. A non-admin caller may only
// list their own webhooks; the auth check runs before
// the DB scan so a wrong parent never leaks another
// user's row count.
func (s *UserServer) ListUserWebhooks(ctx context.Context, req *v1.ListUserWebhooksRequest) (*v1.ListUserWebhooksResponse, error) {
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only list your own webhooks")
	}

	rows, err := s.app.FindRecordsByFilter("user_webhooks", "user={:uid}", "", 0, 0, map[string]any{"uid": target.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	// Resolve the username once and pass it to the
	// converter; FindRecordsByFilter has no expand
	// parameter, so we go through the user record
	// directly. `username` (the parent parser's
	// output) already has the value we want, so we
	// just reuse it.
	out := make([]*v1.UserWebhook, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToUserWebhookWithUsername(r, username))
	}
	return &v1.ListUserWebhooksResponse{Webhooks: out}, nil
}

// CreateUserWebhook inserts a new webhook row. The
// caller must own the parent user (or be an instance
// admin). The URL is re-validated through
// internal/webhook.ValidateURL so the SSRF / private-IP
// guard runs even when the form-level check on the
// `url` column is satisfied by a different protocol
// (e.g. a custom scheme PocketBase's URLField allows).
//
// If the request does not supply a signing_secret,
// the handler generates a fresh Standard Webhooks
// "whsec_<base64>" token. The same generator runs on
// every create, so two consecutive CreateUserWebhook
// calls without a supplied secret produce two
// independent secrets — there is no global "default
// secret" that an attacker could guess.
func (s *UserServer) CreateUserWebhook(ctx context.Context, req *v1.CreateUserWebhookRequest) (*v1.UserWebhook, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.GetWebhook() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "webhook is required")
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
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only create webhooks for yourself")
	}

	if err := validateUserWebhookURL(req.GetWebhook().GetUrl()); err != nil {
		return nil, err
	}
	secret := req.GetWebhook().GetSigningSecret()
	if secret == "" {
		generated, genErr := webhook.GenerateSigningSecret()
		if genErr != nil {
			return nil, connect.NewError(connect.CodeInternal, genErr.Error())
		}
		secret = generated
	} else {
		if err := validateUserWebhookSecret(secret); err != nil {
			return nil, err
		}
	}

	col, err := s.app.FindCollectionByNameOrId("user_webhooks")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(col)
	rec.Set("user", target.Id)
	rec.Set("url", req.GetWebhook().GetUrl())
	if display := req.GetWebhook().GetDisplayName(); display != "" {
		rec.Set("display_name", display)
	}
	rec.Set("signing_secret", secret)

	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		// Surface the underlying validation error to
		// the caller; the URL form check is the
		// most common cause and a generic
		// "invalid argument" is more helpful than a
		// 500.
		if isValidationError(err) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToUserWebhookWithUsername(rec, target.GetString("username")), nil
}

// UpdateUserWebhook patches a subset of
// {url, display_name, signing_secret}. The
// update_mask follows the same convention as the
// UserSetting Update RPC: empty mask means "apply what
// the request populated". Fields the request omits
// are left untouched; clearing a field requires an
// explicit empty string with the corresponding mask
// path.
//
// Authorisation is the same as CreateUserWebhook: the
// caller must own the webhook or be an instance
// admin. The URL and signing-secret validators only
// run when those fields are part of the update —
// touching only `display_name` does not re-validate
// the receiver.
func (s *UserServer) UpdateUserWebhook(ctx context.Context, req *v1.UpdateUserWebhookRequest) (*v1.UserWebhook, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.GetWebhook() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "webhook is required")
	}
	username, id, ok := splitUserWebhookName(req.GetWebhook().GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	rec, err := s.app.FindRecordById("user_webhooks", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "webhook not found")
	}
	if !canManageUserWebhook(viewer, rec) {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only update your own webhooks")
	}
	// Cross-check that the row's owner matches the
	// parent named in the request. The id alone
	// would be sufficient (it uniquely identifies
	// the row) but matching the username protects
	// against a future client bug that swaps two
	// resource names.
	if rec.GetString("user") != target.Id {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name does not match the webhook's owner")
	}

	paths, all := fieldMaskPaths(req.GetUpdateMask())
	if all {
		// fieldMaskPaths returns all=true when the
		// mask is nil/empty. We treat that as
		// "no fields to update" — the proto's
		// "any populated field" convention is not
		// safe here because the field set is
		// mostly strings, where the zero value
		// is indistinguishable from "not set".
		// The React client always sends a mask.
		return nil, connect.NewError(connect.CodeInvalidArgument, "update_mask is required and must include at least one of url, display_name, signing_secret")
	}
	if !hasPath(paths, "url") && !hasPath(paths, "display_name") && !hasPath(paths, "signing_secret") {
		return nil, connect.NewError(connect.CodeInvalidArgument, "update_mask must include at least one of url, display_name, signing_secret")
	}
	if hasPath(paths, "url") {
		if err := validateUserWebhookURL(req.GetWebhook().GetUrl()); err != nil {
			return nil, err
		}
		rec.Set("url", req.GetWebhook().GetUrl())
	}
	if hasPath(paths, "display_name") {
		rec.Set("display_name", req.GetWebhook().GetDisplayName())
	}
	if hasPath(paths, "signing_secret") {
		if err := validateUserWebhookSecret(req.GetWebhook().GetSigningSecret()); err != nil {
			return nil, err
		}
		rec.Set("signing_secret", req.GetWebhook().GetSigningSecret())
	}

	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		if isValidationError(err) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToUserWebhookWithUsername(rec, target.GetString("username")), nil
}

// DeleteUserWebhook removes a webhook. The cascade in
// the migration takes care of any queued deliveries
// (the user_webhook relation has CascadeDelete, so the
// outbox rows go with the webhook). The handler is
// idempotent: deleting a non-existent webhook is a
// NotFound, which the Memos React client treats as
// "the row is already gone, refresh the list".
func (s *UserServer) DeleteUserWebhook(ctx context.Context, req *v1.DeleteUserWebhookRequest) (*emptypb.Empty, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, id, ok := splitUserWebhookName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	if _, err := lookupUserByName(s.app, username); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	rec, err := s.app.FindRecordById("user_webhooks", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "webhook not found")
	}
	if !canManageUserWebhook(viewer, rec) {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only delete your own webhooks")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// GetUserWebhookSigningSecret returns the raw
// signing secret, in the Standard Webhooks
// "whsec_<base64>" form. The column is INPUT_ONLY in
// the proto (never echoed on list / get) so the only
// way to retrieve it after creation is this RPC.
//
// The handler refuses to return an empty secret —
// "no secret configured" is an InvalidArgument, not a
// successful empty response, because the Memos React
// client uses the presence of the secret to decide
// whether to render a "rotate" button.
func (s *UserServer) GetUserWebhookSigningSecret(ctx context.Context, req *v1.GetUserWebhookSigningSecretRequest) (*v1.GetUserWebhookSigningSecretResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, id, ok := splitUserWebhookName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	if _, err := lookupUserByName(s.app, username); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	rec, err := s.app.FindRecordById("user_webhooks", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "webhook not found")
	}
	if !canManageUserWebhook(viewer, rec) {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only read your own webhooks")
	}
	secret := recordToUserWebhookSigningSecret(rec)
	if secret == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "webhook has no signing secret configured")
	}
	return &v1.GetUserWebhookSigningSecretResponse{SigningSecret: secret}, nil
}

// validateUserWebhookURL wraps internal/webhook's
// ValidateURL, translating its gRPC status error to
// a Connect InvalidArgument so the client sees a
// consistent error shape across the Connect surface.
//
// The lib is shared with the Memos-style
// notification path, which still uses gRPC statuses;
// we keep the lib as-is rather than fork it to
// return Connect errors, because the lib is owned by
// the email/webhook team and we want a single
// source of truth.
func validateUserWebhookURL(rawURL string) error {
	if err := webhook.ValidateURL(rawURL); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return nil
}

// validateUserWebhookSecret wraps
// webhook.ValidateSigningSecret, same translation
// rule as validateUserWebhookURL.
func validateUserWebhookSecret(secret string) error {
	if err := webhook.ValidateSigningSecret(secret); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return nil
}

// hasPath reports whether paths contains the named
// entry. The map-from-fieldMaskPaths convention is
// "empty map means apply everything" so callers must
// also keep an `all` flag from the helper; the helper
// in translate.go already returns the all flag, so
// callers in this file use the bool and fall back to
// hasPath only when the mask is non-empty.
func hasPath(paths map[string]struct{}, name string) bool {
	_, ok := paths[name]
	return ok
}

// isValidationError is a coarse string-based check
// for PocketBase's record-validation failures. The
// framework does not expose typed sentinels, so the
// handlers pattern-match on the error message; the
// substring set is stable across PocketBase 0.40.x
// and is what memo_shares.go uses for the same
// purpose.
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "validation_failed") ||
		strings.Contains(msg, "Validation failed") ||
		strings.Contains(msg, "invalid format")
}
