package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

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
	// relationshipWriters holds the plugin-contributed WRITE sides of
	// the relationships.
	relationshipWriters map[string][]RelationshipWriterSource
	// renderer renders a post's Markdown source into the HTML the
	// frontend displays.
	renderer ContentRenderer
	// extensions are the forum's plugins, by their RAW id.
	//
	// The admin application addresses a plugin by the FLATTENED id
	// (`vendor-name`) — it is a path segment — and the state table
	// records the RAW id, so the server needs both spellings.
	extensions map[string]ExtensionInfo
}

// ExtensionInfo describes one of the forum's plugins.
//
// It is the API server's own narrow view of a plugin: the id and the
// name are all the admin surface needs. The plugin package's Manifest
// is richer, and the API package does not import it — the two packages
// are siblings, and the dependency would point the wrong way.
type ExtensionInfo struct {
	// ID is the plugin's raw id, e.g. "riff/likes".
	ID string
	// Name is the plugin's display name.
	Name string
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
	// Visible decides whether the attribute is present at all.
	//
	// It is upstream's `Field::visible()`, and nil means "always". An
	// INVISIBLE field is ABSENT from the document rather than null:
	// upstream's serializer does not emit it, and a null is a different
	// statement ("this field exists and has no value"). The distinction
	// matters for a field a frontend branches on —
	// `attribute<T[] | undefined>() ?? fallback` falls through for both,
	// but `'statisticsEntities' in attributes` does not.
	Visible func(req AttributeRequest) bool
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
	// IsAdmin reports whether the actor is an administrator.
	//
	// Upstream gates a field's VISIBILITY on the actor
	// (`Field::visible(fn (object $model, Context $context) =>
	// $context->getActor()->isAdmin())`), which is how the Statistics
	// extension publishes `statisticsEntities` to administrators only. A
	// contributor that does not ask cannot port such a field, so the flag
	// travels with the request.
	IsAdmin bool
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
	// Extensions are the forum's plugins, by their raw id.
	//
	// The admin surface needs them to map the FLATTENED id the
	// frontend addresses a plugin by onto the raw id the state table
	// records.
	Extensions map[string]ExtensionInfo
}

// NewServer returns an API server.
func NewServer(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	basePath := "/" + strings.Trim(opts.BasePath, "/")
	return &Server{
		app:                 opts.App,
		repo:                opts.Repository,
		sessions:            opts.Sessions,
		permissions:         opts.Permissions,
		basePath:            basePath,
		apiPath:             basePath + "/api",
		logger:              logger,
		bus:                 opts.Bus,
		renderer:            opts.Renderer,
		attributes:          map[string][]AttributeSource{},
		relationships:       map[string][]RelationshipSource{},
		relationshipWriters: map[string][]RelationshipWriterSource{},
	}
}

// SetExtensions records the forum's plugins for the admin surface.
//
// It is called by the module AFTER the plugins are registered: the API
// server is built before that, so the list is not known when it is
// constructed.
func (s *Server) SetExtensions(extensions map[string]ExtensionInfo) {
	s.extensions = extensions
}

// APIPath returns the JSON:API prefix.
func (s *Server) APIPath() string { return s.apiPath }

// BasePath returns the module's mount path.
func (s *Server) BasePath() string { return s.basePath }

// IsAdministrator reports whether an account administers the forum.
//
// A superuser always does, and so does a member of the administrator group
// (`forumgroupadmin`). It is exported for the plugins: upstream refuses to
// suspend an administrator (`Suspend\Listener\SavingUser`), and the rule
// is the forum's, not the plugin's to re-derive.
func (s *Server) IsAdministrator(userID string) bool {
	if userID == "" || s.repo == nil {
		return false
	}

	user, err := s.repo.UserByID(userID)
	if err != nil || user == nil {
		return false
	}

	return user.IsAdmin
}

// AddAttribute registers a plugin-contributed attribute.
//
// It is called by the plugin manager when a plugin declares an attribute
// contribution.
func (s *Server) AddAttribute(resourceType, name string, value func(ctx context.Context, req AttributeRequest) (any, error)) {
	s.attributes[resourceType] = append(s.attributes[resourceType], AttributeSource{Name: name, Value: value})
}

