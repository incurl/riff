package plugin

import (
	"context"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
)

// RouteDef declares an HTTP route a plugin owns.
//
// The path is relative to the module's base path, never to the host
// application root: a plugin route is always mounted beneath the forum,
// so it cannot collide with the host's own routes.
type RouteDef struct {
	// Method is the HTTP method, e.g. "GET".
	Method string
	// Path is the route path relative to the module base path, e.g.
	// "/api/acme/bookmarks".
	Path string
	// Handler serves the route.
	Handler RequestHandler
	// Middleware runs before the handler.
	Middleware []RequestHandler
}

// RouteRegistrar mounts routes on a router.
//
// The router handed to a plugin is already scoped to the module's base
// path, so a plugin cannot register a route outside the forum.
type RouteRegistrar interface {
	// RegisterRoute mounts a plugin route.
	RegisterRoute(def RouteDef)
}

// Migration is one forward-only schema or seed step.
//
// Migrations are idempotent: a step that has already been applied is
// skipped, and a step that finds its target already in place succeeds.
type Migration struct {
	// Name identifies the migration within its plugin.
	Name string
	// Up applies the migration.
	Up func(ctx context.Context, app core.App) error
}

// SettingsSchema declares a plugin's admin settings.
//
// The settings are FLAT keys, not groups: a setting's key is the whole
// key it is stored under (`flarum-likes.like_own_post`), which is
// upstream's own spelling — an operator's saved setting carries over,
// and the admin application looks the setting up by that key.
//
// The schema is the SERVER's half of the contract: it names the
// settings the plugin reads and the value each falls back to. The
// admin application's half — the form the setting is edited on — is
// the plugin's frontend bundle, which registers it with
// `Extend.Admin().setting(...)`.
//
// The two halves must agree on the KEY: the server reads the setting
// by it, and the form writes it by it.
type SettingsSchema struct {
	// Fields describes the individual settings.
	Fields []SettingField
}

// SettingField is one configurable value in a plugin's settings.
type SettingField struct {
	// Key is the setting's key, e.g. "flarum-likes.like_own_post".
	//
	// It is the FULL key, not a name within a group: the settings
	// table is a flat key/value store, and upstream's keys are
	// prefixed with the extension's name.
	Key string
	// Label is the human-readable label.
	Label string
	// Type is the input type: "bool", "number" or "text".
	//
	// It is the admin application's own vocabulary
	// (`FormGroup`'s `BooleanSettingTypes` and friends), so the
	// schema and the form that renders it speak the same language.
	Type string
	// Default is the value used when the setting is unset.
	//
	// It is what the server falls back to, so it MUST be the value
	// the plugin's own reader uses. A default here that disagrees
	// with the reader's is a setting the admin portal shows one
	// value for and the forum uses another for.
	Default any
	// Options are the allowed values for a "select" field.
	Options []string
}

// NotificationType is a notification kind a plugin SENDS.
//
// A plugin declares it for the same reason it declares the settings it
// reads: the forum publishes a notification preference for every declared
// kind (`notify_<kind>_<channel>`), and a kind nobody declares is a kind a
// reader cannot switch off. Upstream's equivalent is the notification
// blueprint registered through `Extend\Notification()`
// (`flarum/likes`: `->type(PostLikedBlueprint::class, ['alert'])`), whose
// `Notification::getDefaults()` call produces the same keys.
//
// The kind must be one the `forum_notifications` collection's `type` select
// accepts: the collection refuses a value it does not list, so a kind
// declared here and missing there is a notification write that fails.
type NotificationType struct {
	// Type is the kind stored in the notifications table, e.g. "postLiked".
	Type string
	// Channels are the notification drivers the kind uses, e.g. "alert".
	//
	// Only a channel the forum actually DELIVERS should be listed: the
	// settings page renders a checkbox per (kind, driver) pair and
	// disables the ones the reader has no preference for, so declaring
	// "email" for a kind the forum never emails would show a checkbox
	// that toggles nothing.
	Channels []string
}

// AttributeContributor contributes an attribute to a JSON:API resource.
//
// This is how a plugin exposes a field on a core resource without the
// core knowing about the plugin.
type AttributeContributor struct {
	// Resource is the JSON:API resource type, e.g. "posts".
	Resource string
	// Name is the attribute name.
	Name string
	// Visible decides whether the attribute is present at all; nil means
	// always. It is upstream's `Field::visible()`, and an invisible field
	// is ABSENT rather than null.
	Visible func(req *AttributeRequest) bool
	// Value computes the attribute for a record.
	Value func(ctx context.Context, req *AttributeRequest) (any, error)
}

