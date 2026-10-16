// Phase 3 hook tests. Uses an in-process HTTP request path so
// multiple requests share the same test app (and therefore the
// same users / data).
package hooks_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/api"
	"github.com/incurl/riff/pb/hooks"
	_ "github.com/incurl/riff/pb/migrations"
)

// testEnv wraps a test app + the pre-bound router mux so tests
// can make multiple in-process HTTP requests sharing state.
type testEnv struct {
	app *tests.TestApp
	mux http.Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)

	RegisterAll(app)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	alice := core.NewRecord(users)
	alice.Set("username", "alice")
	alice.Set("email", "alice@example.com")
	alice.Set("password", "alicepassw")
	require.NoError(t, app.Save(alice))
	bob := core.NewRecord(users)
	bob.Set("username", "bob")
	bob.Set("email", "bob@example.com")
	bob.Set("password", "bobpassw0rd")
	require.NoError(t, app.Save(bob))

	// Build the router and fire OnServe so our custom routes bind.
	var mux http.Handler
	baseRouter, err := apis.NewRouter(app)
	require.NoError(t, err)
	serveEvent := new(core.ServeEvent)
	serveEvent.App = app
	serveEvent.Router = baseRouter
	require.NoError(t, app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		m, err := e.Router.BuildMux()
		if err != nil {
			return err
		}
		mux = m
		return nil
	}))
	return &testEnv{app: app, mux: mux}
}

// RegisterAll wires hooks + routes for tests.
func RegisterAll(app core.App) {
	hooks.Register(app)
	api.Register(app, nil)
}

