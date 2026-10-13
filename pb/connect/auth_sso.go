package connect

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbauth "github.com/pocketbase/pocketbase/tools/auth"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// signInWithSSO handles the `sso_credentials` branch of
// AuthService.SignIn. The flow:
//
//  1. Resolve the IdP row by its slug
//     (`identity-providers/{idp}` -> `idp`).
//  2. Initialise an auth.Provider via
//     core.OAuth2ProviderConfig.InitProvider — the same
//     entry point PocketBase's built-in OAuth2 endpoint
//     uses, so the vendor set is whatever
//     auth.Providers has registered (Google, GitHub, etc).
//  3. Exchange the auth code for a token, then fetch the
//     OAuth userinfo.
//  4. Find-or-create the matching `users` row. The
//     existing row is located first by the
//     `_externalAuths` link, then by email, then
//     auto-provisioned when `disallowUserRegistration` is
//     false. The latter path mirrors
//     apis.recordAuthWithOAuth2 + oauth2Submit in the
//     upstream PocketBase.
//  5. Link the ExternalAuth row (idempotent) and mint a
//     session token via rec.NewAuthToken().
//
// `users` is the auth collection, so creating a new row
// there goes through PocketBase's normal validation + hook
// chain via app.Save; the OnRecordAfterCreate hook in
// `pb/hooks` (memo author auto-assign, etc.) fires for
// SSO-born users exactly as it does for password-born
// ones.
func (s *AuthServer) signInWithSSO(ctx context.Context, sso *v1.SignInRequest_SSOCredentials) (*v1.SignInResponse, error) {
	if sso == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "sso_credentials is required")
	}
	if sso.GetIdpName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "idp_name is required")
	}
	if sso.GetCode() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "code is required")
	}
	if sso.GetRedirectUri() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "redirect_uri is required")
	}

	// (1)-(3) Resolve the IdP, exchange the code for an OAuth
	// token, and fetch the userinfo. resolveIdpAndExchange is the
	// shared helper the LinkedIdentity RPC also calls; keeping the
	// two paths in lockstep means a future IdP feature (PKCE for
	// link flows, an identifier_filter tweak) lands once.
	_, _, providerName, oauthUser, err := resolveIdpAndExchange(ctx, s.app, sso.GetIdpName(), sso.GetRedirectUri(), sso.GetCode(), sso.GetCodeVerifier())
	if err != nil {
		return nil, err
	}

	// (4) find-or-create the user
	users, err := s.app.FindCollectionByNameOrId("users")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	var authedRec *core.Record
	err = s.app.RunInTransaction(func(txApp core.App) error {
		// Existing link?
		link, lerr := txApp.FindFirstExternalAuthByExpr(dbx.HashExp{
			"collectionRef": users.Id,
			"provider":      providerName,
			"providerId":    oauthUser.Id,
		})
		if lerr != nil && !errors.Is(lerr, sql.ErrNoRows) {
			return lerr
		}
		if link != nil {
			rec, ferr := txApp.FindRecordById("users", link.RecordRef())
			if ferr != nil {
				return ferr
			}
			authedRec = rec
			return nil
		}
		// Fall-through: match by email.
		if oauthUser.Email != "" {
			rec, ferr := txApp.FindAuthRecordByEmail("users", oauthUser.Email)
			if ferr == nil && rec != nil {
				authedRec = rec
				return nil
			}
			if ferr != nil && !errors.Is(ferr, sql.ErrNoRows) {
				return ferr
			}
		}
		// No existing user — gate on disallowUserRegistration.
		settings := instanceGeneralSetting(txApp)
		if settings.GetDisallowUserRegistration() {
			return connect.NewError(connect.CodePermissionDenied, "user registration is not allowed")
		}
		newRec := core.NewRecord(users)
		newRec.Set("username", deriveUsername(oauthUser))
		if oauthUser.Email != "" {
			newRec.Set("email", oauthUser.Email)
		}
		displayName := oauthUser.Name
		if displayName == "" {
			displayName = oauthUser.Username
		}
		if displayName == "" {
			displayName = oauthUser.Email
		}
		newRec.Set("display_name", displayName)
		newRec.SetRandomPassword()
		if err := txApp.Save(newRec); err != nil {
			return err
		}
		// Mark verified only when the IdP-supplied email
		// is non-empty (the Google provider only populates
		// Email when email_verified is true, so the empty
		// case already implies "unverified").
		if oauthUser.Email != "" {
			newRec.SetVerified(true)
			if err := txApp.Save(newRec); err != nil {
				return err
			}
		}
		authedRec = newRec
		return nil
	})
	if err != nil {
		var cErr *connect.Error
		if errors.As(err, &cErr) {
			return nil, cErr
		}
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if authedRec == nil {
		return nil, connect.NewError(connect.CodeInternal, "OAuth2 sign-in produced no user")
	}

	// (5) link the ExternalAuth row if not already linked,
	// then mint a session token.
	if err := s.app.RunInTransaction(func(txApp core.App) error {
		existing, lerr := txApp.FindFirstExternalAuthByExpr(dbx.HashExp{
			"collectionRef": users.Id,
			"provider":      providerName,
			"providerId":    oauthUser.Id,
		})
		if lerr != nil && !errors.Is(lerr, sql.ErrNoRows) {
			return lerr
		}
		if existing != nil {
			return nil
		}
		link := core.NewExternalAuth(txApp)
		link.SetCollectionRef(users.Id)
		link.SetRecordRef(authedRec.Id)
		link.SetProvider(providerName)
		link.SetProviderId(oauthUser.Id)
		return txApp.Save(link)
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, "failed to link OAuth2 identity: "+err.Error())
	}

	accessToken, err := authedRec.NewAuthToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "token mint failed: "+err.Error())
	}
	return &v1.SignInResponse{
		User:                 recordToUser(authedRec),
		AccessToken:          accessToken,
		AccessTokenExpiresAt: timestamppb.New(time.Now().Add(14 * 24 * time.Hour)),
	}, nil
}

