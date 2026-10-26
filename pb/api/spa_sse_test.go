package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spaSSETestRouter mounts the SPA's live-refresh route the way the
// real router does — behind the RequireAuth middleware.
func spaSSETestRouter(t *testing.T, app core.App) http.Handler {
	t.Helper()
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	route := r.GET("/api/v1/sse", spaSSEHandler(app))
	route.Bind(apis.RequireAuth())
	mux, err := r.BuildMux()
	require.NoError(t, err)
	return mux
}

// TestSPA_SSE_RequiresAuth pins that an anonymous caller cannot open
// the live-refresh stream.
func TestSPA_SSE_RequiresAuth(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := spaSSETestRouter(t, app)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"an anonymous caller must not open the stream: %s", w.Body.String())
}

// TestSPA_SSE_ContentTypeAndFrameShape pins the wire format the SPA
// client parses.
//
// The client matches on `line.startsWith("data: ")` — WITH a space —
// and reads the event type from the JSON body, not from the SSE
// `event:` field. PocketBase's own `subscriptions.Message.WriteSSE`
// writes `data:` without a space, which the client would ignore.
func TestSPA_SSE_ContentTypeAndFrameShape(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := spaSSETestRouter(t, app)

	user := seedAuditUser(t, app, "spauser")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse", nil)
	req = req.WithContext(contextWithViewer(req.Context(), user))
	w := httptest.NewRecorder()

	// The handler blocks on the stream, so give it a short-lived
	// context: its select returns when the context is done.
	ctx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"),
		"the client checks the content type before trusting the stream")

	body := w.Body.String()
	assert.Contains(t, body, ": connected",
		"the client needs the first frame to consider the stream live")
	// The `: connected` frame is a comment, not an event; the event
	// frames are checked by TestSPA_SSE_EventTypeIsInTheBody.
}

// TestSPA_SSE_EventTypeIsInTheBody pins that the event type travels
// in the JSON body, the way the SPA client reads it.
func TestSPA_SSE_EventTypeIsInTheBody(t *testing.T) {
	t.Parallel()

	// A broadcast payload for a memo.
	payload, err := json.Marshal(map[string]any{
		"collection": "memos",
		"id":         "abc123",
		"action":     "create",
	})
	require.NoError(t, err)

	msg := subscriptions.Message{Name: "po33:new_content", Data: payload}
	assert.Equal(t, spaSSEEventMemoChanged, spaEventTypeFor(msg),
		"a memo broadcast maps to the memo.changed event")

	// A space broadcast.
	spacePayload, err := json.Marshal(map[string]any{
		"collection": "spaces",
		"id":         "sp1",
		"action":     "create",
	})
	require.NoError(t, err)
	msg = subscriptions.Message{Name: "po33:new_content", Data: spacePayload}
	assert.Equal(t, spaSSEEventSpaceChanged, spaEventTypeFor(msg),
		"a space broadcast maps to the space.changed event")

	// The rendered frame.
	var buf strings.Builder
	writeSPAEvent(&buf, nil, msg)
	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "data: "),
		"the frame must start with `data: `: %q", out)
	assert.Contains(t, out, `"type":"space.changed"`,
		"the event type must be in the body: %q", out)
}