// do is a small helper that makes a JSON HTTP request and
// returns the response recorder for inspection.
func (e *testEnv) do(t *testing.T, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			bodyReader = strings.NewReader(b)
		case io.Reader:
			bodyReader = b
		default:
			buf, err := json.Marshal(b)
			require.NoErrorf(t, err, "marshal body for %s %s", method, path)
			bodyReader = bytes.NewReader(buf)
		}
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("content-type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// authToken returns a JWT for the given username, bypassing the
// password+MFA flow. PocketBase v0.40+ auto-enrolls new auth
// records in MFA by default, which makes the password endpoint
// return an mfaId instead of a token. For tests, we mint a
// short-lived token directly via Record.NewAuthToken.
func (e *testEnv) authToken(t *testing.T, username, _ string) string {
	t.Helper()
	users, err := e.app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rows, err := e.app.FindRecordsByFilter(users, "username={:u}", "", 1, 0, map[string]any{"u": username})
	require.NoError(t, err)
	require.Len(t, rows, 1, "user %s should exist", username)
	tok, err := rows[0].NewAuthToken()
	require.NoError(t, err)
	return tok
}

func urlEncode(s string) string { return url.QueryEscape(s) }

// --- Tests -----------------------------------------------------------

func TestSpaces_CreateAddsCreatorAsAdmin(t *testing.T) {
	env := newTestEnv(t)
	tok := env.authToken(t, "alice", "alicepassw")

	rec := env.do(t, http.MethodPost, "/api/collections/spaces/records",
		map[string]any{
			"uid":         "po-33-labs",
			"title":       "PO-33 Labs",
			"description": "Community patches",
			"icon":        "🔬",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "create space: %s", rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)

	rec = env.do(t, http.MethodGet,
		"/api/collections/space_members/records?filter="+urlEncode("space='"+created.ID+"'"),
		nil,
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "list members: %s", rec.Body.String())
	var listed struct {
		Items []struct {
			Status string `json:"status"`
			Role   string `json:"role"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed.Items, 1, "exactly one space_members row should exist")
	assert.Equal(t, "ACTIVE", listed.Items[0].Status)
	assert.Equal(t, "ADMIN", listed.Items[0].Role)
}

func TestMemos_CreateAutoAssignsAuthorAndHashtags(t *testing.T) {
	env := newTestEnv(t)
	tok := env.authToken(t, "alice", "alicepassw")

	rec := env.do(t, http.MethodPost, "/api/collections/memos/records",
		map[string]any{
			"content":    "Loving my new PO-33 #sampling #ambient patch — #sampling again",
			"visibility": "PUBLIC",
			"type":       "note",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "create memo: %s", rec.Body.String())
	var body struct {
		Author string   `json:"author"`
		Tags   []string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body.Author, "author must be set by the hook")
	assert.ElementsMatch(t, []string{"sampling", "ambient"}, body.Tags, "tags should be deduplicated and lowercased")
}

func TestMemos_CreateSpaceVisibilityRejectsNonMember(t *testing.T) {
	env := newTestEnv(t)
	aliceTok := env.authToken(t, "alice", "alicepassw")
	bobTok := env.authToken(t, "bob", "bobpassw0rd")

	rec := env.do(t, http.MethodPost, "/api/collections/spaces/records",
		map[string]any{"uid": "x-labs", "title": "X"},
		map[string]string{"Authorization": aliceTok},
	)
	require.Equalf(t, 200, rec.Code, "create space: %s", rec.Body.String())
	var sp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sp))

	rec = env.do(t, http.MethodPost, "/api/collections/memos/records",
		map[string]any{
			"content":    "Hi from bob",
			"visibility": "SPACE",
			"type":       "note",
			"space":      sp.ID,
		},
		map[string]string{"Authorization": bobTok},
	)
	assert.NotEqual(t, 200, rec.Code, "bob should not be allowed to post a SPACE memo in alice's space: %s", rec.Body.String())
}

// memoPayload mirrors the JSON shape of the riff `payload` column
// (proto/gen/store.MemoPayload minus tags). It exists for tests
// only; production code uses hooks.payloadShape internally.
type memoPayload struct {
	Property struct {
		HasLink            bool   `json:"has_link"`
		HasTaskList        bool   `json:"has_task_list"`
		HasCode            bool   `json:"has_code"`
		HasIncompleteTasks bool   `json:"has_incomplete_tasks"`
		Title              string `json:"title"`
	} `json:"property"`
	Location struct {
		Placeholder string  `json:"placeholder"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
	} `json:"location"`
}

type memoPayloadResponse struct {
	Author  string      `json:"author"`
	Tags    []string    `json:"tags"`
	Payload memoPayload `json:"payload"`
}

func TestMemos_CreateMaterialisesPayloadProperty(t *testing.T) {
	env := newTestEnv(t)
	tok := env.authToken(t, "alice", "alicepassw")

	rec := env.do(t, http.MethodPost, "/api/collections/memos/records",
		map[string]any{
			"content":    "# Topper\n\nA [link](https://example.com) and a\n\n- [ ] unfinished task\n",
			"visibility": "PUBLIC",
			"type":       "note",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "create memo: %s", rec.Body.String())

	var body memoPayloadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body.Author, "author must be set by the hook")
	assert.Empty(t, body.Tags, "no hashtags in the content")
	assert.Equal(t, "Topper", body.Payload.Property.Title, "first H1 must be extracted as title")
	assert.True(t, body.Payload.Property.HasLink, "the autolink must set has_link")
	assert.True(t, body.Payload.Property.HasTaskList, "the task list must set has_task_list")
	assert.True(t, body.Payload.Property.HasIncompleteTasks, "the unchecked task must set has_incomplete_tasks")
	assert.False(t, body.Payload.Property.HasCode, "no code fences present")
	// Location is the upstream-shaped empty shell.
	assert.Equal(t, "", body.Payload.Location.Placeholder)
	assert.Equal(t, float64(0), body.Payload.Location.Latitude)
	assert.Equal(t, float64(0), body.Payload.Location.Longitude)
}

func TestMemos_UpdateRematerialisesPayload(t *testing.T) {
	env := newTestEnv(t)
	tok := env.authToken(t, "alice", "alicepassw")

	// Create with no link.
	rec := env.do(t, http.MethodPost, "/api/collections/memos/records",
		map[string]any{
			"content":    "plain text, no links",
			"visibility": "PUBLIC",
			"type":       "note",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "create memo: %s", rec.Body.String())
	var created memoPayloadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.False(t, created.Payload.Property.HasLink, "no link in initial content")
	memoID := created.Author // placeholder, replaced below

	// Re-parse the actual id from the response.
	var raw struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	memoID = raw.ID
	require.NotEmpty(t, memoID)

	// Edit to add a link; payload must be re-materialised.
	rec = env.do(t, http.MethodPatch, "/api/collections/memos/records/"+memoID,
		map[string]any{
			"content": "now with a [link](https://example.com)",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "update memo: %s", rec.Body.String())
	var updated memoPayloadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	assert.True(t, updated.Payload.Property.HasLink, "has_link must be true after the edit")
}

func TestMemos_UpdatePreservesPayloadWhenContentUnchanged(t *testing.T) {
	env := newTestEnv(t)
	tok := env.authToken(t, "alice", "alicepassw")

	rec := env.do(t, http.MethodPost, "/api/collections/memos/records",
		map[string]any{
			"content":    "stay the same",
			"visibility": "PUBLIC",
			"type":       "note",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "create memo: %s", rec.Body.String())
	var raw struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))

	// Update a non-content field; payload must not be touched (and
	// the existing materialised shape must survive a round-trip).
	rec = env.do(t, http.MethodPatch, "/api/collections/memos/records/"+raw.ID,
		map[string]any{
			"difficulty": "beginner",
		},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "update memo: %s", rec.Body.String())
	var after memoPayloadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &after))
	// Even with no content change, the property block exists; the
	// fields are all-false for plain text.
	assert.False(t, after.Payload.Property.HasLink)
	assert.False(t, after.Payload.Property.HasTaskList)
	assert.False(t, after.Payload.Property.HasCode)
	assert.False(t, after.Payload.Property.HasIncompleteTasks)
	assert.Equal(t, "", after.Payload.Property.Title)
}

func TestSpaceMembers_LastAdminInvariant(t *testing.T) {
	env := newTestEnv(t)
	tok := env.authToken(t, "alice", "alicepassw")

	rec := env.do(t, http.MethodPost, "/api/collections/spaces/records",
		map[string]any{"uid": "solo", "title": "Solo"},
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "create space: %s", rec.Body.String())
	var sp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sp))

	rec = env.do(t, http.MethodGet,
		"/api/collections/space_members/records?filter="+urlEncode("space='"+sp.ID+"'"),
		nil,
		map[string]string{"Authorization": tok},
	)
	require.Equalf(t, 200, rec.Code, "list: %s", rec.Body.String())
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed.Items, 1)
	memID := listed.Items[0].ID

	rec = env.do(t, http.MethodDelete,
		"/api/collections/space_members/records/"+memID,
		nil,
		map[string]string{"Authorization": tok},
	)
	assert.NotEqual(t, 200, rec.Code, "deleting the last ADMIN should fail: %s", rec.Body.String())
}

