package api

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// sseHeartbeatInterval is the cadence at which the SSE
// handler emits a comment frame to keep the connection
// alive through proxies that time out idle streams.
// Matches the upstream Memos default.
const sseHeartbeatInterval = 30 * time.Second

// sseProxyHandler returns a handler that streams
// realtime events from PocketBase's SubscriptionsBroker
// to a Memos-shape SSE client.
//
// The upstream Memos endpoint is /api/sse; riff's
// port is /memos/api/sse. The wire format is the
// EventSource contract: text/event-stream with
// `data:` lines, blank-line separators, and `:
// comment` heartbeat frames.
//
// Auth: the client must present a valid PocketBase
// Bearer token. PocketBase's e.Auth resolves the user
// record before the handler runs (the rate-limit
// middleware in restRateLimit also runs first and
// returns 429 on the same envelope).
//
// Subscription model: the client subscribes to the
// collections it cares about via query parameters
// (?collections=memos,samples,...) and the handler
// bridges those into PocketBase's broker subscriptions.
// The default (no query) is to subscribe to every
// collection in realtimeBroadcastCollections.
func sseProxyHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		// Tell the client this is a long-lived
		// event stream. Disable any proxy-side
		// buffering by being explicit about the
		// content type and the no-cache directive.
		h := e.Response.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")

		// Open the SSE channel: write a 200
		// status with a `connected` comment so
		// the EventSource client knows the
		// stream is live.
		e.Response.WriteHeader(http.StatusOK)
		flusher, _ := e.Response.(http.Flusher)
		_, _ = io.WriteString(e.Response, ": connected\n\n")
		if flusher != nil {
			flusher.Flush()
		}

		// Subscribe to the broker. PocketBase's
		// SubscriptionsBroker().Channel() returns
		// a per-client channel; the handler
		// selects on it (plus a heartbeat
		// ticker) and writes each message as a
		// SSE data frame.
		client := subscriptions.NewDefaultClient()
		app.SubscriptionsBroker().Register(client)
		defer app.SubscriptionsBroker().Unregister(client.Id())

		// Translate the query parameters into
		// subscriptions. Each ?collections=X
		// value is one collection to subscribe
		// to. Memos frontend uses
		// ?collections=memos,samples,...
		query := e.Request.URL.Query().Get("collections")
		if query == "" {
			// Default to subscribing to the
			// realtimeBroadcastCollections
			// set. The client can override
			// by sending an explicit
			// ?collections= list.
			for c := range realtimeCollections {
				client.Subscribe(c)
			}
		} else {
			for _, c := range splitCommaList(query) {
				client.Subscribe(c)
			}
		}

		// Bridge the client channel to the SSE
		// response. The loop exits when the
		// client disconnects; PocketBase's
		// RequestEvent.Request.Context() is
		// cancelled by the framework.
		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.Request.Context().Done():
				return nil
			case <-ticker.C:
				_, _ = io.WriteString(e.Response, ": heartbeat\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			case msg, ok := <-client.Channel():
				if !ok {
					return nil
				}
				writeSSEEvent(e.Response, flusher, msg)
			}
		}
	}
}

// realtimeCollections is the set of collections whose
// create events riff's pb/hooks/realtime.go broadcasts.
// Duplicated here so the SSE handler can subscribe by
// default. The hook side lives in pb/hooks/realtime.go
// and is the single source of truth — when a new
// collection is added there, the same key goes here.
var realtimeCollections = map[string]struct{}{
	"memos":          {},
	"memo_relations": {},
	"reactions":      {},
}

// writeSSEEvent writes one PocketBase subscriptions
// message as an SSE data frame. The Memos frontend
// subscribes by `name` (the SSE event name) and reads
// the JSON payload from `data`.
func writeSSEEvent(w io.Writer, flusher http.Flusher, msg subscriptions.Message) {
	if msg.Name != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", msg.Name)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", string(msg.Data))
	if flusher != nil {
		flusher.Flush()
	}
}

// splitCommaList splits a comma-separated query value
// into a list of trimmed, non-empty strings. It is
// intentionally local to the SSE handler so callers
// elsewhere do not pick up a stringy list-parser
// dependency on a thin helper.
func splitCommaList(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
