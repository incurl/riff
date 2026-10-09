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
// exists.
package connect

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// Register mounts the Connect-RPC services onto the PocketBase
// router. Memos uses Connect over HTTP/1.1; the standard URL
// scheme is /<package>.<service>/<Method>.
func Register(app core.App) error {
	srv := connect.NewServer(withAuth(app))

	// Implemented services.
	apiv1connect.RegisterAuthServiceHandler(srv, NewAuthServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, NewUserServer(app))
	apiv1connect.RegisterSpaceServiceHandler(srv, NewSpaceServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, NewAttachmentServer(app))
	apiv1connect.RegisterInstanceServiceHandler(srv, NewInstanceServer(app))

	// Stub services: every method returns Unimplemented.
	apiv1connect.RegisterAIServiceHandler(srv, apiv1connect.UnimplementedAIServiceHandler{})
	apiv1connect.RegisterIdentityProviderServiceHandler(srv, apiv1connect.UnimplementedIdentityProviderServiceHandler{})

	// Mount the connect-go HTTP handlers on a stdlib mux.
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// Register the catch-all for any path under
		// /memos.api.v1. PocketBase's router doesn't
		// translate the {rpc...} wildcard to stdlib
		// syntax the way Echo does, so we just bind the
		// stdlib mux directly to the path the procedure
		// will arrive at. We register a separate route
		// per known procedure.
		for spec := range srv.Specs() {
			procedure := spec.Procedure // e.g. /memos.api.v1.AuthService/SignIn
			e.Router.Any(procedure, func(re *core.RequestEvent) error {
				mux.ServeHTTP(re.Response, re.Request)
				return nil
			})
		}
		return e.Next()
	})
	return nil
}

// withAuth is a server-side interceptor that authenticates the
// caller via a Bearer token and stashes the *core.Record in
// the context. Handlers retrieve the current user via
// CurrentUser(ctx).
//
// In connect-go v2, server interceptors have the signature
// func(next connect.ServerFunc) connect.ServerFunc. The request
// headers are reached via CallInfo stored in the context.
//
// The token is a PocketBase auth JWT for the `users` collection.
// FindAuthRecordByToken takes the token first and the accepted token
// types (auth, file, verification, …) after it; core.TokenTypeAuth
// keeps refresh/file tokens from being accepted here.
func withAuth(app core.App) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return connect.ServerFunc(func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if info, ok := connect.CallInfoForServerContext(ctx); ok {
				tok := bearerToken(info.RequestHeader().Get("Authorization"))
				if tok != "" {
					rec, err := app.FindAuthRecordByToken(tok, core.TokenTypeAuth)
					if err == nil {
						ctx = context.WithValue(ctx, ctxKeyAuthRecord{}, rec)
					}
				}
			}
			return next(ctx, spec, stream)
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

// CurrentUser returns the auth record stashed by the
// interceptor, or nil if the request was unauthenticated.
func CurrentUser(ctx context.Context) *core.Record {
	v := ctx.Value(ctxKeyAuthRecord{})
	if v == nil {
		return nil
	}
	return v.(*core.Record)
}
