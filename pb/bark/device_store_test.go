package bark

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeviceStoreRoundTrip exercises the PocketBase-backed DeviceStore
// against a real database.
//
// The store is the piece that replaces upstream's bbolt bucket, so the
// behaviours upstream's bbolt implementation had are pinned here.
//
// The most important one is the key generation rule, which surprises
// people: upstream's bbolt store replaces the caller's key with a
// freshly generated one whenever the key is not ALREADY registered:
//
//	if key == "" || bucket.Get([]byte(key)) == nil {
//	    key = shortuuid.New()
//	}
//
// So a client that registers with "mykey" is handed a different key, and
// only a key the server itself issued is kept (which is the update path).
func TestDeviceStoreRoundTrip(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, ensureDeviceCollection(app))

	store := newPBDeviceStore(app)

	// A first registration replaces the caller's key with a generated
	// one.
	key, err := store.SaveDeviceTokenByKey("mykey", "token123")
	require.NoError(t, err)
	assert.NotEqual(t, "mykey", key, "an unregistered key must be replaced")
	assert.Len(t, key, 22, "the generated key must have upstream's shape")

	token, err := store.DeviceTokenByKey(key)
	require.NoError(t, err)
	assert.Equal(t, "token123", token)

	// Updating the token with the key the server issued keeps it.
	key2, err := store.SaveDeviceTokenByKey(key, "token456")
	require.NoError(t, err)
	assert.Equal(t, key, key2, "a registered key must be kept")

	token, err = store.DeviceTokenByKey(key)
	require.NoError(t, err)
	assert.Equal(t, "token456", token)

	// The count reflects the single registration.
	count, err := store.CountAll()
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// An empty key is also replaced.
	other, err := store.SaveDeviceTokenByKey("", "tokenabc")
	require.NoError(t, err)
	assert.NotEmpty(t, other)
	assert.NotEqual(t, key, other, "generated keys must be unique")

	count, err = store.CountAll()
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

// TestDeviceStoreRejectsAnInvalidDevice pins the empty-token semantics.
//
// Upstream stores an empty token when APNs rejects a device, and its
// DeviceTokenByKey then fails with "device token invalid" — the key
// stays reserved, but the device can no longer be pushed to.
func TestDeviceStoreRejectsAnInvalidDevice(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, ensureDeviceCollection(app))

	store := newPBDeviceStore(app)

	key, err := store.SaveDeviceTokenByKey("mykey", "token123")
	require.NoError(t, err)
	require.NotEmpty(t, key)

	// The APNs-invalid path clears the token.
	key2, err := store.SaveDeviceTokenByKey(key, "")
	require.NoError(t, err)
	assert.Equal(t, key, key2, "the key is kept when the token is cleared")

	// The device is now invalid.
	_, err = store.DeviceTokenByKey(key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device token invalid")

	// The registration still exists, so the key stays reserved.
	count, err := store.CountAll()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestDeviceStoreUnknownKeyIsAnError pins the lookup failure.
func TestDeviceStoreUnknownKeyIsAnError(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, ensureDeviceCollection(app))

	store := newPBDeviceStore(app)

	_, err := store.DeviceTokenByKey("nosuchkey")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nosuchkey")
}

// TestDeviceStoreDeleteRemovesTheRegistration pins the delete path.
func TestDeviceStoreDeleteRemovesTheRegistration(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, ensureDeviceCollection(app))

	store := newPBDeviceStore(app)

	key, err := store.SaveDeviceTokenByKey("mykey", "token123")
	require.NoError(t, err)
	require.NotEmpty(t, key)

	require.NoError(t, store.DeleteDeviceByKey(key))

	_, err = store.DeviceTokenByKey(key)
	require.Error(t, err)

	// Deleting a missing key is a no-op, like upstream's bbolt
	// delete.
	require.NoError(t, store.DeleteDeviceByKey("mykey"))
}

// TestEnsureDeviceCollectionIsIdempotent pins that the collection helper
// can be called twice without failing.
func TestEnsureDeviceCollectionIsIdempotent(t *testing.T) {
	app := newTestApp(t)

	require.NoError(t, ensureDeviceCollection(app))
	require.NoError(t, ensureDeviceCollection(app))

	collection, err := app.FindCollectionByNameOrId(collectionName)
	require.NoError(t, err)
	assert.Equal(t, collectionName, collection.Name)
}

// TestDeviceKeyShape pins the generated key format.
//
// Upstream uses lithammer/shortuuid, whose output is a 22 character
// URL-safe base64 string. A Bark client that validates the key shape
// must keep working, so the port has to produce the same shape.
func TestDeviceKeyShape(t *testing.T) {
	key, err := generateDeviceKey()
	require.NoError(t, err)

	assert.Len(t, key, 22, "the key must have upstream's 22 character shape")
	assert.NotContains(t, key, "=", "the key must be unpadded")
	assert.NotContains(t, key, "+", "the key must be URL-safe")
	assert.NotContains(t, key, "/", "the key must be URL-safe")

	// Two calls must not collide.
	other, err := generateDeviceKey()
	require.NoError(t, err)
	assert.NotEqual(t, key, other)
}
