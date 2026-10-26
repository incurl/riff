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
			broadcastCreate(e.App, c, e.Record)
			return nil
		})
	}
}

// broadcastCreate sends a create notification to every SSE client
// subscribed to the collection that is ALLOWED to see the record.
//
// The permission check is the point of this function. The previous
// version sent the record id to every subscriber, so a PRIVATE memo's
// id reached anonymous clients — a leak of both the id and the
// existence of the memo.
//
// The check mirrors PocketBase's own realtime implementation
// (apis/realtime.go): the subscription is mapped to the collection's
// ListRule (for a wildcard topic) and evaluated per client with the
// client's OWN auth record, so a client only receives events for the
// records it could read anyway.
func broadcastCreate(app core.App, collection string, record *core.Record) {
	BroadcastCreateForTest(app, collection, record)
}

// BroadcastCreateForTest broadcasts a create notification for the
// record. It is exported so the permission behaviour can be tested
// directly — the broadcast is otherwise only reachable through the
// record hooks.
func BroadcastCreateForTest(app core.App, collection string, record *core.Record) {
	clients := app.SubscriptionsBroker().Clients()
	if len(clients) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"collection": collection,
		"id":         record.Id,
		"action":     "create",
	})
	if err != nil {
		slog.Warn("realtime: failed to marshal broadcast payload", "err", err)
		return
	}
	msg := subscriptions.Message{Name: RealtimeEventName, Data: payload}

	coll, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		// The collection is gone; nothing to broadcast.
		return
	}

	for _, client := range clients {
		subs := client.Subscriptions()
		if _, ok := subs[collection]; !ok {
			continue
		}
		if !clientMaySeeRecord(app, client, coll, record) {
			continue
		}
		select {
		case client.Channel() <- msg:
		default:
		}
	}
}

// clientMaySeeRecord reports whether the client's auth record grants
// it access to the record under the collection's ListRule.
//
// A client with no auth record (an anonymous subscriber) is only
// allowed through a PUBLIC rule — the rule engine decides, not this
// function; the anonymous case is expressed by passing a nil auth
// record to the rule evaluation, exactly as PocketBase's own
// realtime does.
func clientMaySeeRecord(app core.App, client subscriptions.Client, coll *core.Collection, record *core.Record) bool {
	// The client's auth record is stashed on the client store when
	// the SSE handler authenticates it (see
	// pb/api/upstream_memos_rest_sse.go).
	auth, _ := client.Get(RealtimeClientAuthKey).(*core.Record)

	info := &core.RequestInfo{Method: "GET", Auth: auth}
	ok, err := app.CanAccessRecord(record, info, coll.ListRule)
	if err != nil {
		// A malformed rule must not widen access.
		slog.Warn("realtime: access check failed", "collection", coll.Name, "err", err)
		return false
	}
	return ok
}

// RealtimeClientAuthKey is the client-store key under which an SSE
// client records its auth state.
//
// It matches PocketBase's own key (apis/realtime.go:
// `RealtimeClientAuthKey`) so the two implementations agree on the
// store layout.
const RealtimeClientAuthKey = "auth"
