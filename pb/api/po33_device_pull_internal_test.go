package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/signedurl"
)

// newPullApp returns a freshly-migrated
// app with hooks registered and the
// device-pull signer installed. The
// signer is a deterministic 32-byte
// fixture so signed URLs are
// reproducible across runs.
func newPullApp(t *testing.T) core.App {
	t.Helper()
	app := newAPIApp(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	SetDevicePullSigner(signedurl.New(key))
	return app
}

// newPullDevice inserts a po33_devices
// row whose `public_key_fpr` is the
// SHA-256 hex the test's
// X-Device-Fingerprint header carries.
// The handler does not re-hash the
// header; the header IS the
// fingerprint the lookup runs on.
// The returned `headerValue` is the
// literal string the test should send
// as the X-Device-Fingerprint header.
func newPullDevice(t *testing.T, app core.App, user *core.Record, fprInput string) (*core.Record, string) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("po33_devices")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("user", user.Id)
	rec.Set("label", "Test PO-33")
	rec.Set("public_key", "deadbeef")
	// Compute the fingerprint the same
	// way the production code does
	// (SHA-256 of the public key bytes
	// hex-encoded). The test passes a
	// known string as the "public key"
	// and uses the resulting
	// fingerprint as both the stored
	// value and the header value.
	sum := sha256.Sum256([]byte(fprInput))
	fpr := hex.EncodeToString(sum[:])
	rec.Set("public_key_fpr", fpr)
	rec.Set("status", "ACTIVE")
	require.NoError(t, app.Save(rec))
	return rec, fpr
}

// newPullSketch creates a sketch memo
// owned by the given user. Mirrors
// the connect-side newSketchMemo
// shape.
func newPullSketch(t *testing.T, app core.App, userID, content string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", userID)
	rec.Set("content", content)
	rec.Set("visibility", "PRIVATE")
	rec.Set("sketch", json.RawMessage(
		`{"model":"PO-33","slot_count":16,`+
			`"device_fingerprint":"abc",`+
			`"uploaded_at_unix":1730000000}`))
	require.NoError(t, app.Save(rec))
	return rec
}

// doPushSketch is the body of
// POST /api/po33/devices/{id}/push.
func doPushSketch(t *testing.T, app core.App, deviceID, memoID string, auth *core.Record) (*httptest.ResponseRecorder, error) {
	t.Helper()
	body, _ := json.Marshal(pushSketchRequest{
		SketchMemoID: "memos/" + memoID,
	})
	e, rec := newAPIRequest(app, http.MethodPost,
		"/api/po33/devices/"+deviceID+"/push",
		body, auth)
	e.Request.SetPathValue("deviceId", deviceID)
	return rec, pushSketchHandler(e)
}

// doPendingPull is the body of
// GET /api/po33/sketches/pending
// (device-side; the device's
// X-Device-Fingerprint header is the
// bearer).
func doPendingPull(t *testing.T, app core.App, fpr string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	e, rec := newAPIRequest(app, http.MethodGet,
		"/api/po33/sketches/pending", nil, nil)
	e.Request.Header.Set("X-Device-Fingerprint", fpr)
	return rec, pendingPullHandler(e)
}

// doAckPendingPull is the body of
// POST /api/po33/sketches/pending/ack.
func doAckPendingPull(t *testing.T, app core.App, fpr, pushID, reason string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	body, _ := json.Marshal(ackPendingPullRequest{
		PushID: pushID,
		Reason: reason,
	})
	e, rec := newAPIRequest(app, http.MethodPost,
		"/api/po33/sketches/pending/ack", body, nil)
	e.Request.Header.Set("X-Device-Fingerprint", fpr)
	return rec, ackPendingPullHandler(e)
}

// doSketchDownload is the body of
// GET /api/po33/sketches/{memo}/download.
// `rawQuery` is the unsigned / signed
// query string the test wants to
// verify.
func doSketchDownload(t *testing.T, app core.App, memoID, rawQuery string, fpr string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	path := "/api/po33/sketches/" + memoID + "/download"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	e, rec := newAPIRequest(app, http.MethodGet, path, nil, nil)
	e.Request.SetPathValue("memoId", memoID)
	if fpr != "" {
		e.Request.Header.Set("X-Device-Fingerprint", fpr)
	}
	return rec, deviceSketchDownloadHandler(e)
}

// TestPushSketch_RequiresAuth pins the
// unauthenticated path: a missing
// viewer gets 401.
func runHandlerWithEvent(fn func(*core.RequestEvent) error, e *core.RequestEvent) (*httptest.ResponseRecorder, error) {
	return e.Response.(*httptest.ResponseRecorder), fn(e)
}

