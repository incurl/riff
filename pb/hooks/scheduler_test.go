package hooks_test

import (
	"testing"

	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/incurl/riff/pb/migrations"
)

// TestScheduler_RegistersACronJob pins that the device-authorization
// sweep is registered on POCKETBASE'S OWN cron.
//
// The sweep used to be a hand-rolled ticker goroutine with its own
// per-app shutdown channel, per-app wait group and two teardown
// hooks — all of which PocketBase's cron already provides (it is
// started on OnServe and stopped on OnBootstrapClear).
func TestScheduler_RegistersACronJob(t *testing.T) {
	t.Parallel()
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)

	before := app.Cron().Total()
	RegisterAll(app)
	after := app.Cron().Total()

	assert.Greater(t, after, before,
		"the sweep must be registered on PocketBase's cron")
}

// TestScheduler_CronJobIsIdempotent pins that registering the hooks
// twice does not add a second job: PocketBase keys its jobs by id.
func TestScheduler_CronJobIsIdempotent(t *testing.T) {
	t.Parallel()
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)

	RegisterAll(app)
	first := app.Cron().Total()
	RegisterAll(app)
	second := app.Cron().Total()

	assert.Equal(t, first, second,
		"re-registering must replace the job, not duplicate it")
}
