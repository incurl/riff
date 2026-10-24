// Package connect wires the Memos Connect/gRPC services onto a
// PocketBase app. The generated client code under
// proto/gen/api/v1/apiv1connect expects handlers that satisfy
// the per-service Handler interface; this package provides
// concrete implementations that translate between the protobuf
// types and the PocketBase record types.
//
// Phase 4 landed the read path the Memos frontend needs to boot.
// Wave 4 then made the write path real and filled in the memo
// interaction and space governance layers. Methods that still have
// no backing collection in riff's schema return
// connect.CodeUnimplemented or an explicit precondition failure, so
// the frontend sees a precise error instead of a server-side panic.
//
// Every write goes through the record-request bridge in records.go:
// a synthetic *core.RequestEvent fed to PocketBase's OnRecord*Request
// hook chain, so the rules in pb/hooks apply exactly as they do to a
// collection REST call. See that file for why a bare app.Save is not
// sufficient.
//
// ratelimit.go exposes a RateLimit middleware that any Connect or
// REST route can attach via Route.BindFunc; it consumes one unit per
// request from a ratelimit.Limiter and is a no-op when no limiter is
// configured, so feature work can land before the policy wiring
// exists. Register() consults procedureScope (in ratelimit.go) to
// decide which Connect procedures get the middleware; the rest fall
// through to the catch-all that the authorizer (a future wave) will
// layer on top.
//
// The package-level connectMux variable holds the in-process
// http.ServeMux that Mount installs on Register. pb/api reads
// it through ConnectMux() to dispatch the /memos/api/... REST
// surface through the same connect.Server that answers the
// public /memos.api.v1.* Connect procedures. Sharing the
// server keeps the two surfaces byte-identical without
// duplicating any business logic.
package connect

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/incurl/riff/internal/ratelimit"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// Register mounts the Connect-RPC services onto the PocketBase
// router. Memos uses Connect over HTTP/1.1; the standard URL
// scheme is /<package>.<service>/<Method>.
//
// limiter is the rate limiter to consult for every known procedure.
// Pass nil to keep the previous unbounded behaviour; the registration
// loop and the per-procedure BindFunc both treat a nil limiter as a
// no-op (see RateLimit in ratelimit.go). When a non-nil limiter is
// passed, the loop in OnServe consults procedureScope (also in
// ratelimit.go) and attaches a Route.BindFunc(RateLimit(...)) for every
// procedure that has a row.
//
// Register also bootstraps the install-wide
// signed-URL signer used by the SketchService
// push flow (see sketch_signer_bootstrap.go).
// The signer is read or minted on the first
// OnServe pass and installed in the connect-go
// interceptor chain so every request that
// reaches PushSketch can read the signer via
// PendingPullSignerFromContext.
// ftsSearch is set from Profile.FTS (RIFF_FTS) by SetFTSSearch before
// Register runs. It is a package-level pointer rather than a parameter
// because Register is called from a dozen test files that pass no
// profile; the setter keeps every one of them compiling unchanged.
var ftsSearch bool

// SetFTSSearch enables the FTS5-backed content search for the servers
// this package registers. It must be called before Register.
//
// Off by default: an instance only changes search behaviour when an
// operator sets RIFF_FTS=1.
func SetFTSSearch(enabled bool) { ftsSearch = enabled }

