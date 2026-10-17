package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/incurl/riff/internal/ratelimit"
	"github.com/incurl/riff/pb/connect"
)

// upstreamMemosHandlerFactory picks the right handler for one row
// of upstreamMemosRoutes. The five flavors — connect_proxy,
// pb_records_crud, custom, file_server, sse — all share the
// same input/output type (core.RequestEvent) and the same
// rate-limit registration shape, so the only difference is the
// handler closure. The factory pattern keeps the route-table
// loop in mountUpstreamMemosRoutes a one-liner per row.
func upstreamMemosHandlerFactory(app core.App, entry upstreamRouteEntry) func(*core.RequestEvent) error {
	switch entry.Flavor {
	case "connect_proxy":
		return connectProxyHandler(app, entry.Target)
	case "pb_records_crud":
		return pbRecordsCRUDHandler(app, entry.Target)
	case "custom":
		return customBuilder(app, entry.Builder)
	case "file_server":
		return fileServerHandler(app, entry.Builder)
	case "sse":
		return sseProxyHandler(app)
	default:
		// Unknown flavor — fail loud at registration time.
		// A typo in upstreamMemosRoutes should not silently
		// match a no-op handler; a 500 on the first request
		// is a clear bug-report signal.
		return unknownFlavorHandler(entry.Flavor)
	}
}

// connectProxyHandler returns a handler that forwards the
// incoming request to the given Connect procedure. The
// bridge is:
//
//  1. Read the original body (always buffered; the
//     proxy may be called with a streaming body for
//     upload-style calls, and the connect handler
//     requires a fully-buffered request).
//  2. Build a new *http.Request whose path is the
//     procedure path (e.g. /memos.api.v1.MemoService/
//     GetMemo) and whose URL query string is copied
//     verbatim. The headers and method are cloned
//     so the connect handler's withAuth interceptor
//     re-resolves the bearer token.
//  3. Serve that request on the in-process connect
//     ServeMux (set at Register time via
//     installConnectProxyMux). The ServeMux dispatches
//     to the same connect.Server that owns the
//     /memos.api.v1.* routes, so the rule engine,
//     hooks, and rate-limiter are all exactly the
//     same.
//  4. Copy the response (status, headers, body) back
//     onto the original core.RequestEvent.
//
// No business logic lives here. Every Memos-shaped
// concern is owned by the connect handler; this file
// is purely an HTTP-path translator.
func connectProxyHandler(app core.App, procedure string) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		mux := connectProxyMux()
		if mux == nil {
			// Registration didn't install a proxy mux.
			// The factory in production always has one
			// (see installConnectProxyMux); a missing
			// mux is a programming error in tests, so
			// return 503 rather than panic.
			e.Response.Header().Set("Content-Type", "application/json")
			e.Response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(e.Response, `{"code":13,"message":"connect proxy mux not installed"}`)
			return nil
		}

		// 1. Read body once. Some procedures (exportMemos
		// returns a HttpBody) need the request body
		// fully buffered because the in-process dispatch
		// re-parses it. A GET request that arrives
		// with no body is forwarded as `{}` so the
		// connect handler's protojson.Unmarshal finds
		// a valid empty-object value to parse; the
		// gateway equivalent (`runtime.NewServeMux`)
		// does the same for GET-with-no-body
		// requests.
		var bodyBytes []byte
		if e.Request.Body != nil {
			b, err := io.ReadAll(e.Request.Body)
			if err != nil {
				e.Response.Header().Set("Content-Type", "application/json")
				e.Response.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(e.Response, `{"code":3,"message":"could not read request body"}`)
				return nil
			}
			bodyBytes = b
		}
		if len(bodyBytes) == 0 {
			bodyBytes = []byte("{}")
		}

		// 2. Build the in-process request. We
		// always send POST to the connect
		// procedure regardless of the original
		// HTTP verb: connect-go v2 only allows
		// GET for procedures marked
		// IdempotencyNoSideEffects, which none
		// of the Memos Connect handlers are.
		// The proto's `option (google.api.http)
		// = {get: ...}` annotation is OpenAPI-only
		// (it does not affect connect's server
		// behaviour), so the connect handler
		// does not care which verb the request
		// used. Re-encoding the method as POST
		// keeps the proxy uniform across all 84
		// procedures without per-route branching.
		upstream, err := http.NewRequestWithContext(
			e.Request.Context(),
			http.MethodPost,
			procedure,
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			e.Response.Header().Set("Content-Type", "application/json")
			e.Response.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(e.Response, `{"code":13,"message":"could not build upstream request"}`)
			return nil
		}
		// Clone the headers (so the Authorization header
		// is forwarded verbatim) and the query string.
		upstream.Header = e.Request.Header.Clone()
		upstream.URL.RawQuery = e.Request.URL.RawQuery
		// Connect's Content-Type negotiation. Without
		// one of `application/json`,
		// `application/proto`, or `application/grpc`
		// in the Accept-Post header set, the connect
		// dispatcher returns 415 Unsupported Media
		// Type. The JSON variant is the Memos default
		// and matches what the connect handler emits.
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
		// Make sure the in-process dispatch sees the
		// same view of who the caller is. The connect
		// handler's withAuth interceptor also reads the
		// Bearer token, so this is belt-and-suspenders.
		if e.Auth != nil {
			ctx := contextWithViewer(upstream.Context(), e.Auth)
			upstream = upstream.WithContext(ctx)
		}

		// 3. Dispatch.
		resp := newConnectProxyResponseWriter()
		mux.ServeHTTP(resp, upstream)

		// 4. Copy the response back onto the original
		// writer. The connect handler emits a single
		// header block + body; we replay both.
		for k, vv := range resp.headers {
			for _, v := range vv {
				e.Response.Header().Add(k, v)
			}
		}
		status := resp.status
		if status == 0 {
			status = http.StatusOK
		}
		e.Response.WriteHeader(status)
		if _, err := io.Copy(e.Response, &resp.body); err != nil {
			return errors.Wrap(err, "write proxy response")
		}
		return nil
	}
}