// AttributeRequest is what an attribute contributor receives.
type AttributeRequest struct {
	// App is the PocketBase app.
	App core.App
	// ActorID is the id of the acting user, empty for anonymous.
	ActorID string
	// RecordID is the id of the record the attribute belongs to.
	RecordID string
	// Type is the JSON:API resource type.
	Type string
	// IsAdmin reports whether the actor is an administrator.
	//
	// Upstream gates a field's visibility on the actor
	// (`->visible(fn (object $model, Context $context) =>
	// $context->getActor()->isAdmin())`), which is how the Statistics
	// extension publishes `statisticsEntities` to administrators only.
	IsAdmin bool
	// Can reports whether the actor may perform an action on a subject.
	//
	// It is the forum's permission service, so a plugin's `can*` attribute
	// answers exactly what its own route answers. A plugin that
	// re-implemented the rule would drift from its write path, and the UI
	// would offer a control the endpoint then refuses.
	Can func(action, subjectType, subjectID string) bool
}

// RelationshipContributor contributes a relationship to a JSON:API
// resource.
type RelationshipContributor struct {
	// Resource is the JSON:API resource type the relationship is on.
	Resource string
	// Name is the relationship name.
	Name string
	// Type is the JSON:API type of the related resources, e.g. "users".
	Type string
	// Many reports whether the relationship is to-many.
	Many bool
	// Resolve computes the related resource identifiers.
	Resolve func(ctx context.Context, req *RelationshipRequest) ([]Identifier, error)
}

// RelationshipRequest is what a relationship contributor receives.
type RelationshipRequest struct {
	// App is the PocketBase app.
	App core.App
	// ActorID is the id of the acting user, empty for anonymous.
	ActorID string
	// RecordID is the id of the record the relationship belongs to.
	RecordID string
	// Type is the JSON:API resource type.
	Type string
	// IsAdmin reports whether the actor is an administrator.
	//
	// Upstream gates a field's visibility on the actor
	// (`->visible(fn (object $model, Context $context) =>
	// $context->getActor()->isAdmin())`), which is how the Statistics
	// extension publishes `statisticsEntities` to administrators only.
	IsAdmin bool
	// Can reports whether the actor may perform an action on a subject.
	//
	// It is the forum's permission service, so a plugin's relationship
	// answers exactly what its own route answers. It is the same
	// checker [AttributeRequest] carries: a relationship that filters
	// by what the caller may see needs the same question answered for
	// the same caller.
	Can func(action, subjectType, subjectID string) bool
}

// RelationshipWriter contributes the WRITE side of a relationship.
//
// Upstream's JSON:API lets a client set a relationship by saving the
// resource with it: `discussion.save({ relationships: { tags } })`. The
// core does not know which relationships are writable — that is the
// extension's business — so a plugin declares the write side here and
// the core calls it when a request carries the relationship.
//
// It is the counterpart of [RelationshipContributor], which contributes
// the READ side.
type RelationshipWriter struct {
	// Resource is the JSON:API resource type, e.g. "discussions".
	Resource string
	// Name is the relationship name, e.g. "tags".
	Name string
	// Write applies the relationship's identifiers to the record.
	//
	// The identifiers are the resource ids the client named. The
	// contributor decides what to do with them: the core does not know
	// whether a relationship is a set of links, a single foreign key
	// or something else.
	//
	// It returns an error when the relationship cannot be applied, and
	// the core turns that into a validation failure.
	Write func(ctx context.Context, req *RelationshipWriteRequest) error
}

// RelationshipWriteRequest is what a relationship writer receives.
type RelationshipWriteRequest struct {
	// App is the PocketBase app.
	App core.App
	// ActorID is the id of the acting user, empty for anonymous.
	ActorID string
	// RecordID is the id of the record the relationship belongs to.
	RecordID string
	// Type is the JSON:API resource type.
	Type string
	// IDs are the related resource ids the client named.
	IDs []string
	// Creating reports whether the record is being created.
	Creating bool
	// Can reports whether the actor may perform an action on a subject.
	Can func(action, subjectType, subjectID string) bool
	// CanScoped reports whether the actor may perform an action on the
	// subject ALONE — the subject's own rules answer the question and
	// the global grant is not consulted.
	//
	// It is what a RESTRICTED tag asks: upstream's `TagPolicy` returns
	// the scoped answer alone for a restricted tag, because upstream's
	// `User::hasPermission` is an EXACT match against the group's
	// permission rows. A tag that inherited the global grant would
	// restrict nobody.
	//
	// It is nil when the forum does not provide one, and a plugin must
	// then fall back to [RelationshipWriterRequest.Can].
	CanScoped func(action, subjectType, subjectID string) bool
}

