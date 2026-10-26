package hooks

import (
	"log/slog"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// schedulerJobID is the id of the periodic sweep job in
// PocketBase's cron. PocketBase keys its jobs by id, so a
// re-registration replaces the previous one.
const schedulerJobID = "riffDeviceAuthorizationSweep"

// registerScheduler schedules the periodic sweep that expires stale
// device authorizations.
//
// The job runs on POCKETBASE'S OWN CRON (`app.Cron()`), which is
// started on OnServe and stopped on OnBootstrapClear
// (core/base.go). That replaces a hand-rolled ticker goroutine with
// its own per-app shutdown channel, per-app wait group and
// `OnTerminate`/`OnBootstrapClear` teardown hooks — all of which
// PocketBase already provides, and all of which were the source of
// the teardown races documented in AGENTS.md.
//
// The expression is every minute, matching the previous ticker's
// cadence.
func registerScheduler(app core.App) {
	app.Cron().MustAdd(schedulerJobID, "* * * * *", func() {
		defer func() {
			if r := recover(); r != nil {
				// The app may have shut down between the
				// tick firing and the sweep running (a long
				// test run that tears the app down while
				// the job is mid-tick). A closed-DB panic
				// here would otherwise crash the host
				// process.
				slog.Warn("scheduler sweep panicked; app likely shutting down",
					"err", r)
			}
		}()
		purgeExpiredAuthorizations(app)
	})
}

// purgeExpiredAuthorizations deletes (or marks
// EXPIRED) every device_authorizations row whose
// expires_at is in the past and whose status is
// still PENDING. The "mark EXPIRED then delete"
// two-step is the same pattern memo_links uses:
// the row stays in the table for a brief window
// so the SPA can render a precise "your
// authorization expired" diagnostic if it polls.
//
// A future hardening pass could also delete
// FAILED and EXPIRED rows older than 30 days to
// keep the table bounded; today the table only
// grows during a pairing session and is reset
// to empty once the device is bound, so growth
// is bounded by the number of pairing attempts.
func purgeExpiredAuthorizations(app core.App) {
	now := time.Now()
	rows, err := app.FindRecordsByFilter("device_authorizations",
		"status = 'PENDING' && expires_at < {:now}", "", 0, 0,
		map[string]any{"now": now})
	if err != nil {
		// The most common cause is the
		// collection not existing yet (a
		// pre-migration run); ignore silently.
		return
	}
	for _, r := range rows {
		// Mark EXPIRED first so the next
		// /pair/finish that happens to race
		// gets a precise error rather than a
		// 404.
		r.Set("status", "EXPIRED")
		_ = app.Save(r)
		// Then delete. The race is
		// benign: if the device just
		// signed and we are about to delete,
		// the device sees a CONSUMED status
		// from the original /pair/finish
		// call; if the row was already
		// deleted, the next /pair/finish
		// 404s.
		_ = app.Delete(r)
	}
}
