package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPbRecordsCRUD_List covers the happy-path for
// the pb_records_crud flavor: a GET against a
// registered collection returns a Memos-shape
// paginated list. The list is empty (no rows seeded)
// so the body has 0 items.
//
// `users` is the test collection because the GET,
// POST, PATCH, DELETE for users are all
// pb_records_crud (the spaces collection's
// create/update/delete go through the connect
// proxy because the upstream Memos surface
// required sub-collection membership rules that
// riff's SpaceServer Connect handler implements).
func TestPbRecordsCRUD_List(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/users", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body, "items", "list response must include items")
	assert.Contains(t, body, "page", "list response must include page")
	assert.Contains(t, body, "per_page", "list response must include per_page")
	assert.Contains(t, body, "total_items", "list response must include total_items")
}

// TestPbRecordsCRUD_List_Pagination_Default
// covers the list path's default pagination
// values: page=1, per_page=30. The 30 comes from
// the paginationFromQuery default in pb/api/api.go.
func TestPbRecordsCRUD_List_Pagination_Default(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/users", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.EqualValues(t, 1, body["page"])
	assert.EqualValues(t, 30, body["per_page"])
}

// TestPbRecordsCRUD_Get_NotFound covers the
// pb_records_crud flavor on a path whose {name}
// is not a real record id. The handler returns
// 404 with a Memos-shape error envelope.
func TestPbRecordsCRUD_Get_NotFound(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	req := httptest.NewRequest(http.MethodGet, "/memos/api/users/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, float64(5), body["code"], "Memos-shape error envelope uses code 5 for not found")
	assert.NotEmpty(t, body["message"])
}

// TestPbRecordsCRUD_Delete_NotFound covers the
// delete path on a missing record. The handler
// returns 404.
func TestPbRecordsCRUD_Delete_NotFound(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	req := httptest.NewRequest(http.MethodDelete, "/memos/api/users/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestPbRecordsCRUD_RejectsUnknownField is a
// negative-path test: a body that includes a
// non-schema field must not crash. The handler
// is supposed to drop unknown fields so the
// save can proceed. PB's own validation may
// then reject the row if the collection's
// rules require more fields; in that case
// 400 is correct. What we are testing here
// is that the unknown field did NOT cause a
// 500.
func TestPbRecordsCRUD_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	mux := makeTestRouter(t, app)

	body := bytes.NewBufferString(`{"username":"x","definitely_not_a_field":"y"}`)
	req := httptest.NewRequest(http.MethodPost, "/memos/api/users", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.True(t, w.Code < 500,
		"unknown fields must not 500; body=%s", w.Body.String())
}

// _ ensures io.Discard is referenced for the
// test helpers that do not actually read the
// body but want to be explicit about that.
var _ = io.Discard