func Register(app core.App, limiter ratelimit.Limiter) error {
	// Bootstrap the signer. The hook runs
	// before the HTTP listener is bound,
	// so every request that reaches the
	// connect-go mux sees a non-nil
	// signer. A second Register() call
	// (test path) is a no-op because the
	// persisted key is stable across
	// boots.
	installSignedURLSigner(app)
	srv := connect.NewServer(
		WithAuth(app),
		WithSignedURLSigner(InstallSignedURLSignerForREST),
		WithErrorLogging(),
	)

	// Implemented services.
	apiv1connect.RegisterAuthServiceHandler(srv, NewAuthServer(app, limiter))
	apiv1connect.RegisterUserServiceHandler(srv, NewUserServer(app))
	apiv1connect.RegisterSpaceServiceHandler(srv, NewSpaceServer(app))
	memoSrv := NewMemoServer(app)
	memoSrv.SetFTSSearch(ftsSearch)
	apiv1connect.RegisterMemoServiceHandler(srv, memoSrv)
	apiv1connect.RegisterAttachmentServiceHandler(srv, NewAttachmentServer(app))
	apiv1connect.RegisterInstanceServiceHandler(srv, NewInstanceServer(app))
	apiv1connect.RegisterAIServiceHandler(srv, NewAIServer(app))

	// IdentityProviderService — admin-gated CRUD for the
	// `identity_providers` table. The Connect handler is
	// the sole authority (the collection's *Rule fields
	// are nil). See `identity_providers.go` for the
	// List/Get/Create/Update/Delete implementations and
	// `auth_sso.go` for the SignIn sso_credentials branch
	// that consumes the `config` blob.
	apiv1connect.RegisterIdentityProviderServiceHandler(srv, NewIdentityProviderServer(app))

	// DeviceService — PO-33 device binding CRUD. The
	// SPA-facing read side (List / Get) and the
	// device-binding write side (Revoke) live here.
	// The device-side signing handshake lives in
	// /api/po33/pair/* (REST, no auth, bearer-keyed)
	// rather than in this service because the device
	// has no auth token until after it pairs.
	apiv1connect.RegisterDeviceServiceHandler(srv, NewDeviceServer(app))

	// SketchService — SPA-facing read of the caller's
	// own sketch memos. The actual device download
	// path is /api/po33/sketches/download (REST) and
	// the device upload path is a future slice. The
	// server also implements PushSketch and
	// GetPushStatus for the device-pull push flow
	// (see docs/design/po33-device-binding.md,
	// 2026-10-19 appendix).
	apiv1connect.RegisterSketchServiceHandler(srv, NewSketchServer(app, limiter))

	// Mount the connect-go HTTP handlers on a stdlib mux.
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	// Publish the mux so the upstream-memos REST surface in
	// pb/api/upstream_memos_rest_proxy.go can dispatch through
	// the same connect.Server without rebuilding the handlers
	// (and re-running every Register call's hook chain).
	connectMuxMu.Lock()
	connectMux = mux
	connectMuxMu.Unlock()

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// Start the import-session sweeper so staged
		// import archives older than 30 minutes are
		// reaped. The sweeper honours app.OnTerminate
		// so a test app's teardown does not leak a
		// goroutine. The upload-session sweeper was
		// installed in slice 2 (commit c5ca6a7c).
		installImportManager(app)
		registerConnectRoutes(srv, mux, e.Router, limiter)
		return e.Next()
	})
	return nil
}

// registerConnectRoutes adds one PocketBase route per Connect
// procedure exposed by srv, dispatching each one to the connect-go
// stdlib mux. When limiter is non-nil and a procedure is present in
// procedureScope (ratelimit.go), the route gets a RateLimit BindFunc
// so the policy is enforced before the connect-go handler runs.
// Procedures with no row in the table fall through to the catch-all
// budgets that the authorizer (a future wave) will layer on top.
//
// Extracted from Register so tests can drive the same registration
// path against a router they control, without spinning up the full
// OnServe / app.Start machinery.
func registerConnectRoutes(srv *connect.Server, mux *http.ServeMux, r *router.Router[*core.RequestEvent], limiter ratelimit.Limiter) {
	for spec := range srv.Specs() {
		procedure := spec.Procedure // e.g. /memos.api.v1.AuthService/SignIn
		route := r.Any(procedure, func(re *core.RequestEvent) error {
			mux.ServeHTTP(re.Response, re.Request)
			return nil
		})
		if scope, ok := procedureScope[procedure]; ok {
			route.BindFunc(RateLimit(limiter, scope, IPKey))
		}
	}
}

