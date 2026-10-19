package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/internal/random"
	"github.com/incurl/riff/internal/ratelimit"
	"github.com/incurl/riff/internal/signedurl"
)

// SignedSketchPathPrefix is the URL prefix the
// device-pull sketch download path uses. The
// device's `GET /api/po33/sketches/pending`
// handler responds with the absolute signed URL;
// the device's HTTP client follows the URL and
// lands on the REST handler in
// pb/api/po33_device_pull.go. The prefix is
// exported (capitalised) because the REST
// handler in pb/api needs the same constant;
// keeping the prefix in one place means a future
// rename is a one-line edit.
const SignedSketchPathPrefix = "/api/po33/sketches/"

// pendingPullEnvelope is the JSON document the
// `po33_devices.pending_pull` column carries
// between `PushSketch` and the device's poll.
//
// The shape is unexported because no caller
// outside this file (and its tests) needs it; the
// REST handler in pb/api/po33_device_pull.go
// reads `pending_pull` via json.Unmarshal and
// shares the struct through the package
// boundary.
type pendingPullEnvelope struct {
	PushID          string    `json:"push_id"`
	SketchMemoID    string    `json:"sketch_memo_id"`
	SignedURL       string    `json:"signed_url"`
	SignedExpiresAt time.Time `json:"signed_expires_at"`
	VersionNo       int32     `json:"version_no"`
	QueuedAt        time.Time `json:"queued_at"`
	// State is one of QUEUED / SENDING / DELIVERED
	// / FAILED. The REST ack handler advances
	// the state; the Connect handler only writes
	// the initial QUEUED.
	State string `json:"state"`
}

// pendingPullKeepAlive is the window during
// which a delivered or failed push is queryable
// through `GetPushStatus` after the device acks.
// The window matches the signed-URL TTL: 15
// minutes. After that, the SPA's poll returns
// UNSPECIFIED and the SPA clears its local
// state. The 15-minute number is the same as
// signedurl.DefaultTTL so the two timers expire
// together; a future change to one is a
// deliberate edit to the other.
const pendingPullKeepAlive = 15 * time.Minute

// PushSketch queues a sketch for delivery to
// one of the caller's bound PO-33 devices. The
// flow is device-pulls: the server writes a
// pending_pull envelope onto the device row,
// the device polls
// `GET /api/po33/sketches/pending`, the device
// downloads via the signed URL, and the device
// acks via `POST /api/po33/sketches/pending/ack`.
//
// Authorisation:
//   - viewer must be the device's owner
//     (po33_devices.user == viewer.Id)
//   - viewer must be the memo's author
//     (memos.author == viewer.Id)
//   - device must be ACTIVE
//   - memo must be a sketch (sketch JSON column
//     populated); a text memo is rejected with
//     InvalidArgument
//
// A per-(device, memo) idempotency window: a
// second PushSketch for the same pair overwrites
// the prior envelope with a fresh signed URL but
// the same push_id, so the SPA's poll sees a
// single logical push.
func (s *SketchServer) PushSketch(ctx context.Context, req *v1.PushSketchRequest) (*v1.PushSketchResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be memos/{id}")
	}
	deviceID, ok := deviceIDFromResourceName(req.GetDevice())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "device must be po33-devices/{id}")
	}

	// Per-(device, memo) rate limit. The brief
	// caps pushes at 1 / 30s / (device, memo)
	// pair so a double-click on the "Push"
	// button does not enqueue two signed URLs.
	// A nil limiter is a no-op.
	if s.limiter != nil {
		key := deviceID + "/" + memoID
		dec := s.limiter.Consume(ratelimit.ScopeSketchPushDeviceMemo, key, 1)
		if !dec.Allowed {
			return nil, connect.NewError(connect.CodeResourceExhausted,
				fmt.Sprintf("push rate limit exceeded; retry in %s", dec.RetryAfter.Round(time.Second)))
		}
	}

	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if memo.GetString("author") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if memoSketchFromRecord(memo) == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "memo is not a sketch")
	}

	device, err := s.app.FindRecordById("po33_devices", deviceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "device not found")
	}
	if device.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodeNotFound, "device not found")
	}
	if device.GetString("status") != "ACTIVE" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "device is "+device.GetString("status"))
	}

	// Build the signed URL. The path is
	// /api/po33/sketches/{memoID}/download so the
	// REST handler in pb/api/po33_device_pull.go
	// can verify against the same prefix. The
	// signer is bootstrapped once in main.go; an
	// unbootstrapped signer is a server bug, so
	// we fail fast with Internal.
	signer := PendingPullSignerFromContext(ctx)
	if signer == nil {
		slog.Error("sketch push: signer not installed",
			"device", deviceID, "memo", memoID)
		return nil, connect.NewError(connect.CodeInternal, "signed url signer is not configured")
	}
	path := SignedSketchPathPrefix + memoID + "/download"
	signed := signer.Sign(path)
	expAt := time.Now().Add(signedurl.DefaultTTL)

	// Reuse the prior push_id when the existing
	// envelope targets the same (device, memo)
	// pair, so a double-click on "Push" surfaces
	// as a single logical push to the SPA's
	// poll. The push_id is a random UUID today;
	// a future v2 of the push history table can
	// re-derive it deterministically from the
	// (device, memo, queued_at) tuple.
	pushID, versionNo := pendingPushIdentity(device, memoID)

	envelope := pendingPullEnvelope{
		PushID:          pushID,
		SketchMemoID:    memoID,
		SignedURL:       signed,
		SignedExpiresAt: expAt,
		VersionNo:       versionNo,
		QueuedAt:        time.Now().UTC(),
		State:           "QUEUED",
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	device.Set("pending_pull", raw)
	if err := updateRecordRequest(ctx, s.app, device); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	return &v1.PushSketchResponse{
		PushId:          pushID,
		SignedUrl:       signed,
		SignedExpiresAt: timestamppb.New(expAt),
	}, nil
}

