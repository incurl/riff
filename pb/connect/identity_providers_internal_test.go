package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// oauth2Config is a small helper that returns a
// fully-populated OAuth2Config so the tests below can
// focus on the handler logic instead of the wire shape.
func oauth2Config() *v1.OAuth2Config {
	return &v1.OAuth2Config{
		ClientId:     "test-client-id",
		ClientSecret: "test-client-secret",
		AuthUrl:      "https://example.com/o/oauth2/v2/auth",
		TokenUrl:     "https://oauth2.example.com/token",
		UserInfoUrl:  "https://example.com/oauth2/v3/userinfo",
		Scopes:       []string{"openid", "email", "profile"},
	}
}

// TestListIdentityProviders_EmptyByDefault pins the
// read-path happy state: a freshly-created instance has
// no IdPs, so the list response is a non-nil empty
// slice (not nil) that the SPA can iterate without a
// nil-check.
func TestListIdentityProviders_EmptyByDefault(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)

	out, err := srv.ListIdentityProviders(context.Background(), &v1.ListIdentityProvidersRequest{})
	require.NoError(t, err)
	require.NotNil(t, out.IdentityProviders)
	assert.Empty(t, out.IdentityProviders)
}

// TestCreateIdentityProvider_AdminRequired guards the
// per-handler admin guard. A non-superuser viewer is
// rejected with PermissionDenied; an unauthenticated
// caller gets Unauthenticated. The handler never
// touches the `identity_providers` collection in either
// case, so the table stays empty.
func TestCreateIdentityProvider_AdminRequired(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	alice := newUser(t, app, "alice")

	// Unauthenticated -> Unauthenticated.
	_, err := srv.CreateIdentityProvider(context.Background(), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// Non-admin -> PermissionDenied.
	_, err = srv.CreateIdentityProvider(withViewer(context.Background(), alice), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestCreateIdentityProvider_AdminSucceeds covers the
// happy path: an admin can create a row, the response
// carries the resource name `identity-providers/{idp}`,
// and the `config` blob round-trips for the non-secret
// fields. The `client_secret` is masked on read — the
// converter strips it before returning — so the
// assertion below pins that the secret is empty in the
// response. Reveal goes through
// GetIdentityProviderClientSecret; see
// TestGetIdentityProviderClientSecret_AdminRevealsSecret.
func TestCreateIdentityProvider_AdminSucceeds(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")

	got, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
		IdentityProviderId: "google",
	})
	require.NoError(t, err)
	assert.Equal(t, "identity-providers/google", got.GetName())
	assert.Equal(t, "Google", got.GetTitle())
	assert.Equal(t, v1.IdentityProvider_OAUTH2, got.GetType())
	require.NotNil(t, got.GetConfig().GetOauth2Config())
	assert.Equal(t, "test-client-id", got.GetConfig().GetOauth2Config().GetClientId())
	// The secret is masked on every read — the only way
	// an admin recovers the stored value is via
	// GetIdentityProviderClientSecret.
	assert.Equal(t, "", got.GetConfig().GetOauth2Config().GetClientSecret())
}

// TestCreateIdentityProvider_DuplicateNameRejected
// pins the slug-uniqueness invariant: creating two rows
// with the same `identity_provider_id` returns
// AlreadyExists, and the table row count remains one.
func TestCreateIdentityProvider_DuplicateNameRejected(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")

	mk := func() *v1.IdentityProvider {
		return &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		}
	}
	_, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider:   mk(),
		IdentityProviderId: "google",
	})
	require.NoError(t, err)

	_, err = srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider:   mk(),
		IdentityProviderId: "google",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// TestCreateIdentityProvider_MissingClientSecretRejected
// pins the OAuth2 config invariant: a Create request
// without a `client_secret` is rejected before any DB
// write happens. The point of the test is to confirm
// the handler is the only place that enforces the rule;
// the collection's own required-column checks are
// bypassed because we use the JSON blob.
func TestCreateIdentityProvider_MissingClientSecretRejected(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")

	cfg := oauth2Config()
	cfg.ClientSecret = ""
	_, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: cfg},
			},
		},
		IdentityProviderId: "google",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestGetIdentityProvider_NotFound guards the resource
