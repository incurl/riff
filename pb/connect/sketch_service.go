package connect

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/ratelimit"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// SketchServer implements the PO-33 SketchService.
// RPCs:
//   - ListMySketches — SPA's "my sketches" page
//   - GetSketch      — SPA's memo detail page
//   - PublishSketch  — flip DRAFT → NORMAL
//   - ArchiveSketch  — flip NORMAL → ARCHIVED
//   - PushSketch     — queue a sketch for a device
//   - GetPushStatus  — poll the push state
//
// The device-side download path
// (/api/po33/sketches/{memo}/download with a
// signed URL) is a REST route rather than a
// Connect RPC because the device runs an
// HTTP/1.1 client with a one-shot signature
// header rather than a Connect library. The
// Connect service is SPA-shaped: it sits behind
// the standard Bearer auth interceptor and serves
// only the caller's own sketch memos.
//
// The limiter parameter is optional. A nil
// limiter keeps the previous unbounded behaviour
// for the read paths (List / Get / Publish /
// Archive / GetPushStatus). PushSketch consults
// the limiter with the per-(device, memo)
// ScopeSketchPushDeviceMemo so a double-click on
// the "Push" button enqueues a single signed
// URL; the consultation lives inside the
// handler rather than in the route-level
// procedureScope table because the bucket key
// is in the request body.
type SketchServer struct {
	apiv1connect.UnimplementedSketchServiceHandler
	app     core.App
	limiter ratelimit.Limiter
}

// NewSketchServer constructs the SketchService
// implementation. Pass nil for limiter to keep
// the unbounded behaviour.
func NewSketchServer(app core.App, limiter ratelimit.Limiter) *SketchServer {
	return &SketchServer{app: app, limiter: limiter}
}

// ListMySketches returns every memo the caller
// authored with `source = "sketch"`. The filter
// deliberately excludes the caller's non-sketch
// memos so a sketch-only page renders without
// extra client-side filtering. The memo's
// visibility rule still applies: a PRIVATE sketch
// only surfaces to its author; a SPACE sketch
// only surfaces to active space members.
func (s *SketchServer) ListMySketches(ctx context.Context, req *v1.ListMySketchesRequest) (*v1.ListMySketchesResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	rows, err := s.app.FindRecordsByFilter("memos",
		"author = {:uid} && sketch != null", "-created",
		int(req.GetPageSize()), 0,
		map[string]any{"uid": viewer.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.Memo, 0, len(rows))
	for _, r := range rows {
		if m := recordToMemo(s.app, r); m != nil {
			out = append(out, m)
		}
	}
	return &v1.ListMySketchesResponse{Memos: out}, nil
}

// GetSketch returns a single sketch memo by
// resource name. The visibility check is the
// same `canViewMemo` predicate `GetMemo` uses, so
// a non-viewer gets NotFound rather than
// Forbidden (the sketch's existence is not
// leaked). A non-sketch memo (no `sketch` JSON
// column) is also NotFound: the SPA's
// `MemoDetail` page only mounts the player on
// the `source === "sketch"` branch, and a stray
// GetSketch against a text memo is a SPA bug.
func (s *SketchServer) GetSketch(ctx context.Context, req *v1.GetSketchRequest) (*v1.Memo, error) {
	id := memoIDFromName(req.GetName())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	rec, err := s.app.FindRecordById("memos", id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "sketch not found")
	}
	if !canViewMemo(s.app, ctx, rec) {
		return nil, connect.NewError(connect.CodeNotFound, "sketch not found")
	}
	if memoSketchFromRecord(rec) == nil {
		// A non-sketch memo. Return NotFound rather
		// than the Memo (which would let a caller
		// retrieve a private text memo via the
		// sketch path). The SPA's MemoDetail page
		// never sends a non-sketch id here; the
		// 404 is the safety net.
		return nil, connect.NewError(connect.CodeNotFound, "sketch not found")
	}
	return recordToMemo(s.app, rec), nil
}
