package pb_test

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"
)

// newPhase2App builds a fresh PocketBase app in a per-test temp dir,
// bootstraps it, and runs all migrations (system + Phase 1 + Phase 2).
func newPhase2App(t *testing.T) core.App {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_test_env_riff_p2",
	})
	t.Cleanup(func() {
		_ = app.ClearBootstrap()
	})

	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	_, err := app.DB().NewQuery("SELECT 1").Execute()
	require.NoError(t, err, "DB should be reachable after Bootstrap")
	_, err = app.AuxDB().NewQuery("SELECT 1").Execute()
	require.NoError(t, err, "AuxDB should be reachable after Bootstrap")

	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")
	return app
}

// mustCols looks up collections by name and returns them. Fails the
// test immediately if any are missing. Use it to keep test bodies
// focused on behavior, not on collection plumbing.
func mustCols(t *testing.T, app core.App, names ...string) []*core.Collection {
	t.Helper()
	out := make([]*core.Collection, 0, len(names))
	for _, name := range names {
		c, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "collection %q must exist", name)
		out = append(out, c)
	}
	return out
}

// newAuthRecord creates a new user (auth collection record) with the
// given username, email, and password. The password is hashed
// automatically by PocketBase.
func newAuthRecord(t *testing.T, app core.App, users *core.Collection, username, email, password string) *core.Record {
	t.Helper()
	r := core.NewRecord(users)
	r.Set("username", username)
	r.Set("email", email)
	r.Set("password", password)
	require.NoError(t, app.Save(r), "save user %q", username)
	return r
}
