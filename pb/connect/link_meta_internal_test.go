package connect

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/linkmeta"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// linkMetaTestFetcher builds a fetcher that talks to the supplied
// transport. WithTransport is the public hook the library
// exposes for this; the SSRF-safe defaults (redirect policy,
// dialer) are otherwise unchanged.
func linkMetaTestFetcher(transport http.RoundTripper) *linkmeta.HTMLMetaFetcher {
	return linkmeta.NewHTMLMetaFetcher().WithTransport(transport)
}

func linkMetaTestHTML(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

// TestGetLinkMetadata_OpenGraphPrecedence covers the happy path
// the React client relies on: an OG-rich page returns its OG
// title, description, and image (with the image URL resolved
// against the page URL, not the test transport's scheme).
func TestGetLinkMetadata_OpenGraphPrecedence(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "/article", req.URL.Path)
		return linkMetaTestHTML(req, `<html><head>
			<title>Standard title</title>
			<meta property="og:title" content="OG title">
			<meta property="og:description" content="OG description">
			<meta property="og:image" content="/cover.png">
		</head></html>`), nil
	})))
	ctx := withViewer(context.Background(), alice)

	got, err := srv.GetLinkMetadata(ctx, &v1.GetLinkMetadataRequest{Url: "https://example.com/article"})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/article", got.GetUrl())
	assert.Equal(t, "OG title", got.GetTitle(), "OG title outranks the <title>")
	assert.Equal(t, "OG description", got.GetDescription())
	assert.Equal(t, "https://example.com/cover.png", got.GetImage(), "image must be resolved against the page URL")
}

// TestGetLinkMetadata_RequiresAuth asserts anonymous callers
// cannot use the server as an SSRF probe.
func TestGetLinkMetadata_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport must not be hit for an unauthenticated request")
		return nil, nil
	})))

	_, err := srv.GetLinkMetadata(context.Background(), &v1.GetLinkMetadataRequest{Url: "https://example.com/"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestGetLinkMetadata_RejectsEmptyURL guards the URL field
// before any network call.
func TestGetLinkMetadata_RejectsEmptyURL(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport must not be hit when the URL is empty")
		return nil, nil
	})))
	ctx := withViewer(context.Background(), alice)

	_, err := srv.GetLinkMetadata(ctx, &v1.GetLinkMetadataRequest{Url: ""})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestGetLinkMetadata_RejectsInternalIP maps the library's
// ErrInternalIP to FailedPrecondition so the client can show a
// human-readable message instead of an opaque Internal.
func TestGetLinkMetadata_RejectsInternalIP(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport must not be hit for an internal-IP URL")
		return nil, nil
	})))
	ctx := withViewer(context.Background(), alice)

	_, err := srv.GetLinkMetadata(ctx, &v1.GetLinkMetadataRequest{Url: "http://127.0.0.1/admin"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "got %v", err)
}

// TestGetLinkMetadata_RejectsInvalidScheme guards against
// non-http(s) URLs being passed through to the transport.
func TestGetLinkMetadata_RejectsInvalidScheme(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport must not be hit for a non-http(s) URL")
		return nil, nil
	})))
	ctx := withViewer(context.Background(), alice)

	_, err := srv.GetLinkMetadata(ctx, &v1.GetLinkMetadataRequest{Url: "file:///etc/passwd"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestGetLinkMetadata_PageWithoutOG confirms the standard
// metadata path still produces a usable title when the page
// has no OG / Twitter / JSON-LD tags.
func TestGetLinkMetadata_PageWithoutOG(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return linkMetaTestHTML(req, `<html><head><title>Just a title</title></head><body><p>Body text.</p></body></html>`), nil
	})))
	ctx := withViewer(context.Background(), alice)

	got, err := srv.GetLinkMetadata(ctx, &v1.GetLinkMetadataRequest{Url: "https://example.com/plain"})
	require.NoError(t, err)
	assert.Equal(t, "Just a title", got.GetTitle())
	assert.Empty(t, got.GetDescription(), "no description source, no description")
	assert.Empty(t, got.GetImage())
}

// TestGetLinkMetadata_Non2xxReturnsError documents the
// contract: the library treats anything outside 2xx as an
// error, and we surface that as a Connect Internal. The React
// client renders the URL alone when the metadata RPC fails,
// which is the user-facing behaviour we want.
func TestGetLinkMetadata_Non2xxReturnsError(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     http.StatusText(http.StatusNotFound),
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("not found")),
			Request:    req,
		}, nil
	})))
	ctx := withViewer(context.Background(), alice)

	_, err := srv.GetLinkMetadata(ctx, &v1.GetLinkMetadataRequest{Url: "https://example.com/missing"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// TestBatchGetLinkMetadata_PreservesOrderAndToleratesFailures
// is the load-bearing test for the batch path: two valid URLs
// and one that the library will reject must come back in input
// order, with the failed URL still represented in the response
// (so the React client can map errors back to input rows).
func TestBatchGetLinkMetadata_PreservesOrderAndToleratesFailures(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	var calls atomic.Int32
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		switch req.URL.Path {
		case "/first":
			return linkMetaTestHTML(req, `<html><head><meta property="og:title" content="First"></head></html>`), nil
		case "/second":
			return linkMetaTestHTML(req, `<html><head><meta property="og:title" content="Second"></head></html>`), nil
		default:
			t.Fatalf("unexpected request: %s", req.URL.String())
			return nil, nil
		}
	})))
	ctx := withViewer(context.Background(), alice)

	resp, err := srv.BatchGetLinkMetadata(ctx, &v1.BatchGetLinkMetadataRequest{
		Urls: []string{
			"https://example.com/first",
			"http://127.0.0.1/blocked", // ErrInternalIP — must not hit transport
			"https://example.com/second",
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetLinkMetadata(), 3)
	assert.Equal(t, "https://example.com/first", resp.GetLinkMetadata()[0].GetUrl())
	assert.Equal(t, "First", resp.GetLinkMetadata()[0].GetTitle())
	assert.Equal(t, "http://127.0.0.1/blocked", resp.GetLinkMetadata()[1].GetUrl(), "the blocked URL must still appear in its slot")
	assert.Empty(t, resp.GetLinkMetadata()[1].GetTitle(), "a failed URL must not have metadata")
	assert.Equal(t, "https://example.com/second", resp.GetLinkMetadata()[2].GetUrl())
	assert.Equal(t, "Second", resp.GetLinkMetadata()[2].GetTitle())
	assert.Equal(t, int32(2), calls.Load(), "internal-IP URL must not be fetched")
}

// TestBatchGetLinkMetadata_RequiresAuth mirrors the single-URL
// auth check; the batch path can be tempting to skip auth on
// when scaling client-side, so we keep both tests honest.
func TestBatchGetLinkMetadata_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport must not be hit for an unauthenticated batch")
		return nil, nil
	})))

	_, err := srv.BatchGetLinkMetadata(context.Background(), &v1.BatchGetLinkMetadataRequest{
		Urls: []string{"https://example.com/"},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestBatchGetLinkMetadata_RejectsEmptyList guards the request
// body: BatchGetLinkMetadata with an empty urls array is a
// client bug, not a no-op.
func TestBatchGetLinkMetadata_RejectsEmptyList(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app).setLinkMetaFetcher(linkMetaTestFetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport must not be hit for an empty batch")
		return nil, nil
	})))
	ctx := withViewer(context.Background(), alice)

	_, err := srv.BatchGetLinkMetadata(ctx, &v1.BatchGetLinkMetadataRequest{Urls: nil})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