// AddVisibleAttribute registers a plugin-contributed attribute that is
// present only for the callers `visible` admits.
//
// It is upstream's `Extend\ApiResource(...)->fields(fn () => [
// Schema\Arr::make('statisticsEntities')->visible(...)])`: a field whose
// visibility is a property of the ACTOR, not of the record.
func (s *Server) AddVisibleAttribute(
	resourceType, name string,
	visible func(req AttributeRequest) bool,
	value func(ctx context.Context, req AttributeRequest) (any, error),
) {
	s.attributes[resourceType] = append(s.attributes[resourceType], AttributeSource{Name: name, Visible: visible, Value: value})
}

// AddRelationship registers a plugin-contributed relationship.
func (s *Server) AddRelationship(resourceType, name, relatedType string, many bool, resolve func(ctx context.Context, req RelationshipRequest) ([]Identifier, error)) {
	s.relationships[resourceType] = append(s.relationships[resourceType], RelationshipSource{
		Name: name, Type: relatedType, Many: many, Resolve: resolve,
	})
}

// AddRelationshipWriter registers the write side of a
// plugin-contributed relationship.
//
// The core does not know which relationships are writable — that is the
// extension's business — so a plugin declares it here and the core
// calls it when a request carries the relationship.
func (s *Server) AddRelationshipWriter(resourceType, name string, write func(ctx context.Context, req RelationshipWriteRequest) error) {
	s.relationshipWriters[resourceType] = append(s.relationshipWriters[resourceType], RelationshipWriterSource{
		Name: name, Write: write,
	})
}

// RelationshipWriterSource is a plugin's write side of a relationship.
type RelationshipWriterSource struct {
	// Name is the relationship name.
	Name string
	// Write applies the relationship's identifiers to the record.
	Write func(ctx context.Context, req RelationshipWriteRequest) error
}

// RelationshipWriteRequest is what a relationship writer receives.
type RelationshipWriteRequest struct {
	// ActorID is the acting user's id, empty for anonymous.
	ActorID string
	// RecordID is the id of the record the relationship belongs to.
	RecordID string
	// Type is the JSON:API resource type.
	Type string
	// IDs are the related resource ids the client named.
	IDs []string
	// Creating reports whether the record is being created.
	Creating bool
}

// writeRelationships applies the writable relationships a request
// carries.
//
// It is called by the mutation handlers after the record is written,
// because the relationship may name the record's own id (a discussion's
// tags, for instance, can only be attached once the discussion exists).
//
// A relationship the request does not carry is left alone: the client
// named no tags, so the discussion keeps the tags it has.
func (s *Server) writeRelationships(e *core.RequestEvent, resourceType, recordID string, creating bool, relationships map[string]any) error {
	writers := s.relationshipWriters[resourceType]
	if len(writers) == 0 || len(relationships) == 0 {
		return nil
	}

	for _, writer := range writers {
		relationship, present := relationships[writer.Name].(map[string]any)
		if !present {
			continue
		}

		ids, err := relationshipIDs(relationship)
		if err != nil {
			return err
		}

		request := RelationshipWriteRequest{
			ActorID:  s.actor(e).ID(),
			RecordID: recordID,
			Type:     resourceType,
			IDs:      ids,
			Creating: creating,
		}
		if err := writer.Write(s.serverContext(), request); err != nil {
			return err
		}
	}

	return nil
}

