package hooks

import (
	"encoding/json"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// RealtimeEventName is the SSE event name we publish for feed
// updates. The frontend subscribes to a single name and dispatches
// on the `collection` field of the payload.
const RealtimeEventName = "po33:new_content"

// realtimeBroadcastCollections is the set of collections whose
// create events are broadcast as a feed update.
var realtimeBroadcastCollections = map[string]struct{}{
	"memos":          {},
	"memo_relations": {},
	"reactions":      {},
}

// registerRealtime binds a model-level AfterCreateSuccess hook per
// collection that broadcasts a po33:new_content event to all
// currently connected SSE clients subscribed to that collection.
//
// We use the model-level After hook (not the HTTP-level
// CreateRequest hook) because we only need the record ID, and the
// After hook fires after the record is committed — which is the
// right moment to tell the world about it.
func registerRealtime(app core.App) {
	for col := range realtimeBroadcastCollections {
		c := col
		app.OnRecordAfterCreateSuccess(c).BindFunc(func(e *core.RecordEvent) error {
			defer func() { _ = e.Next() }()
			broadcastCreate(e.App, c, e.Record.Id)
			return nil
		})
	}
}

func broadcastCreate(app core.App, collection, recordID string) {
	clients := app.SubscriptionsBroker().Clients()
	if len(clients) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"collection": collection,
		"id":         recordID,
		"action":     "create",
	})
	if err != nil {
		slog.Warn("realtime: failed to marshal broadcast payload", "err", err)
		return
	}
	msg := subscriptions.Message{Name: RealtimeEventName, Data: payload}
	for _, client := range clients {
		subs := client.Subscriptions()
		if _, ok := subs[collection]; !ok {
			continue
		}
		select {
		case client.Channel() <- msg:
		default:
		}
	}
}
