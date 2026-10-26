package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createBarkDevicesCollection backs the Bark push extension in pb/bark.
//
// One row per registered Bark device. The row replaces bark-server's
// bbolt bucket, so a device registration survives a restart and lives in
// the same database as the rest of the instance.
//
// Fields:
//
//	device_key   (text, required, max 64, unique) — the push key the
//	              Bark client uses. It is the credential: whoever holds
//	              it can receive the device's notifications, so it is
//	              never exposed through the collection API.
//	device_token (text, optional, max 200) — the APNs device token. It
//	              is optional because an APNs-rejected token is stored
//	              as an empty string, which marks the device as
//	              invalid while keeping its key reserved.
//	created      (autodate, onCreate) — output-only creation timestamp.
//	updated      (autodate, onCreate+onUpdate) — output-only timestamp of
//	              the last token change.
//
// Access rules: server-only on every verb. The device_key is the bearer
// credential for a push, so the only authority on who may read or write
// a row is the Bark handler in pb/bark, which reaches the table through
// the DeviceStore interface and never through the collection REST API.
// All `*Rule` fields are nil, so a superuser is the only actor the
// generic collection API lets in.
func init() {
	migrations.Register(runCreateBarkDevices, func(txApp core.App) error {
		collection, err := txApp.FindCollectionByNameOrId("bark_devices")
		if err != nil {
			return err
		}
		return txApp.Delete(collection)
	})
}

// runCreateBarkDevices creates the bark_devices collection.
//
// It is a named function (rather than an inline closure) so the
// idempotency test can call it directly.
func runCreateBarkDevices(txApp core.App) error {
	// Idempotency: re-running migrations is a supported
	// PocketBase path. Same shape as
	// 1750000000_create_memo_shares.go.
	if existing, err := txApp.FindCollectionByNameOrId("bark_devices"); err == nil && existing != nil {
		return nil
	}

	collection := core.NewBaseCollection("bark_devices")

	collection.Fields.Add(&core.TextField{
		Name:     "device_key",
		Required: true,
		Max:      64,
		Hidden:   true,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "device_token",
		Required: false,
		Max:      200,
		Hidden:   true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created",
		OnCreate: true,
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "updated",
		OnCreate: true,
		OnUpdate: true,
	})

	// Server-only. See the file-level comment.
	collection.ListRule = nil
	collection.ViewRule = nil
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_bark_devices_key", true, "device_key", "")

	return txApp.Save(collection)
}