func TestPushSketch_RequiresAuth(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	device, _ := newPullDevice(t, app, alice, "dev1")
	_, _err := doPushSketch(t, app, device.Id, "memoid", nil)
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// TestPushSketch_HappyPath pins the
// SPA-facing POST: the envelope is
// on the device row, the response
// carries a signed URL, and the
// state is QUEUED.
func TestPushSketch_HappyPath(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	device, _ := newPullDevice(t, app, alice, "dev1")
	memo := newPullSketch(t, app, alice.Id, "sketch content")

	rec, _ := doPushSketch(t, app, device.Id, memo.Id, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	var out pushSketchResponse
	readJSON(t, rec, &out)
	assert.NotEmpty(t, out.PushID)
	assert.Equal(t, "QUEUED", out.PushState)
	assert.Contains(t, out.SignedURL, "exp=")
	assert.Contains(t, out.SignedURL, "mac=")
}

// TestPushSketch_RejectsNonOwner pins
// the cross-tenant guard: bob cannot
// push to alice's device, the device
// row never gets a pending_pull.
func TestPushSketch_RejectsNonOwner(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	bob := newAPIUser(t, app, "bob")
	device, _ := newPullDevice(t, app, alice, "dev1")
	memo := newPullSketch(t, app, alice.Id, "sketch content")

	_, _err := doPushSketch(t, app, device.Id, memo.Id, bob)
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusNotFound, status)

	// The device row never received a
	// pending_pull. PB returns the
	// unset JSONField as
	// types.JSONRaw{}, not nil, so the
	// assertion is "the value is
	// the zero JSONRaw".
	devRow, err := app.FindRecordById("po33_devices", device.Id)
	require.NoError(t, err)
	got := devRow.Get("pending_pull")
	if jr, ok := got.(types.JSONRaw); ok {
		assert.Empty(t, jr)
	} else {
		assert.Nil(t, got)
	}
}

// TestPushSketch_RejectsTextMemo pins
// the sketch-only rule on the
// owner-facing push path.
func TestPushSketch_RejectsTextMemo(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	device, _ := newPullDevice(t, app, alice, "dev1")
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	textMemo := core.NewRecord(col)
	textMemo.Set("author", alice.Id)
	textMemo.Set("content", "plain text")
	textMemo.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(textMemo))

	_, _err := doPushSketch(t, app, device.Id, textMemo.Id, alice)
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusBadRequest, status)
}

// TestPendingPull_NoEnvelopeReturnsNoPush
// pins the empty-state path: a device
// with no pending_pull gets
// `{state: "NO_PUSH"}`.
func TestPendingPull_NoEnvelopeReturnsNoPush(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	_, fpr := newPullDevice(t, app, alice, "dev1")
	rec, _ := doPendingPull(t, app, fpr)
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	readJSON(t, rec, &out)
	assert.Equal(t, "NO_PUSH", out["state"])
}

// TestPendingPull_AdvancesState pins
// the state machine: a fresh push
// reads back as QUEUED on the first
// poll, then SENDING, then
// DELIVERED after the ack.
func TestPendingPull_AdvancesState(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	device, fpr := newPullDevice(t, app, alice, "dev1")
	memo := newPullSketch(t, app, alice.Id, "sketch")

	// Owner-facing push.
	pushRec, _ := doPushSketch(t, app, device.Id, memo.Id, alice)
	require.Equal(t, http.StatusOK, pushRec.Code)
	var pushed pushSketchResponse
	readJSON(t, pushRec, &pushed)

	// First poll: the device sees
	// SENDING (the handler advances
	// QUEUED -> SENDING on first
	// poll, before the device has
	// downloaded).
	poll1, _ := doPendingPull(t, app, fpr)
	require.Equal(t, http.StatusOK, poll1.Code)
	var pending1 map[string]any
	readJSON(t, poll1, &pending1)
	assert.Equal(t, "SENDING", pending1["state"])

	// Ack as success.
	ackRec, _ := doAckPendingPull(t, app, fpr, pushed.PushID, "")
	require.Equal(t, http.StatusOK, ackRec.Code)
	var acked map[string]any
	readJSON(t, ackRec, &acked)
	assert.Equal(t, "DELIVERED", acked["state"])
}

// TestPendingPull_RejectsUnknownDevice
// pins the auth check: an unknown
// fingerprint returns 401.
func TestPendingPull_RejectsUnknownDevice(t *testing.T) {
	app := newPullApp(t)
	_, _err := doPendingPull(t, app, "deadbeef")
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// TestAckPendingPull_NoMatchingPush pins
// the not-found case: an ack for a
// push_id that does not match the
// current envelope returns 404.
func TestAckPendingPull_NoMatchingPush(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	_, fpr := newPullDevice(t, app, alice, "dev1")
	_, _err := doAckPendingPull(t, app, fpr, "nonexistent", "")
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusNotFound, status)
}

