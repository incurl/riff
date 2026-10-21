package connect

import (
	"context"
	"regexp"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// deviceIDPattern is the row id format PocketBase
// itself uses. We re-validate the segment between
// the resource name prefix and the call site to
// reject a name like "po33-devices/../../etc/passwd"
// before the DB ever sees it.
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{15}$`)

// DeviceServer implements the PO-33
// DeviceService. Three RPCs today:
//
//   - ListDevices — the SPA's "my devices" page.
//   - GetDevice — single device by resource name.
//   - RevokeDevice — flip status to REVOKED; the
//     device_revoked hook then nulls the live
//     `device` relation on every patch/sample the
//     device uploaded and copies the fingerprint
//     into the per-attachment snapshot column.
//
// The handler is the sole authority for "who can
// read or mutate the device row". The migration
// sets every `*Rule` field to nil so the PocketBase
// rule engine cannot be tricked into letting a
// non-owner through the collection REST surface.
// The rate-limit scope rows for List / Get /
// Revoke are wired in `pb/connect/ratelimit.go`.
type DeviceServer struct {
	apiv1connect.UnimplementedDeviceServiceHandler
	app core.App
}

// NewDeviceServer constructs the
// DeviceService implementation.
func NewDeviceServer(app core.App) *DeviceServer {
	return &DeviceServer{app: app}
}

// ListDevices returns every device the caller owns.
// The result is sorted by created-desc so the most
// recently paired device is the first card on the
// SPA. Pagination is offset-based: page_size is
// clamped to 1..1000 and page_token is an opaque
// base64 offset (see pageWindow / nextPageToken);
// an empty page_token means "from the start".
func (s *DeviceServer) ListDevices(ctx context.Context, req *v1.ListDevicesRequest) (*v1.ListDevicesResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	limit, offset, err := pageWindow(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	rows, err := s.app.FindRecordsByFilter("po33_devices",
		"user = {:uid}", "-created",
		limit, offset,
		map[string]any{"uid": viewer.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.Device, 0, len(rows))
	for _, r := range rows {
		if d := recordToDevice(r); d != nil {
			out = append(out, d)
		}
	}
	return &v1.ListDevicesResponse{Devices: out, NextPageToken: nextPageToken(offset, limit, len(rows))}, nil
}

// GetDevice returns a single device by resource name.
// The caller must be the device owner; a non-owner
// gets PermissionDenied so we don't leak the
// device's existence to a stranger. The resource
// name `po33-devices/{id}` is parsed inline; the
// pattern check rejects a name with slashes that
// would otherwise be turned into a FindRecordById
// for an arbitrary id.
func (s *DeviceServer) GetDevice(ctx context.Context, req *v1.GetDeviceRequest) (*v1.Device, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	id, ok := deviceIDFromResourceName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be po33-devices/{id}")
	}
	rec, err := s.app.FindRecordById("po33_devices", id)
	if err != nil || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, "device not found")
	}
	if rec.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "device is not yours")
	}
	return recordToDevice(rec), nil
}

// RevokeDevice marks the device as REVOKED. The
// device is not deleted — the row stays so the
// po33_sketch_downloads audit log retains its
// relation and the per-attachment snapshot column
// can be populated from the live `device` relation
// before it is nulled.
//
// The actual cascade (null `device` on patches/
// samples, copy fingerprint into
// `device_fingerprint_snapshot`) is implemented in
// `pb/hooks/device_revoked.go` as an
// OnRecordAfterUpdateSuccess hook. The hook reads
// `status` and only acts on the ACTIVE -> REVOKED
// transition, so a re-revoke of an already-revoked
// device is a no-op.
func (s *DeviceServer) RevokeDevice(ctx context.Context, req *v1.RevokeDeviceRequest) (*emptypb.Empty, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	id, ok := deviceIDFromResourceName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be po33-devices/{id}")
	}
	rec, err := s.app.FindRecordById("po33_devices", id)
	if err != nil || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, "device not found")
	}
	if rec.GetString("user") != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "device is not yours")
	}
	if rec.GetString("status") == "REVOKED" {
		// Idempotent: a second revoke is a no-op.
		return &emptypb.Empty{}, nil
	}
	rec.Set("status", "REVOKED")
	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// deviceIDFromResourceName strips the
// `po33-devices/` prefix and validates the row id
// against the PB shape. Returns ("", false) on any
// malformed input.
func deviceIDFromResourceName(name string) (string, bool) {
	const prefix = "po33-devices/"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(name, prefix)
	if !deviceIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}
