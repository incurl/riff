package api

import (
	"context"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/session"
)

// serverKey is the context key the API server travels under.
//
// The plugin routes are plain handlers, so they reach the forum's
// services through the request context rather than through a captured
// pointer. The module injects the server before a plugin handler runs.
type serverKey struct{}

// serverContext returns a context carrying the server.
func serverContext(ctx context.Context, server *Server) context.Context {
	return context.WithValue(ctx, serverKey{}, server)
}

// serverFrom returns the server carried by a context, or nil.
func serverFrom(ctx context.Context) *Server {
	server, ok := ctx.Value(serverKey{}).(*Server)
	if !ok {
		return nil
	}
	return server
}

// WithServer returns a request carrying the API server.
//
// The module calls it on every request that reaches a plugin route, so
// the plugin's handler can resolve the caller and ask the permission
// service.
func WithServer(e *core.RequestEvent, server *Server) {
	if e == nil || server == nil {
		return
	}
	e.Request = e.Request.WithContext(serverContext(e.Request.Context(), server))
}

// CurrentActor resolves the request's caller.
//
// It returns an error when no server is attached to the request, which
// means the route was mounted without the module's wrapper.
func CurrentActor(e *core.RequestEvent) (session.Actor, error) {
	server := serverFrom(e.Request.Context())
	if server == nil {
		return session.Actor{}, errUnauthenticated("The forum is not available.")
	}
	return server.actor(e), nil
}

// Can reports whether the caller may perform the action.
//
// It is the plugin-facing form of the permission check: a plugin asks
// the forum's permission service rather than implementing its own.
func Can(e *core.RequestEvent, action, subjectType, subjectID string) (bool, error) {
	server := serverFrom(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.permissions.Can(e.Request.Context(), server.domainActor(e), action, subjectType, subjectID)
}

// VerifyCSRF checks a mutating request's CSRF token.
func VerifyCSRF(e *core.RequestEvent) error {
	return session.VerifyCSRF(e)
}

// WriteDocument writes a JSON:API document.
//
// It is the plugin-facing form of the response writer, so a plugin's
// answers carry the same content type as the core's.
func WriteDocument(e *core.RequestEvent, status int, doc Document) error {
	e.Response.Header().Set("Content-Type", ContentType)
	return e.JSON(status, doc)
}

// PermissionKey returns the permission key of an action.
//
// It is a convenience for the plugins, which name their own keys.
func PermissionKey(action string) string { return domain.NormaliseScope(action) }

// unusedHTTP keeps the net/http import honest.
var _ = http.StatusOK
