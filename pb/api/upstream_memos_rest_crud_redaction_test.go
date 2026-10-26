package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crudTestRouter mounts a single pb_records_crud route for the
// given collection and returns the built mux.
func crudTestRouter(t *testing.T, app core.App, collection string) http.Handler {
	t.Helper()
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	r.GET("/memos/api/"+collection, pbRecordsCRUDHandler(app, collection))
	r.GET("/memos/api/"+collection+"/{name}", pbRecordsCRUDHandler(app, collection))
	mux, err := r.BuildMux()
	require.NoError(t, err)
	return mux
}

// crudListItems performs a GET against the mounted route and returns
// the decoded "items" array.
func crudListItems(t *testing.T, mux http.Handler, path string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	raw, _ := body["items"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, _ := it.(map[string]any)
		items = append(items, m)
	}
	return items
}

// TestPbRecordsCRUD_RedactsSecrets pins that the pb_records_crud
// flavour never returns a field PocketBase considers unsafe.
//
// The previous implementation projected every collection field
// through rec.Get() and returned the raw values, so an anonymous
// GET returned:
//
//   - `tokenKey` — half of the JWT signing key
//     (core/record_tokens.go: `key := tokenKey + AuthToken.Secret`)
//   - `email`   — even though the record's emailVisibility was false
//   - `token_hash` / `signing_secret` — the Hidden:true secrets
//
// PublicExport (core/record_model.go) is what deletes them.
func TestPbRecordsCRUD_RedactsSecrets(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	owner := core.NewRecord(users)
	owner.Set("username", "secretowner")
	owner.Set("email", "secretowner@example.test")
	owner.Set("name", "Secret Owner")
	owner.SetPassword("audit-password-123")
	require.NoError(t, app.Save(owner))

	// A personal access token: token_hash is Hidden:true.
	pats, err := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, err)
	pat := core.NewRecord(pats)
	pat.Set("user", owner.Id)
	pat.Set("token_hash", "$2a$10$abcdefghijklmnopqrstuv")
	require.NoError(t, app.Save(pat))

	mux := crudTestRouter(t, app, "users")
	items := crudListItems(t, mux, "/memos/api/users")
	require.Len(t, items, 1, "the seeded user must be listed")

	item := items[0]
	assert.NotContains(t, item, "tokenKey", "the JWT signing key must never be exported")
	assert.NotContains(t, item, "password", "the password field must never be exported")
	assert.NotContains(t, item, "password_hash", "the password hash must never be exported")
	assert.NotContains(t, item, "email", "emailVisibility is false, so the email must not be exported")
	assert.Contains(t, item, "username", "the public profile fields must still be exported")

	// The same collection through the token route: the Hidden:true
	// token_hash must not leak either.
	patMux := crudTestRouter(t, app, "personal_access_tokens")
	patItems := crudListItems(t, patMux, "/memos/api/personal_access_tokens")
	require.Len(t, patItems, 1)
	assert.NotContains(t, patItems[0], "token_hash", "a Hidden:true field must never be exported")
}

// TestPbRecordsCRUD_OwnerSeesOwnEmail pins the owner case: the
// record's owner always sees their own email, exactly as
// PocketBase's own record API does.
func TestPbRecordsCRUD_OwnerSeesOwnEmail(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	owner := core.NewRecord(users)
	owner.Set("username", "emailowner")
	owner.Set("email", "emailowner@example.test")
	owner.Set("name", "Email Owner")
	owner.SetPassword("audit-password-123")
	require.NoError(t, app.Save(owner))

	// Re-fetch so the record carries the persisted state, then
	// project it as the owner would see it.
	fetched, err := app.FindRecordById("users", owner.Id)
	require.NoError(t, err)

	out := recordToJSON(fetched, owner)
	assert.Equal(t, "emailowner@example.test", out["email"],
		"the owner must see their own email")

	// A different viewer must not.
	other := core.NewRecord(users)
	other.Set("username", "emailother")
	other.Set("email", "emailother@example.test")
	other.SetPassword("audit-password-123")
	require.NoError(t, app.Save(other))

	out = recordToJSON(fetched, other)
	assert.NotContains(t, out, "email", "another user must not see this email")
}

// TestPbRecordsCRUD_ExpandKeyPresent pins that the Memos clients
// still receive the "expand" key, which PublicExport omits when the
// record has no expansion.
func TestPbRecordsCRUD_ExpandKeyPresent(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "expanduser")
	rec.Set("email", "expanduser@example.test")
	rec.SetPassword("audit-password-123")
	require.NoError(t, app.Save(rec))

	out := recordToJSON(rec, nil)
	assert.Contains(t, out, "expand", "the expand key must always be present")
	assert.Contains(t, out, "collectionId")
	assert.Contains(t, out, "collectionName")
	assert.Contains(t, out, "id")
}