// relationshipIDs reads a relationship's identifiers from a request
// body.
//
// The JSON:API shape is `{"data": [{"id": "…"}, …]}` for a to-many
// relationship and `{"data": {"id": "…"}}` for a to-one one.
func relationshipIDs(relationship map[string]any) ([]string, error) {
	data, ok := relationship["data"]
	if !ok {
		return nil, nil
	}

	switch typed := data.(type) {
	case []any:
		ids := make([]string, 0, len(typed))
		for _, item := range typed {
			identifier, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, ok := identifier["id"].(string)
			if !ok {
				continue
			}
			ids = append(ids, id)
		}
		return ids, nil
	case map[string]any:
		id, ok := typed["id"].(string)
		if !ok {
			return nil, nil
		}
		return []string{id}, nil
	default:
		return nil, nil
	}
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

// serverContext returns a context carrying this server.
//
// The relationship writer is called BY the server, and a plugin's
// writer has to ask the forum's own questions — the discussion's
// author rule, the forum's settings — which live on the server. The
// request's own context carries the server only for the PLUGIN routes
// (the module's wrapper attaches it), so the core's own mutation
// handlers cannot rely on it.
func (s *Server) serverContext() context.Context {
	return serverContext(context.Background(), s)
}

// actorFor builds a session actor for an account id.
//
// It is the form a plugin's write path uses: the relationship writer
// runs from the mutation handler, and it knows WHO is writing but has
// no request to resolve.
func (s *Server) actorFor(actorID string) (session.Actor, error) {
	if actorID == "" {
		return session.Actor{}, nil
	}

	user, err := s.repo.UserByID(actorID)
	if err != nil {
		return session.Actor{}, err
	}
	if user == nil {
		return session.Actor{}, nil
	}

	return session.Actor{User: user}, nil
}

// domainActor projects the session actor onto the domain actor.
func (s *Server) domainActor(e *core.RequestEvent) domain.Actor {
	return s.domainActorOf(s.actor(e))
}

// domainActorOf projects a session actor onto the domain actor.
func (s *Server) domainActorOf(actor session.Actor) domain.Actor {
	if actor.User == nil {
		return domain.Actor{}
	}
	// A suspended account is treated as a guest, exactly as upstream's
	// `flarum/suspend` does. Administrators are exempt: upstream refuses
	// to suspend one at all.
	if !actor.User.IsAdmin && !actor.User.SuspendedUntil.IsZero() && actor.User.SuspendedUntil.After(time.Now()) {
		return domain.SuspendedActor(actor.User.ID, actor.User.IsAdmin)
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

// canDeletePost reports whether the caller may delete a post.
//
// The same author rule as [Server.canEditPost].
func (s *Server) canDeletePost(e *core.RequestEvent, postID string) (bool, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false, nil
	}

	post, err := s.repo.FindPost(postID)
	if err != nil {
		return false, err
	}

	if post.UserID == actor.User.ID {
		return true, nil
	}

	return s.canAsModerator(e, domain.PermissionDeletePost, TypePost, post.ID)
}

// canDeleteDiscussion reports whether the caller may delete a
// discussion: the account that started it, or a caller with a moderator
// grant.
//
// The author rule matches [Server.canEditDiscussion].
func (s *Server) canDeleteDiscussion(e *core.RequestEvent, discussion *domain.Discussion) bool {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false
	}

	if discussion.UserID == actor.User.ID {
		return true
	}

	allowed, err := s.canAsModerator(e, domain.PermissionDeleteDiscussion, TypeDiscussion, discussion.ID)
	if err != nil {
		s.logger.Error("forum: permission check failed",
			slog.String("action", domain.PermissionDeleteDiscussion),
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

// allowWithinWindow reports whether a policy window permits the
// action, given the setting that governs it.
//
// It is upstream's shape, which the core's DiscussionPolicy::rename,
// the core's PostPolicy::edit and the tags extension's
// DiscussionPolicy::tag all share:
//
//   - "-1"    — always;
//   - "reply" — the "until somebody answers" window, whose predicate
//     differs per ability (see the callers);
//   - a number — for that many minutes after the subject was created.
//
// An unset or unparsable setting permits nothing.
func allowWithinWindow(setting string, createdAt time.Time, replyWindow bool) bool {
	switch setting {
	case "-1":
		return true
	case "reply":
		return replyWindow
	}

	minutes, err := strconv.Atoi(strings.TrimSpace(setting))
	if err != nil {
		return false
	}

	return time.Since(createdAt) < time.Duration(minutes)*time.Minute
}

// CanRenameDiscussion reports whether the caller may rename a
// discussion: the account that started it, while the
// `allow_renaming` setting permits it.
//
// It mirrors upstream's `DiscussionPolicy::rename()`, which requires
// the author AND the ability to reply AND the setting. The moderator
// grant is separate: upstream's `discussion.rename` group permission
// reaches the same action through `can()`.
func (s *Server) canRenameDiscussion(e *core.RequestEvent, discussionID string) (bool, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false, nil
	}

	discussion, err := s.repo.FindDiscussion(discussionID)
	if err != nil {
		return false, err
	}

	// The moderator's grant: upstream's `discussion.rename` group
	// permission.
	allowed, err := s.permissions.CanAsModerator(e.Request.Context(), s.domainActor(e),
		domain.PermissionEditDiscussion, TypeDiscussion, discussionID)
	if err != nil {
		return false, err
	}
	if allowed {
		return true, nil
	}

	// The author's own, while the setting permits it.
	if discussion.UserID != actor.User.ID {
		return false, nil
	}

	canReply, err := s.permissions.Can(e.Request.Context(), s.domainActor(e), domain.PermissionReply, TypeDiscussion, discussionID)
	if err != nil {
		return false, err
	}
	if !canReply {
		return false, nil
	}

	setting, err := s.settingValue(allowRenamingSetting)
	if err != nil {
		return false, err
	}

	// "reply" means: while nobody has answered yet.
	return allowWithinWindow(setting, discussion.CreatedAt, discussion.ParticipantCount <= 1), nil
}

// CanEditPostAsAuthor reports whether the caller may edit a post they
// wrote, while the `allow_post_editing` setting permits it.
//
// It mirrors upstream's `PostPolicy::edit()`, which requires the
// author AND the ability to reply AND the setting. The moderator's
// grant is separate: upstream's `discussion.editPosts` group
// permission.
func (s *Server) canEditPostAsAuthor(e *core.RequestEvent, postID string) (bool, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false, nil
	}

	post, err := s.repo.FindPost(postID)
	if err != nil {
		return false, err
	}

	// The moderator's grant.
	allowed, err := s.permissions.CanAsModerator(e.Request.Context(), s.domainActor(e),
		domain.PermissionEditPost, TypePost, postID)
	if err != nil {
		return false, err
	}
	if allowed {
		return true, nil
	}

	// The author's own, while the setting permits it.
	if post.UserID != actor.User.ID {
		return false, nil
	}

	canReply, err := s.permissions.Can(e.Request.Context(), s.domainActor(e), domain.PermissionReply, TypeDiscussion, post.DiscussionID)
	if err != nil {
		return false, err
	}
	if !canReply {
		return false, nil
	}

	setting, err := s.settingValue(allowPostEditingSetting)
	if err != nil {
		return false, err
	}

	// "reply" means: while the post is the last one, so the author may
	// fix a typo until somebody answers.
	discussion, err := s.repo.FindDiscussion(post.DiscussionID)
	if err != nil {
		return false, err
	}

	return allowWithinWindow(setting, post.CreatedAt, post.Number >= discussion.LastPostNumber), nil
}

// allowRenamingSetting and allowPostEditingSetting are the settings
// that govern whether an author may rename a discussion or edit a
// post.
//
// The names are upstream's.
const (
	allowRenamingSetting     = "allow_renaming"
	allowPostEditingSetting  = "allow_post_editing"
	allowHideOwnPostsSetting = "allow_hide_own_posts"
)

// CanTagDiscussion reports whether the caller may change a
// discussion's tags.
//
// It mirrors upstream Flarum's tags extension, whose
// `Access\\DiscussionPolicy::tag()` is the authority:
//
//   - the AUTHOR of the discussion, and
//   - only while the `allow_tag_change` setting permits it.
//
// The setting has upstream's three shapes:
//
//   - "-1"    — always;
//   - "reply" — while the discussion has one participant (so the
//     author may tag their own topic before anyone answers);
//   - a number — for that many minutes after the discussion was
//     created.
//
// An UNSET setting permits nothing, which is upstream's default: the
// permission is off until an administrator turns it on.
//
// The moderator grant is deliberately NOT part of this: upstream's
// `tag` ability is a POLICY, not a group permission, so a moderator
// group row does not widen it.
func (s *Server) canTagDiscussion(e *core.RequestEvent, discussionID string) (bool, error) {
	actor := s.actor(e)
	if !actor.IsAuthenticated() {
		return false, nil
	}

	return s.canTagDiscussionAs(e.Request.Context(), actor, discussionID)
}

// canTagDiscussionAs reports whether an ACTOR may change a
// discussion's tags.
//
// It is the form a plugin's write path uses: the relationship writer
// runs from the mutation handler, and it has no request event of its
// own, but it still has to ask the same question the `canTag`
// attribute answers — otherwise the flag and the write would disagree.
func (s *Server) canTagDiscussionAs(ctx context.Context, actor session.Actor, discussionID string) (bool, error) {
	if !actor.IsAuthenticated() {
		return false, nil
	}

	discussion, err := s.repo.FindDiscussion(discussionID)
	if err != nil {
		return false, err
	}

	// Only the author may tag their own discussion.
	if discussion.UserID != actor.User.ID {
		return false, nil
	}

	// The author must also be able to reply, which is upstream's
	// condition on the policy.
	canReply, err := s.permissions.Can(ctx, s.domainActorOf(actor), domain.PermissionReply, TypeDiscussion, discussionID)
	if err != nil {
		return false, err
	}
	if !canReply {
		return false, nil
	}

	allow, err := s.settingValue(AllowTagChangeSetting)
	if err != nil {
		return false, err
	}

	switch allow {
	case "-1":
		return true, nil
	case "reply":
		return discussion.ParticipantCount <= 1, nil
	}

	// A number of minutes after the discussion was created.
	minutes, err := strconv.Atoi(strings.TrimSpace(allow))
	if err != nil {
		// An unset or unparsable setting permits nothing, which is
		// upstream's default: the permission is off until an
		// administrator picks an option.
		return false, nil //nolint:nilerr // an unparsable setting permits nothing
	}

	return time.Since(discussion.CreatedAt) < time.Duration(minutes)*time.Minute, nil
}

// AllowTagChangeSetting is the setting that governs whether the
// author of a discussion may change its tags.
//
// The name is upstream's.
const AllowTagChangeSetting = "allow_tag_change"

// SettingString reads a setting's value as a string.
//
// The settings column is a PocketBase JSONField, so the value comes
// back as `types.JSONRaw` (a []byte of JSON). A JSON string decodes to
// the string it holds; a JSON number decodes to its text form, because
// the upstream settings are compared against "10" and "-1" as text.
//
// It is EXPORTED because a plugin's own reader needs it, and a
// reader that only handles `value.(string)` never matches: the stored
// value is raw JSON bytes, so the switch falls through to the
// fallback and the setting reads as UNSET — silently, and in a way
// that looks like the setting was never saved.
func SettingString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case types.JSONRaw:
		return decodeJSONString([]byte(v))
	case []byte:
		return decodeJSONString(v)
	default:
		return "", false
	}
}

