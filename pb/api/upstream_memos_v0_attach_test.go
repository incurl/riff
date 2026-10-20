package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
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

// The MoeMemos V0 client posts a memo with an attachment
// in three steps:
//
//  1. POST /api/v1/memo            — the memo with its text
//  2. POST /api/v1/resource/blob   — the file upload
//  3. PATCH /api/v1/memo/{id}      — the memo update that
//     carries the `resourceList` linking the uploaded
//     resource to the memo
//
// Step 3 is the one that used to be dropped on the floor:
// the V0 PATCH shaper left `resourceList` in the request
// body, so it landed in the V1 `update_mask` — and the V1
// `UpdateMemo` handler has no `resourceList` field, so the
// attachment stayed bound to the upload stub instead of
// moving to the real memo.
//
// The visible symptom was two memos in the feed: the real
// one with the text, and the stub with the attachment.
func TestV0MemoWithAttachmentIsNotSplit(t *testing.T) {
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

	// A real user plus a real PAT.
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moe")
	rec.Set("email", "moe@example.com")
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
	patRec.Set("description", "MoeMemos V0 attachment regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	// Mount BOTH the V0 and V1 surfaces: the MoeMemos
	// client interleaves them, and the SPA feed the user
	// sees is the V1 list.
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	doReq := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// 1. Create the memo with its text.
	w1 := doReq(http.MethodPost, "/api/v1/memo",
		`{"content":"from V0 with a file","visibility":"PUBLIC"}`)
	require.Equal(t, http.StatusOK, w1.Code,
		"V0 memo create must answer 200, got %d: %s", w1.Code, w1.Body.String())

	var created map[string]any
	dec := json.NewDecoder(bytes.NewReader(w1.Body.Bytes()))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&created),
		"V0 memo create body must be valid JSON: %s", w1.Body.String())
	createdID, ok := created["id"].(json.Number)
	require.True(t, ok, "V0 memo create must include a numeric id: %v", created)
	memoV0ID := createdID.String()

	// 2. Upload the file.
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, ferr := mw.CreateFormFile("file", "photo.jpeg")
	require.NoError(t, ferr)
	_, _ = fw.Write([]byte("fake-jpeg-bytes"))
	require.NoError(t, mw.Close())

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/resource/blob", &b)
	req2.Header.Set("Authorization", "Bearer "+pat)
	req2.Header.Set("Content-Type", mw.FormDataContentType())
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 resource upload must answer 200, got %d: %s", w2.Code, w2.Body.String())

	var uploaded map[string]any
	dec2 := json.NewDecoder(bytes.NewReader(w2.Body.Bytes()))
	dec2.UseNumber()
	require.NoError(t, dec2.Decode(&uploaded),
		"V0 resource upload body must be valid JSON: %s", w2.Body.String())
	resourceID, ok := uploaded["id"].(json.Number)
	require.True(t, ok, "V0 resource upload must include a numeric id: %v", uploaded)

	// 3. PATCH the memo with the resourceList that links
	//    the uploaded resource to the memo. This is the
	//    step that used to be a no-op.
	w3 := doReq(http.MethodPatch, "/api/v1/memo/"+memoV0ID,
		`{"id":`+memoV0ID+`,"content":"from V0 with a file","visibility":"PUBLIC","rowStatus":"NORMAL","resourceList":[{"id":`+
			resourceID.String()+`,"filename":"photo.jpeg","type":"image/jpeg","externalLink":"/api/files/attachments/x/photo.jpeg"}]}`)
	require.Equal(t, http.StatusOK, w3.Code,
		"V0 memo patch must answer 200, got %d: %s", w3.Code, w3.Body.String())

	// The V1 list is what the SPA feed renders.
	v1List := doReq(http.MethodGet, "/api/v1/memos?pageSize=50", "")
	require.Equal(t, http.StatusOK, v1List.Code,
		"V1 memo list must answer 200, got %d: %s", v1List.Code, v1List.Body.String())
	var v1Body map[string]any
	require.NoError(t, json.Unmarshal(v1List.Body.Bytes(), &v1Body))
	for _, raw := range v1Body["memos"].([]any) {
		m := raw.(map[string]any)
		assert.NotEqual(t, connect.StubMemoContent, m["content"],
			"a legacy upload stub must not appear in the SPA feed")
	}

	// The memos table must hold exactly the one memo the client
	// created — no phantom row for the upload.
	memos := listMemosForTest(t, app)
	var realID string
	for _, m := range memos {
		name, _ := m["id"].(string)
		content, _ := m["content"].(string)
		t.Logf("memo=%s content=%q", name, content)
		if content == "from V0 with a file" {
			realID = name
		}
	}
	require.NotEmpty(t, realID, "the memo created via V0 must exist")
	require.Len(t, memos, 1,
		"a V0 upload must not manufacture a second memo row")

	// The attachment must hang off the real memo.
	owners := attachmentOwners(t, app)
	t.Logf("attachment owners: %v", owners)
	assert.Equal(t, []string{realID}, owners,
		"the uploaded attachment must be bound to the real memo, not the stub")
}

