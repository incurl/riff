package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestV0BootServesRealCreationMetadata is the end-to-end
// regression for "MoeMemos is not getting memos metadata
// like date of creation".
//
// Every unit test of the V0 shaper risks validating a
// hand-written V1 body that the connect handler never
// actually emits. This one drives the real HTTP surface
// end to end — create memo, upload a file, PATCH to link
// it, then list — and asserts on what the client actually
// receives, so it covers the shaper, the handler glue and
// the protojson encoding in one shot.
//
// The failure it pins: the shaper read `createTime` as
// `{"seconds": N}` and `size` as a `float64`, but
// protojson emits a `Timestamp` as an RFC 3339 string and
// an `int64` as a string. Every V0 response therefore
// carried `createdTs: 0` / `size: 0`, which the Kotlin
// client renders as the epoch.
func TestV0BootServesRealCreationMetadata(t *testing.T) {
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
	patRec.Set("description", "MoeMemos V0 metadata regression")
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

	// The V0 client decodes 64-bit ids as `Long`, so every
	// numeric field is read with UseNumber — without it a
	// `Long` id loses precision and stops matching.
	decodeV0 := func(t *testing.T, body []byte) map[string]any {
		t.Helper()
		var out map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		require.NoError(t, dec.Decode(&out), "V0 body must be valid JSON: %s", body)
		return out
	}

	// 1. Create the memo.
	w1 := doReq(http.MethodPost, "/api/v1/memo",
		`{"content":"from V0 with a file","visibility":"PUBLIC"}`)
	require.Equal(t, http.StatusOK, w1.Code,
		"V0 memo create must answer 200, got %d: %s", w1.Code, w1.Body.String())
	created := decodeV0(t, w1.Body.Bytes())

	memoV0ID := fmt.Sprintf("%v", created["id"])
	createdTs, ok := created["createdTs"].(json.Number)
	require.True(t, ok,
		"V0 memo create must return a numeric createdTs, got %#v", created["createdTs"])
	createdUnix, cerr := createdTs.Int64()
	require.NoError(t, cerr)

	// The creation date is the reported symptom: the memo must
	// carry a real timestamp, not the epoch.
	assert.Greater(t, createdUnix, int64(1_600_000_000),
		"V0 memo create must report a plausible creation time, got %d", createdUnix)
	// And it must be the *store's* time, not a fabricated one.
	assert.WithinDuration(t, time.Now(), time.Unix(createdUnix, 0), time.Minute,
		"V0 createdTs must track the real creation time")

	// 2. Upload a file with a known size.
	const fileBytes = "fake-jpeg-bytes"
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, ferr := mw.CreateFormFile("file", "photo.jpeg")
	require.NoError(t, ferr)
	_, _ = fw.Write([]byte(fileBytes))
	require.NoError(t, mw.Close())

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/resource/blob", &b)
	req2.Header.Set("Authorization", "Bearer "+pat)
	req2.Header.Set("Content-Type", mw.FormDataContentType())
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code,
		"V0 resource upload must answer 200, got %d: %s", w2.Code, w2.Body.String())
	uploaded := decodeV0(t, w2.Body.Bytes())
	resourceID := fmt.Sprintf("%v", uploaded["id"])

	// The upload response is the first place the client learns
	// the file's type and size; both used to be wrong.
	size, ok := uploaded["size"].(json.Number)
	require.True(t, ok, "V0 resource upload must return a numeric size, got %#v", uploaded["size"])
	sizeInt, serr := size.Int64()
	require.NoError(t, serr)
	assert.EqualValues(t, len(fileBytes), sizeInt,
		"V0 resource upload must report the real byte count")
	assert.Equal(t, "image/jpeg", uploaded["type"],
		"V0 resource upload must report the file's MIME type")

	// 3. PATCH to link the resource to the memo.
	w3 := doReq(http.MethodPatch, "/api/v1/memo/"+memoV0ID,
		`{"id":`+memoV0ID+`,"content":"from V0 with a file","visibility":"PUBLIC","rowStatus":"NORMAL","resourceList":[{"id":`+
			resourceID+`,"filename":"photo.jpeg","type":"image/jpeg","externalLink":"/api/files/attachments/x/photo.jpeg"}]}`)
	require.Equal(t, http.StatusOK, w3.Code,
		"V0 memo patch must answer 200, got %d: %s", w3.Code, w3.Body.String())
	patched := decodeV0(t, w3.Body.Bytes())

	// The PATCH response must show the binding the client just
	// made, or its next edit rebuilds `resourceList` from an
	// empty list and drops the file.
	resources, _ := patched["resourceList"].([]any)
	require.Len(t, resources, 1,
		"V0 memo patch response must echo the bound resource, got %v", patched["resourceList"])
	bound, _ := resources[0].(map[string]any)
	require.NotNil(t, bound)

	// 4. The list is what MoeMemos actually renders. Every row
	// must carry real metadata.
	w4 := doReq(http.MethodGet, "/api/v1/memo?pageSize=50", "")
	require.Equal(t, http.StatusOK, w4.Code,
		"V0 memo list must answer 200, got %d: %s", w4.Code, w4.Body.String())

	// The list body is a top-level array, so it gets its own
	// UseNumber decode — a plain `json.Unmarshal` would widen
	// the `Long` timestamps to float64 and defeat the
	// numeric assertions below.
	var rows []any
	decList := json.NewDecoder(bytes.NewReader(w4.Body.Bytes()))
	decList.UseNumber()
	require.NoError(t, decList.Decode(&rows),
		"V0 memo list must be a top-level array: %s", w4.Body.String())
	require.Len(t, rows, 1, "exactly one memo must exist: %s", w4.Body.String())

	listed, _ := rows[0].(map[string]any)
	require.NotNil(t, listed)
	listedCreated, ok := listed["createdTs"].(json.Number)
	require.True(t, ok,
		"V0 memo list must return a numeric createdTs, got %#v", listed["createdTs"])
	listedUnix, lerr := listedCreated.Int64()
	require.NoError(t, lerr)
	assert.Equal(t, createdUnix, listedUnix,
		"the listed memo must carry its real creation time, not the epoch")
	assert.NotZero(t, listed["updatedTs"],
		"V0 memo list must return a non-zero updatedTs")

	// The attached resource must be visible from the memo and
	// must name its owning memo.
	listRes, _ := listed["resourceList"].([]any)
	require.Len(t, listRes, 1,
		"the listed memo must carry its attachment: %v", listed["resourceList"])
	listRes0, _ := listRes[0].(map[string]any)
	require.NotNil(t, listRes0)
	memoIDNum, ok := listRes0["memoId"].(json.Number)
	require.True(t, ok,
		"a bound V0 resource must report a numeric memoId, got %#v", listRes0["memoId"])
	memoIDInt, merr := memoIDNum.Int64()
	require.NoError(t, merr)
	assert.NotZero(t, memoIDInt,
		"a bound V0 resource must not report memoId 0")
	resSize, ok := listRes0["size"].(json.Number)
	require.True(t, ok,
		"a V0 resource must report a numeric size, got %#v", listRes0["size"])
	resSizeInt, rserr := resSize.Int64()
	require.NoError(t, rserr)
	assert.EqualValues(t, len(fileBytes), resSizeInt,
		"the listed resource must report the real byte count")

	// The store must agree: one memo, one attachment bound to
	// it, with the right MIME and size persisted.
	memos := listMemosForTest(t, app)
	require.Len(t, memos, 1,
		"a V0 upload must not manufacture a second memo row")
	realID, _ := memos[0]["id"].(string)
	assert.Equal(t, []string{realID}, attachmentOwners(t, app),
		"the uploaded attachment must be bound to the real memo")

	col, aerr := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, aerr)
	rowsA, aerr2 := app.FindRecordsByFilter(col, "", "-created", 0, 0)
	require.NoError(t, aerr2)
	require.Len(t, rowsA, 1)
	assert.EqualValues(t, len(fileBytes), rowsA[0].GetInt("size"),
		"the stored size column must hold the real byte count")
	assert.Equal(t, "image/jpeg", rowsA[0].GetString("mime_type"),
		"the stored mime_type must be derived from the filename when the "+
			"multipart part header is the generic application/octet-stream")
}
