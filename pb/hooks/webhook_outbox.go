package hooks

import (
	"encoding/json"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
)

// webhookOutboxLog is the slog logger for the
// outbox-enqueue path. Pulled out as a package-level
// var so tests can substitute a discard handler.
var webhookOutboxLog = slog.Default()

// registerWebhookOutbox subscribes the
// webhook-delivery outbox to the `memos` collection's
// after-create, after-update, and after-delete
// success hooks. For every memo lifecycle event we
// enqueue one webhook_deliveries row per webhook the
// memo's author has configured. The drain goroutine
// (webhook_drain.go) is the only thing that actually
// posts to receivers; this hook is the writer half of
// the queue.
//
// Failure mode: a panicking enqueue must not break
// the after-save hook chain (a missed webhook is
// recoverable from logs; a crashed memo save is not).
// Every code path in this file is wrapped in a
// recover() and a log line so a transient bug here
// surfaces as a warning rather than a 500.
func registerWebhookOutbox(app core.App) {
	enqueue := func(eventType string) func(*core.RecordEvent) error {
		return func(e *core.RecordEvent) error {
			defer func() { _ = e.Next() }()
			defer recoverOutboxPanic(e.Record.Id, eventType)
			enqueueMemoWebhook(app, e.Record, eventType)
			return nil
		}
	}
	app.OnRecordAfterCreateSuccess("memos").BindFunc(enqueue("memo.create"))
	app.OnRecordAfterUpdateSuccess("memos").BindFunc(enqueue("memo.update"))
	app.OnRecordAfterDeleteSuccess("memos").BindFunc(enqueue("memo.delete"))
}

// enqueueMemoWebhook fans out one webhook_deliveries
// row per webhook owned by the memo's author. The
// function is a no-op when:
//   - the memo's `author` column is empty (a
//     pre-author migration, e.g. seeded data);
//   - the author has no webhooks configured;
//   - the webhook_deliveries collection has not been
//     migrated yet (a fresh instance mid-bootstrap).
//
// In every case the call is silent: missing hooks
// must not become user-visible 500s, so we log a
// warning and move on.
func enqueueMemoWebhook(app core.App, rec *core.Record, eventType string) {
	if rec == nil {
		return
	}
	authorID := rec.GetString("author")
	if authorID == "" {
		// On the after-delete hook the row is gone;
		// rec.GetString still works because the
		// record's in-memory copy carries the
		// pre-delete values. We always read the
		// author from the snapshot, not from a
		// follow-up query.
		return
	}
	hooks, err := app.FindRecordsByFilter("user_webhooks", "user={:uid}", "", 0, 0, map[string]any{"uid": authorID})
	if err != nil {
		webhookOutboxLog.Warn("webhook outbox: list user_webhooks failed",
			"memo", rec.Id, "author", authorID, "err", err)
		return
	}
	if len(hooks) == 0 {
		return
	}
	deliveriesCol, err := app.FindCollectionByNameOrId("webhook_deliveries")
	if err != nil {
		webhookOutboxLog.Warn("webhook outbox: webhook_deliveries collection not found",
			"memo", rec.Id, "err", err)
		return
	}
	payload, err := buildMemoEventPayload(rec, eventType)
	if err != nil {
		webhookOutboxLog.Warn("webhook outbox: payload marshal failed",
			"memo", rec.Id, "event", eventType, "err", err)
		return
	}
	for _, hook := range hooks {
		row := core.NewRecord(deliveriesCol)
		row.Set("user_webhook", hook.Id)
		row.Set("event_type", eventType)
		row.Set("payload", payload)
		row.Set("status", webhookStatusPending)
		row.Set("attempts", 0)
		row.Set("next_attempt_at", nowDateTime())
		if err := app.Save(row); err != nil {
			webhookOutboxLog.Warn("webhook outbox: enqueue failed",
				"memo", rec.Id, "webhook", hook.Id, "err", err)
		}
	}
}

// buildMemoEventPayload returns the JSON body the
// drain goroutine will POST. We deliberately keep the
// shape small and stable — receivers downstream
// (Zapier, n8n, custom Node.js) parse the event type
// and a resource name, then make a follow-up call
// against the riff Connect API for the full memo
// body. This decouples the wire format from the
// internal memo schema: a future migration that adds
// columns to `memos` does not require receiver
// updates.
//
// The shape mirrors the Memos 0.31 webhook
// convention (event + actor + memo), with one
// riff-specific field:
//
//   - `memo.name`     — the resource name the
//     receiver can resolve via
//     GetMemo.
//   - `memo.content`  — the raw content (so a
//     receiver can pre-filter
//     without an extra round
//     trip).
//   - `memo.visibility` — for the receiver to decide
//     whether the event is
//     worth its attention.
func buildMemoEventPayload(rec *core.Record, eventType string) (string, error) {
	body := map[string]any{
		"event": eventType,
		"memo": map[string]any{
			"id":         rec.Id,
			"name":       "memos/" + rec.Id,
			"content":    rec.GetString("content"),
			"visibility": rec.GetString("visibility"),
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// recoverOutboxPanic turns a panic in the outbox
// path into a log line, so a malformed payload (or a
// future migration bug) does not crash the request
// that committed the memo. The `memo` and `event`
// arguments are passed by value to avoid the
// deferred call capturing a pointer to a record that
// may be re-used by the next request.
func recoverOutboxPanic(memoID, eventType string) {
	if r := recover(); r != nil {
		webhookOutboxLog.Error("webhook outbox: panic recovered",
			"memo", memoID, "event", eventType, "panic", r)
	}
}