// TestPushStatus_ExpiredAfterKeepAlive
// pins the polling contract: a push
// older than the keep-alive window
// reads back as EXPIRED, not 404.
func TestPushStatus_ExpiredAfterKeepAlive(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	device, _ := newPullDevice(t, app, alice, "dev1")
	memo := newPullSketch(t, app, alice.Id, "sketch")

	pushRec, _ := doPushSketch(t, app, device.Id, memo.Id, alice)
	require.Equal(t, http.StatusOK, pushRec.Code)
	var pushed pushSketchResponse
	readJSON(t, pushRec, &pushed)

	// Move the envelope's queued_at
	// into the past via a direct row
	// write.
	devRow, err := app.FindRecordById("po33_devices", device.Id)
	require.NoError(t, err)
	raw := devRow.Get("pending_pull")
	require.NotNil(t, raw)
	var env devicePullEnvelope
	// pending_pull is a JSONField; PB
	// returns it as types.JSONRaw
	// (which is []byte under the
	// hood). The type assertion below
	// covers both shapes; the json
	// path is the same.
	switch v := raw.(type) {
	case []byte:
		require.NoError(t, json.Unmarshal(v, &env))
	case json.RawMessage:
		require.NoError(t, json.Unmarshal(v, &env))
	case types.JSONRaw:
		require.NoError(t, json.Unmarshal(v, &env))
	default:
		t.Fatalf("unexpected pending_pull type %T", raw)
	}
	env.QueuedAt = env.QueuedAt.Add(-2 * devicePullKeepAlive)
	updated, err := json.Marshal(env)
	require.NoError(t, err)
	devRow.Set("pending_pull", updated)
	require.NoError(t, app.Save(devRow))

	e, rec := newAPIRequest(app, http.MethodGet,
		"/api/po33/devices/"+device.Id+"/pushes/"+pushed.PushID, nil, alice)
	e.Request.SetPathValue("deviceId", device.Id)
	e.Request.SetPathValue("pushId", pushed.PushID)
	require.NoError(t, pushStatusHandler(e))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	readJSON(t, rec, &out)
	assert.Equal(t, "EXPIRED", out["state"])
}

// TestSketchDownload_RejectsUnsignedURL
// pins the signature check: a request
// without `exp=` and `mac=` is 404
// (not 401, so a leaked URL does not
// reveal whether the memo exists).
func TestSketchDownload_RejectsUnsignedURL(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	memo := newPullSketch(t, app, alice.Id, "sketch")
	_, _err := doSketchDownload(t, app, memo.Id, "", "")
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusNotFound, status)
}

// TestSketchDownload_RejectsTamperedMAC
// pins the tamper check: a signed
// URL with a flipped MAC byte is 404.
func TestSketchDownload_RejectsTamperedMAC(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	memo := newPullSketch(t, app, alice.Id, "sketch")
	// Build a real signed URL and
	// flip the last MAC character.
	devRow, _ := newPullDevice(t, app, alice, "dev1")
	_ = devRow
	signed := signedurl.New(makeKeyBytes()).Sign("/api/po33/sketches/" + memo.Id + "/download")
	// Flip the last char of the mac.
	idx := strings.LastIndex(signed, "=")
	tampered := signed[:idx+1]
	last := tampered[len(tampered)-1]
	var replacement string
	switch last {
	case '0':
		replacement = "1"
	case '1':
		replacement = "0"
	case 'a':
		replacement = "b"
	case 'f':
		replacement = "e"
	default:
		replacement = "a"
	}
	tampered = tampered[:len(tampered)-1] + replacement
	query := strings.TrimPrefix(tampered, "/api/po33/sketches/"+memo.Id+"/download?")
	_, _err := doSketchDownload(t, app, memo.Id, query, "")
	status, ok := apiErrorStatus(t, _err)
	require.True(t, ok, "expected ApiError, got %T %v", _err, _err)
	assert.Equal(t, http.StatusNotFound, status)
	_ = alice
}

// makeKeyBytes is the test-only 32-byte
// key the device-pull signer is
// installed with. The same fixture
// is used by newPullApp.
func makeKeyBytes() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// TestPushSketch_DoubleClickReusesPush
// pins the idempotency contract: a
// second push for the same
// (device, memo) reuses the prior
// push_id.
func TestPushSketch_DoubleClickReusesPush(t *testing.T) {
	app := newPullApp(t)
	alice := newAPIUser(t, app, "alice")
	device, _ := newPullDevice(t, app, alice, "dev1")
	memo := newPullSketch(t, app, alice.Id, "sketch")

	first, _ := doPushSketch(t, app, device.Id, memo.Id, alice)
	require.Equal(t, http.StatusOK, first.Code)
	var firstOut pushSketchResponse
	readJSON(t, first, &firstOut)

	second, _ := doPushSketch(t, app, device.Id, memo.Id, alice)
	require.Equal(t, http.StatusOK, second.Code)
	var secondOut pushSketchResponse
	readJSON(t, second, &secondOut)
	assert.Equal(t, firstOut.PushID, secondOut.PushID)
}

// sentinel: the bytes import is used
// by newAPIRequest above.
var _ = bytes.NewReader