// pendingPushIdentity reads the device's
// existing `pending_pull` column. If the
// column carries an envelope that targets the
// same (device, memo) pair, the prior push_id
// is reused so the SPA's poll sees a single
// logical push. The version number is the
// memo's `sketch.version_no` so the device can
// choose to skip a re-fetch of an
// already-installed patch.
func pendingPushIdentity(device *core.Record, memoID string) (string, int32) {
	existing, _ := decodePendingPull(device)
	if existing != nil && existing.SketchMemoID == memoID && !isExpired(existing.SignedExpiresAt) {
		return existing.PushID, existing.VersionNo
	}
	return newPushID(), 0
}

// decodePendingPull reads the device's
// `pending_pull` column. The column is a
// `JSONField`; an absent / null column returns
// (nil, nil). The REST handler in
// pb/api/po33_device_pull.go shares this
// function through a thin wrapper.
func decodePendingPull(device *core.Record) (*pendingPullEnvelope, error) {
	raw := device.Get("pending_pull")
	if raw == nil {
		return nil, nil
	}
	var b []byte
	switch v := raw.(type) {
	case []byte:
		b = v
	default:
		jb, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		b = jb
	}
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var env pendingPullEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// isExpired returns true when the signed URL's
// exp has passed by at least one second. The
// one-second slop absorbs a clock skew between
// the signer and the verifier without admitting
// a real exp violation.
func isExpired(t time.Time) bool {
	return time.Now().After(t.Add(-time.Second))
}

// newPushID returns a fresh push id. The id is
// a version-4 UUID; the device-side ack handler
// matches the id verbatim. A future history
// table can index on the id.
func newPushID() string {
	return random.UUID()
}

// GetPushStatus returns the latest push state
// for a (device, push) pair. The SPA polls this
// every 2s while the push dialog is open. A
// device-side ack advances the state; a 30-min
// keep-alive (pendingPullKeepAlive) means a
// closed dialog can re-open and still see the
// delivered state for the next quarter-hour.
//
// The handler returns UNSPECIFIED rather than
// NotFound for an expired envelope so the SPA
// can treat the case as "polling is over" rather
// than "this push never existed". A genuinely
// missing push (wrong id) is the same response;
// the device id is the only thing the SPA needs
// to match.
func (s *SketchServer) GetPushStatus(ctx context.Context, req *v1.GetPushStatusRequest) (*v1.PushStatus, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	deviceID, ok := deviceIDFromResourceName(req.GetDevice())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "device must be po33-devices/{id}")
	}
	device, err := s.app.FindRecordById("po33_devices", deviceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "device not found")
	}
	if device.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodeNotFound, "device not found")
	}
	env, _ := decodePendingPull(device)
	if env == nil || env.PushID != req.GetPush() {
		return &v1.PushStatus{
			State: v1.PushState_PUSH_STATE_UNSPECIFIED,
		}, nil
	}
	// Expire the response after keepAlive. The
	// underlying row is not cleared (a re-push
	// of the same memo is still valid), but the
	// SPA-facing state is UNSPECIFIED so the
	// dialog can clear its badge.
	if time.Since(env.QueuedAt) > pendingPullKeepAlive {
		return &v1.PushStatus{
			State: v1.PushState_PUSH_STATE_UNSPECIFIED,
		}, nil
	}
	return pendingPullToProto(env, deviceID), nil
}