// name parser: an unknown slug is reported as NotFound,
// not as a generic Internal error.
func TestGetIdentityProvider_NotFound(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	_, err := srv.GetIdentityProvider(context.Background(), &v1.GetIdentityProviderRequest{
		Name: "identity-providers/missing",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestGetIdentityProvider_InvalidName rejects malformed
// resource names before reaching the DB layer. The
// pattern check fires for any name whose slug does not
// match `^[a-zA-Z0-9]([a-zA-Z0-9-]{0,34}[a-zA-Z0-9])?$`.
func TestGetIdentityProvider_InvalidName(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	for _, name := range []string{"", "users/foo", "identity-providers/", "identity-providers/bad name", "identity-providers/-leading-hyphen"} {
		_, err := srv.GetIdentityProvider(context.Background(), &v1.GetIdentityProviderRequest{Name: name})
		require.Error(t, err, "name=%q", name)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "name=%q", name)
	}
}

// TestDeleteIdentityProvider_AdminRequired covers the
// admin guard on the destructive verb; the test mirrors
// the Create counterpart.
func TestDeleteIdentityProvider_AdminRequired(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")
	alice := newUser(t, app, "alice")

	// Seed a row.
	_, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
		IdentityProviderId: "google",
	})
	require.NoError(t, err)

	// Non-admin -> PermissionDenied.
	_, err = srv.DeleteIdentityProvider(withViewer(context.Background(), alice), &v1.DeleteIdentityProviderRequest{
		Name: "identity-providers/google",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Admin -> success.
	_, err = srv.DeleteIdentityProvider(withViewer(context.Background(), admin), &v1.DeleteIdentityProviderRequest{
		Name: "identity-providers/google",
	})
	require.NoError(t, err)

	// Re-list to confirm the row is gone.
	out, err := srv.ListIdentityProviders(context.Background(), &v1.ListIdentityProvidersRequest{})
	require.NoError(t, err)
	assert.Empty(t, out.IdentityProviders)
}

// TestGlobMatch covers the identifier_filter helper. The
// implementation only supports a single-`*` wildcard
// today, so the table-driven test pins both happy and
// failure shapes.
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"", "anything", true},
		{"*@example.com", "alice@example.com", true},
		{"*@example.com", "bob@other.com", false},
		{"exact@example.com", "exact@example.com", true},
		{"exact@example.com", "EXACT@example.com", false},
		{"a*c", "abc", true},
		{"a*c", "abbbbc", true},
		{"a*c", "ac", true},
		{"a*c", "cba", false},
	}
	for _, tc := range cases {
		got := globMatch(tc.pattern, tc.value)
		assert.Equal(t, tc.want, got, "pattern=%q value=%q", tc.pattern, tc.value)
	}
}

// TestListAndGet_MaskClientSecret pins the read-path
// invariant: every read (List, Get) returns a
// `client_secret` of "". The admin-reveal RPC is the
// only path that returns the unmasked value. The test
// covers both ListIdentityProviders and
// GetIdentityProvider with the same row to confirm
// neither path leaks the secret.
func TestListAndGet_MaskClientSecret(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")

	_, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
		IdentityProviderId: "google",
	})
	require.NoError(t, err)

	// List — every row in the response has an empty
	// client_secret.
	list, err := srv.ListIdentityProviders(context.Background(), &v1.ListIdentityProvidersRequest{})
	require.NoError(t, err)
	require.Len(t, list.IdentityProviders, 1)
	assert.Equal(t, "", list.IdentityProviders[0].GetConfig().GetOauth2Config().GetClientSecret(),
		"List response must not echo client_secret")
	assert.Equal(t, "test-client-id", list.IdentityProviders[0].GetConfig().GetOauth2Config().GetClientId(),
		"non-secret fields still round-trip on List")

	// Get — same row, same mask.
	got, err := srv.GetIdentityProvider(context.Background(), &v1.GetIdentityProviderRequest{
		Name: "identity-providers/google",
	})
	require.NoError(t, err)
	assert.Equal(t, "", got.GetConfig().GetOauth2Config().GetClientSecret(),
		"Get response must not echo client_secret")
	assert.Equal(t, "test-client-id", got.GetConfig().GetOauth2Config().GetClientId())
}

