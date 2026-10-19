package connect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/random"
	"github.com/incurl/riff/internal/signedurl"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// makeTestKey returns the deterministic
// 32-byte test key the connect package
// uses for fixture-only Signers. The key
// matches the package's other test
// helpers (see the sketch_service_internal_test.go
// fixture patterns); a future change is
// a one-line edit.
func makeTestKey() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// ctxWithViewerAndSigner returns a
// context that satisfies both
// CurrentUser and
// PendingPullSignerFromContext. The
// PushSketch handler reads both. The
// helper is the single seam between the
// test's auth + signer setup and the
// production context keys; a future
// rename of either key in connect.go
// fails the build of this file.
func ctxWithViewerAndSigner(viewer *core.Record, signer *signedurl.Signer) context.Context {
	ctx := context.WithValue(context.Background(), ctxKeyAuthRecord{}, viewer)
	return WithPendingPullSigner(ctx, signer)
}

// TestPushSketch_QueuesEnvelope pins
// the happy path: a sketch owner can
// push to one of their ACTIVE
// devices. The response carries a
// signed URL the device's poll can
// follow; the envelope is on the
// device's `pending_pull` column.
func TestPushSketch_QueuesEnvelope(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memo := newSketchMemo(t, app, alice.Id, "test sketch")
	deviceID, _ := newPo33Device(t, app, alice.Id)
	signer := signedurl.New(makeTestKey())

	srv := NewSketchServer(app, nil)
	ctx := ctxWithViewerAndSigner(alice, signer)

	out, err := srv.PushSketch(ctx, &v1.PushSketchRequest{
		Name:   "memos/" + memo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.GetPushId())
	require.NotEmpty(t, out.GetSignedUrl())
	require.Contains(t, out.GetSignedUrl(), "exp=")
	require.Contains(t, out.GetSignedUrl(), "mac=")

	device, err := app.FindRecordById("po33_devices", deviceID)
	require.NoError(t, err)
	env, err := decodePendingPull(device)
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.Equal(t, "QUEUED", env.State)
	assert.Equal(t, memo.Id, env.SketchMemoID)
}

// TestPushSketch_DoubleClickReusesPushID
// pins the idempotency contract: a
// second push for the same
// (device, memo) pair overwrites the
// prior envelope with a fresh signed
// URL but the same push_id, so the
// SPA's poll sees a single logical
// push.
func TestPushSketch_DoubleClickReusesPushID(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memo := newSketchMemo(t, app, alice.Id, "test sketch")
	deviceID, _ := newPo33Device(t, app, alice.Id)
	signer := signedurl.New(makeTestKey())
	srv := NewSketchServer(app, nil)
	ctx := ctxWithViewerAndSigner(alice, signer)

	first, err := srv.PushSketch(ctx, &v1.PushSketchRequest{
		Name:   "memos/" + memo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.NoError(t, err)

	second, err := srv.PushSketch(ctx, &v1.PushSketchRequest{
		Name:   "memos/" + memo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.NoError(t, err)

	assert.Equal(t, first.GetPushId(), second.GetPushId(),
		"double-click on Push should reuse the push_id")
}

// TestPushSketch_RejectsNonOwner pins
// the cross-tenant guard: alice
// cannot push to bob's device. The
// handler returns NotFound rather
// than Forbidden so a stranger
// cannot enumerate device ids.
func TestPushSketch_RejectsNonOwner(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	memo := newSketchMemo(t, app, alice.Id, "alice sketch")
	deviceID, _ := newPo33Device(t, app, bob.Id)
	signer := signedurl.New(makeTestKey())
	srv := NewSketchServer(app, nil)
	ctx := ctxWithViewerAndSigner(alice, signer)

	_, err := srv.PushSketch(ctx, &v1.PushSketchRequest{
		Name:   "memos/" + memo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestPushSketch_RejectsRevokedDevice
// pins the active-only rule: a push
// to a REVOKED device fails with
// FailedPrecondition.
func TestPushSketch_RejectsRevokedDevice(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memo := newSketchMemo(t, app, alice.Id, "sketch")
	deviceID, _ := newPo33Device(t, app, alice.Id)
	dev, err := app.FindRecordById("po33_devices", deviceID)
	require.NoError(t, err)
	dev.Set("status", "REVOKED")
	require.NoError(t, app.Save(dev))
	signer := signedurl.New(makeTestKey())
	srv := NewSketchServer(app, nil)
	ctx := ctxWithViewerAndSigner(alice, signer)

	_, err = srv.PushSketch(ctx, &v1.PushSketchRequest{
		Name:   "memos/" + memo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestPushSketch_RejectsTextMemo pins
// the sketch-only rule: pushing a
// text memo is rejected with
// InvalidArgument.
func TestPushSketch_RejectsTextMemo(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	textMemo := core.NewRecord(col)
	textMemo.Set("author", alice.Id)
	textMemo.Set("content", "plain text")
	textMemo.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(textMemo))
	deviceID, _ := newPo33Device(t, app, alice.Id)
	signer := signedurl.New(makeTestKey())
	srv := NewSketchServer(app, nil)
	ctx := ctxWithViewerAndSigner(alice, signer)

	_, err = srv.PushSketch(ctx, &v1.PushSketchRequest{
		Name:   "memos/" + textMemo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestGetPushStatus_UnknownPushIsExpired
// pins the polling contract: an
// unknown push id returns
// UNSPECIFIED (NOT NotFound) so the
// SPA can clear its local state
// without treating it as a server
// error.
func TestGetPushStatus_UnknownPushIsExpired(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	deviceID, _ := newPo33Device(t, app, alice.Id)
	srv := NewSketchServer(app, nil)
	ctx := withViewer(context.Background(), alice)

	out, err := srv.GetPushStatus(ctx, &v1.GetPushStatusRequest{
		Device: "po33-devices/" + deviceID,
		Push:   "00000000-0000-0000-0000-000000000000",
	})
	require.NoError(t, err)
	assert.Equal(t, v1.PushState_PUSH_STATE_UNSPECIFIED, out.GetState())
}

// TestGetPushStatus_QueuedReturnsQueued
// pins the QUEUED path: a freshly
// pushed sketch reads back as
// QUEUED. The push_id matches the
// envelope.
func TestGetPushStatus_QueuedReturnsQueued(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memo := newSketchMemo(t, app, alice.Id, "sketch")
	deviceID, _ := newPo33Device(t, app, alice.Id)
	signer := signedurl.New(makeTestKey())
	srv := NewSketchServer(app, nil)
	pushCtx := ctxWithViewerAndSigner(alice, signer)
	pushed, err := srv.PushSketch(pushCtx, &v1.PushSketchRequest{
		Name:   "memos/" + memo.Id,
		Device: "po33-devices/" + deviceID,
	})
	require.NoError(t, err)

	out, err := srv.GetPushStatus(withViewer(context.Background(), alice), &v1.GetPushStatusRequest{
		Device: "po33-devices/" + deviceID,
		Push:   pushed.GetPushId(),
	})
	require.NoError(t, err)
	assert.Equal(t, v1.PushState_PUSH_STATE_QUEUED, out.GetState())
	assert.Equal(t, "memos/"+memo.Id, out.GetMemo())
}

// TestGetPushStatus_RejectsNonOwner
// pins the cross-tenant guard on
// the poll path.
func TestGetPushStatus_RejectsNonOwner(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	deviceID, _ := newPo33Device(t, app, bob.Id)
	srv := NewSketchServer(app, nil)

	_, err := srv.GetPushStatus(withViewer(context.Background(), alice), &v1.GetPushStatusRequest{
		Device: "po33-devices/" + deviceID,
		Push:   "irrelevant",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestGetSketch_OwnerSucceeds pins
// the read path: the sketch's author
// can fetch a single sketch memo by
// resource name. The response is
// the Memo proto with the
// SketchPayload envelope populated.
func TestGetSketch_OwnerSucceeds(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memo := newSketchMemo(t, app, alice.Id, "sketch content")
	srv := NewSketchServer(app, nil)

	out, err := srv.GetSketch(withViewer(context.Background(), alice), &v1.GetSketchRequest{
		Name: "memos/" + memo.Id,
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "memos/"+memo.Id, out.GetName())
	require.NotNil(t, out.GetPayload())
	require.NotNil(t, out.GetPayload().GetSketch())
	assert.Equal(t, "PO-33", out.GetPayload().GetSketch().GetModel())
}

// TestGetSketch_NonOwnerNotFound pins
// the cross-tenant guard: a
// non-viewer of a PRIVATE sketch
// gets NotFound (not Forbidden).
func TestGetSketch_NonOwnerNotFound(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	memo := newSketchMemo(t, app, alice.Id, "private")
	srv := NewSketchServer(app, nil)

	_, err := srv.GetSketch(withViewer(context.Background(), bob), &v1.GetSketchRequest{
		Name: "memos/" + memo.Id,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestGetSketch_RejectsTextMemo pins
// the sketch-only rule on the read
// path.
func TestGetSketch_RejectsTextMemo(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	textMemo := core.NewRecord(col)
	textMemo.Set("author", alice.Id)
	textMemo.Set("content", "plain text")
	textMemo.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(textMemo))
	srv := NewSketchServer(app, nil)

	_, err = srv.GetSketch(withViewer(context.Background(), alice), &v1.GetSketchRequest{
		Name: "memos/" + textMemo.Id,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestPendingPullEnvelope_RoundTrip pins
// the JSON shape the connect and REST
// packages share.
func TestPendingPullEnvelope_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	in := pendingPullEnvelope{
		PushID:          random.UUID(),
		SketchMemoID:    "abc123",
		SignedURL:       "/api/po33/sketches/abc/download?exp=1&mac=ff",
		SignedExpiresAt: now.Add(15 * time.Minute),
		VersionNo:       3,
		QueuedAt:        now,
		State:           "QUEUED",
	}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var out pendingPullEnvelope
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, in, out)
}

// TestGetSketch_RequiresAuth pins
// the unauthenticated path: a missing
// viewer never sees a private
// sketch's existence, so the
// response is NotFound (not
// Unauthenticated). A non-existent
// memo id is also NotFound; the two
// cases are indistinguishable to
// the client.
func TestGetSketch_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app, nil)
	_, err := srv.GetSketch(context.Background(), &v1.GetSketchRequest{
		Name: "memos/whatever",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestPushSketch_RequiresAuth pins
// the unauthenticated path on the
// write side.
func TestPushSketch_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewSketchServer(app, nil)
	_, err := srv.PushSketch(context.Background(), &v1.PushSketchRequest{
		Name:   "memos/x",
		Device: "po33-devices/y",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestSignedURL_PathIsCanonical pins
// the path shape the REST
// verification side uses. The
// /download segment is required so
// the device's signed URL lands on
// the right handler.
func TestSignedURL_PathIsCanonical(t *testing.T) {
	got := FormatSignedSketchPath("abc")
	assert.True(t, strings.HasPrefix(got, SignedSketchPathPrefix))
	assert.True(t, strings.HasSuffix(got, "/download"))
}
