package api

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"

	"github.com/incurl/riff/pb/connect"
)

// requireAuthOrPAT rejects a request that carries no usable
// credential.
//
// PocketBase's own `apis.RequireAuth` only understands PocketBase
// auth tokens (the JWT the auth collection mints). riff also
// accepts a PERSONAL ACCESS TOKEN — a `memos_pat_*` bearer — on the
// same routes, and the MoeMemos client authenticates with a PAT
// rather than a JWT. Binding `apis.RequireAuth` on those routes
// therefore rejected a valid PAT client with a 401.
//
// This gate accepts either:
//
//   - a PocketBase auth token (resolved by PB's own loadAuthToken
//     middleware, which has already run and set e.Auth), or
//   - a `memos_pat_*` personal access token, resolved here.
//
// A request with neither is answered 401.
func requireAuthOrPAT(app core.App) *hook.Handler[*core.RequestEvent] {
	return &hook.Handler[*core.RequestEvent]{
		Id: "riffRequireAuthOrPAT",
		Func: func(e *core.RequestEvent) error {
			if e.Auth != nil {
				// A PocketBase auth token was resolved.
				return e.Next()
			}

			// Try a personal access token.
			if tok := bearerTokenFrom(e); tok != "" {
				if rec, err := connect.ResolveFilePersonalAccessToken(app, tok); err == nil && rec != nil {
					e.Auth = rec
					return e.Next()
				}
			}

			return e.UnauthorizedError("The request requires valid record authorization token.", nil)
		},
	}
}

// bearerTokenFrom extracts the bearer token from the request's
// Authorization header.
func bearerTokenFrom(e *core.RequestEvent) string {
	header := e.Request.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
