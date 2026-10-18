package connect

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// SketchServer implements the PO-33 SketchService.
// One RPC today (ListMySketches) plus the
// converter glue that turns `memos` rows into
// Memo protos with the SketchPayload envelope
// populated.
//
// The actual device download path
// (/api/po33/sketches/download) is a REST route
// rather than a Connect RPC because the device
// runs an HTTP/1.1 client with a one-shot
// signature header rather than a Connect library.
// The Connect service is SPA-shaped: it sits behind
// the standard Bearer auth interceptor and serves
// only the caller's own sketch memos.
type SketchServer struct {
	apiv1connect.UnimplementedSketchServiceHandler
	app core.App
}

// NewSketchServer constructs the SketchService
// implementation.
func NewSketchServer(app core.App) *SketchServer {
	return &SketchServer{app: app}
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
