package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// The streaming endpoints.
//
// They answer with NDJSON — one JSON document per line — and they
// FLUSH after every line, so the browser sees each event as it is
// written rather than when the response completes.
//
// That is the whole point of them, and it is what the admin
// application checks: it asks the server for a few events a moment
// apart and SEES whether they arrive apart too. A proxy that buffers
// the response holds them back, and only the browser can tell — the
// server does not know what sits in front of it.
//
// The wire format is upstream's `Flarum\Http\EventStream`: the
// content type is `application/x-ndjson`, and every line carries a
// `type` and the `ms` since the stream began.

// streamingTicks is how many ticks the streaming check sends.
//
// It is upstream's `CheckStreamingController::TICKS`.
const streamingTicks = 4

// streamingTickInterval is how far apart the ticks are.
//
// It is upstream's `CheckStreamingController::TICK_MS`. The events
// have to be far enough apart for the difference to be measurable:
// the check compares the server's span against the browser's, and a
// span too short to tell is reported as "unknown" rather than as a
// verdict.
const streamingTickInterval = 250 * time.Millisecond

// streamingCheckHandler serves GET /api/streaming-check.
//
// It is upstream's `CheckStreamingController`: a few events a moment
// apart, so the admin dashboard can tell whether this server lets a
// streamed response reach the browser as it is written, or holds it
// back until it is complete.
//
// Only an administrator may ask: the endpoint says nothing about the
// forum, but it is a probe of the deployment's plumbing and it costs
// the server a second of work.
func (s *Server) streamingCheckHandler(e *core.RequestEvent) error {
	if _, err := s.requireAdmin(e, "You are not allowed to run this check."); err != nil {
		return fail(e, err)
	}

	stream := newEventStream(e)

	stream.send("start", nil)

	for tick := 1; tick <= streamingTicks; tick++ {
		time.Sleep(streamingTickInterval)

		stream.send("tick", nil)
	}

	stream.send("done", nil)

	return nil
}

// eventStream writes NDJSON events to the response.
//
// Every event is flushed as it is written: a stream that is buffered
// until the response completes would defeat the whole exercise, and
// the check would report "buffered" for a server that streams
// perfectly well.
type eventStream struct {
	e       *core.RequestEvent
	started time.Time
}

// newEventStream prepares the response for a stream of events.
//
// The content type is upstream's, and the response is flushed at once
// so that the headers reach the browser before the first event.
func newEventStream(e *core.RequestEvent) *eventStream {
	stream := &eventStream{e: e, started: time.Now()}

	header := e.Response.Header()
	header.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	// The response is not cacheable: it is a fresh measurement every
	// time, and a cached one would answer the check with the
	// previous run's timings.
	header.Set("Cache-Control", "no-store")

	e.Response.WriteHeader(http.StatusOK)

	stream.flush()

	return stream
}

// send writes one event.
//
// The document is upstream's shape: the `type`, whatever else the
// caller gave it, and the `ms` since the stream began.
func (s *eventStream) send(eventType string, data map[string]any) {
	document := map[string]any{"type": eventType}

	for key, value := range data {
		document[key] = value
	}

	document["ms"] = time.Since(s.started).Milliseconds()

	encoded, err := json.Marshal(document)
	if err != nil {
		return
	}

	// A newline ends the document: NDJSON is one document per line.
	s.e.Response.Write(append(encoded, '\n'))

	s.flush()
}

// flush pushes whatever has been written to the client.
//
// A response that cannot be flushed — because the writer is not an
// `http.Flusher` — is simply not streamed, and the check reports
// what it sees.
func (s *eventStream) flush() {
	if flusher, ok := s.e.Response.(http.Flusher); ok {
		flusher.Flush()
	}
}

// clearCacheHandler serves DELETE /api/cache.
//
// It is upstream's `ClearCacheController`, and it answers with the same
// NDJSON stream: each step is reported as it finishes, because the
// work takes long enough that an admin watching a spinner cannot tell
// a slow clear from a dead request.
//
// What it CLEARS is this port's own. Upstream empties `storage/cache`
// and republishes the assets on disk; this port has neither — the
// frontend is EMBEDDED in the binary (see `pb/flarum/frontend`), and a
// rebuilt bundle is a new binary. So the route reports what is
// actually there, and the steps are the ones the admin application
// already knows how to render.
//
// The route is administrator-only, like upstream's.
func (s *Server) clearCacheHandler(e *core.RequestEvent) error {
	if _, err := s.requireAdmin(e, "You are not allowed to clear the cache."); err != nil {
		return fail(e, err)
	}

	// The POST twin is here for the same `X-HTTP-Method-Override`
	// reason as the other write routes: the frontend rewrites a DELETE
	// into a POST, and PocketBase's router matches on the real method
	// before any handler runs.
	if err := RequireMethod(e, http.MethodDelete); err != nil {
		return fail(e, err)
	}

	stream := newEventStream(e)

	// There is NOTHING to clear, and saying so is the honest answer.
	//
	// Upstream empties `storage/cache` and republishes the assets on
	// disk. This port has neither: the frontend is EMBEDDED in the
	// binary (see `pb/flarum/frontend`), so a rebuilt bundle is a new
	// binary, and the forum reads its settings and permissions per
	// request, so there is no cache to empty.
	//
	// The step says what is true rather than pretending a rebuild
	// happened — the admin application renders the step's own fields,
	// and a step that claims to have rebuilt something that was
	// never rebuilt is a lie in the one place an operator looks to
	// find out what their instance did.
	stream.send("cleared", map[string]any{
		"name":  "forum cache",
		"files": nil,
	})

	// The frontend is embedded: the bundle that is running is the
	// bundle that was compiled into this binary, so there is nothing
	// to rebuild and nothing changed.
	stream.send("rebuilt", map[string]any{
		"state":   "unchanged",
		"bundle":  "embedded",
		"changed": 0,
	})

	stream.send("done", nil)

	return nil
}
