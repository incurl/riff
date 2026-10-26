// collection.go creates the `bark_devices` collection.
//
// The collection is created by a riff migration (see
// pb/migrations/1920000000_create_bark_devices.go) so a riff instance
// gets it through the normal migration path. This file is the fallback
// for a host application that imports this package without riff's
// migrations: ensureDeviceCollection is idempotent and does nothing when
// the collection already exists.
package bark

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// ensureDeviceCollection creates the `bark_devices` collection if it is
// missing.
//
// The collection is server-only: every access rule is nil, so the only
// way to read or write a row is through this package's handlers. That is
// deliberate — the device_key is the credential that authorises a push,
// so it must not be readable through the generic collection API.
func ensureDeviceCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(collectionName); err == nil {
		return nil
	}
	collection := core.NewBaseCollection(collectionName)

	collection.Fields.Add(&core.TextField{
		Name:     "device_key",
		Required: true,
		Max:      64,
		// The key is the bearer credential for a device, so it is
		// never returned by the collection API (the rules are nil
		// anyway) but it is also not something a dashboard should
		// edit by hand.
		Hidden: true,
	})
	collection.Fields.Add(&core.TextField{
		Name:     "device_token",
		Required: false,
		// An APNs device token is a 64 hex character string, but
		// upstream accepts a variable length and rejects only
		// absurdly long ones.
		Max:    200,
		Hidden: true,
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

	// The device key is unique: it is the lookup key for every push.
	collection.AddIndex("idx_bark_devices_key", true, "device_key", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "bark: create %s collection", collectionName)
	}
	return nil
}