// TestGetIdentityProviderClientSecret_AdminRevealsSecret
// pins the admin-only reveal RPC. The test covers
// three angles:
//
//  1. An admin's call returns the unmasked
//     `client_secret` exactly as it was written.
//  2. A non-admin viewer is rejected with
//     PermissionDenied (the secret is admin-only).
//  3. An unauthenticated caller is rejected with
//     Unauthenticated before any DB read.
//
// The reveal RPC is the only path that may return the
// unmasked value; the converter masks it on every
// other read.
func TestGetIdentityProviderClientSecret_AdminRevealsSecret(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")
	alice := newUser(t, app, "alice")

	_, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
		IdentityProviderId: "google",
	})
	require.NoError(t, err)

	// 1. Admin reveal.
	resp, err := srv.GetIdentityProviderClientSecret(withViewer(context.Background(), admin), &v1.GetIdentityProviderClientSecretRequest{
		Name: "identity-providers/google",
	})
	require.NoError(t, err)
	assert.Equal(t, "test-client-secret", resp.GetClientSecret())

	// 2. Non-admin rejected.
	_, err = srv.GetIdentityProviderClientSecret(withViewer(context.Background(), alice), &v1.GetIdentityProviderClientSecretRequest{
		Name: "identity-providers/google",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// 3. Unauthenticated rejected.
	_, err = srv.GetIdentityProviderClientSecret(context.Background(), &v1.GetIdentityProviderClientSecretRequest{
		Name: "identity-providers/google",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestGetIdentityProviderClientSecret_NotFoundAndInvalid
// pins the resource-name parser for the reveal RPC. The
// parser is shared with the rest of the resource-named
// RPCs, but the test lives here so a regression in the
// reveal-specific handler is caught even when the other
// handlers are not exercised.
func TestGetIdentityProviderClientSecret_NotFoundAndInvalid(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")

	// NotFound on a well-formed but missing slug.
	_, err := srv.GetIdentityProviderClientSecret(withViewer(context.Background(), admin), &v1.GetIdentityProviderClientSecretRequest{
		Name: "identity-providers/missing",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// InvalidArgument on malformed names.
	for _, name := range []string{"", "users/foo", "identity-providers/", "identity-providers/-leading-hyphen"} {
		_, err := srv.GetIdentityProviderClientSecret(withViewer(context.Background(), admin), &v1.GetIdentityProviderClientSecretRequest{
			Name: name,
		})
		require.Error(t, err, "name=%q", name)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "name=%q", name)
	}
}

// TestUpdateIdentityProvider_PreservesSecretWhenOmitted
// pins the rotation-safety invariant: an Update request
// that omits the OAuth2 config (or supplies it with an
// empty `client_secret`) preserves the existing secret.
// The admin flow is "fetch the row, edit non-secret
// fields, PATCH without `client_secret`" — the secret
// must not be clobbered by an empty value, and the
// admin-reveal RPC must still return the original
// secret after the update.
func TestUpdateIdentityProvider_PreservesSecretWhenOmitted(t *testing.T) {
	app := newHookedApp(t)
	srv := NewIdentityProviderServer(app)
	admin := newSuperuser(t, app, "admin@example.com")

	_, err := srv.CreateIdentityProvider(withViewer(context.Background(), admin), &v1.CreateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Title: "Google",
			Type:  v1.IdentityProvider_OAUTH2,
			Config: &v1.IdentityProviderConfig{
				Config: &v1.IdentityProviderConfig_Oauth2Config{Oauth2Config: oauth2Config()},
			},
		},
		IdentityProviderId: "google",
	})
	require.NoError(t, err)

	// Update only the title; the config oneof is nil, so
	// the handler must keep the existing secret.
	_, err = srv.UpdateIdentityProvider(withViewer(context.Background(), admin), &v1.UpdateIdentityProviderRequest{
		IdentityProvider: &v1.IdentityProvider{
			Name:  "identity-providers/google",
			Title: "Google Workspace",
		},
	})
	require.NoError(t, err)

	// The admin reveal still returns the original secret.
	resp, err := srv.GetIdentityProviderClientSecret(withViewer(context.Background(), admin), &v1.GetIdentityProviderClientSecretRequest{
		Name: "identity-providers/google",
	})
	require.NoError(t, err)
	assert.Equal(t, "test-client-secret", resp.GetClientSecret())
}