// WithAuth is a server-side interceptor that authenticates the
// caller via a Bearer token and stashes the *core.Record in
// the context. Handlers retrieve the current user via
// CurrentUser(ctx).
//
// In connect-go v2, server interceptors have the signature
// func(next connect.ServerFunc) connect.ServerFunc. The request
// headers are reached via CallInfo stored in the context.
//
// Two token shapes are accepted:
//
//  1. A PocketBase auth JWT for the `users` collection, resolved
//     via FindAuthRecordByToken. The accepted token types are
//     restricted to core.TokenTypeAuth so refresh/file tokens
//     cannot be replayed here.
//
//  2. A riff-issued "memos_pat_*" personal access token, resolved
//     via resolvePersonalAccessToken. PATs are structurally
//     opaque (not JWS) and their only representation on the
//     server side is a bcrypt digest, which FindAuthRecordByToken
//     cannot parse; the PAT branch is the read path for the
//     UserService.PersonalAccessToken RPCs.
//
// The JWT branch runs first because it is the cheap fast path for
// the common case. A non-PAT token (anything that does not start
// with the memos_pat_ prefix) is left to the JWT resolver's
// verdict; a token with the right prefix that does not match any
// row falls through to "unauthenticated" the same way a wrong JWT
// does. Errors from the PAT branch are swallowed: the contract is
// "wrong/expired/revoked token == anonymous", so a 401 is
// produced one layer up by the per-procedure CurrentUser guard.
func WithAuth(app core.App) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return connect.ServerFunc(func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if info, ok := connect.CallInfoForServerContext(ctx); ok {
				hdr := info.RequestHeader()
				tok := bearerToken(hdr.Get("Authorization"))
				if tok != "" {
					rec, err := app.FindAuthRecordByToken(tok, core.TokenTypeAuth)
					if err == nil {
						ctx = context.WithValue(ctx, ctxKeyAuthRecord{}, rec)
					} else if pat, perr := resolvePersonalAccessToken(app, tok); perr == nil && pat != nil {
						// PAT verified; record lives in the
						// same users collection a JWT would
						// resolve to, so handlers see a
						// uniform *core.Record via
						// CurrentUser(ctx).
						ctx = context.WithValue(ctx, ctxKeyAuthRecord{}, pat)
					}
				}
				// The user-facing origin is stashed in the
				// same request slot — see ctxKeyRequestOrigin.
				// Even when the request lands unauthenticated
				// the handlers still want an absolute URL for
				// `externalLink`, because MoeMemos iOS's
				// host-equality check does not require an
				// authenticated caller to be present.
				if origin := requestOriginFromHTTP(hdr); origin != "" {
					ctx = context.WithValue(ctx, ctxKeyRequestOrigin{}, origin)
				}
			}
			return next(ctx, spec, stream)
		})
	}
}

// WithErrorLogging logs every handler error the Connect server
// returns, so a 5xx is visible in the server log instead of only in
// the browser.
//
// Without it, a connect handler that returns
// connect.CodeInternal produces a 500 response and *no log line at
// all*: connect-go does not log handler errors, and the only
// interceptor riff registered (WithAuth) passes them through
// untouched. A 500 that a user reports therefore had nothing to
// diagnose it from — the log stream was clean and only the response
// body carried the message.
//
// The procedure and the error are logged, not the request body: the
// body can contain the user's filter text, and a filter is not worth
// a log line. Internal errors are logged at Error; a client mistake
// (InvalidArgument and friends) is not, because those are expected
// and would drown the signal.
func WithErrorLogging() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return connect.ServerFunc(func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			err := next(ctx, spec, stream)
			if err == nil {
				return nil
			}
			code := connect.CodeOf(err)
			if code == connect.CodeInternal || code == connect.CodeUnknown {
				slog.Error("connect handler failed",
					"procedure", spec.Procedure,
					"code", code.String(),
					"err", err.Error(),
				)
				return err
			}
			slog.Info("connect handler rejected",
				"procedure", spec.Procedure,
				"code", code.String(),
			)
			return err
		})
	}
}

// bearerToken parses a Bearer token from the Authorization
// header. Returns "" if the header is missing or malformed.
func bearerToken(h string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// ctxKeyAuthRecord is the context key under which the auth
// interceptor stashes the *core.Record.
type ctxKeyAuthRecord struct{}

// ctxKeyRequestOrigin is the context key under which the
// request interceptor (see WithAuth) stashes the user-facing
// origin (scheme + host) of the inbound request. The value is a
// string of the form `https://example.com` with no trailing
// slash; an empty string means the interceptor could not
// determine the origin, and `requestOriginFromContext` returns
// "" rather than guessing.
//
// The MoeMemos iOS client's auth middleware (see
// Packages/Services/Sources/ServiceUtils/Auth.swift in the
// `mudkipme/MoeMemos` repository) only attaches `Authorization:
// Bearer …` when `request.url?.host == hostURL.host` — a host
// comparison against the URL it sees the server emit. Relative
// URLs from the server have `host == nil`, so the comparison is
// always false and the bearer is dropped. Every PRIVATE memo's
// attachment then 404s on the file surface, because the file
// surface authorizes via the bearer it never received. Stashing
// the origin here lets every `*v1.Attachment` returned by the
// connect handlers carry an absolute `externalLink` whose host
// matches what the iOS client configured, so the host check
// passes and the bearer goes through.
type ctxKeyRequestOrigin struct{}

// requestOriginFromContext returns the user-facing origin
// stashed by the request interceptor, or "" if none.
func requestOriginFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestOrigin{}).(string)
	return v
}

