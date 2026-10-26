package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"

	"github.com/incurl/riff/pb/hooks"
)

// The SPA's live-refresh endpoint.
//
// The React client (web/src/hooks/useLiveMemoRefresh.ts) fetches
// `/api/v1/sse` and expects:
//
//   - the response content type to be `text/event-stream`
//   - frames of the form `data: {"type":"memo.changed"}\n\n`
//   - the event TYPE inside the JSON body — the client never reads
//     the SSE `event:` field
//   - a `data: ` line with a SPACE after the colon (the client
//     matches on `line.startsWith("data: ")`)
//
// PocketBase's own `subscriptions.Message.WriteSSE` writes `data:`
// WITHOUT a space (tools/subscriptions/message.go), so the SPA's
// parser would ignore its frames. The writer below therefore emits
// the shape the client actually reads.
//
// The endpoint did not exist: the SPA's request fell through to the
// SPA catch-all, which answered 200 with `index.html`. The client
// accepted that (it only checks `response.ok`), parsed the HTML for
// `data: ` lines, found none, and silently never refreshed anything.
//
// This is the V1 shape. The Memos (V0) shape — `/memos/api/sse` for
// the MoeMemos mobile client — is served by
// pb/api/upstream_memos_rest_sse.go and emits the Memos payload
// instead.

// spaSSEEventMemoChanged and spaSSEEventSpaceChanged are the event
// types the SPA dispatches on.
const (
	spaSSEEventMemoChanged = "memo.changed"
	spaSSEEventSpaceChanged = "space.changed"
)

// spaSSEHeartbeatInterval is the cadence of the comment frames that
// keep the connection alive through proxies that time out idle
// streams. It matches the upstream Memos default.
const spaSSEHeartbeatInterval = 30 * time.Second

// spaSSEHandler returns the handler for `GET /api/v1/sse`.
//
// The caller must be authenticated: the route is mounted behind
// `apis.RequireAuth`, and the handler additionally refuses an
// anonymous caller so the stream is never opened for one.
func spaSSEHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if e.Auth == nil {
			return e.UnauthorizedError("The request requires valid record authorization token.", nil)
		}

		h := e.Response.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		// Tell nginx (and caddy) not to buffer the stream.
		h.Set("X-Accel-Buffering", "no")

		e.Response.WriteHeader(http.StatusOK)
		flusher, _ := e.Response.(http.Flusher)
		_, _ = io.WriteString(e.Response, ": connected\n\n")
		if flusher != nil {
			flusher.Flush()
		}

		client := subscriptions.NewDefaultClient()
		// Record the caller's identity so the broadcaster can
		// evaluate the collection's access rule for THIS
		// subscriber (see pb/hooks/realtime.go).
		client.Set(hooks.RealtimeClientAuthKey, e.Auth)
		for _, collection := range hooks.RealtimeBroadcastCollections() {
			client.Subscribe(collection)
		}
		app.SubscriptionsBroker().Register(client)
		defer app.SubscriptionsBroker().Unregister(client.Id())

		ticker := time.NewTicker(spaSSEHeartbeatInterval)
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
				writeSPAEvent(e.Response, flusher, msg)
			}
		}
	}
}

// writeSPAEvent writes one event in the shape the SPA parses.
//
// The payload is the event TYPE — the client dispatches on
// `event.type` and ignores everything else, so the record id and
// the collection are not part of the contract. A memo or space event
// maps to the corresponding `*.changed` type.
func writeSPAEvent(w io.Writer, flusher http.Flusher, msg subscriptions.Message) {
	eventType := spaEventTypeFor(msg)
	if eventType == "" {
		return
	}
	// `data: ` WITH a space — the client matches on
	// `line.startsWith("data: ")`.
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"%s\"}\n\n", eventType)
	if flusher != nil {
		flusher.Flush()
	}
}

// spaEventTypeFor maps a broadcast message to the SPA's event type.
func spaEventTypeFor(msg subscriptions.Message) string {
	switch msg.Name {
	case hooks.RealtimeEventName:
		// The riff broadcast carries the collection in its payload.
		switch spaBroadcastCollection(msg.Data) {
		case "memos":
			return spaSSEEventMemoChanged
		case "spaces":
			return spaSSEEventSpaceChanged
		}
	}
	return ""
}

// spaBroadcastCollection reads the `collection` field out of a
// broadcast payload.
func spaBroadcastCollection(data []byte) string {
	var payload struct {
		Collection string `json:"collection"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return payload.Collection
}
