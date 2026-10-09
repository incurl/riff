package connect

import (
	"context"
	"errors"
	"sync"

	"connectrpc.com/connect/v2"

	"github.com/incurl/riff/internal/linkmeta"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// linkMetaFetcher returns the *linkmeta.HTMLMetaFetcher the
// MemoServer uses for link preview metadata. Production calls
// resolve the same shared instance so the library's cache and
// concurrency limits cover every caller, even when the server is
// reached through REST and Connect at the same time. Tests
// override the per-server field setLinkMetaFetcher sets.
var linkMetaFetcher = sync.OnceValue(func() *linkmeta.HTMLMetaFetcher {
	return linkmeta.NewHTMLMetaFetcher()
})

// GetLinkMetadata fetches OG / Twitter / JSON-LD / standard /
// semantic metadata for a single URL. The result is the same
// shape the Memos React client renders; the mapping is trivial
// because the proto's LinkMetadata was modelled on linkmeta's
// HTMLMeta.
//
// Authentication is required: the endpoint is reachable from
// any signed-in browser, and an unauthenticated variant would
// let anyone use the server as an open SSRF probe. The fetcher
// already rejects internal-IP targets, so the worst an
// authenticated caller can do is drain the server's bandwidth.
func (s *MemoServer) GetLinkMetadata(ctx context.Context, req *v1.GetLinkMetadataRequest) (*v1.LinkMetadata, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	url := req.GetUrl()
	if url == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "url is required")
	}

	meta, err := s.fetcher().Get(ctx, url)
	if err != nil {
		return nil, mapLinkMetaError(err)
	}
	return htmlMetaToProto(url, meta), nil
}

// BatchGetLinkMetadata fetches metadata for up to N URLs in
// parallel. The library coalesces in-flight requests for the same
// URL through singleflight, so repeated URLs in a batch are not
// fetched twice. Results are returned in the same order as the
// input; a per-URL failure is reported in that slot's metadata
// so callers can distinguish "the page returned nothing" from
// "the page could not be reached".
func (s *MemoServer) BatchGetLinkMetadata(ctx context.Context, req *v1.BatchGetLinkMetadataRequest) (*v1.BatchGetLinkMetadataResponse, error) {
	if CurrentUser(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	urls := req.GetUrls()
	if len(urls) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "urls is required")
	}

	fetcher := s.fetcher()
	results := make([]*v1.LinkMetadata, len(urls))

	type indexed struct {
		idx  int
		meta *linkmeta.HTMLMeta
		err  error
	}
	ch := make(chan indexed, len(urls))

	for i, u := range urls {
		go func(idx int, url string) {
			meta, err := fetcher.Get(ctx, url)
			ch <- indexed{idx: idx, meta: meta, err: err}
		}(i, u)
	}
	for range urls {
		r := <-ch
		if r.err != nil {
			// Per-URL failures become empty metadata in
			// the same slot so callers can distinguish
			// "the page returned nothing" from "the
			// page could not be reached" by inspecting
			// the URL alone — the React composer falls
			// back to rendering the URL with no title.
			results[r.idx] = &v1.LinkMetadata{Url: urls[r.idx]}
			continue
		}
		results[r.idx] = htmlMetaToProto(urls[r.idx], r.meta)
	}
	return &v1.BatchGetLinkMetadataResponse{LinkMetadata: results}, nil
}

// htmlMetaToProto projects the library's HTMLMeta into the proto
// type. The proto's Image is "" when the page had none, which the
// React client already handles.
func htmlMetaToProto(url string, meta *linkmeta.HTMLMeta) *v1.LinkMetadata {
	return &v1.LinkMetadata{
		Url:         url,
		Title:       meta.Title,
		Description: meta.Description,
		Image:       meta.Image,
	}
}

// mapLinkMetaError translates the library's error vocabulary to
// Connect codes. The two real cases the React client can recover
// from are invalid input (the URL itself was malformed) and the
// fetch failing (network, server-side SSRF block, timeout).
// Everything else is an Internal that the operator should see in
// the logs.
func mapLinkMetaError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, linkmeta.ErrInvalidURL) {
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	if errors.Is(err, linkmeta.ErrInternalIP) {
		return connect.NewError(connect.CodeFailedPrecondition, "url targets a disallowed address")
	}
	return connect.NewError(connect.CodeInternal, err.Error())
}

// fetcher returns the fetcher the handler should use. Production
// uses the package-level singleton so cache + concurrency limits
// are shared across every MemoServer; tests can override the
// per-server field through setLinkMetaFetcher.
func (s *MemoServer) fetcher() *linkmeta.HTMLMetaFetcher {
	if s.linkMetaFetcher != nil {
		return s.linkMetaFetcher
	}
	return linkMetaFetcher()
}

// setLinkMetaFetcher injects a fetcher on this server instance.
// It returns the server for chaining, matching the style of the
// other memo-server tests. Exposed only to the test package.
func (s *MemoServer) setLinkMetaFetcher(f *linkmeta.HTMLMetaFetcher) *MemoServer {
	s.linkMetaFetcher = f
	return s
}
