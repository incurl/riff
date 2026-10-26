package bark

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	// The blank import registers PocketBase's system migrations, which
	// create the _collections / _migrations tables the bootstrap needs.
	// Without it the bootstrap fails with "no such table: _collections".
	_ "github.com/pocketbase/pocketbase/migrations"
)

// newTestApp bootstraps a PocketBase app for the tests.
//
// The Bark HTTP tests use the fake store and never touch the database,
// but the router still needs a real app so the RequestEvent has one.
//
// The app is deliberately NOT given riff's migrations: pb/bark is a
// standalone package, and its own collection is created by
// ensureDeviceCollection (see collection.go), which is what a host
// application outside riff would use. The riff migration is covered by
// pb/migrations' own tests.
//
// Log persistence is disabled the way PocketBase expects in tests: the
// logger flushes into the data directory on a timer that the teardown
// does not join, which makes a t.TempDir() cleanup fail with "directory
// not empty".
func newTestApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_bark_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	app.Settings().Logs.MaxDays = 0
	return app
}