func TestFeed_RespectsVisibility(t *testing.T) {
	env := newTestEnv(t)
	aliceTok := env.authToken(t, "alice", "alicepassw")
	bobTok := env.authToken(t, "bob", "bobpassw0rd")

	createOne := func(body string) string {
		rec := env.do(t, http.MethodPost, "/api/collections/memos/records", body,
			map[string]string{"Authorization": aliceTok})
		require.Equalf(t, 200, rec.Code, "create memo: %s", rec.Body.String())
		var b struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
		require.NotEmpty(t, b.ID)
		return b.ID
	}
	privID := createOne(`{"content":"alice private","visibility":"PRIVATE","type":"note"}`)
	pubID := createOne(`{"content":"alice public","visibility":"PUBLIC","type":"note"}`)

	rec := env.do(t, http.MethodGet, "/api/po33/feed", nil,
		map[string]string{"Authorization": bobTok},
	)
	require.Equalf(t, 200, rec.Code, "feed: %s", rec.Body.String())
	var feed struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &feed))

	ids := []string{}
	for _, it := range feed.Items {
		if id, ok := it["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	assert.Contains(t, ids, pubID, "bob should see alice's PUBLIC memo")
	assert.NotContains(t, ids, privID, "bob should NOT see alice's PRIVATE memo")
}

func TestSpaceInvite_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	aliceTok := env.authToken(t, "alice", "alicepassw")
	bobTok := env.authToken(t, "bob", "bobpassw0rd")

	rec := env.do(t, http.MethodPost, "/api/collections/spaces/records",
		map[string]any{"uid": "invite-test", "title": "Invite Test"},
		map[string]string{"Authorization": aliceTok},
	)
	require.Equalf(t, 200, rec.Code, "create space: %s", rec.Body.String())
	var sp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sp))

	rec = env.do(t, http.MethodPost, "/api/po33/spaces/"+sp.ID+"/invite",
		map[string]any{"username": "alice", "role": "USER"},
		map[string]string{"Authorization": bobTok},
	)
	assert.NotEqual(t, 200, rec.Code, "bob should not be allowed to invite to alice's space")

	rec = env.do(t, http.MethodPost, "/api/po33/spaces/"+sp.ID+"/invite",
		map[string]any{"username": "bob", "role": "USER"},
		map[string]string{"Authorization": aliceTok},
	)
	require.Equalf(t, 200, rec.Code, "invite: %s", rec.Body.String())
}
