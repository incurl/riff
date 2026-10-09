package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createWebhookDeliveriesCollection is the outbox
// the drain goroutine in pb/hooks/webhook_drain.go
// picks up. One row per pending (or completed) webhook
// delivery; the drain worker transitions rows from
// `pending` to `success`/`failed` and writes backoff
// metadata so a slow receiver doesn't block a fast
// one.
//
// This lives in a separate file from
// 1770000000_create_user_webhooks_main.go because
// PocketBase's migrations.Register uses
// runtime.Caller(1) to derive the migration's
// filename-based dedup key. Two init() functions
// in one file share the same filename, so the
// second registration would overwrite the first.
// Splitting into two files gives each init() a
// distinct caller, which is the only reason this is
// a separate file.
//
// Fields:
//
//	user_webhook  (relation → user_webhooks, single,
//	               required, CascadeDelete) — the
//	               receiver. Deleting a webhook
//	               cancels every queued delivery.
//	event_type    (text, required, max 64) —
//	               "memo.create", "memo.update",
//	               or "memo.delete". The drain
//	               worker uses this to filter the
//	               backlog when (hypothetically) a
//	               future change adds per-event
//	               routing; today every webhook
//	               receives every event for the
//	               owning user.
//	payload       (json, required) — the full JSON
//	               body that will be POSTed to the
//	               receiver. Stored so the drain
//	               worker can retry without
//	               re-running the source hooks.
//	status        (text, required, max 16) — one
//	               of "pending", "success",
//	               "failed". Index for the drain
//	               worker's "WHERE status = pending
//	               AND next_attempt_at <= now"
//	               scan.
//	attempts      (number, required) — incremented
//	               on every drain attempt. Bound
//	               (drain sets status="failed"
//	               past webhookDeliveryMaxAttempts).
//	last_error    (text, max 1000) — the most
//	               recent failure message; cleared
//	               on success.
//	next_attempt_at (date) — earliest instant the
//	               drain worker may pick the row
//	               up. Set to now on enqueue, then
//	               pushed into the future on every
//	               retry.
//	delivered_at  (date) — set on the transition
//	               to "success". Null while
//	               pending or failed.
//	created       (autodate, onCreate)
//
// Access rules: server-only. The drain goroutine
// runs as the PocketBase superuser (no current
// user), so the rule engine must not interfere;
// nil rules are the only safe choice.
//
// Indexes:
//
//	idx_webhook_deliveries_status_next —
//	  composite over (status, next_attempt_at) so
//	  the drain's "pending and due" scan is a
//	  single index range query, regardless of how
//	  many historical rows accumulate.
func init() {
	migrations.Register(func(txApp core.App) error {
		webhooks, err := txApp.FindCollectionByNameOrId("user_webhooks")
		if err != nil {
			return err
		}
		if existing, err := txApp.FindCollectionByNameOrId("webhook_deliveries"); err == nil && existing != nil {
			return nil
		}

		d := core.NewBaseCollection("webhook_deliveries")

		d.Fields.Add(&core.RelationField{
			Name:          "user_webhook",
			CollectionId:  webhooks.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		d.Fields.Add(&core.TextField{
			Name:     "event_type",
			Required: true,
			Max:      64,
		})
		d.Fields.Add(&core.JSONField{
			Name:     "payload",
			Required: true,
		})
		d.Fields.Add(&core.TextField{
			Name:     "status",
			Required: true,
			Max:      16,
		})
		d.Fields.Add(&core.NumberField{
			Name:    "attempts",
			OnlyInt: true,
			Min:     types.Pointer[float64](0),
		})
		d.Fields.Add(&core.TextField{
			Name: "last_error",
			Max:  1000,
		})
		d.Fields.Add(&core.DateField{
			Name: "next_attempt_at",
		})
		d.Fields.Add(&core.DateField{
			Name: "delivered_at",
		})
		d.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		d.ListRule = nil
		d.ViewRule = nil
		d.CreateRule = nil
		d.UpdateRule = nil
		d.DeleteRule = nil

		d.AddIndex("idx_webhook_deliveries_status_next", false, "status, next_attempt_at", "")
		d.AddIndex("idx_webhook_deliveries_webhook", false, "user_webhook", "")

		return txApp.Save(d)
	}, func(txApp core.App) error {
		d, err := txApp.FindCollectionByNameOrId("webhook_deliveries")
		if err != nil {
			return err
		}
		return txApp.Delete(d)
	})
}
