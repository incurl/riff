// Package bark is a self-contained Bark push-notification server for
// PocketBase.
//
// It is a port of finb/bark-server (https://github.com/Finb/bark-server,
// MIT licensed, Copyright (c) 2018 Feng) with two deliberate changes:
//
//   - The bbolt key/value store is replaced by a PocketBase collection
//     (`bark_devices`), so device registrations survive a restart and are
//     stored in the same database as the rest of the instance. See
//     device_store.go.
//
//   - The Fiber router is replaced by PocketBase's own router, so the
//     endpoints are mounted on the instance's HTTP surface instead of a
//     standalone server.
//
// The HTTP surface is upstream's, unchanged, so an existing Bark client
// can point at https://your-instance/bark and keep working:
//
//	POST   /bark/push                                    (V2, JSON)
//	GET    /bark/{device_key}                           (V1 compat)
//	POST   /bark/{device_key}                           (V1 compat)
//	GET    /bark/{device_key}/{body}                    (V1 compat)
//	GET    /bark/{device_key}/{title}/{body}            (V1 compat)
//	GET    /bark/{device_key}/{title}/{subtitle}/{body}  (V1 compat)
//	POST   /bark/register
//	GET    /bark/register                               (V1 compat)
//	GET    /bark/register/{device_key}                  (registration check)
//	GET    /bark/ping
//	GET    /bark/healthz
//	GET    /bark/info
//	GET    /bark/
//	GET    /bark/mcp, /bark/mcp/{device_key}            (MCP `notify` tool)
//
// The package depends only on PocketBase, the APNs client and the
// standard library, so it can be dropped into another PocketBase project
// as-is.
package bark

import (
	"runtime"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// Options configures the Bark server.
//
// The zero value is valid and gives upstream's defaults: the embedded
// APNs key of the official Bark app, one APNs client, and the device
// store backed by the app's database.
type Options struct {
	// Prefix is the path the Bark endpoints are mounted under.
	// Defaults to "/bark".
	Prefix string

	// DeviceStore overrides the device persistence. When nil, a
	// PocketBase-backed store over the `bark_devices` collection is
	// used. Tests use this to inject a fake.
	DeviceStore DeviceStore

	// Pusher overrides the APNs delivery. When nil, the real APNs
	// client is used. Tests use this to inject a fake.
	Pusher Pusher

	// APNsTopic is the bundle ID the pushes are addressed to. It
	// must match the entitlement of the signing key. Defaults to the
	// official Bark app's topic, "me.fin.bark".
	APNsTopic string

	// APNsKeyID is the APNs key identifier. Defaults to the official
	// Bark app's key ID.
	APNsKeyID string

	// APNsTeamID is the APNs team identifier. Defaults to the official
	// Bark app's team ID.
	APNsTeamID string

	// APNsPrivateKey is the PEM-encoded .p8 signing key. When empty,
	// the key embedded in this package is used.
	APNsPrivateKey string

	// APNsMaxClients caps the size of the APNs client pool. Defaults
	// to 1.
	APNsMaxClients int

	// Allow decides whether a push may proceed. It is called with the
	// device key and the client address. Returning false answers 429.
	// When nil, every push is allowed — which is upstream's behaviour.
	Allow func(deviceKey, clientIP string) bool

	// Version is reported by the /bark/info endpoint. Defaults to
	// "dev".
	Version string

	// Commit is reported by the /bark/info endpoint.
	Commit string
}

// Register mounts the Bark endpoints on the given app.
//
// It is the single entry point a host application needs: it ensures the
// device collection exists and binds the routes on the instance's router.
//
// The routes are registered from the OnServe hook, so they are part of
// the same mux PocketBase builds for the rest of the instance.
func Register(app core.App) error {
	return RegisterWithOptions(app, Options{})
}

// RegisterWithOptions mounts the Bark endpoints with the given options.
//
// The device collection is ensured from the OnServe hook, not from here:
// the app is not bootstrapped yet when this function runs, so the
// database is not open and a collection lookup would panic (the app's
// concurrentDB is nil). The OnServe hook runs after the bootstrap and
// after the migrations, which is exactly when the collection must exist.
//
// The APNs client is created lazily on the first push, so a missing or
// wrong APNs key does not stop the server from starting.
func RegisterWithOptions(app core.App, opts Options) error {
	if app == nil {
		return errors.New("bark: nil app")
	}

	prefix := opts.Prefix
	if prefix == "" {
		prefix = "/bark"
	}
	prefix = normalizePrefix(prefix)

	store := opts.DeviceStore
	usesDefaultStore := store == nil
	if usesDefaultStore {
		store = newPBDeviceStore(app)
	}

	pusher := opts.Pusher
	if pusher == nil {
		// The real client is created lazily, on the first push.
		pusher = &lazyPusher{opts: opts}
	}

	svc := &server{
		opts:   opts,
		store:  store,
		pusher: pusher,
		prefix: prefix,
	}

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		if usesDefaultStore {
			// The collection is created by the migration in
			// pb/migrations for a riff instance, but a host
			// application that uses this package standalone (outside
			// riff) has no such migration, so the collection is
			// ensured here as well. It is idempotent.
			if err := ensureDeviceCollection(app); err != nil {
				return err
			}
		}
		svc.mount(e.Router)
		return e.Next()
	})

	return nil
}

// server holds the Bark handlers.
type server struct {
	opts   Options
	store  DeviceStore
	pusher Pusher
	prefix string
}

// normalizePrefix cleans up the mount prefix: it always starts with a
// slash and never ends with one.
func normalizePrefix(prefix string) string {
	for len(prefix) > 0 && prefix[0] == '/' {
		prefix = prefix[1:]
	}
	for len(prefix) > 0 && prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}
	if prefix == "" {
		return ""
	}
	return "/" + prefix
}

// buildInfo returns the payload of the /bark/info endpoint.
func (s *server) buildInfo() map[string]any {
	version := s.opts.Version
	if version == "" {
		version = "dev"
	}
	commit := s.opts.Commit
	if commit == "" {
		commit = "unknown"
	}
	devices := 0
	if count, err := s.store.CountAll(); err == nil {
		devices = count
	}
	return map[string]any{
		"version": version,
		"build":   "",
		"arch":    runtime.GOOS + "/" + runtime.GOARCH,
		"commit":  commit,
		"devices": devices,
	}
}
