package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pkg/errors"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

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

// CanScoped reports whether the caller may perform the action on the
// subject ALONE.
//
// It is the plugin-facing form of the restricted-subject check, and it
// is what a RESTRICTED tag asks: the subject's own rules answer the
// question, and the global grant is not consulted — upstream's exact
// match. A tag that inherited the global grant would restrict nobody.
//
// See [domain.PermissionChecker.CanScoped] for the model.
func CanScoped(e *core.RequestEvent, action, subjectType, subjectID string) (bool, error) {
	server := serverFromContext(e.Request.Context())
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}
	return server.permissions.CanScoped(e.Request.Context(), server.domainActor(e), action, subjectType, subjectID)
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

// SettingValueAt reads a forum setting without a request event.
//
// It is the form a plugin's write path uses: the relationship writer
// runs from the mutation handler, and it has no request event of its
// own, but it still needs the forum's settings.
//
// A missing setting returns the fallback, so a caller never has to
// distinguish "unset" from "set to the default".
func SettingValueAt(app core.App, key string, fallback any) (any, error) {
	if app == nil {
		return fallback, nil
	}

	collection, err := app.FindCollectionByNameOrId("forum_settings")
	if err != nil {
		return fallback, err
	}

	record, err := app.FindFirstRecordByFilter(collection, "key = {:key}", map[string]any{"key": key})
	if err != nil {
		// The setting is unset: the fallback is the answer.
		return fallback, nil //nolint:nilerr // an unset setting falls back
	}

	value := record.Get("value")
	if value == nil {
		return fallback, nil
	}

	return value, nil
}

// SettingTruthy reads a setting's value as a boolean.
//
// The settings table's `value` column is a JSON field, so a value
// written as the string "0" comes back as RAW JSON BYTES
// (`types.JSONRaw`), not as a Go string. A type switch that handles
// the string case alone therefore never matches, and every setting
// reads as its fallback — which is how "the setting is not read"
// happens silently.
//
// The `types.JSONRaw` case is the one that matters: it is the type a
// STORED setting actually arrives as.
func SettingTruthy(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return settingTruthyString(typed, fallback)
	case []byte:
		return settingTruthyString(string(typed), fallback)
	case types.JSONRaw:
		return settingTruthyString(string(typed), fallback)
	case json.RawMessage:
		return settingTruthyString(string(typed), fallback)
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return fallback
	}
}

// settingTruthyString reads a setting's textual value.
//
// The JSON quotes are stripped first, because a JSON-encoded string
// arrives as `"0"` — with the quotes — and a bare comparison against
// "0" would miss it.
func settingTruthyString(raw string, fallback bool) bool {
	text := strings.Trim(strings.TrimSpace(raw), `"`)

	switch strings.ToLower(text) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
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

// CanTagDiscussionAs reports whether an ACCOUNT may change a
// discussion's tags: the account that started it, while the
// `allow_tag_change` setting permits it.
//
// It is the form a plugin's write path uses: the relationship writer
// runs from the mutation handler, and it has no request event of its
// own, but it still has to ask the same question the `canTag`
// attribute answers — otherwise the flag and the write would disagree.
//
// The CONTEXT is what carries the server, so the caller passes the
// request's own context.
func CanTagDiscussionAs(ctx context.Context, actorID, discussionID string) (bool, error) {
	server := serverFromContext(ctx)
	if server == nil {
		return false, errUnauthenticated("The forum is not available.")
	}

	actor, err := server.actorFor(actorID)
	if err != nil {
		return false, err
	}

	return server.canTagDiscussionAs(ctx, actor, discussionID)
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
