package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
	"github.com/incurl/riff/pb/flarum/session"
)

// DefaultPageSize is the number of records a listing returns when the
// client does not ask for a different size.
const DefaultPageSize = 20

// MaxPageSize caps the number of records a listing returns.
const MaxPageSize = 100

// Server serves the forum's JSON:API surface.
//
// It owns the request-scoped state: the resolved actor and the permission
// cache. A Server is created per request, so nothing is shared between
// callers.
type Server struct {
	// app is the PocketBase app.
	app core.App
	// repo is the data access layer.
	repo *repository.Repository
	// sessions resolves the caller.
	sessions *session.Resolver
	// permissions answers access questions.
	permissions domain.PermissionChecker
	// basePath is the module's mount path.
	basePath string
	// apiPath is the JSON:API prefix, e.g. "/forum/api".
	apiPath string
	// logger logs server activity.
	logger *slog.Logger
	// bus is the event bus, optional.
	bus *flarumcore.EventBus
	// attributes holds the plugin-contributed attributes, keyed by
	// resource type.
	attributes map[string][]AttributeSource
	// relationships holds the plugin-contributed relationships.
	relationships map[string][]RelationshipSource
	// renderer renders a post's Markdown source into the HTML the
	// frontend displays.
	renderer ContentRenderer
}

// ContentRenderer renders a post's Markdown source into HTML.
//
// It is an interface so the forum does not depend on the markdown
// package directly: the module binds a markdown service to it, and a
// plugin can replace the renderer through the container.
type ContentRenderer interface {
	// RenderHTML renders markdown source into an HTML fragment.
	RenderHTML(content []byte) (string, error)
}

// AttributeSource computes a plugin-contributed attribute.
type AttributeSource struct {
	// Name is the attribute name.
	Name string
	// Value computes the attribute for a record.
	Value func(ctx context.Context, req AttributeRequest) (any, error)
}

// AttributeRequest is what an attribute source receives.
type AttributeRequest struct {
	// ActorID is the acting user's id, empty for anonymous.
	ActorID string
	// RecordID is the record the attribute belongs to.
	RecordID string
	// Type is the JSON:API resource type.
	Type string
}

// RelationshipSource computes a plugin-contributed relationship.
type RelationshipSource struct {
	// Name is the relationship name.
	Name string
	// Type is the JSON:API type of the related resources.
	Type string
	// Many reports whether the relationship is to-many.
	Many bool
	// Resolve computes the related resource identifiers.
	Resolve func(ctx context.Context, req RelationshipRequest) ([]Identifier, error)
}

// RelationshipRequest is what a relationship source receives.
type RelationshipRequest struct {
	// ActorID is the acting user's id, empty for anonymous.
	ActorID string
	// RecordID is the record the relationship belongs to.
	RecordID string
	// Type is the JSON:API resource type.
	Type string
}

// Options configures the API server.
type Options struct {
	// App is the PocketBase app.
	App core.App
	// Repository is the data access layer.
	Repository *repository.Repository
	// Sessions resolves the caller.
	Sessions *session.Resolver
	// Permissions answers access questions.
	Permissions domain.PermissionChecker
	// BasePath is the module's mount path.
	BasePath string
	// Logger logs server activity.
	Logger *slog.Logger
	// Bus is the event bus, optional.
	Bus *flarumcore.EventBus
	// Renderer renders a post's Markdown into HTML. It is required:
	// the frontend trusts and inserts `contentHtml` as-is, so the
	// forum must always produce a rendered body.
	Renderer ContentRenderer
}

// NewServer returns an API server.
func NewServer(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	basePath := "/" + strings.Trim(opts.BasePath, "/")
	return &Server{
		app:           opts.App,
		repo:          opts.Repository,
		sessions:      opts.Sessions,
		permissions:   opts.Permissions,
		basePath:      basePath,
		apiPath:       basePath + "/api",
		logger:        logger,
		renderer:      opts.Renderer,
		attributes:    map[string][]AttributeSource{},
		relationships: map[string][]RelationshipSource{},
	}
}

// APIPath returns the JSON:API prefix.
func (s *Server) APIPath() string { return s.apiPath }

// BasePath returns the module's mount path.
func (s *Server) BasePath() string { return s.basePath }

// AddAttribute registers a plugin-contributed attribute.
//
// It is called by the plugin manager when a plugin declares an attribute
// contribution.
func (s *Server) AddAttribute(resourceType, name string, value func(ctx context.Context, req AttributeRequest) (any, error)) {
	s.attributes[resourceType] = append(s.attributes[resourceType], AttributeSource{Name: name, Value: value})
}

// AddRelationship registers a plugin-contributed relationship.
func (s *Server) AddRelationship(resourceType, name, relatedType string, many bool, resolve func(ctx context.Context, req RelationshipRequest) ([]Identifier, error)) {
	s.relationships[resourceType] = append(s.relationships[resourceType], RelationshipSource{
		Name: name, Type: relatedType, Many: many, Resolve: resolve,
	})
}

