package hooks

import (
	"encoding/json"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/internal/webhook"
)

// webhook_drain.go owns the constants shared by the
// outbox (webhook_outbox.go) and the drain worker
// (below), plus the drain goroutine itself. Keeping
// the constants in one place avoids the "two literals
// drift apart over a refactor" failure mode that
// would otherwise let the outbox emit "pending" rows
// the drain refuses to touch.

const (
	// webhookStatusPending is the initial state of a
	// freshly enqueued delivery. The drain worker
	// picks up rows in this state whose
	// `next_attempt_at` is in the past.
	webhookStatusPending = "pending"

	// webhookStatusSuccess marks a delivery the
	// receiver acknowledged with a 2xx and a
	// {"code": 0} body. The drain worker sets it
	// on the row and stamps `delivered_at`.
	webhookStatusSuccess = "success"

	// webhookStatusFailed marks a delivery the
	// drain gave up on. The worker promotes a row
	// from "pending" to "failed" once it has
	// exhausted webhookDeliveryMaxAttempts
	// retries; the row stays in the table for
	// audit, but the drain never picks it up
	// again.
	webhookStatusFailed = "failed"

	// webhookDeliveryMaxAttempts caps the retry
	// count. 8 retries at the backoff schedule
	// below (1m, 2m, 4m, 8m, 16m, 30m, 30m, 30m)
	// span about 2 hours of wall-clock time, which
	// is the upper bound on a receiver being
	// genuinely down before the operator should
	// look at it.
	webhookDeliveryMaxAttempts = 8

	// webhookDrainTick is the interval between
	// drain sweeps. 30s is a balance between
	// delivery latency (most receivers want
	// sub-minute) and DB load (each sweep is one
	// index range scan). The cap is documented in
	// docs/design/api-abuse-controls.md as the
	// floor for any per-receiver latency claim.
	webhookDrainTick = 30 * time.Second

	// webhookDrainBatchSize is the max number of
	// rows the drain picks up per tick. A small
	// batch keeps the DB lock window narrow and
	// prevents one slow sweep from blocking the
	// next. The worker fan-out within a batch
	// still runs in parallel (see
	// webhookDrainParallelism), so total
	// throughput scales with both numbers.
	webhookDrainBatchSize = 32

	// webhookDrainParallelism is the number of
	// concurrent in-flight HTTP requests per
	// drain sweep. 5 is a balance: too high and
	// a single misconfigured receiver can starve
	// the others; too low and a burst of
	// deliveries backs up faster than the 30s
	// tick can clear it.
	webhookDrainParallelism = 5
)

// nowDateTime returns the current instant as a
// PocketBase DateTime. Pulled out as a package-level
// var so tests can pin the clock without monkey-
// patching time.Now. Used by both the outbox (for
// `next_attempt_at = now` on enqueue) and the drain
// (for backoff scheduling).
var nowDateTime = func() types.DateTime { return types.NowDateTime() }

// webhookDrainLog is the slog logger for the drain
// worker. Tests substitute a discard handler.
var webhookDrainLog = slog.Default()

// registerWebhookDrain starts the background goroutine
// that drains the webhook_deliveries outbox. The
// goroutine exits cleanly when the PocketBase app
// terminates (OnTerminate fires), so a clean shutdown
// does not leave rows stuck in "pending" without an
// explanation in the logs.
//
// At most one drain goroutine runs per app boot; the
// pattern is the same as memo_links.go: register once
// at OnServe, run until OnTerminate, drop the
// reference.
func registerWebhookDrain(app core.App) {
	stopCh := make(chan struct{})
	state := webhookDrainStateFor(app)
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		// Non-blocking close: a double-close from
		// a forced shutdown is benign because
		// select-on-channel-closed returns
		// immediately. We log and move on; the
		// goroutine sees the closed channel at
		// the next tick and exits.
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
		// Wait for the drain loop to unwind. The stop
		// signal makes it return at its next tick
		// boundary, but without the wait the app can
		// close its database while a batch is still
		// being processed — and a test's data directory
		// can be torn down while the batch is still
		// writing into it.
		state.workers.Wait()
		return e.Next()
	})
	// The same wait on `OnBootstrapClear`, which is the
	// hook a test's `ClearBootstrap` cleanup triggers
	// (`OnTerminate` does NOT fire there).
	//
	// The stop signal is sent FIRST: the wait blocks
	// until the loop returns, and the loop only returns
	// at its next tick — which it reaches only after the
	// stop channel is closed. Waiting without closing it
	// would deadlock the teardown.
	app.OnBootstrapClear().BindFunc(func(e *core.BootstrapEvent) error {
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
		state.workers.Wait()
		return e.Next()
	})
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		state.workers.Add(1)
		go func() {
			defer state.workers.Done()
			webhookDrainLoop(app, stopCh)
		}()
		return e.Next()
	})
}