// ssoProviderNameFromSlug maps an IdP slug to a
// auth.Providers registry key. Today the riff binary
// only ships `google`; future IdPs can either add a row
// to the switch or rely on the lowercase fallback so
// operators can address custom providers by their
// registered name.
func ssoProviderNameFromSlug(slug string) string {
	switch slug {
	case "google", "google-workspace", "workspace":
		return pbauth.NameGoogle
	default:
		return slug
	}
}

// deriveUsername picks a unique-ish username for a
// newly-provisioned SSO user. The auth collection's
// `username` pattern is `^[a-z0-9_]{3,30}$` (see
// migration 1700000001), so the candidate is normalised
// to that shape. The unique index on `username` can
// still reject a duplicate; the caller will receive the
// constraint error from txApp.Save and propagate it.
func deriveUsername(u *pbauth.AuthUser) string {
	seed := u.Username
	if seed == "" {
		seed = u.Email
	}
	if seed == "" {
		seed = "user_" + u.Id
	}
	var b strings.Builder
	for _, r := range strings.ToLower(seed) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == ' ':
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) < 3 {
		out = "user_" + u.Id
	}
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

// globMatch evaluates a single-`*` wildcard pattern
// against a value. It is intentionally minimal — the
// only operator a tenant admin is likely to write is
// `*@example.com`, and a full regex engine here would
// be both overkill and a footgun. A real regex parser
// will land when the spec demands it.
func globMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	idx := strings.Index(pattern, "*")
	if idx < 0 {
		return pattern == value
	}
	prefix := pattern[:idx]
	suffix := pattern[idx+1:]
	return strings.HasPrefix(value, prefix) && strings.HasSuffix(value, suffix) && len(value) >= len(prefix)+len(suffix)
}