// ResourceSerializer renders a plugin-owned JSON:API resource.
//
// This is upstream's `Extend\ApiResource`, and it is the extension point
// a plugin needs when it owns a resource TYPE of its own — the polls
// extension's `polls`, `poll_options` and `poll_votes`.
//
// Without it the forum's `included` array cannot carry a plugin's
// resource, and a relationship to it resolves to `undefined` in the
// frontend. The frontend reads a relation out of the STORE, not out of
// the identifier:
//
//	static hasMany(name) {
//	  const data = this.data.relationships?.[name]?.data;
//	  if (data) return data.map((d) => this.store.getById(d.type, d.id));
//	}
//
// and the store is filled from `payload.included`. So the RELATED
// RESOURCE has to travel with the response, or the relation reads as
// absent — the poll is in the database, the post carries the
// relationship, and the UI shows nothing, with no error anywhere.
type ResourceSerializer struct {
	// Type is the JSON:API resource type, e.g. "polls".
	Type string
	// Render renders one resource by id.
	//
	// It returns nil when the resource does not exist or the caller may
	// not see it: a missing resource is simply ABSENT from the
	// `included` array, which is not a failed request.
	Render func(ctx context.Context, req *ResourceRequest) (*Resource, error)
}

// ResourceRequest is what a resource serializer receives.
type ResourceRequest struct {
	// App is the PocketBase app.
	App core.App
	// ActorID is the id of the acting user, empty for anonymous.
	ActorID string
	// ID is the id of the resource to render.
	ID string
	// IsAdmin reports whether the actor is an administrator.
	IsAdmin bool
	// Can reports whether the actor may perform an action on a subject.
	//
	// It is the forum's permission service, so a plugin's `can*`
	// attribute answers exactly what its own route answers.
	Can func(action, subjectType, subjectID string) bool
	// CanScoped reports whether the actor may perform an action on the
	// subject ALONE — the subject's own rules answer the question and
	// the global grant is not consulted.
	//
	// It is what a RESTRICTED tag asks. It is nil when the forum does
	// not provide one, and a plugin must then fall back to
	// [ResourceRequest.Can].
	CanScoped func(action, subjectType, subjectID string) bool
}

// Resource is a plugin-rendered JSON:API resource.
//
// It is the plugin package's own narrow view of a resource, so the
// plugin package does not depend on the API package — the two are
// siblings, and the dependency would point the wrong way. The module
// adapts it onto the API server's own type.
type Resource struct {
	// Type is the JSON:API resource type, e.g. "polls".
	Type string
	// ID is the resource's id.
	ID string
	// Attributes are the resource's fields.
	Attributes map[string]any
	// Relationships are the resource's related resources.
	//
	// A relation's `Data` is either an [Identifier], a list of them, or
	// nil — the same shapes the API package uses.
	Relationships map[string]Relationship
}

// Relationship is a JSON:API relationship object.
type Relationship struct {
	// Data holds the related resource identifier(s).
	Data any
}

// DefaultIncludeContributor declares a resource type's default include
// paths.
//
// It is upstream's `addDefaultInclude`: the polls extension adds
// `['polls', 'polls.options', 'polls.myVotes', 'polls.myVotes.option']`
// to the POST resource, so a post's poll arrives with its options and
// with the reader's own votes.
//
// The paths are DOTTED chains, exactly as upstream spells them, and the
// forum's `included` walk follows every hop.
type DefaultIncludeContributor struct {
	// Resource is the JSON:API resource type, e.g. "posts".
	Resource string
	// Paths are the include chains, e.g. "polls.options".
	Paths []string
}

// Identifier is a JSON:API resource identifier.
type Identifier struct {
	// Type is the JSON:API resource type.
	Type string
	// ID is the resource's id.
	ID string
}

// PermissionHandler is an access-check handler a plugin registers.
//
// It answers whether an actor may perform an action on a subject. A
// handler that returns nil has no opinion and the next handler decides;
// returning a non-nil error denies the action.
type PermissionHandler struct {
	// Action is the permission key, e.g. "likePost".
	Action string
	// Check answers the access question.
	Check func(ctx context.Context, req *PermissionRequest) error
}

