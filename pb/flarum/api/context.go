package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/pkg/errors"

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

// serverFromContext returns the server carried by a context, or nil.
func serverFromContext(ctx context.Context) *Server {
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

// ServerFrom returns the API server a request carries, or nil.
//
// It is exported for the plugins: a plugin that has to build a URL
// under the forum's mount path needs the server's base path, and the
// request's own path does not carry it (the route is registered at an
// absolute path, and a reverse proxy may rewrite it).
func ServerFrom(e *core.RequestEvent) *Server {
	if e == nil {
		return nil
	}
	return serverFromContext(e.Request.Context())
}

// CurrentActor resolves the request's caller.
//
// It returns an error when no server is attached to the request, which
// means the route was mounted without the module's wrapper.
func CurrentActor(e *core.RequestEvent) (session.Actor, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return session.Actor{}, errUnauthenticated("The forum is not available.")
	}
	return server.actor(e), nil
}

// RequireAuth resolves the request's caller and insists the caller is
// signed in.
//
// It is the plugin-facing form of the core's `requireAuth`. The
// difference from [CurrentActor] matters: an ANONYMOUS caller is a
// valid actor for a read, so `CurrentActor` succeeds for one, while a
// write needs a signed-in caller and must answer 401 rather than let
// the request reach the CSRF check.
//
// The order is the core's: the actor check first, then the CSRF
// check, then the body. An unauthenticated caller therefore learns
// nothing about the request's shape.
func RequireAuth(e *core.RequestEvent) (session.Actor, error) {
	actor, err := CurrentActor(e)
	if err != nil {
		return actor, err
	}
	if !actor.IsAuthenticated() {
		return actor, errUnauthenticated("You must be signed in to do that.")
	}
	return actor, nil
}

// Can reports whether the caller may perform the action.
//
// It is the plugin-facing form of the permission check: a plugin asks
// the forum's permission service rather than implementing its own.
func Can(e *core.RequestEvent, action, subjectType, subjectID string) (bool, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.permissions.Can(e.Request.Context(), server.domainActor(e), action, subjectType, subjectID)
}

// VerifyCSRF checks a mutating request's CSRF token.
func VerifyCSRF(e *core.RequestEvent) error {
	return session.VerifyCSRF(e)
}

// SettingValue reads a forum setting's raw value.
//
// It is the plugin-facing form of the core's setting reader: a plugin
// reads the setting it was given, and the fallback applies when the
// setting is unset. The value comes back as whatever the settings table
// holds (a string in practice), so a caller that wants a boolean
// interprets it.
func SettingValue(e *core.RequestEvent, key string, fallback any) (any, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return fallback, nil
	}
	return server.repo.SettingValue(key, fallback)
}

// DecodeBody decodes a request body into a map.
//
// It is the plugin-facing form of a JSON body read, for a plugin whose
// body shape is loose (the JSON:API envelope, or the flat form). The
// framework's own `BindBody` targets a struct, which is the wrong shape
// here.
//
// The map is taken by POINTER on purpose: a map is a reference type, so
// passing it by value would decode into a copy the caller never sees —
// and the body would read as empty.
func DecodeBody(e *core.RequestEvent, into *map[string]any) error {
	if e == nil || e.Request == nil || e.Request.Body == nil {
		return nil
	}

	if into == nil {
		return errors.New("forum: no destination for the body")
	}

	decoder := json.NewDecoder(e.Request.Body)
	if err := decoder.Decode(into); err != nil {
		return errors.Wrap(err, "forum: the body could not be read")
	}

	return nil
}

// CanEditPost reports whether the caller may edit a post: the account
// that wrote it, while the `allow_post_editing` setting permits it, or
// a moderator.
//
// It is the plugin-facing form of upstream's `PostPolicy::edit()`.
func CanEditPost(e *core.RequestEvent, postID string) (bool, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.canEditPostAsAuthor(e, postID)
}

// CanDeletePost reports whether the caller may delete a post: the
// account that wrote it, or a caller the permission service grants the
// delete permission to.
//
// Upstream has no group permission for post deletion — `PostPolicy`
// defines `edit` and `hide` but no `delete` — so this is the author
// rule plus the moderator's authority.
func CanDeletePost(e *core.RequestEvent, postID string) (bool, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.canDeletePost(e, postID)
}

// CanRenameDiscussion reports whether the caller may rename a
// discussion: the account that started it, while the `allow_renaming`
// setting permits it, or a moderator.
//
// It is the plugin-facing form of upstream's
// `DiscussionPolicy::rename()`.
func CanRenameDiscussion(e *core.RequestEvent, discussionID string) (bool, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.canRenameDiscussion(e, discussionID)
}

// CanTagDiscussion reports whether the caller may change a discussion's
// tags: the account that started it, while the `allow_tag_change`
// setting permits it.
//
// It is the plugin-facing form of the tags extension's
// `Access\\DiscussionPolicy::tag()`.
func CanTagDiscussion(e *core.RequestEvent, discussionID string) (bool, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.canTagDiscussion(e, discussionID)
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