// requestOriginFromHTTP computes the user-facing origin from a
// header set. It accepts connect-go's `*Header` (which is
// `map[string][]string`, identical to `http.Header` in shape) and
// the plain `http.Header` for tests. It trusts
// `X-Forwarded-Proto` first (set by caddy per `scripts/Caddyfile`)
// and falls back to plain `http` when absent — the only time
// that branch fires is the bare-metal `localhost:8090`
// development case.
//
// `Host` is the host the *client* addressed — what the iOS
// MoeMemos client's `hostURL.absoluteString` carries and what
// the host-equality check on the URL is compared against. We
// MUST NOT derive it from `r.Host` for the *connect* path (the
// connect server runs behind caddy, where `r.Host` is the
// in-process loopback 8090); we MUST derive from the same
// `info.RequestHeader()` PocketBase gives us.
func requestOriginFromHTTP(h interface {
	Get(string) string
	Values(string) []string
}) string {
	scheme := h.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = h.Get("X-Forwarded-Protocol")
	}
	if scheme == "" {
		scheme = "http"
	}
	host := h.Get("Host")
	if host == "" {
		return ""
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return scheme + "://" + host
}

// CurrentUser returns the auth record stashed by the
// interceptor, or nil if the request was unauthenticated.
func CurrentUser(ctx context.Context) *core.Record {
	v := ctx.Value(ctxKeyAuthRecord{})
	if v == nil {
		return nil
	}
	return v.(*core.Record)
}

// connectMux is the in-process http.ServeMux that Mount
// installs at Register time. The upstream-memos REST surface
// in pb/api/upstream_memos_rest_proxy.go reads it through
// ConnectMux and dispatches /memos/api/... requests through
// the same connect.Server, so the two surfaces share
// withAuth, the rate-limit middleware, and the per-service
// rule engine. nil until Register runs.
//
// connectMuxMu guards it because both `pb/api` request
// handlers and the test helpers that call SetConnectMux
// reach it from different goroutines. `SetConnectMux` is
// registered as a `t.Cleanup` in several pb/api tests, and
// those cleanups can interleave with a still-running
// request goroutine (or with another test's cleanup) that is
// calling `ConnectMux` — an unsynchronised read/write on the
// pointer that `go test -race` reports as a
// DATA RACE on `SetConnectMux` / `ConnectMux`.
var (
	connectMuxMu sync.RWMutex
	connectMux   *http.ServeMux
)

// ConnectMux returns the in-process http.ServeMux that the
// connect package mounted its procedures on at Register
// time. The mux is nil before Register runs and after the
// process exits; callers that read it at request time (i.e.
// the upstream-memos REST handlers) should treat a nil mux
// as "proxy not installed" and return 503.
//
// The mux is safe to call ServeHTTP on from any goroutine
// because http.ServeMux is concurrency-safe. It is shared
// between the public /memos.api.v1.* surface and the
// /memos/api/... REST surface, so the two answer through
// the same handler instances.
func ConnectMux() *http.ServeMux {
	connectMuxMu.RLock()
	defer connectMuxMu.RUnlock()
	return connectMux
}

// SetConnectMux overrides the in-process http.ServeMux
// for tests. Production code never calls this; it is
// here so the upstream-memos REST tests can inject a
// stub mux without spinning up the full Register
// path. Pass nil to restore the default (which is the
// real mux installed at Register time, or nil).
func SetConnectMux(m *http.ServeMux) {
	connectMuxMu.Lock()
	defer connectMuxMu.Unlock()
	connectMux = m
}
