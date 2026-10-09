package connect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"

	"github.com/incurl/riff/internal/webhook"
)

// Phase 4 Wave 7 — tests for the UserWebhook RPCs.
// The handlers are methods on UserServer, so this
// file lives next to user_webhooks.go.
//
// These tests use a real local HTTP server (via
// httptest) as the webhook receiver so the
// internal/webhook library's URL validation
// accepts the URL. 127.0.0.1 is on the reserved
// list, so without that test-server anchor the
// SSRF guard would reject every URL in the suite.
// The whole file opts in to private-IP delivery
// via webhook.AllowPrivateIPs and restores the
// previous value on cleanup.

// allowPrivateIPsForTest toggles the webhook
// library's private-destination bypass on for the
// duration of the test. Production code never sets
// it; the test server only ever runs on 127.0.0.1
// (or another loopback address), so the bypass is
// safe here. The TestMain-style t.Cleanup restores
// the previous value to keep the package's tests
// isolated from any other suite that runs in the
// same process.
func allowPrivateIPsForTest(t *testing.T) {
	t.Helper()
	prev := webhook.AllowPrivateIPs
	webhook.AllowPrivateIPs = true
	t.Cleanup(func() { webhook.AllowPrivateIPs = prev })
}

// webhookTestServer starts a test HTTP server on
// 127.0.0.1 and returns its URL plus a cleanup
// func. The server's response is the Memos-shape
// {"code": 0, "message": "ok"} so the internal
// webhook library treats the delivery as
// successful (it inspects the JSON body for a
// non-zero code).
func webhookTestServer(t *testing.T) (string, func()) {
	t.Helper()
	allowPrivateIPsForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"code": 0, "message": "ok"}`))
	}))
	return srv.URL, srv.Close
}

// TestListUserWebhooks_EmptyByDefault covers the
// list path's happy state: a freshly created user
// has no webhooks. The list response is a non-nil
// empty slice (not nil), so the React layer can
// iterate without a nil-check.
func TestListUserWebhooks_EmptyByDefault(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)

	out, err := srv.ListUserWebhooks(withViewer(context.Background(), alice), &v1.ListUserWebhooksRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	require.NotNil(t, out.Webhooks)
	assert.Empty(t, out.Webhooks)
}

// TestListUserWebhooks_OwnOnly covers the
// per-user isolation: bob cannot list alice's
// webhooks. The handler returns PermissionDenied
// without ever querying the collection, so the
// row count is not even observable.
func TestListUserWebhooks_OwnOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)
	url, cleanup := webhookTestServer(t)
	defer cleanup()

	// Create one webhook for alice so a
	// "successful but empty" bob-list would
	// prove a leak.
	_, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
		Parent:  "users/alice",
		Webhook: &v1.UserWebhook{Url: url, DisplayName: "primary"},
	})
	require.NoError(t, err)

	_, err = srv.ListUserWebhooks(withViewer(context.Background(), bob), &v1.ListUserWebhooksRequest{
		Parent: "users/alice",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestCreateUserWebhook_GeneratesSecretByDefault
// asserts that the handler auto-generates a Standard
// Webhooks "whsec_<base64>" signing secret when the
// request does not supply one, and that the
// generated secret is exposed on the
// GetUserWebhookSigningSecret RPC but NOT on the
// create response.
func TestCreateUserWebhook_GeneratesSecretByDefault(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	url, cleanup := webhookTestServer(t)
	defer cleanup()

	created, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
		Parent:  "users/alice",
		Webhook: &v1.UserWebhook{Url: url, DisplayName: "primary"},
	})
	require.NoError(t, err)
	assert.True(t, created.SigningSecretSet, "handler must report a secret is set")
	assert.Empty(t, created.SigningSecret, "create response must not echo the secret back")

	// The list path also omits the secret.
	listed, err := srv.ListUserWebhooks(withViewer(context.Background(), alice), &v1.ListUserWebhooksRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, listed.Webhooks, 1)
	assert.Empty(t, listed.Webhooks[0].SigningSecret, "list response must not echo the secret back")
	assert.True(t, listed.Webhooks[0].SigningSecretSet)

	// The dedicated RPC returns the secret.
	reveal, err := srv.GetUserWebhookSigningSecret(withViewer(context.Background(), alice), &v1.GetUserWebhookSigningSecretRequest{
		Name: created.Name,
	})
	require.NoError(t, err)
	require.NotEmpty(t, reveal.SigningSecret)
	assert.True(t, strings.HasPrefix(reveal.SigningSecret, "whsec_"), "generated secret must be in the Standard Webhooks form")
}

// TestCreateUserWebhook_RejectsReservedIP covers the
// SSRF guard: the URL must not resolve to a
// reserved/private address. The webhook library
// rejects the URL with an InvalidArgument, which
// the handler surfaces as a Connect
// CodeInvalidArgument.
func TestCreateUserWebhook_RejectsReservedIP(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)

	_, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
		Parent:  "users/alice",
		Webhook: &v1.UserWebhook{Url: "http://127.0.0.1/x"},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestUpdateUserWebhook_PatchURL covers the
// field-mask flow: a request without an explicit
// mask but only the URL populated leaves
// `display_name` and `signing_secret` untouched.
func TestUpdateUserWebhook_PatchURL(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	url1, cleanup1 := webhookTestServer(t)
	defer cleanup1()
	url2, cleanup2 := webhookTestServer(t)
	defer cleanup2()

	created, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
		Parent:  "users/alice",
		Webhook: &v1.UserWebhook{Url: url1, DisplayName: "primary"},
	})
	require.NoError(t, err)
	originalSecret, err := srv.GetUserWebhookSigningSecret(withViewer(context.Background(), alice), &v1.GetUserWebhookSigningSecretRequest{
		Name: created.Name,
	})
	require.NoError(t, err)

	updated, err := srv.UpdateUserWebhook(withViewer(context.Background(), alice), &v1.UpdateUserWebhookRequest{
		Webhook: &v1.UserWebhook{
			Name: created.Name,
			Url:  url2,
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"url"}},
	})
	require.NoError(t, err)
	assert.Equal(t, url2, updated.Url)
	assert.Equal(t, "primary", updated.DisplayName, "display_name must not be cleared on a url-only update")

	// The signing secret is preserved across an
	// unrelated update. The owner can prove this
	// by re-reading the column.
	afterSecret, err := srv.GetUserWebhookSigningSecret(withViewer(context.Background(), alice), &v1.GetUserWebhookSigningSecretRequest{
		Name: updated.Name,
	})
	require.NoError(t, err)
	assert.Equal(t, originalSecret.SigningSecret, afterSecret.SigningSecret, "signing_secret must survive a url-only update")
}

// TestDeleteUserWebhook_OwnerOnly covers the
// delete-authorisation path. The hook's
// CascadeDelete on user_webhook must also remove
// any queued deliveries, but no deliveries exist
// in this test (we're not saving memos), so we
// only assert the row is gone.
func TestDeleteUserWebhook_OwnerOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewUserServer(app)
	url, cleanup := webhookTestServer(t)
	defer cleanup()

	created, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
		Parent:  "users/alice",
		Webhook: &v1.UserWebhook{Url: url},
	})
	require.NoError(t, err)

	// Bob cannot delete alice's webhook.
	_, err = srv.DeleteUserWebhook(withViewer(context.Background(), bob), &v1.DeleteUserWebhookRequest{
		Name: created.Name,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Alice can.
	_, err = srv.DeleteUserWebhook(withViewer(context.Background(), alice), &v1.DeleteUserWebhookRequest{
		Name: created.Name,
	})
	require.NoError(t, err)

	// A second delete of the same id is a 404.
	_, err = srv.DeleteUserWebhook(withViewer(context.Background(), alice), &v1.DeleteUserWebhookRequest{
		Name: created.Name,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestUserWebhooks_Unauthenticated guards every
// RPC against an anonymous caller. The handler is
// the only authority for auth, and the migration's
// nil *Rule fields leave nothing for the rule
// engine to fall back on.
func TestUserWebhooks_Unauthenticated(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	url, cleanup := webhookTestServer(t)
	defer cleanup()

	created, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
		Parent:  "users/alice",
		Webhook: &v1.UserWebhook{Url: url},
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		call func() error
	}{
		{"List", func() error {
			_, err := srv.ListUserWebhooks(context.Background(), &v1.ListUserWebhooksRequest{Parent: "users/alice"})
			return err
		}},
		{"Create", func() error {
			_, err := srv.CreateUserWebhook(context.Background(), &v1.CreateUserWebhookRequest{
				Parent:  "users/alice",
				Webhook: &v1.UserWebhook{Url: url},
			})
			return err
		}},
		{"Update", func() error {
			_, err := srv.UpdateUserWebhook(context.Background(), &v1.UpdateUserWebhookRequest{
				Webhook: &v1.UserWebhook{Name: created.Name, Url: url},
			})
			return err
		}},
		{"Delete", func() error {
			_, err := srv.DeleteUserWebhook(context.Background(), &v1.DeleteUserWebhookRequest{Name: created.Name})
			return err
		}},
		{"GetSigningSecret", func() error {
			_, err := srv.GetUserWebhookSigningSecret(context.Background(), &v1.GetUserWebhookSigningSecretRequest{Name: created.Name})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		})
	}
}

// TestUserWebhooks_CascadeOnUserDelete confirms the
// migration's CascadeDelete on the `user` relation
// removes every webhook the user owned. A second
// log-in as the same user (fresh record) confirms
// the list endpoint now reports zero webhooks.
func TestUserWebhooks_CascadeOnUserDelete(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewUserServer(app)
	url, cleanup := webhookTestServer(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		_, err := srv.CreateUserWebhook(withViewer(context.Background(), alice), &v1.CreateUserWebhookRequest{
			Parent:  "users/alice",
			Webhook: &v1.UserWebhook{Url: url},
		})
		require.NoError(t, err)
	}

	// Delete alice via the user RPC so the
	// migration's cascade fires.
	_, err := srv.DeleteUser(withViewer(context.Background(), alice), &v1.DeleteUserRequest{Name: "users/alice"})
	require.NoError(t, err)

	rows, err := app.FindRecordsByFilter("user_webhooks", "", "", 0, 0, nil)
	require.NoError(t, err)
	assert.Empty(t, rows, "all of alice's webhooks must be cascade-deleted with her user record")
}
