package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateBarkDevicesCollection pins the shape of the bark_devices
// collection that backs the Bark push extension in pb/bark.
//
// The collection is server-only: the device_key is the credential that
// authorises a push, so no access rule may expose it.
func TestCreateBarkDevicesCollection(t *testing.T) {
	app := newTestApp(t)

	collection, err := app.FindCollectionByNameOrId("bark_devices")
	require.NoError(t, err, "the bark_devices collection must exist")
	assert.Equal(t, "bark_devices", collection.Name)

	// The device key and the APNs token.
	keyField := collection.Fields.GetByName("device_key")
	require.NotNil(t, keyField, "the device_key field must exist")
	textKey, ok := keyField.(*core.TextField)
	require.True(t, ok, "device_key must be a text field")
	assert.True(t, textKey.Required, "device_key is required")

	tokenField := collection.Fields.GetByName("device_token")
	require.NotNil(t, tokenField, "the device_token field must exist")
	textToken, ok := tokenField.(*core.TextField)
	require.True(t, ok, "device_token must be a text field")
	// The token is optional: an APNs-rejected token is stored as an
	// empty string, which marks the device invalid while keeping its
	// key reserved.
	assert.False(t, textToken.Required, "device_token must stay optional")

	// The timestamps.
	assert.NotNil(t, collection.Fields.GetByName("created"))
	assert.NotNil(t, collection.Fields.GetByName("updated"))

	// Server-only: every rule must be nil.
	assert.Nil(t, collection.ListRule, "the list rule must be nil")
	assert.Nil(t, collection.ViewRule, "the view rule must be nil")
	assert.Nil(t, collection.CreateRule, "the create rule must be nil")
	assert.Nil(t, collection.UpdateRule, "the update rule must be nil")
	assert.Nil(t, collection.DeleteRule, "the delete rule must be nil")

	// The device key is unique: it is the lookup key for every push.
	idx := findIndex(collection, "idx_bark_devices_key")
	require.NotNil(t, idx, "the unique device_key index must exist")
	assert.True(t, idx.Unique, "the device_key index must be unique")
}

// findIndex returns the named index of a collection, or nil.
func findIndex(collection *core.Collection, name string) *dbutils.Index {
	for _, raw := range collection.Indexes {
		parsed := dbutils.ParseIndex(raw)
		if parsed.IndexName == name {
			return &parsed
		}
	}
	return nil
}

// TestCreateBarkDevicesCollectionIsIdempotent pins that re-running the
// migration is safe, which PocketBase requires.
func TestCreateBarkDevicesCollectionIsIdempotent(t *testing.T) {
	app := newTestApp(t)

	// The collection already exists from the initial migration run;
	// running it again must not fail.
	require.NoError(t, runCreateBarkDevices(app), "the migration must be idempotent")

	collection, err := app.FindCollectionByNameOrId("bark_devices")
	require.NoError(t, err)
	assert.Equal(t, "bark_devices", collection.Name)
}
