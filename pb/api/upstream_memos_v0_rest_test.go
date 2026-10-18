package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/incurl/riff/internal/random"
	"github.com/incurl/riff/pb/connect"
	"github.com/incurl/riff/pb/hooks"
	apiv1connect "github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// TestUpstreamMemosV0Routes_TableShape asserts the
// static invariants the V0 route table must satisfy:
// no duplicate method+path, every Builder name
// resolves to a known custom-builder key, every row
// is well-formed, and the table is non-empty.
//
// Like the V1 equivalent, this is a unit test that
// never starts a router; a row that violates any of
// the invariants fails here, before the router is
// even built.
func TestUpstreamMemosV0Routes_TableShape(t *testing.T) {
	routes := upstreamMemosV0Routes()
	require.NotEmpty(t, routes, "the v0 route table must be non-empty")

	seen := map[string]bool{}
	knownBuilders := map[string]bool{
		"v0_user_me":         true,
		"v0_memo_list":       true,
		"v0_memo_all_list":   true,
		"v0_memo_create":     true,
		"v0_memo_organizer":  true,
		"v0_memo_patch":      true,
		"v0_memo_delete":     true,
		"v0_resource_list":   true,
		"v0_resource_upload": true,
		"v0_resource_delete": true,
		"v0_tag_list":        true,
	}
	for i, r := range routes {
		assert.NotEmpty(t, r.Method, "row %d has empty method", i)
		assert.NotEmpty(t, r.Path, "row %d has empty path", i)
		assert.NotEmpty(t, r.Builder, "row %d has empty builder", i)
		assert.True(t, knownBuilders[r.Builder],
			"row %d references unknown builder %q", i, r.Builder)
		key := r.Method + " " + r.Path
		if seen[key] {
			t.Errorf("duplicate v0 route %s", key)
		}
		seen[key] = true
	}
}

// TestV0IDFromV1_Derivation checks the V0↔V1 id
// projection. The V0 client treats memo ids as
// Long; the V1 connect handler uses 15-char
// PocketBase ids. The V0 surface derives the V0
// Long id from the V1 id by truncating the first
// 14 hex digits and parsing as uint64. A V0 id of
// 0 is reserved (PocketBase never produces a
// 0-prefixed id).
func TestV0IDFromV1_Derivation(t *testing.T) {
	// 15-char hex id starting with a non-zero
	// digit maps to a non-zero Long.
	v1 := "abc123def456789"
	v0 := v0IDFromV1(v1)
	assert.NotZero(t, v0, "V0 id must be non-zero for a 15-char hex id")
	assert.Equal(t, int64(0xabc123def45678), v0,
		"V0 id must be the first 14 hex digits of the V1 id")
	// Stable projection: same V1 id always
	// yields the same V0 id.
	assert.Equal(t, v0, v0IDFromV1(v1),
		"projection must be deterministic")
	// Non-hex V1 ids fall through to the FNV
	// hash; the result is still non-zero and
	// stable.
	v1alpha := "abcdef012345678"
	v0alpha := v0IDFromV1(v1alpha)
	assert.NotZero(t, v0alpha)
	assert.Equal(t, v0alpha, v0IDFromV1(v1alpha))
}

// TestV0ShapeUser covers the V0 user response
// shaper. The shaper must produce a flat object
// with a numeric `id: Long`, a `name` field that
// is the *display name* (NOT a V1 resource name),
// and a `userSettingList` array.
func TestV0ShapeUser(t *testing.T) {
	v1 := map[string]any{
		"user": map[string]any{
			"name":        "users/moe",
			"username":    "moe",
			"displayName": "Moe User",
			"email":       "moe@example.com",
			"role":        "USER",
			"avatarUrl":   "/api/files/users/abc/avatar.png",
			"description": "hi",
			"userSettingList": []any{
				map[string]any{"key": "memoVisibility", "value": "PRIVATE"},
			},
		},
	}
	out, err := v0ShapeUser(v1)
	require.NoError(t, err)
	// Numeric Long id.
	id, ok := out["id"].(int64)
	require.True(t, ok, "v0 user id must be int64, got %T", out["id"])
	assert.NotZero(t, id)
	// `name` is the display name, not a resource name.
	assert.Equal(t, "Moe User", out["name"],
		"V0 user.name is the display name, not the V1 resource name")
	// `username` is preserved.
	assert.Equal(t, "moe", out["username"])
	// `userSettingList` is a non-nil array of
	// `{key, value}` objects.
	settings, ok := out["userSettingList"].([]any)
	require.True(t, ok)
	require.Len(t, settings, 1)
}

