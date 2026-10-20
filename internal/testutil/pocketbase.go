package testutil

import "github.com/pocketbase/pocketbase/core"

// DisableLogPersistence stops app from queueing log records for the
// data directory, so a test's t.TempDir() teardown cannot race
// PocketBase's own log batch writer.
//
// PocketBase's `initLogger` runs a 3-second ticker that flushes the
// accumulated log batch into `logs.db` (inside the data dir) from a
// `routine.FireAndForget` goroutine. That write is not joined by
// `ClearBootstrap`; the teardown hook only flushes the batches that
// are already queued. A tick that fires in the window between the
// test body returning and `ClearBootstrap` therefore keeps writing
// while the test framework is removing the temp dir. Observed
// symptoms, both intermittent and both landing on whichever test
// happens to be running:
//
//   - `TempDir RemoveAll cleanup: directory not empty`
//   - under `-race`, a DATA RACE between `ClearBootstrap` nil-ing
//     the DB handles and `initLogger`'s `AuxRunInTransaction` →
//     `createTxApp` reading them
//
// Setting `Logs.MaxDays = 0` shuts the writer off at the source:
// `BeforeAddFunc` returns false for every record, so nothing is
// ever queued and `BatchHandler.WriteAll` short-circuits with an
// empty batch before it can call `WriteFunc`. `runLogsWrite` has
// the same guard, so an already-in-flight tick also becomes a
// no-op.
//
// Call this immediately after `app.Bootstrap()` (the settings are
// loaded from the DB during bootstrap, so a mutation made before it
// is discarded). No test in this repo asserts on persisted logs, so
// the only behaviour change is that `logs.db` stays empty.
func DisableLogPersistence(app core.App) {
	if app == nil || app.Settings() == nil {
		return
	}
	app.Settings().Logs.MaxDays = 0
}