// PermissionRequest is what a permission handler receives.
type PermissionRequest struct {
	// App is the PocketBase app.
	App core.App
	// ActorID is the id of the acting user, empty for anonymous.
	ActorID string
	// SubjectType is the kind of resource the action targets, e.g.
	// "posts".
	SubjectType string
	// SubjectID is the id of the targeted resource.
	SubjectID string
}

// Extenders is the accumulator a plugin registers into.
//
// It is the Go equivalent of Flarum's extender objects: each method
// corresponds to one extender area, and a plugin calls the ones it needs.
type Extenders struct {
	// Container is the DI container. A plugin binds or overrides a
	// service through it.
	Container *flarumcore.Container
	// Bus is the event bus. A plugin subscribes to events through it.
	Bus *flarumcore.EventBus
	// Logger is the plugin logger.
	Logger *slog.Logger
	// Registry resolves logical model names to physical collections.
	Registry NameResolver

	// routes collects the plugin's route declarations.
	routes []RouteDef
	// migrations collects the plugin's migrations.
	migrations []Migration
	// settings collects the plugin's settings schemas.
	settings []SettingsSchema
	// attributes collects the plugin's attribute contributions.
	attributes []AttributeContributor
	// relationships collects the plugin's relationship contributions.
	relationships []RelationshipContributor
	// relationshipWriters collects the plugin's relationship write
	// contributions.
	relationshipWriters []RelationshipWriter
	// resourceSerializers collects the plugin's own resource types.
	resourceSerializers []ResourceSerializer
	// defaultIncludes collects the plugin's default include paths.
	defaultIncludes []DefaultIncludeContributor
	// permissions collects the plugin's permission handlers.
	permissions []PermissionHandler
	// assets collects the plugin's frontend asset declarations.
	assets []AssetBundle
	// locales collects the plugin's language-pack declarations.
	locales []LocaleBundle
	// notificationTypes collects the notification kinds the plugins send.
	notificationTypes []NotificationType
}

// NameResolver resolves logical model names to physical collection names.
//
// It is the plugin package's view of the repository registry, kept as an
// interface so the plugin package does not depend on the repository
// package.
type NameResolver interface {
	// Physical returns the physical collection name for a logical name.
	Physical(logical string) string
	// ExtensionPhysical returns the physical collection name for a
	// plugin-owned entity.
	ExtensionPhysical(pluginID, entity string) string
}

// AssetBundle is a plugin's frontend asset declaration.
//
// The bundle files are served by the Go backend under the forum asset
// prefix; the SPA loads them in dependency order.
type AssetBundle struct {
	// PluginID is the owning plugin.
	PluginID string
	// Forum is the forum-app bundle path, relative to the asset root.
	Forum string
	// Admin is the admin-app bundle path, relative to the asset root.
	Admin string
	// CSS is the stylesheet path, relative to the asset root.
	CSS string
	// Locale is the extension's OWN catalogue, relative to the asset
	// root.
	//
	// It is the extension's English strings, compiled into the same
	// `app.translator.addTranslations(…)` script the locale bundles
	// use. It is loaded BEFORE the active locale's catalogue, because a
	// translation is an OVERRIDE: the locale's own copy of a key has to
	// be added last.
	//
	// An extension that ships no catalogue leaves it empty, and nothing
	// is loaded for it.
	Locale string
	// Version is the plugin version, used for cache busting.
	Version string
}

// LocaleBundle is a plugin's language-pack declaration.
//
// A language pack is a catalogue, not a code bundle: the SPA loads exactly
// one locale bundle per frontend (the active locale's), and that bundle
// carries the core's translations for that language as well as the
// extensions'. So a pack declares only the files it adds to the
// `forum-<code>.js` / `admin-<code>.js` pair.
//
// It mirrors the `extra.flarum-locale` block of an upstream language
// pack's composer.json, which is what tells Flarum the extension provides
// a locale rather than code.
type LocaleBundle struct {
	// PluginID is the owning plugin.
	PluginID string
	// Code is the locale code, e.g. "zh-Hans". It is what the host passes
	// as the active locale and what the asset name carries.
	Code string
	// Title is the locale's name in its own language, e.g. "简体中文". It
	// is what the frontend's locale list shows.
	Title string
	// Forum is the forum-app locale bundle, relative to the asset root.
	Forum string
	// Admin is the admin-app locale bundle, relative to the asset root.
	Admin string
	// Version is the plugin version, used for cache busting.
	Version string
}

// NewExtenders returns an empty extender accumulator.
func NewExtenders() *Extenders {
	return &Extenders{
		Container: flarumcore.NewContainer(),
		Bus:       flarumcore.NewEventBus(nil),
	}
}

