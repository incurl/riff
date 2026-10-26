// device_store.go replaces bark-server's bbolt key/value store with a
// PocketBase collection.
//
// Upstream's database.Database interface is reproduced exactly so the
// ported push pipeline reads the same, but the implementation is backed by
// the `bark_devices` collection instead of a bbolt bucket:
//
//	https://github.com/Finb/bark-server/blob/master/database/database.go
//
// The device_key is the credential: whoever holds it can receive pushes
// for the device, so the collection is server-only (every *Rule is nil)
// and the HTTP handler is the only authority on who may read or write a
// row. See pb/migrations/1920000000_create_bark_devices.go.
package bark

import (
	"database/sql"
	"errors"

	pkgerrors "github.com/pkg/errors"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// DeviceStore is the persistence seam for Bark device registrations.
//
// The method set is upstream bark-server's database.Database verbatim, so
// the upstream semantics are preserved exactly:
//
//   - SaveDeviceTokenByKey with an empty or unknown key registers a NEW
//     device and returns the freshly minted key; with a known key it
//     updates the token in place and returns the same key.
//   - DeviceTokenByKey fails for an unknown key and for a key whose token
//     is empty (an "invalid device" marker).
//   - DeleteDeviceByKey removes the registration.
type DeviceStore interface {
	// CountAll returns the number of registered devices.
	CountAll() (int, error)
	// DeviceTokenByKey returns the APNs device token registered for key.
	DeviceTokenByKey(key string) (string, error)
	// SaveDeviceTokenByKey creates or updates the registration and
	// returns the effective device key (which may be newly generated).
	SaveDeviceTokenByKey(key, token string) (string, error)
	// DeleteDeviceByKey removes the registration for key.
	DeleteDeviceByKey(key string) error
}

// collectionName is the PocketBase collection backing DeviceStore.
const collectionName = "bark_devices"

// pbDeviceStore is the PocketBase-backed DeviceStore.
type pbDeviceStore struct {
	app core.App
}

// newPBDeviceStore returns a DeviceStore backed by the app's database.
func newPBDeviceStore(app core.App) DeviceStore {
	return &pbDeviceStore{app: app}
}

// CountAll returns the number of registered devices.
func (s *pbDeviceStore) CountAll() (int, error) {
	collection, err := s.app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return 0, pkgerrors.Wrapf(err, "bark: find %s collection", collectionName)
	}
	total, err := s.app.CountRecords(collection)
	if err != nil {
		return 0, pkgerrors.Wrapf(err, "bark: count %s", collectionName)
	}
	return int(total), nil
}

// DeviceTokenByKey returns the APNs device token registered for key.
//
// An unknown key and a key with an empty token are both errors, matching
// upstream: an empty token marks a device whose APNs registration was
// rejected, and the row is kept so the key stays reserved.
func (s *pbDeviceStore) DeviceTokenByKey(key string) (string, error) {
	record, err := s.findByKey(key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Upstream's bbolt store reports an unknown key as
			// "failed to get [%s] device token from database".
			return "", pkgerrors.Errorf("bark: failed to get [%s] device token from database", key)
		}
		return "", err
	}
	token := record.GetString("device_token")
	if len(token) == 0 {
		return "", pkgerrors.New("bark: device token invalid")
	}
	return token, nil
}

// SaveDeviceTokenByKey creates or updates the registration for key.
//
// A new key is generated when the caller supplies none, or supplies one
// that is not yet registered. The generated key uses the same alphabet and
// length as upstream's shortuuid keys (22 characters, URL-safe) so a
// client that expects a Bark key shape keeps working.
func (s *pbDeviceStore) SaveDeviceTokenByKey(key, deviceToken string) (string, error) {
	collection, err := s.app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return "", pkgerrors.Wrapf(err, "bark: find %s collection", collectionName)
	}

	existing, err := s.findByKey(key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if existing == nil {
		// Either no key was supplied, or the supplied one is not
		// registered yet: this is a new device registration, and
		// upstream's bbolt store replaces the caller's key with a
		// freshly generated one:
		//
		//	if key == "" || bucket.Get([]byte(key)) == nil {
		//	    key = shortuuid.New()
		//	}
		//
		// Note that this means a client cannot choose its own
		// key: it is always given one.
		key, err = generateDeviceKey()
		if err != nil {
			return "", err
		}
		record := core.NewRecord(collection)
		record.Set("device_key", key)
		record.Set("device_token", deviceToken)
		if err := s.app.Save(record); err != nil {
			return "", pkgerrors.Wrap(err, "bark: save device")
		}
		return key, nil
	}

	// Known key: update the token in place.
	existing.Set("device_token", deviceToken)
	if err := s.app.Save(existing); err != nil {
		return "", pkgerrors.Wrap(err, "bark: update device")
	}
	return existing.GetString("device_key"), nil
}

// DeleteDeviceByKey removes the registration for key.
func (s *pbDeviceStore) DeleteDeviceByKey(key string) error {
	record, err := s.findByKey(key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Upstream's bbolt delete of a missing key is a no-op.
			return nil
		}
		return err
	}
	if err := s.app.Delete(record); err != nil {
		return pkgerrors.Wrap(err, "bark: delete device")
	}
	return nil
}

// findByKey returns the registration record for key, or sql.ErrNoRows
// when there is none.
func (s *pbDeviceStore) findByKey(key string) (*core.Record, error) {
	if key == "" {
		return nil, sql.ErrNoRows
	}
	collection, err := s.app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return nil, pkgerrors.Wrapf(err, "bark: find %s collection", collectionName)
	}
	record, err := s.app.FindFirstRecordByFilter(
		collection,
		"device_key = {:key}",
		dbx.Params{"key": key},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, pkgerrors.Wrap(err, "bark: find device by key")
	}
	return record, nil
}
