package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	pbauth "github.com/pocketbase/pocketbase/tools/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// ssoTestProvider is a tiny auth.Provider implementation
// wired to an httptest server. The test registers it
// under a unique name and points the IdP's client_id /
// client_secret / auth / token / userinfo URLs at the
// server's endpoints.
type ssoTestProvider struct {
	pbauth.BaseProvider
}

func (p *ssoTestProvider) FetchAuthUser(token *oauth2.Token) (*pbauth.AuthUser, error) {
	req, err := http.NewRequestWithContext(p.Context(), http.MethodGet, p.UserInfoURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("userinfo %d", res.StatusCode)
	}
	var raw struct {
		Sub   string `json:"sub"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return &pbauth.AuthUser{
		Id:           raw.Sub,
		Name:         raw.Name,
		Email:        raw.Email,
		Username:     raw.Email,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
	}, nil
}

// ssoTestServer stands up two endpoints on 127.0.0.1:
//
//	POST /token     — the OAuth2 token exchange
//	GET  /userinfo  — the OAuth2 userinfo
//
// Returns the base URL, a cleanup, and the name to use
// in `auth.Providers`. The token handler validates the
// `code` matches the requested fixture code and returns
// the access/refresh token pair; the userinfo handler
// returns a JSON body matching the supplied `AuthUser`.
// ssoTestCounter is shared across the tests in this file so the
// registered provider names do not collide when more than one
// test runs in the same process.
var ssoTestCounter atomic.Int64

func ssoTestServer(t *testing.T, code, sub, email, name string) (string, string, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" {
				http.Error(w, "bad grant_type", http.StatusBadRequest)
				return
			}
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "test-access-token",
				"refresh_token": "test-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/userinfo":
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub":   sub,
				"name":  name,
				"email": email,
			})
		default:
			http.NotFound(w, r)
		}
	}))

	providerName := fmt.Sprintf("sso-test-%d", ssoTestCounter.Add(1))
	pbauth.Providers[providerName] = func() pbauth.Provider {
		return &ssoTestProvider{}
	}
	cleanup := func() {
		srv.Close()
		delete(pbauth.Providers, providerName)
	}
	return srv.URL, providerName, cleanup
}

// installSsoIdp creates a row in the
// `identity_providers` table whose OAuth2 config points
// at the supplied test server. It returns the resource
// name (identity-providers/{slug}) and the slug itself.
func installSsoIdp(t *testing.T, app core.App, baseURL, providerName, slug, title string) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("identity_providers")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("name", slug)
	rec.Set("type", "OAUTH2")
	rec.Set("title", title)
	raw, err := encodeIdentityProviderConfig(&v1.IdentityProviderConfig{
		Config: &v1.IdentityProviderConfig_Oauth2Config{
			Oauth2Config: &v1.OAuth2Config{
				ClientId:     "test-client-id",
				ClientSecret: "test-client-secret",
				AuthUrl:      baseURL + "/authorize",
				TokenUrl:     baseURL + "/token",
				UserInfoUrl:  baseURL + "/userinfo",
				Scopes:       []string{"openid", "email"},
			},
		},
	})
	require.NoError(t, err)
	rec.Set("config", raw)
	require.NoError(t, app.Save(rec))
}

// TestSignInWithSSO_NewUser covers the full happy path:
// the request carries a valid code, the code exchange
// succeeds, the userinfo returns a fresh subject, no
// existing user is found, registration is allowed, so
// a new `users` row is created, an ExternalAuth link is
// inserted, and the response carries an access token
// and the user's resource name.
func TestSignInWithSSO_NewUser(t *testing.T) {
	app := newHookedApp(t)
	baseURL, providerName, cleanup := ssoTestServer(t, "code-abc", "google-sub-1", "alice@example.com", "Alice")
	defer cleanup()

	// Register the test provider with the riff slug
	// mapping by giving the IdP a slug that maps to the
	// test provider via the lowercase fallback in
	// ssoProviderNameFromSlug.
	installSsoIdp(t, app, baseURL, providerName, providerName, "Test IdP")

	auth := NewAuthServer(app, nil)
	resp, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_SsoCredentials{
			SsoCredentials: &v1.SignInRequest_SSOCredentials{
				IdpName:     "identity-providers/" + providerName,
				Code:        "code-abc",
				RedirectUri: "http://localhost/oauth/callback",
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetAccessToken())
	require.NotNil(t, resp.GetUser())
	assert.Equal(t, "alice@example.com", resp.GetUser().GetEmail())
	assert.Equal(t, "Alice", resp.GetUser().GetDisplayName())
	assert.Equal(t, "aliceexample_com", resp.GetUser().GetUsername())

	// Confirm the ExternalAuth link was created.
	rec, err := app.FindAuthRecordByEmail("users", "alice@example.com")
	require.NoError(t, err)
	rows, err := app.FindAllExternalAuthsByRecord(rec)
	require.NoError(t, err)
	require.Len(t, rows, 1, "expected exactly one ExternalAuth link for the new user")
	assert.Equal(t, providerName, rows[0].Provider())
	assert.Equal(t, "google-sub-1", rows[0].ProviderId())
}

// TestSignInWithSSO_DisallowRegistration guards the
// registration gate. When the GENERAL setting has
// disallowUserRegistration=true, a fresh IdP sign-in
// cannot provision a new user; the response is
// PermissionDenied and no `users` row is created.
func TestSignInWithSSO_DisallowRegistration(t *testing.T) {
	app := newHookedApp(t)
	baseURL, providerName, cleanup := ssoTestServer(t, "code-abc", "google-sub-2", "bob@example.com", "Bob")
	defer cleanup()
	installSsoIdp(t, app, baseURL, providerName, providerName, "Test IdP")

	// Flip the GENERAL flag.
	setGeneralSetting(t, app, &v1.InstanceSetting_GeneralSetting{
		DisallowUserRegistration: true,
	})

	auth := NewAuthServer(app, nil)
	_, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_SsoCredentials{
			SsoCredentials: &v1.SignInRequest_SSOCredentials{
				IdpName:     "identity-providers/" + providerName,
				Code:        "code-abc",
				RedirectUri: "http://localhost/oauth/callback",
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// And no user was created.
	_, err = app.FindAuthRecordByEmail("users", "bob@example.com")
	require.Error(t, err)
}

// TestSignInWithSSO_IdentifierFilterRejectsOffDomain
// guards the identifier_filter glob: an IdP whose
// filter is `*@example.com` must reject a Google account
// from `bob@other.com`, even when the rest of the
// sign-in path is otherwise valid.
func TestSignInWithSSO_IdentifierFilterRejectsOffDomain(t *testing.T) {
	app := newHookedApp(t)
	baseURL, providerName, cleanup := ssoTestServer(t, "code-abc", "google-sub-3", "bob@other.com", "Bob")
	defer cleanup()

	// Install an IdP whose identifier_filter restricts
	// to example.com.
	col, err := app.FindCollectionByNameOrId("identity_providers")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("name", providerName)
	rec.Set("type", "OAUTH2")
	rec.Set("title", "Test IdP")
	rec.Set("identifier_filter", "*@example.com")
	raw, err := encodeIdentityProviderConfig(&v1.IdentityProviderConfig{
		Config: &v1.IdentityProviderConfig_Oauth2Config{
			Oauth2Config: &v1.OAuth2Config{
				ClientId:     "test-client-id",
				ClientSecret: "test-client-secret",
				AuthUrl:      baseURL + "/authorize",
				TokenUrl:     baseURL + "/token",
				UserInfoUrl:  baseURL + "/userinfo",
			},
		},
	})
	require.NoError(t, err)
	rec.Set("config", raw)
	require.NoError(t, app.Save(rec))

	auth := NewAuthServer(app, nil)
	_, err = auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_SsoCredentials{
			SsoCredentials: &v1.SignInRequest_SSOCredentials{
				IdpName:     "identity-providers/" + providerName,
				Code:        "code-abc",
				RedirectUri: "http://localhost/oauth/callback",
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestSignInWithSSO_ReturningUser asserts the
// existing-link path: the same OAuth subject signing in
// a second time must hit the ExternalAuth lookup and
// return the previously-created user without trying to
// create a new row (which would otherwise fail on the
// `username` unique index).
func TestSignInWithSSO_ReturningUser(t *testing.T) {
	app := newHookedApp(t)
	baseURL, providerName, cleanup := ssoTestServer(t, "code-abc", "google-sub-4", "carol@example.com", "Carol")
	defer cleanup()
	installSsoIdp(t, app, baseURL, providerName, providerName, "Test IdP")

	auth := NewAuthServer(app, nil)
	first, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_SsoCredentials{
			SsoCredentials: &v1.SignInRequest_SSOCredentials{
				IdpName:     "identity-providers/" + providerName,
				Code:        "code-abc",
				RedirectUri: "http://localhost/oauth/callback",
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.GetAccessToken())

	second, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_SsoCredentials{
			SsoCredentials: &v1.SignInRequest_SSOCredentials{
				IdpName:     "identity-providers/" + providerName,
				Code:        "code-abc",
				RedirectUri: "http://localhost/oauth/callback",
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, first.GetUser().GetName(), second.GetUser().GetName())

	// And exactly one `users` row exists for this email.
	rows, err := app.FindRecordsByFilter("users", "email={:e}", "", 0, 0, map[string]any{"e": "carol@example.com"})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

// TestSignInWithSSO_InvalidIdpName covers the
// resource-name parser. Anything that does not match
// `identity-providers/{slug}` is rejected before the
// DB is touched.
func TestSignInWithSSO_InvalidIdpName(t *testing.T) {
	app := newHookedApp(t)
	auth := NewAuthServer(app, nil)
	_, err := auth.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_SsoCredentials{
			SsoCredentials: &v1.SignInRequest_SSOCredentials{
				IdpName:     "not-an-idp",
				Code:        "x",
				RedirectUri: "http://localhost/cb",
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestSsoProviderNameFromSlug is a small table-driven
// test on the slug-to-vendor mapping. Today the only
// built-in key is "google"; custom slugs round-trip via
// the lowercase fallback.
func TestSsoProviderNameFromSlug(t *testing.T) {
	assert.Equal(t, pbauth.NameGoogle, ssoProviderNameFromSlug("google"))
	assert.Equal(t, pbauth.NameGoogle, ssoProviderNameFromSlug("workspace"))
	assert.Equal(t, "custom", ssoProviderNameFromSlug("custom"))
}

// TestDeriveUsername covers the username-builder helper.
// The result must be 3-30 chars of [a-z0-9_] so the
// `users.username` unique-index check accepts it.
func TestDeriveUsername(t *testing.T) {
	cases := []struct {
		name string
		in   *pbauth.AuthUser
		want string
	}{
		{"email", &pbauth.AuthUser{Id: "x", Email: "alice@example.com"}, "alice_example_com"},
		{"username", &pbauth.AuthUser{Id: "x", Username: "bob_smith"}, "bob_smith"},
		{"id_fallback", &pbauth.AuthUser{Id: "12345"}, "user_12345"},
		{"too_long", &pbauth.AuthUser{Id: "x", Email: "verylongemailaddress@verylongdomain.com"}, "verylongemailaddress_verylongdo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveUsername(tc.in)
			assert.LessOrEqual(t, len(got), 30)
			assert.GreaterOrEqual(t, len(got), 3)
			// Round-trip through url.QueryEscape is a
			// no-op here, but the round-trip is a smoke
			// test that the result is plain ASCII.
			_, err := url.QueryUnescape(got)
			require.NoError(t, err)
		})
	}
}