// AddRoute declares an HTTP route.
//
// The path is relative to the module base path.
func (e *Extenders) AddRoute(def RouteDef) {
	e.routes = append(e.routes, def)
}

// AddMigration declares a migration step.
func (e *Extenders) AddMigration(m Migration) {
	e.migrations = append(e.migrations, m)
}

// AddSettings declares a settings schema.
func (e *Extenders) AddSettings(schema SettingsSchema) {
	e.settings = append(e.settings, schema)
}

// AddAttribute declares an attribute contribution.
func (e *Extenders) AddAttribute(contributor AttributeContributor) {
	e.attributes = append(e.attributes, contributor)
}

// AddRelationship declares a relationship contribution.
func (e *Extenders) AddRelationship(contributor RelationshipContributor) {
	e.relationships = append(e.relationships, contributor)
}

// AddRelationshipWriter declares the write side of a relationship.
func (e *Extenders) AddRelationshipWriter(writer RelationshipWriter) {
	e.relationshipWriters = append(e.relationshipWriters, writer)
}

// AddResourceSerializer declares a plugin-owned JSON:API resource type.
//
// It is the extension point a plugin needs when it ships resources of
// its own, so the forum's `included` array can carry them.
func (e *Extenders) AddResourceSerializer(serializer ResourceSerializer) {
	e.resourceSerializers = append(e.resourceSerializers, serializer)
}

// AddDefaultInclude declares a resource type's default include paths.
//
// It is upstream's `addDefaultInclude`.
func (e *Extenders) AddDefaultInclude(contributor DefaultIncludeContributor) {
	e.defaultIncludes = append(e.defaultIncludes, contributor)
}

// AddPermission declares a permission handler.
func (e *Extenders) AddPermission(handler PermissionHandler) {
	e.permissions = append(e.permissions, handler)
}

// AddAsset declares a frontend asset bundle.
func (e *Extenders) AddAsset(bundle AssetBundle) {
	e.assets = append(e.assets, bundle)
}

// AddLocale declares a language pack.
func (e *Extenders) AddLocale(bundle LocaleBundle) {
	e.locales = append(e.locales, bundle)
}

// AddNotificationType declares a notification kind the plugin sends.
func (e *Extenders) AddNotificationType(notificationType NotificationType) {
	e.notificationTypes = append(e.notificationTypes, notificationType)
}

// Routes returns the declared routes.
func (e *Extenders) Routes() []RouteDef { return e.routes }

// Migrations returns the declared migrations.
func (e *Extenders) Migrations() []Migration { return e.migrations }

// Settings returns the declared settings schemas.
func (e *Extenders) Settings() []SettingsSchema { return e.settings }

// Attributes returns the declared attribute contributions.
func (e *Extenders) Attributes() []AttributeContributor { return e.attributes }

// Relationships returns the declared relationship contributions.
func (e *Extenders) Relationships() []RelationshipContributor { return e.relationships }

// RelationshipWriters returns the declared relationship write
// contributions.
func (e *Extenders) RelationshipWriters() []RelationshipWriter { return e.relationshipWriters }

// ResourceSerializers returns the declared resource serializers.
func (e *Extenders) ResourceSerializers() []ResourceSerializer { return e.resourceSerializers }

// DefaultIncludes returns the declared default include paths.
func (e *Extenders) DefaultIncludes() []DefaultIncludeContributor { return e.defaultIncludes }

// Permissions returns the declared permission handlers.
func (e *Extenders) Permissions() []PermissionHandler { return e.permissions }

// Assets returns the declared asset bundles.
func (e *Extenders) Assets() []AssetBundle { return e.assets }

// Locales returns the declared language packs.
func (e *Extenders) Locales() []LocaleBundle { return e.locales }

// NotificationTypes returns the declared notification kinds.
func (e *Extenders) NotificationTypes() []NotificationType { return e.notificationTypes }

// Migrate runs a plugin's migrations.
//
// Each step is recorded per plugin so a step runs once. A step that has
// already been applied is skipped; a step that fails aborts the run with
// an error naming the plugin and the step.
//
// The applied-version bookkeeping lives in the forum's plugin_migrations
// collection, which is why this takes the app rather than a store.
func (e *Extenders) Migrate(ctx context.Context, app core.App, pluginID string, record func(ctx context.Context, pluginID, name string) error) error {
	for _, migration := range e.migrations {
		if migration.Up == nil {
			continue
		}
		if err := migration.Up(ctx, app); err != nil {
			return err
		}
		if record != nil {
			if err := record(ctx, pluginID, migration.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