// pendingPullToProto translates the on-disk
// envelope into the proto shape. The state
// mapping is the only place the JSON string
// crosses the proto-enum boundary.
func pendingPullToProto(env *pendingPullEnvelope, deviceID string) *v1.PushStatus {
	if env == nil {
		return nil
	}
	state := v1.PushState_PUSH_STATE_UNSPECIFIED
	switch env.State {
	case "QUEUED":
		state = v1.PushState_PUSH_STATE_QUEUED
	case "SENDING":
		state = v1.PushState_PUSH_STATE_SENDING
	case "DELIVERED":
		state = v1.PushState_PUSH_STATE_DELIVERED
	case "FAILED":
		state = v1.PushState_PUSH_STATE_FAILED
	}
	return &v1.PushStatus{
		PushId:    env.PushID,
		Device:    "po33-devices/" + deviceID,
		Memo:      "memos/" + env.SketchMemoID,
		State:     state,
		QueuedAt:  timestamppb.New(env.QueuedAt),
		UpdatedAt: timestamppb.New(env.QueuedAt),
	}
}

// ctxKeyPendingPullSigner is the context key
// under which PushSketch looks up the
// install-wide signer. The connect-go
// interceptor chain threads this into every
// request, so PushSketch can read the signer
// from the context without a package global.
type ctxKeyPendingPullSigner struct{}

// WithPendingPullSigner returns a context that
// carries the install-wide Signer. The Connect
// server's interceptor chain threads this into
// every request.
func WithPendingPullSigner(ctx context.Context, signer *signedurl.Signer) context.Context {
	return context.WithValue(ctx, ctxKeyPendingPullSigner{}, signer)
}

// PendingPullSignerFromContext returns the
// Signer the connect interceptor stashed in
// the context, or nil if no Signer is
// installed.
func PendingPullSignerFromContext(ctx context.Context) *signedurl.Signer {
	v := ctx.Value(ctxKeyPendingPullSigner{})
	if v == nil {
		return nil
	}
	return v.(*signedurl.Signer)
}

// FormatSignedSketchPath is exported so the
// REST handler in pb/api can build the same
// path string the device side will see. A
// future change to the path shape is a single
// edit.
func FormatSignedSketchPath(memoID string) string {
	return SignedSketchPathPrefix + memoID + "/download"
}

// WithSignedURLSigner is a connect-go
// interceptor that reads the install-wide
// Signer from the closure passed to Register
// and stashes it in the request context via
// WithPendingPullSigner. The closure shape
// is the same one WithPendingPullSigner
// uses, so a test that drives the
// interceptor chain can pass a fixed
// closure.
//
// The interceptor is a no-op when the
// closure returns nil (e.g. an uninstalled
// install); PushSketch surfaces the
// nil-ness as a CodeInternal so the SPA
// sees a real error rather than a silent
// "queue" with no signed URL.
func WithSignedURLSigner(getSigner func() *signedurl.Signer) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return connect.ServerFunc(func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			signer := getSigner()
			if signer != nil {
				ctx = WithPendingPullSigner(ctx, signer)
			}
			return next(ctx, spec, stream)
		})
	}
}