// attachmentOwners returns the memo ids the attachments are
// bound to, sorted, so the assertion can compare the exact
// set.
func attachmentOwners(t *testing.T, app core.App) []string {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	rows, err := app.FindRecordsByFilter(col, "", "-created", 0, 0)
	require.NoError(t, err)
	out := []string{}
	for _, r := range rows {
		out = append(out, r.GetString("memo"))
	}
	sort.Strings(out)
	return out
}

// listMemosForTest lists the memos the way the V1
// ListMemos handler does, so the assertions see the same
// rows the SPA feed renders.
func listMemosForTest(t *testing.T, app core.App) []map[string]any {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rows, err := app.FindRecordsByFilter(col, "", "-created", 0, 0)
	require.NoError(t, err)

	out := []map[string]any{}
	for _, r := range rows {
		raw, mErr := r.MarshalJSON()
		require.NoError(t, mErr)
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	return out
}

// TestV0UploadLeavesNoPhantomMemo is the regression for the split-memo
// symptom. The V0 client uploads a resource before it has a memo to bind
// it to. When `attachments.memo` was required, that upload manufactured a
// hidden stub memo to hang the file on, and the stub was the empty memo the
// user saw alongside their real one whenever the binding never completed.
//
// The upload now lands as an *unlinked attachment* — upstream's own model,
// where `Attachment.memo` is optional and `GetAttachment` documents that an
// unlinked attachment is visible only to its uploader. No memo row is
// created, so there is nothing to leak into either feed and nothing for a
// later request to clean up.
func TestV0UploadLeavesNoPhantomMemo(t *testing.T) {
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

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", "moe")
	rec.Set("email", "moe@example.com")
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
	patRec.Set("description", "MoeMemos V0 stub visibility regression")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountUpstreamMemosV0Routes(r, app, nil)
	mountUpstreamMemosV1Routes(r, app, nil)
	mux, err := r.BuildMux()
	require.NoError(t, err)

	doReq := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Upload a file with no memo to link it to. The V0
	// path creates the stub.
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, ferr := mw.CreateFormFile("file", "orphan.jpeg")
	require.NoError(t, ferr)
	_, _ = fw.Write([]byte("fake-jpeg-bytes"))
	require.NoError(t, mw.Close())

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/resource/blob", &b)
	req2.Header.Set("Authorization", "Bearer "+pat)
	req2.Header.Set("Content-Type", mw.FormDataContentType())
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 resource upload must answer 200, got %d: %s", w2.Code, w2.Body.String())

	// The upload must not have created a memo at all.
	memos := listMemosForTest(t, app)
	require.Len(t, memos, 0,
		"an upload with no memo must leave the memos table untouched")

	// The file itself must survive as an unlinked attachment.
	owners := attachmentOwners(t, app)
	require.Equal(t, []string{""}, owners,
		"the uploaded file must land as an unlinked attachment, owned by nobody")

	// Neither list can show a memo that does not exist.
	w3 := doReq(http.MethodGet, "/api/v1/memo", "")
	require.Equal(t, http.StatusOK, w3.Code,
		"V0 memo list must answer 200, got %d: %s", w3.Code, w3.Body.String())
	var v0List []any
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &v0List))
	assert.Empty(t, v0List,
		"the V0 list must be empty: %s", w3.Body.String())

	// The V1 list (the SPA feed) must be empty too.
	w4 := doReq(http.MethodGet, "/api/v1/memos?pageSize=50", "")
	require.Equal(t, http.StatusOK, w4.Code,
		"V1 memo list must answer 200, got %d: %s", w4.Code, w4.Body.String())
	var v1Body map[string]any
	require.NoError(t, json.Unmarshal(w4.Body.Bytes(), &v1Body))
	v1Memos, _ := v1Body["memos"].([]any)
	assert.Empty(t, v1Memos,
		"the SPA feed must be empty: %s", w4.Body.String())
}
