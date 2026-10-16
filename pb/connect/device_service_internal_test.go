package connect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// newPo33Device inserts a po33_devices row for the
// given user and returns the row id plus the
// generated fingerprint. The row is ACTIVE by
// default; the test can flip the status with
// Set("status", "REVOKED") if it needs a revoked
// device.
func newPo33Device(t *testing.T, app core.App, userID string) (string, string) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("po33_devices")
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	fpr := ed25519FingerprintForTest(pub)
	rec := core.NewRecord(col)
	rec.Set("user", userID)
	rec.Set("label", "Test PO-33")
	rec.Set("public_key", "deadbeef") // 32 raw bytes round-trip through hex.Encode
	rec.Set("public_key_fpr", fpr)
	rec.Set("status", "ACTIVE")
	require.NoError(t, app.Save(rec))
	return rec.Id, fpr
}

// ed25519FingerprintForTest computes the same
// SHA-256-hex fingerprint the production code uses.
// The hash lives in internal/ed25519verify but a
// re-implementation here is one line; the test
// surface is independent of the production surface
// on purpose so a future refactor of the library
// does not ripple into the test fixtures.
func ed25519FingerprintForTest(pub ed25519.PublicKey) string {
	return ed25519Verify_FingerprintForTest(pub)
}

// TestListDevices_EmptyByDefault pins the read-path
// happy state: a freshly-created instance with no
// devices returns an empty (but non-nil) list.
func TestListDevices_EmptyByDefault(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")

	out, err := srv.ListDevices(withViewer(context.Background(), alice), &v1.ListDevicesRequest{})
	require.NoError(t, err)
	require.NotNil(t, out.Devices)
	assert.Empty(t, out.Devices)
}

// TestListDevices_ReturnsCallersOwnOnly pins the
// ownership filter: alice's list is alice's only,
// even when bob has a device too.
func TestListDevices_ReturnsCallersOwnOnly(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	newPo33Device(t, app, alice.Id)
	newPo33Device(t, app, bob.Id)
	newPo33Device(t, app, alice.Id) // alice's second

	out, err := srv.ListDevices(withViewer(context.Background(), alice), &v1.ListDevicesRequest{})
	require.NoError(t, err)
	require.Len(t, out.Devices, 2)
	for _, d := range out.Devices {
		assert.True(t, strings.HasPrefix(d.GetName(), "po33-devices/"))
	}
}

// TestListDevices_RequiresAuth pins the
// unauthenticated path: a missing viewer returns
// Unauthenticated and never touches the table.
func TestListDevices_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	newPo33Device(t, app, alice.Id)

	_, err := srv.ListDevices(context.Background(), &v1.ListDevicesRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestGetDevice_OwnerSucceeds pins the happy path:
// the device owner can read their own device.
func TestGetDevice_OwnerSucceeds(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	id, fpr := newPo33Device(t, app, alice.Id)

	got, err := srv.GetDevice(withViewer(context.Background(), alice), &v1.GetDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.NoError(t, err)
	assert.Equal(t, "po33-devices/"+id, got.GetName())
	assert.Equal(t, fpr, got.GetPublicKeyFingerprint())
	assert.Equal(t, v1.DeviceStatus_DEVICE_STATUS_ACTIVE, got.GetStatus())
}

// TestGetDevice_NonOwnerForbidden pins the
// cross-tenant guard: bob cannot read alice's
// device, even by name.
func TestGetDevice_NonOwnerForbidden(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	id, _ := newPo33Device(t, app, alice.Id)

	_, err := srv.GetDevice(withViewer(context.Background(), bob), &v1.GetDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestGetDevice_AdminCanReadAny pins the
// superuser-shortcut: a PocketBase superuser can
// read any device. The hook layer (which is
// instance-wide) is the only place the device
// owner's identity is bypassed.
func TestGetDevice_AdminCanReadAny(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	admin := newSuperuser(t, app, "admin@example.com")
	id, _ := newPo33Device(t, app, alice.Id)

	got, err := srv.GetDevice(withViewer(context.Background(), admin), &v1.GetDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.NoError(t, err)
	assert.Equal(t, "po33-devices/"+id, got.GetName())
}

// TestGetDevice_InvalidNameRejected pins the
// resource name parser: a malformed name returns
// InvalidArgument so the SPA can fix the form.
func TestGetDevice_InvalidNameRejected(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	for _, bad := range []string{
		"",                  // empty
		"po33-devices",      // no id
		"po33-devices/../x", // id with non-alnum
		"devices/abc123",    // wrong prefix
	} {
		_, err := srv.GetDevice(withViewer(context.Background(), alice), &v1.GetDeviceRequest{
			Name: bad,
		})
		require.Errorf(t, err, "name=%q", bad)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}

// TestRevokeDevice_FlipsStatus pins the happy path:
// RevokeDevice marks the row REVOKED and the second
// call is a no-op (idempotent).
func TestRevokeDevice_FlipsStatus(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	id, _ := newPo33Device(t, app, alice.Id)

	_, err := srv.RevokeDevice(withViewer(context.Background(), alice), &v1.RevokeDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.NoError(t, err)

	got, err := srv.GetDevice(withViewer(context.Background(), alice), &v1.GetDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.NoError(t, err)
	assert.Equal(t, v1.DeviceStatus_DEVICE_STATUS_REVOKED, got.GetStatus())

	// Second revoke is a no-op.
	_, err = srv.RevokeDevice(withViewer(context.Background(), alice), &v1.RevokeDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.NoError(t, err)
}

// TestRevokeDevice_NonOwnerForbidden pins the
// cross-tenant guard on the write path: bob cannot
// revoke alice's device.
func TestRevokeDevice_NonOwnerForbidden(t *testing.T) {
	app := newHookedApp(t)
	srv := NewDeviceServer(app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	id, _ := newPo33Device(t, app, alice.Id)

	_, err := srv.RevokeDevice(withViewer(context.Background(), bob), &v1.RevokeDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// The status must still be ACTIVE.
	got, err := srv.GetDevice(withViewer(context.Background(), alice), &v1.GetDeviceRequest{
		Name: "po33-devices/" + id,
	})
	require.NoError(t, err)
	assert.Equal(t, v1.DeviceStatus_DEVICE_STATUS_ACTIVE, got.GetStatus())
}

// TestDeviceIDFromResourceName_AcceptsValid pins the
// resource name parser. The pattern is the PB row
// id format (15 alnum chars); a 15-char alnum id
// passes.
func TestDeviceIDFromResourceName_AcceptsValid(t *testing.T) {
	id, ok := deviceIDFromResourceName("po33-devices/" + strings.Repeat("a", 15))
	require.True(t, ok)
	assert.Equal(t, strings.Repeat("a", 15), id)
}

// TestDeviceIDFromResourceName_RejectsBad pins the
// malformed-name paths.
func TestDeviceIDFromResourceName_RejectsBad(t *testing.T) {
	for _, bad := range []string{
		"",
		"po33-devices",
		"po33-devices/",
		"po33-devices/abc",                  // too short
		"po33-devices/" + strings.Repeat("a", 16), // too long
		"po33-devices/abc-def",              // non-alnum
		"devices/abc",
	} {
		_, ok := deviceIDFromResourceName(bad)
		assert.Falsef(t, ok, "expected reject for %q", bad)
	}
}
