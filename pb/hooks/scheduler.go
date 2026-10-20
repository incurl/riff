package hooks

import (
	"fmt"
	"log/slog"
	"sync"
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
	state := &schedulerState{shutdown: make(chan struct{})}
	schedulerStatesMu.Lock()
	schedulerShutdowns[app] = state
	schedulerStatesMu.Unlock()
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		stopSchedulerLoops(app)
		return e.Next()
	})
	// The same teardown on `OnBootstrapClear`, which is
	// the hook a test's `ClearBootstrap` cleanup triggers
	// (`OnTerminate` does NOT fire there).
	//
	// The shutdown signal is sent FIRST: the wait blocks
	// until the loop returns, and the loop only returns
	// at its next select — which it reaches only after the
	// shutdown channel is signalled. Waiting without
	// signalling would deadlock the teardown.
	//
	// Both hooks route through `stopSchedulerLoops`,
	// which removes the map entry as well as signalling
	// and waiting. The OnTerminate path used to delete
	// the entry inline and this one did not, so a
	// ClearBootstrap-only teardown — which is what every
	// test cleanup does — leaked one entry per app into a
	// package-level map keyed by the app value. Keyed on
	// an app that never terminates, the leak is small, but
	// it is a map that only ever grows.
	app.OnBootstrapClear().BindFunc(func(e *core.BootstrapEvent) error {
		stopSchedulerLoops(app)
		return e.Next()
	})

	state.workers.Add(1)
	go func() {
		defer state.workers.Done()
		runSchedulerLoop(app, state.shutdown)
	}()
}

// schedulerShutdowns holds the per-app shutdown
// channels AND the per-app wait group.
//
// The map is process-global; multiple
// registerScheduler calls on different apps
// (which the test suite does for isolation) each
// get their own channel and are torn down by
// OnTerminate.
//
// The wait group must be PER APP, not package-level: a
// package-level `sync.WaitGroup` cannot be waited on
// twice concurrently, and two apps tearing down at the
// same time would panic with "sync: WaitGroup is
// reused before previous Wait has returned".
type schedulerState struct {
	shutdown chan struct{}
	workers  sync.WaitGroup
}

var schedulerShutdowns map[core.App]*schedulerState

// schedulerStatesMu guards schedulerShutdowns. The map
// is written from `registerScheduler` (once per app) and
// read from the teardown hooks; two apps booting
// concurrently would otherwise race on the map itself.
var schedulerStatesMu sync.Mutex

func init() {
	schedulerShutdowns = map[core.App]*schedulerState{}
}

// stopSchedulerLoops signals the scheduler loops of
// one app to return, waits for them to unwind, and drops
// the app's map entry.
//
// It is the single teardown path for both
// `OnTerminate` and `OnBootstrapClear`; whichever fires
// first does the work and the second finds nothing to do.
//
// The channels are CLOSED, not sent on. The loop
// parks in a `select` on the shutdown channel and
// the ticker; an unbuffered send is only received
// if the loop happens to be parked at that exact
// moment, and the `default:` branch of a
// non-blocking send silently drops it otherwise. A
// closed channel is seen by every future select, so
// the loop always returns.
//
// The close is guarded per app so a double stop
// (the OnTerminate hook and the OnBootstrapClear
// hook can both fire) does not panic.
//
// Every read and write of `schedulerShutdowns` happens
// under `schedulerStatesMu`. The reads used to be bare:
// `registerScheduler` writes the map while the loop
// goroutine and the teardown hooks read it, and an
// unsynchronised read concurrent with a write is a
// `fatal error: concurrent map read and map write` —
// an unrecoverable crash, not the recoverable data race
// the race detector reports. The mutex is released
// BEFORE the wait, so a teardown never holds it across
// the loop's exit.
func stopSchedulerLoops(app core.App) {
	schedulerStatesMu.Lock()
	state := schedulerShutdowns[app]
	delete(schedulerShutdowns, app)
	schedulerStatesMu.Unlock()
	if state == nil {
		return
	}
	select {
	case <-state.shutdown:
		// Already closed: the receive above took
		// the zero value, so a close would panic.
		// Nothing to do.
	default:
		close(state.shutdown)
	}
	// Wait for the loop to unwind. The shutdown signal
	// makes it return at its next select, but without
	// the wait the app can close its database while a
	// sweep is still running — and a test's data
	// directory can be torn down while the sweep is
	// still writing into it.
	state.workers.Wait()
}

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
