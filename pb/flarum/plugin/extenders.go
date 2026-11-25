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
	// permissions collects the plugin's permission handlers.
	permissions []PermissionHandler
	// assets collects the plugin's frontend asset declarations.
	assets []AssetBundle
	// locales collects the plugin's language-pack declarations.
	locales []LocaleBundle
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

// Permissions returns the declared permission handlers.
func (e *Extenders) Permissions() []PermissionHandler { return e.permissions }

// Assets returns the declared asset bundles.
func (e *Extenders) Assets() []AssetBundle { return e.assets }

// Locales returns the declared language packs.
func (e *Extenders) Locales() []LocaleBundle { return e.locales }

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