// actor resolves the request's caller.
//
// A failure to resolve is not fatal: an anonymous caller is a valid
// caller, and a broken credential is treated as no credential.
func (s *Server) actor(e *core.RequestEvent) session.Actor {
	actor, err := s.sessions.Resolve(e)
	if err != nil {
		s.logger.Debug("forum: could not resolve the caller", slog.String("error", err.Error()))
		return session.Actor{}
	}
	return actor
}

// domainActor projects the session actor onto the domain actor.
func (s *Server) domainActor(e *core.RequestEvent) domain.Actor {
	actor := s.actor(e)
	if actor.User == nil {
		return domain.Actor{}
	}
	groupIDs, err := s.repo.UserGroupIDs(actor.User.ID)
	if err != nil {
		groupIDs = nil
	}
	return domain.ActorFor(actor.User.ID, actor.User.IsAdmin, groupIDs)
}

// can reports whether the caller may perform the action.
func (s *Server) can(e *core.RequestEvent, action, subjectType, subjectID string) bool {
	allowed, err := s.permissions.Can(e.Request.Context(), s.domainActor(e), action, subjectType, subjectID)
	if err != nil {
		s.logger.Error("forum: permission check failed",
			slog.String("action", action),
			slog.String("error", err.Error()),
		)
		return false
	}
	return allowed
}

// canEditPost reports whether the caller may change a post.
//
// The AUTHOR may always edit their own post.
//
// For anyone else the answer is the permission service's, and the
// permission rows are GROUP-scoped: they can say "the member group may
// edit posts", but they cannot say "the member group may edit THEIR
// OWN posts". A group-scoped grant is therefore a MODERATOR grant —
// it has to come from a group that is not the implicit member group,
// because the implicit member group holds the row for every signed-in
// caller.
//
// Without this distinction every member could rewrite every post in
// the forum: the permission check alone answered "yes" for any member
// and any post.
func (s *Server) canEditPost(e *core.RequestEvent, post *domain.Post) (bool, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false, nil
	}

	if post.UserID == actor.User.ID {
		return true, nil
	}

	return s.canAsModerator(e, domain.PermissionEditPost, TypePost, post.ID)
}

// canDeletePost reports whether the caller may delete a post.
//
// The same author rule as [Server.canEditPost].
func (s *Server) canDeletePost(e *core.RequestEvent, post *domain.Post) (bool, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false, nil
	}

	if post.UserID == actor.User.ID {
		return true, nil
	}

	return s.canAsModerator(e, domain.PermissionDeletePost, TypePost, post.ID)
}

// canEditDiscussion reports whether the caller may change a discussion:
// the account that started it, or a caller with a moderator grant.
//
// The author rule matches [Server.canEditPost].
func (s *Server) canEditDiscussion(e *core.RequestEvent, discussion *domain.Discussion) bool {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false
	}

	if discussion.UserID == actor.User.ID {
		return true
	}

	allowed, err := s.canAsModerator(e, domain.PermissionEditDiscussion, TypeDiscussion, discussion.ID)
	if err != nil {
		s.logger.Error("forum: permission check failed",
			slog.String("action", domain.PermissionEditDiscussion),
			slog.String("error", err.Error()),
		)
		return false
	}
	return allowed
}

// canAsModerator reports whether the caller holds a permission that was
// granted to a group OTHER than the implicit member group.
//
// The implicit member group is every signed-in caller, so a row for it
// carries no information about the individual. A row for another group
// (an explicit moderator group) does: it names a set of accounts.
//
// An administrator holds every permission outright, so they never need
// a row.
func (s *Server) canAsModerator(e *core.RequestEvent, action, subjectType, subjectID string) (bool, error) {
	actor := s.domainActor(e)
	if actor.IsAdmin {
		return true, nil
	}

	return s.permissions.CanAsModerator(e.Request.Context(), actor, action, subjectType, subjectID)
}

// requireAuth rejects an anonymous caller.
func (s *Server) requireAuth(e *core.RequestEvent) (session.Actor, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return actor, errUnauthenticated("You must be signed in to do that.")
	}
	return actor, nil
}

// pageParams reads the pagination parameters of a request.
//
// The frontend sends `page[offset]` and `page[limit]`.
func pageParams(e *core.RequestEvent) (limit, offset int) {
	limit = DefaultPageSize
	offset = 0

	query := e.Request.URL.Query()
	if raw := query.Get("page[limit]"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if raw := query.Get("page[offset]"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = value
		}
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	return limit, offset
}

// writeDocument writes a JSON:API document.
func (*Server) writeDocument(e *core.RequestEvent, doc Document) error {
	e.Response.Header().Set("Content-Type", ContentType)
	return e.JSON(http.StatusOK, doc)
}

// writeDocumentStatus writes a JSON:API document with a status code.
func (*Server) writeDocumentStatus(e *core.RequestEvent, status int, doc Document) error {
	e.Response.Header().Set("Content-Type", ContentType)
	return e.JSON(status, doc)
}

// notFoundError builds a not-found apiError.
func notFoundError(detail string) *apiError { return errNotFound(detail) }

// errInternal builds an internal apiError.
func errInternal(detail string, err error) *apiError {
	return newAPIError(http.StatusInternalServerError, CodeInternal, detail, err)
}

// unusedGuard keeps the errors import honest for future use.
var _ = errors.New