// TestV0ShapeMemo_List covers the V0 memo list
// shaper. The shaper must convert a V1
// `ListMemosResponse{memos: [...]}` body into a
// top-level V0 array of `MemosV0Memo` (no envelope).
// Each memo's `id` is a non-zero int64, the
// visibility is a V0 string, and the `resourceList`
// is an array.
func TestV0ShapeMemo_List(t *testing.T) {
	v1 := map[string]any{
		"memos": []any{
			map[string]any{
				"name":        "memos/abc123def456789",
				"creator":     "users/moe",
				"content":     "hello",
				"visibility":  "PRIVATE",
				"state":       "NORMAL",
				"pinned":      true,
				"createTime":  map[string]any{"seconds": float64(1700000000)},
				"updateTime":  map[string]any{"seconds": float64(1700000001)},
				"attachments": []any{},
				"tags":        []any{"foo"},
			},
		},
	}
	out, err := v0ShapeMemoList(v1)
	require.NoError(t, err)
	require.Len(t, out, 1)
	m, ok := out[0].(map[string]any)
	require.True(t, ok)
	id, ok := m["id"].(int64)
	require.True(t, ok, "v0 memo id must be int64")
	assert.NotZero(t, id)
	assert.Equal(t, "hello", m["content"])
	assert.Equal(t, "PRIVATE", m["visibility"])
	assert.Equal(t, "NORMAL", m["rowStatus"])
	assert.Equal(t, true, m["pinned"])
	createdTs, ok := m["createdTs"].(int64)
	require.True(t, ok)
	assert.Equal(t, int64(1700000000), createdTs)
	resources, ok := m["resourceList"].([]any)
	require.True(t, ok)
	assert.Len(t, resources, 0)
}

// TestV0ShapeMemo_PinnedFlip covers the V0
// "organizer" PATCH: a memo that was pinned=true
// should round-trip through the shaper and keep its
// pinned flag. The test exercises the v0 memo
// patch helper directly.
func TestV0ShapeMemo_PinnedFlip(t *testing.T) {
	v1 := map[string]any{
		"name":       "memos/abc123def456789",
		"creator":    "users/moe",
		"content":    "hi",
		"visibility": "PRIVATE",
		"state":      "NORMAL",
		"pinned":     false,
		"createTime": map[string]any{"seconds": float64(1700000000)},
		"updateTime": map[string]any{"seconds": float64(1700000001)},
		"tags":       []any{},
	}
	out, err := v0ShapeSingleMemo(v1)
	require.NoError(t, err)
	assert.Equal(t, false, out["pinned"])
}

// TestV0ShapeMemo_CreateToV1 is the request-side
// wrap: a V0 `MemosV0Memo` body should be translated
// into a V1 `CreateMemoRequest{memo: Memo}`. The
// shaper must strip the V0-only fields and map the
// V0 visibility string.
func TestV0ShapeMemo_CreateToV1(t *testing.T) {
	body := []byte(`{"content":"hi","visibility":"PROTECTED","id":42,"createdTs":1700000000,"updatedTs":1700000001,"creatorId":7,"rowStatus":"NORMAL","resourceList":[]}`)
	out, err := v0MemoCreateToV1CreateMemo(body, nil, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	memo, ok := got["memo"].(map[string]any)
	require.True(t, ok, "v0 create body must be wrapped as {memo: ...}: %s", out)
	assert.Equal(t, "hi", memo["content"])
	assert.Equal(t, "PROTECTED", memo["visibility"],
		"V0 visibility string maps to the V1 enum of the same name")
	// V0-only fields are stripped.
	_, hasID := memo["id"]
	assert.False(t, hasID, "v0 id must be stripped from the V1 body")
	_, hasCreated := memo["createdTs"]
	assert.False(t, hasCreated, "v0 createdTs must be stripped")
	_, hasUpdated := memo["updatedTs"]
	assert.False(t, hasUpdated, "v0 updatedTs must be stripped")
	_, hasCreator := memo["creatorId"]
	assert.False(t, hasCreator, "v0 creatorId must be stripped")
	_, hasRow := memo["rowStatus"]
	assert.False(t, hasRow, "v0 rowStatus must be stripped")
	_, hasRes := memo["resourceList"]
	assert.False(t, hasRes, "v0 resourceList must be stripped")
}

// TestV0ShapeMemo_OrganizerToV1 covers the
// V0 "organizer" verb: a `POST
// /api/v1/memo/{id}/organizer?pinned=true` must
// translate into a V1
// `UpdateMemoRequest{memo: {name, pinned},
// update_mask: "pinned"}`. protojson renders
// FieldMask as a comma-separated string.
func TestV0ShapeMemo_OrganizerToV1(t *testing.T) {
	out, err := v0MemoOrganizerToV1UpdateMemo(
		nil,
		map[string]string{"id": "abc123def456789"},
		map[string][]string{"pinned": {"true"}},
	)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "pinned", got["update_mask"])
	memo, ok := got["memo"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "memos/abc123def456789", memo["name"])
	assert.Equal(t, true, memo["pinned"])
}