// decodeJSONString decodes a JSON document that holds a string or a
// number.
func decodeJSONString(raw []byte) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, true
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), true
	}

	return "", false
}

// upstreamSettingDefaults are the settings upstream writes at install
// (framework/core/src/Install/Steps/WriteSettings.php) that the
// forum's POLICIES read.
//
// They are the fallback when the settings table has no row for the key,
// because upstream writes those rows at install and this port starts
// with an empty table. Without the fallback a policy would read an
// absent setting as "unset", and an unset setting permits NOTHING — so
// the author of a discussion could never rename it or edit their post,
// where upstream allows both (its defaults are "10" and "reply").
//
// The values match upstream's: `allow_renaming` is "10" (ten minutes),
// `allow_post_editing` and `allow_hide_own_posts` are "reply" (until
// somebody answers), and the tags extension's `allow_tag_change` is
// "0" ("Never", until an administrator picks an option).
var upstreamSettingDefaults = map[string]string{
	allowRenamingSetting:     "10",
	allowPostEditingSetting:  "reply",
	allowHideOwnPostsSetting: "reply",
	AllowTagChangeSetting:    "0",
}

// settingValue reads a setting, falling back to upstream's installed
// default.
func (s *Server) settingValue(key string) (string, error) {
	fallback, ok := upstreamSettingDefaults[key]
	if !ok {
		fallback = ""
	}

	value, err := s.repo.SettingValue(key, fallback)
	if err != nil {
		return "", err
	}

	setting, ok := SettingString(value)
	if !ok {
		return fallback, nil
	}
	return setting, nil
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

// writeEmpty answers with a bodyless status.
//
// It is for the admin surface's command endpoints, which upstream
// answers with `new EmptyResponse(204)`. A 204 carrying a body is not
// what upstream sends, and `e.JSON` would serialise the zero Document as
// `{"data":null}`.
func (*Server) writeEmpty(e *core.RequestEvent, status int) error {
	e.Response.WriteHeader(status)
	return nil
}

// notFoundError builds a not-found apiError.
func notFoundError(detail string) *apiError { return errNotFound(detail) }

// errInternal builds an internal apiError.
func errInternal(detail string, err error) *apiError {
	return newAPIError(http.StatusInternalServerError, CodeInternal, detail, err)
}

// unusedGuard keeps the errors import honest for future use.
var _ = errors.New
