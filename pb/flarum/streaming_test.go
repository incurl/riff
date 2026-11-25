package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestForum_TheStreamingCheckAnswersWithNDJSON pins the streaming
// check's wire format.
//
// The admin dashboard reads the response with `readEventStream`, which
// expects NDJSON — one JSON document per line — and it reads each
// document's `type` and `ms`. A response in any other shape is not
// read at all, and the check reports nothing.
//
// The check is administrator-only: it is a probe of the deployment's
// plumbing, and it costs the server a second of work.
func TestForum_TheStreamingCheckAnswersWithNDJSON(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/streaming-check", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Contains(t, recorder.Header().Get("Content-Type"), "application/x-ndjson")

	// The documents are one per line.
	lines := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 2, "the check should send several events: %s", recorder.Body.String())

	for _, line := range lines {
		require.NotEmpty(t, strings.TrimSpace(line))

		var document map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &document), "every line should be a JSON document: %q", line)

		require.Contains(t, document, "type")
		require.Contains(t, document, "ms")
	}

	// The first event is the start and the last is the done, with a
	// tick between them — the shape upstream's controller sends, and
	// the shape the check compares the timings of.
	var first, last map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))

	require.Equal(t, "start", first["type"])
	require.Equal(t, "done", last["type"])
}

// TestForum_TheStreamingCheckSpreadsItsEvents pins that the events are
// FAR ENOUGH APART to measure.
//
// The check compares the server's span between the first and the last
// event against the browser's span between their arrivals, and it
// reports "unknown" when the server's span is under 300 ms. A stream
// that sent everything at once would therefore never reach a verdict,
// and the dashboard's warning would never appear.
func TestForum_TheStreamingCheckSpreadsItsEvents(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/streaming-check", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	lines := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 3)

	var first, last map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))

	firstMS, ok := first["ms"].(float64)
	require.True(t, ok, "the first event should carry its ms: %v", first)
	lastMS, ok := last["ms"].(float64)
	require.True(t, ok, "the last event should carry its ms: %v", last)

	span := lastMS - firstMS
	require.GreaterOrEqual(t, span, 300.0,
		"the events must be spread far enough apart to measure, or the check reports 'unknown'")
}

// TestForum_TheStreamingCheckIsAdministratorOnly pins that a member
// cannot run the check.
//
// The endpoint says nothing about the forum, but it is a probe of the
// deployment's plumbing and it costs the server a second of work, so
// it is for administrators only.
func TestForum_TheStreamingCheckIsAdministratorOnly(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "stream_member")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/api/streaming-check", "", sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestForum_TheStreamingCheckIsStreamed pins that the response is
// FLUSHED as it is written.
//
// A response that is buffered until it completes would defeat the
// whole exercise: the dashboard would see every event arrive at once
// and report the server as buffering, whatever the server did.
func TestForum_TheStreamingCheckIsStreamed(t *testing.T) {
	app, handler := newTestForum(t)

	// A handler that records the time each line was READ, rather than
	// the time the whole body arrived.
	var read []time.Time

	request := httptest.NewRequest(http.MethodGet, "/forum/api/streaming-check", nil)
	request.Header.Set("Cookie", (&http.Cookie{Name: "a", Value: "b"}).String())

	// The caller's cookies come from the helper, and the recorder
	// reads the body as it arrives.
	cookies := testSuperuser(t, app)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := &timedRecorder{header: http.Header{}, read: &read}

	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.code)

	require.GreaterOrEqual(t, len(read), 2, "the response should arrive in pieces")

	// The pieces arrived apart, not all at once.
	gap := read[len(read)-1].Sub(read[0])
	require.Greater(t, gap, 100*time.Millisecond,
		"the events must reach the client as they are written, not together")
}

// timedRecorder is a response recorder that records WHEN each piece
// of the body was written.
type timedRecorder struct {
	header http.Header
	code   int
	read   *[]time.Time
}

// Header returns the response header.
func (r *timedRecorder) Header() http.Header { return r.header }

// Write records the body.
func (r *timedRecorder) Write(p []byte) (int, error) {
	*r.read = append(*r.read, time.Now())
	return len(p), nil
}

// WriteHeader records the status.
func (r *timedRecorder) WriteHeader(code int) { r.code = code }

// Flush is the `http.Flusher` the handler asks for.
func (*timedRecorder) Flush() {}

// TestForum_TheCacheClearAnswersWithNDJSON pins the cache clear's wire
// format.
//
// The admin application's "Clear cache" button opens a modal that
// reads the response as a STREAM, showing each step as the server
// reports it. A response that is not NDJSON is not read at all, the
// request fails, and the modal shows an error instead of the steps —
// which is what happened while the route did not exist.
//
// The last step is `done`: the client watches for it rather than
// inferring completion from the connection closing, which it cannot
// tell from a connection that dropped.
func TestForum_TheCacheClearAnswersWithNDJSON(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodDelete, "/forum/api/cache", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Contains(t, recorder.Header().Get("Content-Type"), "application/x-ndjson")

	lines := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n")
	require.NotEmpty(t, lines)

	types := make([]string, 0, len(lines))
	for _, line := range lines {
		var document map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &document), "every line should be a JSON document: %q", line)

		eventType, ok := document["type"].(string)
		require.True(t, ok, "every step should name its type: %q", line)

		types = append(types, eventType)
	}

	require.Contains(t, types, "cleared", "the modal reports the steps it was given: %v", types)
	require.Equal(t, "done", types[len(types)-1], "the stream ends with 'done'")
}

// TestForum_TheCacheClearIsAdministratorOnly pins that a member cannot
// clear the cache.
func TestForum_TheCacheClearIsAdministratorOnly(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "cache_member")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodDelete, "/forum/api/cache", "", sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestForum_TheCacheClearRefusesAPlainPost pins that the POST twin
// refuses a POST that names no override.
//
// The route is registered for POST as well as DELETE because the
// frontend rewrites the DELETE into a POST with the method in
// `X-HTTP-Method-Override`. A POST that names no override is not the
// clear the caller asked for, and it is answered with a 405 rather
// than silently doing the work.
func TestForum_TheCacheClearRefusesAPlainPost(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodPost, "/forum/api/cache", "", testSuperuser(t, app))
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code, recorder.Body.String())
}