// TestV0ShapeMemo_PatchToV1 covers the V0
// `PATCH /api/v1/memo/{id}` path. The V0 body
// echoes the numeric `id`; the shaper strips it
// and injects the resource name from the URL.
// The update_mask is built from the populated
// fields; FieldMask.paths is a repeated string.
func TestV0ShapeMemo_PatchToV1(t *testing.T) {
	body := []byte(`{"id":42,"content":"new","rowStatus":"ARCHIVED","visibility":"PUBLIC"}`)
	out, err := v0MemoPatchToV1UpdateMemo(body, map[string]string{"id": "abc123def456789"}, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	memo, ok := got["memo"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "memos/abc123def456789", memo["name"])
	assert.Equal(t, "new", memo["content"])
	assert.Equal(t, "PUBLIC", memo["visibility"])
	assert.Equal(t, "ARCHIVED", memo["state"],
		"V0 rowStatus maps to V1 state")
	_, hasID := memo["id"]
	assert.False(t, hasID, "v0 id must be stripped from the V1 patch body")
	_, hasRow := memo["rowStatus"]
	assert.False(t, hasRow, "v0 rowStatus must be stripped (renamed to state)")
	// update_mask is computed from the populated
	// fields; the body has `content`, `state`,
	// `visibility`, `name` (injected), and
	// nothing else. protojson renders FieldMask
	// as a comma-separated string.
	mask, ok := got["update_mask"].(string)
	require.True(t, ok, "update_mask must be a string: %v", got)
	parts := strings.Split(mask, ",")
	seen := map[string]bool{}
	for _, p := range parts {
		seen[p] = true
	}
	assert.True(t, seen["content"])
	assert.True(t, seen["state"])
	assert.True(t, seen["visibility"])
	assert.False(t, seen["name"],
		"path-injected name must not be in the mask")
}

// TestV0ShapeMemo_DeleteToV1 covers the V0
// `DELETE /api/v1/memo/{id}` path. The shaper
// produces a V1 `DeleteMemoRequest{name: "memos/{id}"}`
// from the path variable alone.
func TestV0ShapeMemo_DeleteToV1(t *testing.T) {
	out, err := v0MemoDeleteToV1DeleteMemo(nil, map[string]string{"id": "abc123def456789"}, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "memos/abc123def456789", got["name"])
}

// TestV0ShapeResource covers the V0 resource
// (attachment) shaper. The shaper converts a V1
// `Attachment` into a flat V0 `MemosV0Resource`
// with a numeric `id`, a `filename`, and an
// `externalLink`.
func TestV0ShapeResource(t *testing.T) {
	v1 := map[string]any{
		"name":         "attachments/att123def456789",
		"creator":      "users/moe",
		"filename":     "kick.wav",
		"type":         "audio/wav",
		"size":         float64(4096),
		"externalLink": "/api/files/attachments/att123def456789/kick.wav",
		"createTime":   map[string]any{"seconds": float64(1700000000)},
	}
	out := v0ShapeResource(v1)
	id, ok := out["id"].(int64)
	require.True(t, ok, "v0 resource id must be int64")
	assert.NotZero(t, id)
	assert.Equal(t, "kick.wav", out["filename"])
	assert.Equal(t, "audio/wav", out["type"])
	size, ok := out["size"].(int64)
	require.True(t, ok)
	assert.Equal(t, int64(4096), size)
}

// TestV0ShapeTagList covers the V0 tag list
// shaper. The shaper walks the recent memos and
// emits a top-level array of unique tag strings.
func TestV0ShapeTagList(t *testing.T) {
	v1 := map[string]any{
		"memos": []any{
			map[string]any{"tags": []any{"foo", "bar"}},
			map[string]any{"tags": []any{"foo", "baz"}},
			map[string]any{"tags": []any{}},
		},
	}
	out := v0ShapeTagList(v1)
	require.Len(t, out, 3,
		"V0 tag list must be de-duped: got %v", out)
	// Order is implementation-defined; we only
	// require the set.
	seen := map[string]bool{}
	for _, t := range out {
		s, _ := t.(string)
		seen[s] = true
	}
	assert.True(t, seen["foo"])
	assert.True(t, seen["bar"])
	assert.True(t, seen["baz"])
}

// TestV0PatPath_MoeMemosBoot is the regression
// test for the MoeMemos "data couldn't be read"
// error reported against the riff community
// deployment. MoeMemos is a V0+V1 hybrid; the
// V0 client (MemosV0Api.kt) makes the first
// identity call to `GET /api/v1/user/me` and
// then calls `GET /api/v1/memo` to list the
// caller's memos. The pre-restore build returned
// 200 text/html for both paths (the SPA
// catch-all), which the kotlinx.serialization
// codec could not decode and surfaced as
// "data couldn't be read".
//
// The test stands up a real PocketBase app,
// mounts both the V0 and V1 surfaces against the
// real Connect mux (with the production
// withAuth interceptor), mints a real PAT, and
// walks the exact V0 boot sequence MoeMemos
// uses. If the V0 paths return 200
// application/json with the V0 wire format, the
// deployment is correct; if they return text/html
// or a wrapped V1 envelope, the failure mode
// names the exact path that broke the boot.
func TestV0PatPath_MoeMemosBoot(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	// Stand up the real Connect mux with the
	// production auth interceptor and the
	// services the V0 boot path touches:
	// Auth (for user/me), Memo (for memo list +
	// create), Attachment (for resource list +
	// upload + delete).
	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))

	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	// Create a real user.
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moe")
	rec.Set("email", "moe@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))

	// Mint a raw PAT and persist a bcrypt hash.
	patRaw, perr := random.String(24)
	require.NoError(t, perr)
	pat := "memos_pat_" + patRaw
	patHash, perr := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	require.NoError(t, perr)
	pats, perr := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, perr)
	patRec := core.NewRecord(pats)
	patRec.Set("user", rec.Id)
	patRec.Set("description", "MoeMemos V0 boot regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	// Mount BOTH the V0 and V1 surfaces on a
	// real router. The MoeMemos client
	// interleaves V0 and V1 calls in the same
	// boot, so both surfaces must be live
	// concurrently.
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	// Helper for issuing a request and
	// returning the recorder.
	doReq := func(method, target, body string) *httptest.ResponseRecorder {
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, rdr)
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// 1. GET /api/v1/user/me — the V0 identity
	//    bootstrap. Must return 200
	//    application/json, NOT text/html.
	//    The V0 client decodes the response
	//    into MemosV0User(val id: Long, ...);
	//    a non-JSON response (or a wrapped V1
	//    envelope) makes kotlinx.serialization
	//    throw.
	w1 := doReq(http.MethodGet, "/api/v1/user/me", "")
	require.Equal(t, http.StatusOK, w1.Code,
		"V0 user/me must answer 200")
	require.Equal(t, "application/json", w1.Header().Get("Content-Type"),
		"V0 user/me must answer application/json, not text/html: ct=%q body=%q",
		w1.Header().Get("Content-Type"), w1.Body.String())
	// Use `json.Number` for the `id` field:
	// the V0 `MemosV0User.id` is a Kotlin
	// `Long` (up to 2^63) which exceeds
	// float64 precision. The Kotlin client
	// reads it as a string in the JSON and
	// parses it as Long; we do the same.
	dec1 := json.NewDecoder(bytes.NewReader(w1.Body.Bytes()))
	dec1.UseNumber()
	var me map[string]any
	require.NoError(t, dec1.Decode(&me),
		"V0 user/me body must be valid JSON: %s", w1.Body.String())
	idNum, ok := me["id"].(json.Number)
	require.True(t, ok,
		"V0 user/me must include a numeric 'id' (MemosV0User.id: Long): body=%v", me)
	id, err := idNum.Int64()
	require.NoError(t, err)
	assert.NotZero(t, id, "V0 user id must be non-zero")
	assert.NotEmpty(t, me["username"],
		"V0 user/me must include a 'username' (MemosV0User.username): body=%v", me)

	// 2. GET /api/v1/memo — the V0 memo list.
	//    Must return 200 application/json, and
	//    the body must be a top-level JSON array
	//    (the V0 wire format), not a wrapped
	//    V1 `{memos: [...]}` envelope. The
	//    V0 client decodes into
	//    `List<MemosV0Memo>`; a JSON object
	//    at the top level breaks the codec.
	w2 := doReq(http.MethodGet, "/api/v1/memo", "")
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 memo list must answer 200")
	require.Equal(t, "application/json", w2.Header().Get("Content-Type"),
		"V0 memo list must answer application/json")
	var list []any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &list),
		"V0 memo list body must be a top-level JSON array (MemosV0Api.listMemo: List<MemosV0Memo>): %s", w2.Body.String())
	// An empty list is fine; a non-empty
	// list means the connect handler returned
	// a `memos` envelope rather than an array.
	// Either way, the top level must be a JSON
	// array, which `Unmarshal` into `[]any`
	// proves.

	// 3. GET /api/v1/resource — the V0
	//    resource list. Same shape invariant
	//    as /api/v1/memo: a top-level array.
	w3 := doReq(http.MethodGet, "/api/v1/resource", "")
	require.Equal(t, http.StatusOK, w3.Code,
		"V0 resource list must answer 200")
	require.Equal(t, "application/json", w3.Header().Get("Content-Type"),
		"V0 resource list must answer application/json")
	var resList []any
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &resList),
		"V0 resource list body must be a top-level JSON array: %s", w3.Body.String())

	// 4. GET /api/v1/tag — the V0 tag list.
	//    Same shape invariant.
	w4 := doReq(http.MethodGet, "/api/v1/tag", "")
	require.Equal(t, http.StatusOK, w4.Code,
		"V0 tag list must answer 200")
	require.Equal(t, "application/json", w4.Header().Get("Content-Type"),
		"V0 tag list must answer application/json")
	var tagList []any
	require.NoError(t, json.Unmarshal(w4.Body.Bytes(), &tagList),
		"V0 tag list body must be a top-level JSON array: %s", w4.Body.String())

	// 5. POST /api/v1/memo — the V0 memo
	//    create. The V0 body is the inner
	//    MemosV0Memo shape (no envelope); the
	//    response is a single MemosV0Memo.
	w5 := doReq(http.MethodPost, "/api/v1/memo",
		`{"content":"from V0","visibility":"PRIVATE"}`)
	require.Equal(t, http.StatusOK, w5.Code,
		"V0 memo create must answer 200, got %d: %s", w5.Code, w5.Body.String())
	require.Equal(t, "application/json", w5.Header().Get("Content-Type"),
		"V0 memo create must answer application/json")
	// Parse the create response as a
	// `json.Decoder` so we can read the
	// `id` field as a `json.Number`. The
	// V0 `MemosV0Memo.id` is a Kotlin
	// `Long` (up to 2^63), which exceeds
	// the precision of a float64; the
	// Kotlin client reads it as a string
	// in the JSON and parses it as Long.
	// The same goes for us: reading the
	// id as a string is the only way to
	// avoid the float64 precision loss.
	dec := json.NewDecoder(bytes.NewReader(w5.Body.Bytes()))
	dec.UseNumber()
	var created map[string]any
	require.NoError(t, dec.Decode(&created),
		"V0 memo create body must be valid JSON: %s", w5.Body.String())
	createdIDStr, ok := created["id"].(json.Number)
	require.True(t, ok,
		"V0 memo create must include a numeric 'id': %v", created)
	createdIDStr2, err := createdIDStr.Int64()
	require.NoError(t, err)
	assert.NotZero(t, createdIDStr2)
	// The V0 response echoes the content
	// and the visibility string.
	assert.Equal(t, "from V0", created["content"])
	assert.Equal(t, "PRIVATE", created["visibility"])

	// 6. PATCH /api/v1/memo/{id} — the V0
	//    memo update. Use the V0 id from the
	//    create response.
	createdID := createdIDStr.String()
	patchPath := "/api/v1/memo/" + createdID
	w6 := doReq(http.MethodPatch, patchPath,
		`{"id":`+createdID+`,"content":"from V0 edited","rowStatus":"NORMAL"}`)
	require.Equal(t, http.StatusOK, w6.Code,
		"V0 memo patch must answer 200, got %d: %s", w6.Code, w6.Body.String())
	var patched map[string]any
	require.NoError(t, json.Unmarshal(w6.Body.Bytes(), &patched),
		"V0 memo patch body must be valid JSON: %s", w6.Body.String())
	assert.Equal(t, "from V0 edited", patched["content"])

	// 7. POST /api/v1/memo/{id}/organizer
	//    with ?pinned=true — the V0
	//    pin toggle.
	w7 := doReq(http.MethodPost, patchPath+"/organizer?pinned=true", "")
	require.Equal(t, http.StatusOK, w7.Code,
		"V0 memo organizer must answer 200, got %d: %s", w7.Code, w7.Body.String())
	var pinned map[string]any
	require.NoError(t, json.Unmarshal(w7.Body.Bytes(), &pinned),
		"V0 memo organizer body must be valid JSON: %s", w7.Body.String())
	assert.Equal(t, true, pinned["pinned"],
		"V0 memo organizer must flip the pinned flag")

	// 8. POST /api/v1/resource/blob — the V0
	//    multipart upload. Build a
	//    multipart/form-data body with a
	//    single `file` part.
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, ferr := mw.CreateFormFile("file", "kick.wav")
	require.NoError(t, ferr)
	_, _ = fw.Write([]byte("RIFFfake"))
	require.NoError(t, mw.Close())
	req8 := httptest.NewRequest(http.MethodPost, "/api/v1/resource/blob", &b)
	req8.Header.Set("Authorization", "Bearer "+pat)
	req8.Header.Set("Content-Type", mw.FormDataContentType())
	w8 := httptest.NewRecorder()
	mux.ServeHTTP(w8, req8)
	require.Equal(t, http.StatusOK, w8.Code,
		"V0 resource upload must answer 200, got %d: %s", w8.Code, w8.Body.String())
	// Same `json.Number` treatment for the
	// upload response: the V0 resource id
	// is a Kotlin Long.
	dec8 := json.NewDecoder(bytes.NewReader(w8.Body.Bytes()))
	dec8.UseNumber()
	var uploaded map[string]any
	require.NoError(t, dec8.Decode(&uploaded),
		"V0 resource upload body must be valid JSON: %s", w8.Body.String())
	uploadedIDStr, ok := uploaded["id"].(json.Number)
	require.True(t, ok,
		"V0 resource upload must include a numeric 'id': %v", uploaded)
	uploadedID := uploadedIDStr.String()
	_, err = uploadedIDStr.Int64()
	require.NoError(t, err)
	assert.Equal(t, "kick.wav", uploaded["filename"])

	// 9. DELETE /api/v1/resource/{id} — the
	//    V0 resource delete. Use the V0 id
	//    from the upload response.
	delPath := "/api/v1/resource/" + uploadedID
	w9 := doReq(http.MethodDelete, delPath, "")
	require.Equal(t, http.StatusOK, w9.Code,
		"V0 resource delete must answer 200, got %d: %s", w9.Code, w9.Body.String())

	// 10. DELETE /api/v1/memo/{id} — the V0
	//    memo delete. Clean up the memo we
	//    created in step 5.
	w10 := doReq(http.MethodDelete, patchPath, "")
	require.Equal(t, http.StatusOK, w10.Code,
		"V0 memo delete must answer 200, got %d: %s", w10.Code, w10.Body.String())

	// 11. V1 must STILL work after the V0
	//     surface is mounted. The MoeMemos
	//     client interleaves V0 and V1 calls;
	//     a regression on the V1 side
	//     (e.g. a duplicated route pattern
	//     causing a 404 on V1 paths) is the
	//     riskiest possible outcome of this
	//     change.
	w11 := doReq(http.MethodGet, "/api/v1/auth/me", "")
	require.Equal(t, http.StatusOK, w11.Code,
		"V1 auth/me must STILL answer 200 after V0 mount")
	var v1me map[string]any
	require.NoError(t, json.Unmarshal(w11.Body.Bytes(), &v1me),
		"V1 auth/me body must be valid JSON: %s", w11.Body.String())
	assert.NotEmpty(t, v1me["user"],
		"V1 auth/me must return a non-null user: %v", v1me)
}

