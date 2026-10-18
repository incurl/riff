package hooks

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// schedulerLog is the slog logger for the scheduler.
// Pulled out so tests can substitute a discard
// handler. The default is the package-level slog
// default; pre-refactor this log path was inline.
var schedulerLog = slog.Default()

// registerScheduler starts a 1-minute ticker that
// purges expired in-flight device authorizations.
// The purge is best-effort: rows that fail to
// delete are logged but not retried, because the
// /pair/finish handler also marks an expired row
// EXPIRED the first time it sees one.
//
// The ticker honours app.OnTerminate via a per-app
// shutdown channel, matching the pattern in
// memo_links.go. Tests that bootstrap the app and
// exit without OnTerminate see the goroutine
// leak at the OS level (the process exits before
// the leak matters), but the shutdown channel
// is the only safe way to stop the loop in a
// long-running test or a production server.
func registerScheduler(app core.App) {
	shutdown := make(chan struct{})
	schedulerShutdowns[app] = append(schedulerShutdowns[app], shutdown)
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		for _, ch := range schedulerShutdowns[app] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		delete(schedulerShutdowns, app)
		return e.Next()
	})

	go runSchedulerLoop(app, shutdown)
}

// schedulerShutdowns holds the per-app shutdown
// channels. The map is process-global; multiple
// registerScheduler calls on different apps
// (which the test suite does for isolation) each
// get their own channel and are torn down by
// OnTerminate.
var schedulerShutdowns = map[core.App][]chan struct{}{}

// runSchedulerLoop is the worker body. It ticks
// every minute and runs all registered sweep
// functions in order. The loop returns when
// shutdown is closed.
func runSchedulerLoop(app core.App, shutdown <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-shutdown:
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						// The app may have shut down
						// between the ticker firing
						// and the next sweep call
						// (a long test run that
						// GC's the app while the
						// goroutine is mid-tick).
						// The shutdown channel is
						// the only safe teardown;
						// a closed-DB panic here
						// would otherwise crash
						// the host process.
						schedulerLog.Warn("scheduler sweep panicked; app likely shutting down",
							"err", fmt.Sprintf("%v", r))
					}
				}()
				purgeExpiredAuthorizations(app)
			}()
		}
	}
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
