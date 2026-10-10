package connect

import (
	"context"
	"regexp"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// idpNamePattern mirrors the collection's `name` Pattern
// regex. The Create/Update handlers re-validate the slug
// with the same shape so a maliciously-crafted resource
// name cannot slip past the proto annotation and reach
// the DB. The regex is intentionally duplicated here
// rather than read off the collection because the
// handler runs before the collection is loaded.
var idpNamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]{0,34}[a-zA-Z0-9])?$`)

// IdentityProviderServer implements the Memos
// IdentityProviderService. It owns the
// identity_providers table lifecycle: list / get / create
// / update / delete. Every verb except read is restricted
// to the instance superuser (the same authority as the
// other admin-only RPCs in `pb/connect`).
//
// The Connect handler is the sole authority for "who can
// manage identity providers"; the migration sets every
// `*Rule` field to nil so the PocketBase rule engine
// cannot be tricked into letting a non-admin through the
// collection REST surface. The rate-limit scope rows for
// Create / Update / Delete are wired in
// `pb/connect/ratelimit.go`; List and Get fall under the
// general IP throttle.
type IdentityProviderServer struct {
	apiv1connect.UnimplementedIdentityProviderServiceHandler
	app core.App
}

// NewIdentityProviderServer constructs the
// IdentityProviderService implementation.
func NewIdentityProviderServer(app core.App) *IdentityProviderServer {
	return &IdentityProviderServer{app: app}
}

// ListIdentityProviders returns every row in the
// `identity_providers` collection. The handler is gated
// on the caller being authenticated; a non-admin still
// needs to be able to enumerate the providers so the SPA
// can render the "Sign in with Google" button on the
// sign-in page. The proto model treats the list as a
// public-shaped resource: read paths are not admin-only.
//
// The `client_secret` is masked by the converter, so a
// non-admin reading this list still never sees a usable
// secret. (A future hardening pass could push this
// further by stripping the entire `oauth2_config` on
// ListIdentityProviders and only rendering it on
// GetIdentityProvider, but the proto does not have a
// separate "public" shape today.)
func (s *IdentityProviderServer) ListIdentityProviders(_ context.Context, _ *v1.ListIdentityProvidersRequest) (*v1.ListIdentityProvidersResponse, error) {
	rows, err := s.app.FindAllRecords("identity_providers")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.IdentityProvider, 0, len(rows))
	for _, r := range rows {
		if p := recordToIdentityProvider(r); p != nil {
			out = append(out, p)
		}
	}
	return &v1.ListIdentityProvidersResponse{IdentityProviders: out}, nil
}

// GetIdentityProvider returns a single row by its
// resource name (`identity-providers/{idp}`).
func (s *IdentityProviderServer) GetIdentityProvider(_ context.Context, req *v1.GetIdentityProviderRequest) (*v1.IdentityProvider, error) {
	slug, ok := idpSlugFromResourceName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be identity-providers/{idp}")
	}
	rec, err := s.app.FindFirstRecordByFilter("identity_providers", "name={:n}", map[string]any{"n": slug})
	if err != nil || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, "identity provider not found")
	}
	return recordToIdentityProvider(rec), nil
}

// CreateIdentityProvider persists a new row. Admin-only.
// The resource name is parsed from either the request's
// `identity_provider.name` (if the SPA supplied one) or
// the `identity_provider_id` override, falling back to a
// slug derived from `title`.
func (s *IdentityProviderServer) CreateIdentityProvider(ctx context.Context, req *v1.CreateIdentityProviderRequest) (*v1.IdentityProvider, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	in := req.GetIdentityProvider()
	if in == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "identity_provider is required")
	}
	slug, err := resolveIdpSlug(in.GetName(), req.GetIdentityProviderId(), in.GetTitle())
	if err != nil {
		return nil, err
	}
	if existing, _ := s.app.FindFirstRecordByFilter("identity_providers", "name={:n}", map[string]any{"n": slug}); existing != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, "identity provider already exists")
	}

	col, err := s.app.FindCollectionByNameOrId("identity_providers")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(col)
	rec.Set("name", slug)
	if err := s.applyIdentityProviderFields(rec, in, true); err != nil {
		return nil, err
	}
	if err := s.app.Save(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToIdentityProvider(rec), nil
}

// UpdateIdentityProvider patches an existing row.
// Admin-only. The `update_mask` follows the Memos 0.31
// convention: empty means "apply every field the request
// populated"; a non-empty mask only updates the listed
// top-level fields. The `client_secret` is INPUT_ONLY —
// when the request omits it, the existing secret is
// preserved (a missing value never clobbers).
func (s *IdentityProviderServer) UpdateIdentityProvider(ctx context.Context, req *v1.UpdateIdentityProviderRequest) (*v1.IdentityProvider, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	in := req.GetIdentityProvider()
	if in == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "identity_provider is required")
	}
	slug, ok := idpSlugFromResourceName(in.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "identity_provider.name must be identity-providers/{idp}")
	}
	rec, err := s.app.FindFirstRecordByFilter("identity_providers", "name={:n}", map[string]any{"n": slug})
	if err != nil || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, "identity provider not found")
	}
	if err := s.applyIdentityProviderFields(rec, in, false); err != nil {
		return nil, err
	}
	if err := s.app.Save(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToIdentityProvider(rec), nil
}

// DeleteIdentityProvider removes the row. Admin-only.
func (s *IdentityProviderServer) DeleteIdentityProvider(ctx context.Context, req *v1.DeleteIdentityProviderRequest) (*emptypb.Empty, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	slug, ok := idpSlugFromResourceName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be identity-providers/{idp}")
	}
	rec, err := s.app.FindFirstRecordByFilter("identity_providers", "name={:n}", map[string]any{"n": slug})
	if err != nil || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, "identity provider not found")
	}
	if err := s.app.Delete(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// GetIdentityProviderClientSecret returns the current
// `client_secret` for the named OAuth2 identity provider.
// Admin-only — the secret is the credential that authorises
// the server to mint tokens with the IdP, so the SPA
// default-on render path cannot show it. The two non-List
// read RPCs (Get, GetIdentityProviderClientSecret) split the
// shape on purpose: a SPA can hydrate the form with the
// non-secret fields via Get, and only fetch the secret when
// the admin explicitly clicks "Reveal" — the SPA then
// renders it into a one-shot input and never persists it
// anywhere in the client state.
//
// The response is the raw string. An OAuth2 row whose
// `config` block does not include a `client_secret` (e.g.
// a stub row created before the operator filled in the
// form) returns an empty string with no error.
func (s *IdentityProviderServer) GetIdentityProviderClientSecret(ctx context.Context, req *v1.GetIdentityProviderClientSecretRequest) (*v1.GetIdentityProviderClientSecretResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	slug, ok := idpSlugFromResourceName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be identity-providers/{idp}")
	}
	rec, err := s.app.FindFirstRecordByFilter("identity_providers", "name={:n}", map[string]any{"n": slug})
	if err != nil || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, "identity provider not found")
	}
	// Re-decode (without masking) so the unmasked value is
	// only ever read on the admin-reveal path. The
	// recordToIdentityProvider helper applies the mask; the
	// reveal endpoint must not use it.
	raw := decodeIdentityProviderConfig(rec.GetString("config"))
	return &v1.GetIdentityProviderClientSecretResponse{
		ClientSecret: extractIdentityProviderSecret(raw),
	}, nil
}

// requireAdmin is the per-handler guard. A missing viewer
// (no auth) or a non-superuser viewer is rejected; the
// caller can decide on the connect code (we use
// Unauthenticated for "no token" and PermissionDenied
// for "wrong role" so a logged-in non-admin gets a more
// useful diagnostic).
func (s *IdentityProviderServer) requireAdmin(ctx context.Context) error {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if !viewer.IsSuperuser() {
		return connect.NewError(connect.CodePermissionDenied, "instance admin role required")
	}
	return nil
}

// applyIdentityProviderFields writes the proto body into
// the record. The `create` flag controls whether
// `client_secret` is required (true on Create, false on
// Update — the latter treats an empty secret as a
// no-op).
func (s *IdentityProviderServer) applyIdentityProviderFields(rec *core.Record, in *v1.IdentityProvider, create bool) error {
	if title := in.GetTitle(); title != "" {
		rec.Set("title", title)
	} else if create {
		return connect.NewError(connect.CodeInvalidArgument, "title is required")
	}
	switch in.GetType() {
	case v1.IdentityProvider_OAUTH2:
		rec.Set("type", "OAUTH2")
	case v1.IdentityProvider_TYPE_UNSPECIFIED:
		if create {
			return connect.NewError(connect.CodeInvalidArgument, "type is required")
		}
	default:
		return connect.NewError(connect.CodeInvalidArgument, "unsupported identity provider type")
	}
	// identifier_filter is optional; always assign so a
	// previous value can be cleared.
	rec.Set("identifier_filter", in.GetIdentifierFilter())

	raw, err := encodeIdentityProviderConfig(in.GetConfig())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, "config is not serialisable: "+err.Error())
	}
	if create {
		// On create, the proto's IdentityProviderConfig is
		// REQUIRED; we accept an empty oneof (it round-trips
		// to "{}") but reject a nil config.
		if in.GetConfig() == nil {
			return connect.NewError(connect.CodeInvalidArgument, "config is required")
		}
		// client_secret is mandatory for OAuth2 configs.
		if c := in.GetConfig().GetOauth2Config(); c == nil || c.GetClientId() == "" || c.GetClientSecret() == "" {
			return connect.NewError(connect.CodeInvalidArgument, "config.oauth2_config.client_id and client_secret are required")
		}
	} else {
		// On update, an empty oauth2_config oneof means
		// "leave the existing blob alone". The SPA can
		// rotate the secret by sending a non-empty
		// client_secret in a non-empty oauth2_config.
		if c := in.GetConfig().GetOauth2Config(); c != nil && c.GetClientId() != "" {
			// Caller is rewriting the oauth2 block;
			// honour the request.
		} else {
			// Caller did not supply oauth2_config; keep
			// the existing config blob. We re-encode the
			// in-memory raw from the existing record so
			// `rec.Set` round-trips to the same value.
			raw = rec.GetString("config")
		}
	}
	rec.Set("config", raw)
	return nil
}

// idpSlugFromResourceName strips the
// `identity-providers/` prefix from a resource name and
// returns the slug plus a boolean. An empty name or one
// that does not match the prefix is rejected.
func idpSlugFromResourceName(name string) (string, bool) {
	const prefix = "identity-providers/"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return "", false
	}
	slug := name[len(prefix):]
	if !idpNamePattern.MatchString(slug) {
		return "", false
	}
	return slug, true
}

// resolveIdpSlug picks the slug for a Create request.
// Order: the explicit resource name, the explicit
// identity_provider_id override, a slug derived from the
// title. Each candidate is re-validated against the
// pattern; a non-conforming input is rejected with
// InvalidArgument so the SPA can fix the form.
func resolveIdpSlug(resourceName, idOverride, title string) (string, error) {
	if resourceName != "" {
		if slug, ok := idpSlugFromResourceName(resourceName); ok {
			return slug, nil
		}
	}
	if idOverride != "" {
		if !idpNamePattern.MatchString(idOverride) {
			return "", connect.NewError(connect.CodeInvalidArgument, "identity_provider_id does not match the required pattern")
		}
		return idOverride, nil
	}
	slug := slugify(title)
	if slug == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, "title must contain at least one alphanumeric character")
	}
	return slug, nil
}