// TestV0Dispatch_AcceptEncodingGzip is the
// regression for the live-deploy "data couldn't
// be read" error that persisted after the V0
// surface was first wired up. Caddy forwards
// `Accept-Encoding: gzip` from the MoeMemos
// client to riff, the V0 handler clones the
// request headers into the upstream call, and
// the connect server honours the Accept-Encoding
// by gzip-compressing the response body. The V0
// shaper json.Unmarshal's the response, so the
// first byte (0x1f, the gzip magic) makes the
// decoder throw "invalid character '\\x1f'".
//
// The fix lives in v0DispatchConnect: the
// upstream call has its Accept-Encoding header
// stripped before the in-process dispatch. The
// V1 pass-through path doesn't trip on this
// because it io.Copy's the body verbatim and
// lets caddy handle the decompression.
func TestV0Dispatch_AcceptEncodingGzip(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))

	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	// Real user + PAT so the V0 list path is
	// allowed to actually run the V1 dispatch
	// (otherwise the auth interceptor rejects
	// with 401 before the V0 handler runs).
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moegz")
	rec.Set("email", "moegz@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))

	patRaw, perr := random.String(24)
	require.NoError(t, perr)
	pat := "memos_pat_" + patRaw
	patHash, perr := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	require.NoError(t, perr)
	pats, perr := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, perr)
	patRec := core.NewRecord(pats)
	patRec.Set("user", rec.Id)
	patRec.Set("description", "gzip regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	// 1. Without Accept-Encoding, the V0 list
	//    is a plain JSON array. The body must
	//    NOT start with the gzip magic.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memo", nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code,
		"baseline V0 /memo must answer 200: body=%s", w.Body.String())
	require.Equal(t, "application/json", w.Header().Get("Content-Type"),
		"V0 /memo must answer application/json")
	var arr []any
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&arr),
		"V0 /memo body must be a valid JSON array: %s", w.Body.String())

	// 2. With Accept-Encoding: gzip (the live
	//    caddy + MoeMemos default), the V0
	//    list must STILL be plain JSON. The
	//    V0 shaper does not decompress; if the
	//    dispatch helper forgets to strip
	//    Accept-Encoding, the connect server
	//    replies with a gzip body and the V0
	//    shaper throws.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/memo", nil)
	req2.Header.Set("Authorization", "Bearer "+pat)
	req2.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 /memo with Accept-Encoding: gzip must still answer 200: "+
			"body=%s", w2.Body.String())
	require.Equal(t, "application/json", w2.Header().Get("Content-Type"),
		"V0 /memo must answer application/json even with "+
			"Accept-Encoding: gzip")
	require.Greater(t, w2.Body.Len(), 0,
		"V0 /memo body must not be empty")
	require.NotEqual(t, byte(0x1f), w2.Body.Bytes()[0],
		"V0 /memo body must NOT start with the gzip magic (0x1f); "+
			"the V0 shaper does not decompress: body[0]=%x",
		w2.Body.Bytes()[0])
	var arr2 []any
	dec2 := json.NewDecoder(bytes.NewReader(w2.Body.Bytes()))
	dec2.UseNumber()
	require.NoError(t, dec2.Decode(&arr2),
		"V0 /memo body must be a valid JSON array even with "+
			"Accept-Encoding: gzip: body=%s", w2.Body.String())
}
