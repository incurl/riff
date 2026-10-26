package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedNotification creates a notification owned by a fresh user and
// returns that user.
func seedNotification(t *testing.T, app core.App) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	owner := core.NewRecord(users)
	owner.Set("username", "ruleowner")
	owner.Set("email", "ruleowner@example.test")
	owner.Set("name", "Rule Owner")
	owner.SetPassword("audit-password-123")
	require.NoError(t, app.Save(owner))

	notifications, err := app.FindCollectionByNameOrId("notifications")
	require.NoError(t, err)

	rec := core.NewRecord(notifications)
	rec.Set("user", owner.Id)
	rec.Set("read", false)
	rec.Set("type", "MEMO_COMMENT")
	rec.Set("payload", map[string]any{"memoId": "audit-memo"})
	require.NoError(t, app.Save(rec))

	return owner
}

// TestPbRecordsCRUD_ListRespectsListRule pins that the crud flavour
// evaluates the collection's ListRule.
//
// The `notifications` collection's ListRule is
// "user = @request.auth.id", so a caller must only ever see their
// own notifications. The crud handler calls
// `app.FindRecordsByFilter` directly, which does NOT apply
// collection rules, so the rule has to be evaluated per row.
func TestPbRecordsCRUD_ListRespectsListRule(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	owner := seedNotification(t, app)

	mux := crudTestRouter(t, app, "notifications")

	// An anonymous caller is rejected outright by the auth gate.
	req := httptest.NewRequest(http.MethodGet, "/memos/api/notifications", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"the route is behind RequireAuth: %s", w.Body.String())

	// The owner sees their own notification and nobody else's.
	items := crudListItems(t, mux, "/memos/api/notifications", owner)
	require.Len(t, items, 1, "the owner must see their own notification")
}

// TestPbRecordsCRUD_ListRespectsListRule_OwnerSeesOwn pins the
// positive half: the owner DOES see their own notification.
func TestPbRecordsCRUD_ListRespectsListRule_OwnerSeesOwn(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	owner := seedNotification(t, app)

	mux := crudTestRouter(t, app, "notifications")

	// Build a request that carries the owner's auth, the way the
	// router would after the global loadAuthToken middleware ran.
	req := httptest.NewRequest(http.MethodGet, "/memos/api/notifications", nil)
	req = req.WithContext(contextWithViewer(req.Context(), owner))
	w := httptest.NewRecorder()
	authedMux(t, app, mux).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	raw, _ := body["items"].([]any)
	assert.Len(t, raw, 1, "the owner must see their own notification")
}

// TestPbRecordsCRUD_GetRespectsViewRule pins that a denied record
// answers 404, not 403 — a 403 would confirm the record exists.
func TestPbRecordsCRUD_GetRespectsViewRule(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	owner := seedNotification(t, app)

	notifications, err := app.FindCollectionByNameOrId("notifications")
	require.NoError(t, err)
	rec, err := app.FindFirstRecordByFilter("notifications", "user={:u}", map[string]any{"u": owner.Id})
	require.NoError(t, err)

	mux := crudTestRouter(t, app, "notifications")

	// Anonymous: rejected by the auth gate.
	req := httptest.NewRequest(http.MethodGet, "/memos/api/notifications/"+rec.Id, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"an anonymous caller is rejected: %s", w.Body.String())

	// The owner.
	req = httptest.NewRequest(http.MethodGet, "/memos/api/notifications/"+rec.Id, nil)
	req = req.WithContext(contextWithViewer(req.Context(), owner))
	w = httptest.NewRecorder()
	authedMux(t, app, mux).ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "the owner must see their own notification: %s", w.Body.String())

	_ = notifications
}

// authedMux wraps a mux so the event factory populates e.Auth from
// the context viewer — the role PocketBase's global loadAuthToken
// middleware plays on the real router. The rule engine reads
// e.Auth, so a test that wants an authenticated caller has to set
// it the same way.
func authedMux(t *testing.T, app core.App, inner http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viewer, _ := r.Context().Value(ctxKeyConnectViewer{}).(*core.Record)
		r = r.WithContext(contextWithViewer(r.Context(), viewer))
		inner.ServeHTTP(w, r)
	})
}