// webhookDrainStates holds the per-app drain state.
//
// The wait group must be PER APP, not package-level: a
// package-level `sync.WaitGroup` cannot be waited on
// twice concurrently, and two apps tearing down at the
// same time would panic with "sync: WaitGroup is
// reused before previous Wait has returned".
type webhookDrainState struct {
	workers sync.WaitGroup
}

// webhookDrainStateStoreKey is the app.Store() key the per-app drain
// state lives under.
const webhookDrainStateStoreKey = "riff.webhook_drain_state"

// webhookDrainStateFor returns the per-app drain state, creating it
// on first use.
//
// It used to be a package-level `map[core.App]*webhookDrainState`
// guarded by its own mutex. The app's own store replaces it: the store
// is already per app and concurrency-safe, so the map and its mutex
// were both redundant — and the map's entry was never deleted, so it
// outlived the app it belonged to.
func webhookDrainStateFor(app core.App) *webhookDrainState {
	raw := app.Store().GetOrSet(webhookDrainStateStoreKey, func() any {
		return &webhookDrainState{}
	})
	state, ok := raw.(*webhookDrainState)
	if !ok {
		// Another component claimed the key. Fall back to a fresh
		// state rather than panicking on the type assertion.
		return &webhookDrainState{}
	}
	return state
}

// webhookDrainLoop is the worker. It sleeps for
// webhookDrainTick, wakes up, processes up to
// webhookDrainBatchSize rows, and repeats. A
// closed stopCh causes the loop to exit at the next
// tick boundary (no mid-sweep cancel — a delivery
// in flight is allowed to complete; a delivery not
// yet started is left in "pending" with
// `next_attempt_at` set to the next tick instant).
func webhookDrainLoop(app core.App, stopCh <-chan struct{}) {
	ticker := time.NewTicker(webhookDrainTick)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			processWebhookDrainBatch(app)
		}
	}
}