// resolveIdpAndExchange is the shared IdP-resolution +
// code-exchange path used by both the SignIn sso_credentials
// branch and the UserService.CreateLinkedIdentity RPC. It
// returns the IdP record (so callers can read the slug for
// the proto resource name), the auth.Providers registry
// key, and the OAuth userinfo resolved from the code.
//
// Returning a *connect.Error is the convention; the helper
// never writes to the database itself, so callers can
// choose between signing the caller in (signInWithSSO) and
// persisting a fresh link row (CreateLinkedIdentity).
func resolveIdpAndExchange(
	ctx context.Context,
	app core.App,
	idpNameResource, redirectURI, code, codeVerifier string,
) (slug string, idpRec *core.Record, providerName string, oauthUser *pbauth.AuthUser, err error) {
	if idpNameResource == "" {
		return "", nil, "", nil, connect.NewError(connect.CodeInvalidArgument, "idp_name is required")
	}
	slug, ok := idpSlugFromResourceName(idpNameResource)
	if !ok {
		return "", nil, "", nil, connect.NewError(connect.CodeInvalidArgument, "idp_name must be identity-providers/{idp}")
	}
	idpRec, lerr := app.FindFirstRecordByFilter("identity_providers", "name={:n}", map[string]any{"n": slug})
	if lerr != nil || idpRec == nil {
		return "", nil, "", nil, connect.NewError(connect.CodeNotFound, "identity provider not found")
	}
	if idpRec.GetString("type") != "OAUTH2" {
		return "", nil, "", nil, connect.NewError(connect.CodeFailedPrecondition, "identity provider is not OAuth2")
	}

	cfgProto := decodeIdentityProviderConfig(idpRec.GetString("config")).GetOauth2Config()
	if cfgProto == nil {
		return "", nil, "", nil, connect.NewError(connect.CodeFailedPrecondition, "identity provider has no OAuth2 config")
	}
	if cfgProto.GetClientId() == "" || cfgProto.GetClientSecret() == "" {
		return "", nil, "", nil, connect.NewError(connect.CodeFailedPrecondition, "identity provider config is missing client_id or client_secret")
	}
	providerName = ssoProviderNameFromSlug(slug)
	if _, lerr := pbauth.NewProviderByName(providerName); lerr != nil {
		return "", nil, "", nil, connect.NewError(connect.CodeFailedPrecondition, "unsupported OAuth2 provider: "+providerName)
	}
	pbConfig := core.OAuth2ProviderConfig{
		Name:         providerName,
		ClientId:     cfgProto.GetClientId(),
		ClientSecret: cfgProto.GetClientSecret(),
		AuthURL:      cfgProto.GetAuthUrl(),
		TokenURL:     cfgProto.GetTokenUrl(),
		UserInfoURL:  cfgProto.GetUserInfoUrl(),
		DisplayName:  idpRec.GetString("title"),
	}
	provider, lerr := pbConfig.InitProvider()
	if lerr != nil {
		return "", nil, "", nil, connect.NewError(connect.CodeInternal, "failed to initialise OAuth2 provider: "+lerr.Error())
	}
	provider.SetContext(ctx)
	provider.SetRedirectURL(redirectURI)

	var opts []oauth2.AuthCodeOption
	if provider.PKCE() && codeVerifier != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	}
	token, lerr := provider.FetchToken(code, opts...)
	if lerr != nil {
		return "", nil, "", nil, connect.NewError(connect.CodeUnauthenticated, "failed to exchange OAuth2 code: "+lerr.Error())
	}
	oauthUser, lerr = provider.FetchAuthUser(token)
	if lerr != nil {
		return "", nil, "", nil, connect.NewError(connect.CodeUnauthenticated, "failed to fetch OAuth2 user: "+lerr.Error())
	}
	if oauthUser == nil || oauthUser.Id == "" {
		return "", nil, "", nil, connect.NewError(connect.CodeUnauthenticated, "OAuth2 provider returned no user id")
	}

	// identifier_filter is a glob applied to the OAuth
	// email. The single-`*` wildcard parser mirrors the
	// comment on the original in-line block; a full
	// regex parser is deferred.
	if filt := idpRec.GetString("identifier_filter"); filt != "" {
		if oauthUser.Email == "" || !globMatch(filt, oauthUser.Email) {
			return "", nil, "", nil, connect.NewError(connect.CodePermissionDenied, "identity does not match the provider's identifier_filter")
		}
	}
	return slug, idpRec, providerName, oauthUser, nil
}