// connectProxyResponseWriter is a minimal http.ResponseWriter
// that buffers the upstream response so the bridge can copy
// headers, status, and body back onto the original
// core.RequestEvent. connect-go writes the body via Write, so
// a bytes.Buffer is sufficient.
type connectProxyResponseWriter struct {
	status  int
	headers http.Header
	body    bytes.Buffer
}

func newConnectProxyResponseWriter() *connectProxyResponseWriter {
	return &connectProxyResponseWriter{headers: http.Header{}}
}

func (w *connectProxyResponseWriter) Header() http.Header { return w.headers }
func (w *connectProxyResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *connectProxyResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

// connectProxyMux returns the in-process http.ServeMux the
// bridge dispatches against. It is the same mux the connect
// package's Register installed for the public /memos.api.v1.*
// surface, so the proxy and the Connect surface share
// withAuth, the rule engine, and the rate-limit middleware.
// A nil mux means connect.Register has not run yet (the call
// happens at OnServe time); the handler returns 503 in that
// case.
func connectProxyMux() *http.ServeMux {
	return connect.ConnectMux()
}

// restRateLimit returns a PocketBase Route.BindFunc that
// consumes one unit from the limiter under the given scope
// before the route handler runs. It mirrors the
// RateLimit middleware in pb/connect/ratelimit.go but is
// separate because the connect side expects a connect-go
// error envelope and the REST side emits a Memos-shape
// JSON error.
func restRateLimit(limiter ratelimit.Limiter, scope ratelimit.Scope) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		key := e.RealIP()
		if e.Auth != nil {
			key = e.Auth.Id
		}
		decision := limiter.Consume(scope, key, 1)
		if !decision.Allowed {
			e.Response.Header().Set("Content-Type", "application/json")
			e.Response.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(e.Response, `{"code":8,"message":"rate limit exceeded"}`)
			return nil
		}
		return e.Next()
	}
}

// contextWithViewer stashes the auth record on the context
// under a key the connect handler can pick up. The connect
// interceptor also reads the Bearer token, so this is a
// belt-and-suspenders path for handlers that read the
// viewer through the connect helper.
type ctxKeyConnectViewer struct{}

// contextWithViewer adds the auth record to the context so
// downstream handlers that look for it can short-circuit
// the FindAuthRecordByToken call.
func contextWithViewer(ctx context.Context, rec *core.Record) context.Context {
	return context.WithValue(ctx, ctxKeyConnectViewer{}, rec)
}

// unknownFlavorHandler returns a closure that emits a 500
// the first time it runs. It is a safety net for typos in
// upstreamMemosRoutes.
func unknownFlavorHandler(flavor string) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		e.Response.Header().Set("Content-Type", "application/json")
		e.Response.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(e.Response,
			fmt.Sprintf(`{"code":13,"message":"upstream memos REST: unknown flavor %q"}`, flavor))
		return nil
	}
}

// matchMethod returns the appropriate router method
// (GET/POST/...) for a given HTTP verb. PocketBase's
// router has one method per verb; this helper centralises
// the switch so the registration loop stays readable.
func matchMethod(r *router.Router[*core.RequestEvent], method string) func(string, func(*core.RequestEvent) error) *router.Route[*core.RequestEvent] {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return r.GET
	case http.MethodPost:
		return r.POST
	case http.MethodPatch:
		return r.PATCH
	case http.MethodPut:
		return r.PUT
	case http.MethodDelete:
		return r.DELETE
	default:
		// Fall back to Any; PocketBase's router does
		// not have a generic method here, so the
		// registration loop should never call this
		// with an unknown verb.
		return r.Any
	}
}