// processWebhookDrainBatch picks up to
// webhookDrainBatchSize due rows and dispatches them
// in parallel. The function returns when the entire
// batch has either succeeded, failed terminally, or
// been re-queued for a later sweep. Errors are
// recorded on the row (last_error, next_attempt_at,
// attempts) and logged; a per-row failure never
// aborts the batch.
func processWebhookDrainBatch(app core.App) {
	now := nowDateTime()
	rows, err := app.FindRecordsByFilter("webhook_deliveries",
		"status={:s} && next_attempt_at <= {:now}",
		"next_attempt_at",
		webhookDrainBatchSize, 0,
		map[string]any{"s": webhookStatusPending, "now": now})
	if err != nil {
		webhookDrainLog.Warn("webhook drain: query failed", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	// Bounded parallel dispatcher. A semaphore
	// channel of size webhookDrainParallelism keeps
	// the in-flight count predictable; rows queued
	// past that capacity wait inline. A sync.WaitGroup
	// is the join barrier — the function returns only
	// when every worker goroutine has finished
	// (success, failure-terminal, or re-queued),
	// which prevents a tick boundary from racing
	// with an outstanding POST and leaving the row
	// in an inconsistent state when the next sweep
	// runs.
	sem := make(chan struct{}, webhookDrainParallelism)
	var wg sync.WaitGroup
	for _, row := range rows {
		sem <- struct{}{}
		wg.Add(1)
		go func(r *core.Record) {
			defer wg.Done()
			defer func() { <-sem }()
			processOneDelivery(app, r)
		}(row)
	}
	wg.Wait()
}

// processOneDelivery attempts a single POST against
// the receiver the row points at. On 2xx-with-code-0
// the row is marked "success" with `delivered_at =
// now`. On any other outcome the row is updated with
// the new attempt count, the truncated error string,
// and a backoff-scheduled `next_attempt_at`. Past
// webhookDeliveryMaxAttempts the row is promoted to
// "failed" and never re-tried.
//
// All DB writes go through the OnRecord*Request
// chain (so the migrations' nil *Rule fields apply
// and the row's mutability is consistent with how
// the outbox wrote it).
func processOneDelivery(app core.App, row *core.Record) {
	hook, err := app.FindRecordById("user_webhooks", row.GetString("user_webhook"))
	if err != nil {
		// The webhook was deleted between enqueue
		// and drain; the migration's CascadeDelete
		// should have removed the delivery row too,
		// but a race with a concurrent
		// DeleteUserWebhook is possible. Mark the
		// row failed so the operator can see why
		// it stalled.
		markWebhookDeliveryFailed(app, row, "webhook no longer exists", webhookDeliveryMaxAttempts+1)
		return
	}
	payload := row.GetString("payload")
	request := &webhook.Request{
		URL:           hook.GetString("url"),
		Label:         row.GetString("event_type"),
		SigningSecret: hook.GetString("signing_secret"),
		Payload:       json.RawMessage(payload),
	}
	if err := webhook.Post(request); err != nil {
		scheduleWebhookRetry(app, row, err)
		return
	}
	markWebhookDeliverySuccess(app, row)
}

// markWebhookDeliverySuccess transitions the row to
// "success" and stamps `delivered_at`. `last_error`
// is cleared so a later eyeball of the table
// distinguishes fresh successes from re-runs.
func markWebhookDeliverySuccess(app core.App, row *core.Record) {
	row.Set("status", webhookStatusSuccess)
	row.Set("delivered_at", nowDateTime())
	row.Set("last_error", "")
	if err := app.Save(row); err != nil {
		webhookDrainLog.Warn("webhook drain: save success failed", "id", row.Id, "err", err)
	}
}

// scheduleWebhookRetry increments the attempt count,
// truncates the error, and pushes `next_attempt_at`
// forward by the backoff schedule. Past
// webhookDeliveryMaxAttempts the row is promoted to
// "failed" instead.
func scheduleWebhookRetry(app core.App, row *core.Record, deliveryErr error) {
	attempts := int(row.GetInt("attempts")) + 1
	if attempts >= webhookDeliveryMaxAttempts {
		markWebhookDeliveryFailed(app, row, deliveryErr.Error(), attempts)
		return
	}
	row.Set("attempts", attempts)
	row.Set("status", webhookStatusPending)
	row.Set("last_error", truncateWebhookError(deliveryErr.Error()))
	row.Set("next_attempt_at", nowDateTime().Add(time.Duration(webhookBackoff(attempts))*time.Second))
	if err := app.Save(row); err != nil {
		webhookDrainLog.Warn("webhook drain: save retry failed", "id", row.Id, "err", err)
	}
}

// markWebhookDeliveryFailed is the terminal
// transition. The row is left in place (audit) and
// flagged with the final error message.
func markWebhookDeliveryFailed(app core.App, row *core.Record, msg string, attempts int) {
	row.Set("status", webhookStatusFailed)
	row.Set("attempts", attempts)
	row.Set("last_error", truncateWebhookError(msg))
	row.Set("next_attempt_at", nowDateTime())
	if err := app.Save(row); err != nil {
		webhookDrainLog.Warn("webhook drain: save failed-status failed", "id", row.Id, "err", err)
	}
}

// webhookBackoff returns the delay (in seconds) before
// the Nth retry. The schedule is exponential with a
// 30-minute cap:
//
//	1 → 60s        (1m)
//	2 → 120s       (2m)
//	3 → 240s       (4m)
//	4 → 480s       (8m)
//	5 → 960s       (16m)
//	6 → 1800s      (30m)
//	7 → 1800s      (30m, cap)
//
// The cap matches webhookDeliveryMaxAttempts: every
// row gets 8 attempts spaced as above, totalling
// roughly 2 hours of wall-clock time before the
// "failed" transition.
func webhookBackoff(attempts int) int {
	if attempts < 1 {
		attempts = 1
	}
	const cap = 30 * 60 // 30 minutes in seconds
	base := 60 * int(math.Pow(2, float64(attempts-1)))
	if base > cap {
		return cap
	}
	return base
}

// truncateWebhookError bounds the stored error
// string. The last_error column is max 1000 chars;
// for very long chains we keep the head and drop
// the tail, preserving the most informative prefix
// (network errors, status codes, the first failed
// DNS resolution).
func truncateWebhookError(msg string) string {
	const max = 1000
	if len(msg) <= max {
		return msg
	}
	return msg[:max-1] + "…"
}
