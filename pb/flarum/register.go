// Package flarum is a self-contained Flarum-compatible forum module
// for the riff PocketBase host.
//
// It reimplements Flarum 2.x's public JSON:API contract in Go, keeps
// Flarum's extension (Extender) model, and serves the reused Flarum
// Mithril frontend. No PHP runtime is involved.
//
// The import path is github.com/incurl/riff/pb/flarum: the module
// root is `pb/flarum/`, and the vendored Flarum frontend lives
// beside it in `pb/flarum/frontend/packages/`.
//
// # Mounting
//
// The module is mounted under a configurable base path (default /forum) by
// [Register], which binds the route tree onto the host app's router:
//
//	GET    /forum                       SPA shell (the forum app)
//	GET    /forum/admin                 SPA shell (the admin app)
//	GET    /forum/api/...               JSON:API
//	POST   /forum/login                 session login
//	POST   /forum/logout                session logout
//	GET    /forum/assets/...            built frontend + plugin bundles
//
// Nothing outside the base path is touched: the forum's middleware, routes
// and PocketBase collections are all scoped to the module.
//
// # Data
//
// PocketBase is the single system of record. The shared built-in `users`
// auth collection is the account source of truth and is used directly (there
// is no forum_users store); every other forum collection carries the
// `forum_` prefix. See the repository package for the name registry that
// resolves logical model names to physical collection names.
//
// # Extension model
//
// The plugin package provides the Extender-equivalent: a DI container, an
// event bus, a migration framework, and a plugin manager with a
// Register/Boot/Shutdown lifecycle. Tags and Likes ship as first-party
// static Go plugins and are the reference implementation.
package flarum

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/markdown"
	"github.com/incurl/riff/pb/flarum/api"
	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/frontend"
	"github.com/incurl/riff/pb/flarum/migrations"
	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/repository"
	"github.com/incurl/riff/pb/flarum/session"
)

// DefaultBasePath is the mount path the forum uses when the host does not
// configure one.
const DefaultBasePath = "/forum"

// CoreVersion is the forum core's version.
//
// It is what a plugin's `coreRequire` range is checked against.
const CoreVersion = "2.0.0"

// Options configures the forum module.
type Options struct {
	// BasePath is the path the module is mounted at. Defaults to
	// "/forum".
	BasePath string
	// CollectionPrefix is the reserved prefix for the forum's own
	// collections. Defaults to "forum_".
	CollectionPrefix string
	// Logger logs the module's activity. Defaults to the standard
	// logger.
	Logger *slog.Logger
	// Plugins are the plugins to register. The first-party Tags and
	// Likes plugins are added by default unless Plugins is set
	// explicitly.
	Plugins []plugin.Plugin
	// Locale is the active locale, e.g. "en" or "zh-Hans". An unset or
	// unknown value falls back to "en", so a typo never leaves the forum
	// with no catalogue at all.
	//
	// The value selects which `forum-<locale>.js` / `admin-<locale>.js`
	// pair the shell loads: the core ships `en`, and a language-pack
	// plugin declares the others.
	Locale string
	// DisableDefaultPlugins turns off the first-party plugins. It exists
	// so a test can exercise the core alone.
	DisableDefaultPlugins bool
}

// Module is the forum's runtime.
//
// The host owns it: it registers the routes, boots the plugins, and
// shuts them down.
type Module struct {
	// app is the PocketBase app.
	app core.App
	// opts are the module's options.
	opts Options
	// logger logs the module's activity.
	logger *slog.Logger
	// registry resolves logical names to physical collections.
	registry *repository.Registry
	// repo is the data access layer.
	repo *repository.Repository
	// sessions resolves the caller.
	sessions *session.Resolver
	// permissions answers access questions.
	permissions *domain.PermissionService
	// bus is the event bus.
	bus *flarumcore.EventBus
	// container is the DI container.
	container *flarumcore.Container
	// plugins is the plugin manager.
	plugins *plugin.Manager
	// api is the JSON:API server.
	api *api.Server
	// assets are the built frontend bundles.
	assets *frontend.Assets
}

// Register mounts the forum on a PocketBase app.
//
// It binds the OnServe hook that creates the module's services and
// mounts its routes. Everything is built there rather than here: the app
// is not bootstrapped when Register runs, so the collections may not
// exist yet and a collection lookup would panic.
func Register(app core.App, opts Options) error {
	if app == nil {
		return errors.New("forum: nil app")
	}

	if opts.BasePath == "" {
		opts.BasePath = DefaultBasePath
	}
	opts.BasePath = "/" + strings.Trim(opts.BasePath, "/")
	if opts.CollectionPrefix == "" {
		opts.CollectionPrefix = repository.DefaultPrefix
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		module, err := newModule(app, opts)
		if err != nil {
			return err
		}
		if err := module.boot(context.Background()); err != nil {
			return err
		}
		module.mount(e.Router)
		return e.Next()
	})

	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		return e.Next()
	})

	return nil
}

// extensionInfos describes the forum's plugins for the API server.
//
// The admin surface addresses a plugin by the FLATTENED id
// (`vendor-name`) — it is a path segment — while the state table
// records the RAW id (`vendor/name`), so the server is told both.
//
// The list is the REGISTERED plugins, not the enabled ones: a disabled
// plugin is still a plugin the administrator can switch back on.
func extensionInfos(manager *plugin.Manager) map[string]api.ExtensionInfo {
	infos := map[string]api.ExtensionInfo{}

	for _, manifest := range manager.RegisteredManifests() {
		infos[manifest.ID] = api.ExtensionInfo{
			ID:   manifest.ID,
			Name: manifest.Name,
		}
	}

	return infos
}

// newModule builds the module's services.
func newModule(app core.App, opts Options) (*Module, error) {
	registry := repository.NewRegistry(opts.CollectionPrefix)

	// The pre-flight collision check runs before any migration: a name
	// the forum does not own must never be attached to.
	if err := migrations.CheckCollisions(app, registry); err != nil {
		return nil, err
	}

	if err := migrations.Core(app, registry); err != nil {
		return nil, errors.Wrap(err, "forum: the core migrations failed")
	}
	if err := migrations.Seed(app, registry); err != nil {
		return nil, errors.Wrap(err, "forum: the core seeding failed")
	}

	logger := opts.Logger
	bus := flarumcore.NewEventBus(logger)
	container := flarumcore.NewContainer()

	repo := repository.New(app, registry)
	sessions := session.NewResolver(app, opts.BasePath).WithGroups(func(userID string) []string {
		// The groups decide whether the caller administers the forum, so
		// the resolver has to be able to read them. The lookup is
		// deliberately forgiving: a failure to read the memberships
		// leaves the caller with none, which is the safe direction.
		groups, err := repo.UserGroupIDs(userID)
		if err != nil {
			return nil
		}
		return groups
	})
	permissions := domain.NewPermissionService(repo, busAdapter{bus})

	assets, err := frontend.Load()
	if err != nil {
		return nil, err
	}

	// The forum renders a post's Markdown server-side: the frontend
	// inserts `contentHtml` as trusted HTML, so the body has to be
	// rendered before it is stored. The engine is riff's own markdown
	// package, at its plain GFM configuration — the same CommonMark plus
	// tables, strikethrough, task lists and linkify that Flarum core's
	// formatter supports.
	//
	// The tag extension is deliberately NOT enabled. It is riff's memos
	// syntax (a #tag becomes a memo tag), and it builds AST nodes with
	// no HTML renderer, so enabling it would silently drop every `#tag`
	// from the rendered body. In a forum post a `#tag` is literal text,
	// which is what Flarum core shows too.
	//
	// The mention extension IS enabled, and it is the one exception:
	// upstream Flarum's `flarum/mentions` renders `@username` as a link
	// to the account's profile, and the forum does the same. It is
	// enabled through `WithMentionLinks`, which does BOTH halves — the
	// parser and the HTML node renderer — because a mention node carries
	// no source segment: a renderer that emits nothing DELETES the
	// mention from the rendered body.
	//
	// The URL is the forum's own user route (`/u/:username`), so the
	// link resolves inside the mount path.
	//
	// The service is stateless and safe to share across goroutines.
	markdownService := markdown.NewService(
		markdown.WithMentionLinks(func(username string) string {
			return opts.BasePath + "/u/" + url.PathEscape(username)
		}),
	)

	apiServer := api.NewServer(api.Options{
		App:         app,
		Repository:  repo,
		Sessions:    sessions,
		Permissions: permissions,
		BasePath:    opts.BasePath,
		Logger:      logger,
		Bus:         bus,
		Renderer:    markdownService,
	})

	manager := plugin.NewManager(CoreVersion, bus, container, logger, registry)

	module := &Module{
		app:         app,
		opts:        opts,
		logger:      logger,
		registry:    registry,
		repo:        repo,
		sessions:    sessions,
		permissions: permissions,
		bus:         bus,
		container:   container,
		plugins:     manager,
		api:         apiServer,
		assets:      assets,
	}

	// The core services are bound first, so a plugin's binding (which
	// carries a higher priority) can override them.
	flarumcore.Bind[domain.PermissionChecker](container, permissions)
	flarumcore.Bind[*api.Server](container, apiServer)
	flarumcore.Bind[*repository.Repository](container, repo)

	plugins := opts.Plugins
	if !opts.DisableDefaultPlugins {
		plugins = append(defaultPlugins(), plugins...)
	}
	for _, p := range plugins {
		if err := manager.Register(p); err != nil {
			return nil, err
		}
	}

	// The API server is built before the plugins are registered, so the
	// admin surface's plugin list is filled in here: the server needs
	// both spellings of every plugin id (the flattened one the frontend
	// addresses a plugin by, and the raw one the state table records).
	apiServer.SetExtensions(extensionInfos(manager))

	return module, nil
}

// pluginCan returns a permission checker for a plugin callback.
//
// It resolves the actor once, so a resource carrying several
// plugin-contributed `can*` attributes does not read the account and its
// groups once per attribute.
func (m *Module) pluginCan(ctx context.Context, actorID string) func(action, subjectType, subjectID string) bool {
	actor := m.pluginActor(actorID)

	return func(action, subjectType, subjectID string) bool {
		if actor.UserID == "" {
			return false
		}
		allowed, err := m.permissions.Can(ctx, actor, action, subjectType, subjectID)
		return err == nil && allowed
	}
}

// pluginCanScoped is the scoped form of [Module.pluginCan]: the
// subject's own rules answer the question, and the global grant is not
// consulted.
//
// It is what a RESTRICTED tag asks, and it is upstream's exact match —
// see [domain.PermissionChecker.CanScoped].
func (m *Module) pluginCanScoped(ctx context.Context, actorID string) func(action, subjectType, subjectID string) bool {
	actor := m.pluginActor(actorID)

	return func(action, subjectType, subjectID string) bool {
		if actor.UserID == "" {
			return false
		}
		allowed, err := m.permissions.CanScoped(ctx, actor, action, subjectType, subjectID)
		return err == nil && allowed
	}
}

// pluginActor resolves the domain actor for an account id.
//
// A PocketBase SUPERUSER is not a `users` record, and this is where that
// matters most: every plugin's `can*` attribute (`canLock`, `canSticky`,
// `canSuspend`, `canTag`, `canLike`, `fof-upload.canUpload`, ...) is computed
// through here, and a lookup that only knows the `users` collection answers
// FALSE for the instance's own operator while the endpoints allow them — the
// flag and the write disagree, and the UI hides controls the server would
// accept. The session's own projection already treats a superuser as an
// administrator (`session.userFromRecord`, which is the union of the
// administrator group and the superuser collection), so this fallback asks
// the same question the same way.
func (m *Module) pluginActor(actorID string) domain.Actor {
	if actorID == "" {
		return domain.Actor{}
	}

	if user, err := m.repo.UserByID(actorID); err == nil && user != nil {
		return domain.ActorFor(user.ID, user.IsAdmin, user.Groups)
	}

	superusers, err := m.app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return domain.Actor{}
	}

	// A record id is unique per collection, so a `users` id simply does not
	// resolve here — and the other way round, which is why this fallback is
	// safe to try unconditionally.
	if record, err := m.app.FindRecordById(superusers, actorID); err == nil {
		return domain.ActorFor(record.Id, true, nil)
	}

	return domain.Actor{}
}

// boot runs the plugin lifecycle.
func (m *Module) boot(ctx context.Context) error {
	// The state table is seeded before it is read, because a plugin
	// with no state row is DISABLED (see `enabledPlugins`).
	if err := m.seedPluginState(); err != nil {
		return err
	}

	enabled, err := m.enabledPlugins()
	if err != nil {
		return err
	}
	if err := m.plugins.Load(ctx, enabled); err != nil {
		return err
	}

	// Each plugin's migrations run here, in dependency order, and the
	// applied versions are recorded per plugin.
	for _, id := range m.plugins.Order() {
		extenders := m.plugins.PluginExtenders()[id]
		if extenders == nil {
			continue
		}
		if err := extenders.Migrate(ctx, m.app, id, m.recordMigration); err != nil {
			return errors.Wrapf(err, "forum: the %q plugin migrations failed", id)
		}
	}

	// The plugin extenders are wired into the API server, so a plugin's
	// attribute and relationship contributions reach the wire.
	for _, extenders := range m.plugins.PluginExtenders() {
		for _, attribute := range extenders.Attributes() {
			attribute := attribute

			value := func(ctx context.Context, req api.AttributeRequest) (any, error) {
				return attribute.Value(ctx, &plugin.AttributeRequest{
					App:      m.app,
					ActorID:  req.ActorID,
					RecordID: req.RecordID,
					Type:     req.Type,
					IsAdmin:  req.IsAdmin,
					Can:      m.pluginCan(ctx, req.ActorID),
				})
			}

			if attribute.Visible == nil {
				m.api.AddAttribute(attribute.Resource, attribute.Name, value)
				continue
			}

			m.api.AddVisibleAttribute(attribute.Resource, attribute.Name, func(req api.AttributeRequest) bool {
				return attribute.Visible(&plugin.AttributeRequest{
					App:      m.app,
					ActorID:  req.ActorID,
					RecordID: req.RecordID,
					Type:     req.Type,
					IsAdmin:  req.IsAdmin,
				})
			}, value)
		}
		for _, writer := range extenders.RelationshipWriters() {
			m.api.AddRelationshipWriter(writer.Resource, writer.Name,
				func(ctx context.Context, req api.RelationshipWriteRequest) error {
					return writer.Write(ctx, &plugin.RelationshipWriteRequest{
						App:       m.app,
						ActorID:   req.ActorID,
						RecordID:  req.RecordID,
						Type:      req.Type,
						IDs:       req.IDs,
						Creating:  req.Creating,
						Can:       m.pluginCan(ctx, req.ActorID),
						CanScoped: m.pluginCanScoped(ctx, req.ActorID),
					})
				})
		}
		for _, include := range extenders.DefaultIncludes() {
			m.api.AddDefaultInclude(include.Resource, include.Paths)
		}
		for _, serializer := range extenders.ResourceSerializers() {
			m.api.AddResourceSerializer(api.ResourceSerializer{
				Type: serializer.Type,
				Render: func(ctx context.Context, req api.ResourceRequest) (*api.Resource, error) {
					rendered, err := serializer.Render(ctx, &plugin.ResourceRequest{
						App:       m.app,
						ActorID:   req.ActorID,
						ID:        req.ID,
						IsAdmin:   req.IsAdmin,
						Can:       req.Can,
						CanScoped: req.CanScoped,
					})
					if err != nil {
						return nil, err
					}
					if rendered == nil {
						return nil, nil
					}

					relationships := map[string]api.Relationship{}
					for name, relationship := range rendered.Relationships {
						relationships[name] = api.Relationship{Data: adaptRelationshipData(relationship.Data)}
					}

					return &api.Resource{
						Type:          rendered.Type,
						ID:            rendered.ID,
						Attributes:    rendered.Attributes,
						Relationships: relationships,
					}, nil
				},
			})
		}
		for _, relationship := range extenders.Relationships() {
			m.api.AddPluginRelationship(relationship.Resource, relationship.Name)
			m.api.AddRelationship(relationship.Resource, relationship.Name, relationship.Type, relationship.Many,
				func(ctx context.Context, req api.RelationshipRequest) ([]api.Identifier, error) {
					resolved, err := relationship.Resolve(ctx, &plugin.RelationshipRequest{
						App:      m.app,
						ActorID:  req.ActorID,
						RecordID: req.RecordID,
						Type:     req.Type,
						Can:      m.pluginCan(ctx, req.ActorID),
					})
					if err != nil {
						return nil, err
					}
					out := make([]api.Identifier, 0, len(resolved))
					for _, identifier := range resolved {
						out = append(out, api.Identifier{Type: identifier.Type, ID: identifier.ID})
					}
					return out, nil
				})
		}
	}

	// The notification preferences a reader starts with. Declared by the
	// plugins (a plugin is what sends a kind), published by the user
	// resource, and honoured by `CreateNotification` — one place, so the
	// checkbox on the settings page and the write path cannot disagree.
	if m.repo != nil {
		m.repo.SetNotificationDefaults(m.notificationDefaults())
	}

	return nil
}

// Shutdown releases the module's resources.
//
// The host calls it when the app terminates.
func (m *Module) Shutdown(ctx context.Context) {
	m.plugins.Shutdown(ctx)
}

// seedPluginState makes sure every registered plugin has a state row.
//
// **A DISABLED PLUGIN IS A ROW THAT IS NOT THERE** (see
// `enabledPlugins`), so a plugin that has never been toggled would
// read as disabled. The state table is therefore SEEDED: a row per
// plugin, saying it is enabled.
//
// The seeding is IDEMPOTENT: a plugin that already has a row keeps
// it, so a plugin the operator disabled stays disabled across a
// restart.
func (m *Module) seedPluginState() error {
	physical := m.registry.Physical("plugin_state")
	collection, err := m.app.FindCollectionByNameOrId(physical)
	if err != nil {
		// The state table is missing: every plugin is enabled, and
		// there is nothing to seed.
		return nil //nolint:nilerr // a missing table means "all enabled"
	}

	for _, manifest := range m.plugins.RegisteredManifests() {
		existing, err := m.app.FindFirstRecordByFilter(collection, "plugin_id = {:plugin}",
			map[string]any{"plugin": manifest.ID})
		if err == nil && existing != nil {
			// The plugin already has a state: an operator's choice,
			// or a row the seeding wrote earlier.
			continue
		}

		record := core.NewRecord(collection)
		record.Set("plugin_id", manifest.ID)
		record.Set("enabled", true)
		if err := m.app.Save(record); err != nil {
			return errors.Wrapf(err, "forum: the %q plugin state could not be seeded", manifest.ID)
		}
	}

	return nil
}

// enabledPlugins reads the persisted enabled state of the plugins.
//
// **A DISABLED PLUGIN IS A ROW THAT IS NOT THERE.** The state table's
// `enabled` column is a `BoolField` with `Required: true`, and
// PocketBase's `Required` on a bool means the value must be "TRUE" —
// so a `false` can never be stored, and the write fails with "enabled:
// cannot be blank". The state is therefore expressed by the row's
// ABSENCE: a row says the plugin is enabled, and a plugin with no row
// is disabled.
//
// The forum ships its plugins enabled, so the state table is SEEDED
// with a row per plugin at boot. A plugin that is disabled has its row
// removed, and a plugin that is switched back on gets its row back.
func (m *Module) enabledPlugins() (map[string]bool, error) {
	enabled := map[string]bool{}
	for _, manifest := range m.plugins.RegisteredManifests() {
		enabled[manifest.ID] = false
	}

	physical := m.registry.Physical("plugin_state")
	collection, err := m.app.FindCollectionByNameOrId(physical)
	if err != nil {
		// The state table is missing: every plugin is enabled.
		for id := range enabled {
			enabled[id] = true
		}
		return enabled, nil //nolint:nilerr // a missing table means "all enabled"
	}

	records, err := m.app.FindAllRecords(collection)
	if err != nil {
		// The state table cannot be read: every plugin is enabled.
		for id := range enabled {
			enabled[id] = true
		}
		return enabled, nil //nolint:nilerr // an unreadable table means "all enabled"
	}
	for _, record := range records {
		id := record.GetString("plugin_id")
		if _, known := enabled[id]; !known {
			continue
		}
		enabled[id] = record.GetBool("enabled")
	}
	return enabled, nil
}

// busAdapter bridges the core event bus to the domain's narrow
// interface.
type busAdapter struct {
	bus *flarumcore.EventBus
}

// DispatchAfter notifies the observers of a completed action.
func (a busAdapter) DispatchAfter(ctx context.Context, event domain.Event) {
	if a.bus == nil {
		return
	}
	a.bus.DispatchAfter(ctx, flarumcore.Event{
		Name:   event.Name,
		Actor:  event.Actor,
		Target: event.Target,
		Data:   event.Data,
	})
}

// recordMigration records an applied plugin migration.
//
// The applied versions live in the forum's plugin_migrations
// collection, which is what makes a migration run once. A migration
// that has already been applied is recorded again without creating a
// second row.
func (m *Module) recordMigration(_ context.Context, pluginID, name string) error {
	physical := m.registry.Physical("plugin_migrations")
	collection, err := m.app.FindCollectionByNameOrId(physical)
	if err != nil {
		return err
	}

	existing, err := m.app.FindFirstRecordByFilter(collection,
		"plugin_id = {:plugin} && version = {:version}",
		map[string]any{"plugin": pluginID, "version": name})
	if err == nil && existing != nil {
		return nil
	}

	record := core.NewRecord(collection)
	record.Set("plugin_id", pluginID)
	record.Set("version", name)
	return m.app.Save(record)
}

// adaptRelationshipData converts a plugin relationship's data onto the
// API package's own identifier type.
//
// **THE WIRE SHAPE DEPENDS ON THIS.** The plugin package declares its
// own `Identifier`, which the API package does not import — and a Go
// struct without JSON tags serialises its FIELD NAMES, so the payload
// read `{"Type":"polls","ID":"abc"}` instead of
// `{"type":"polls","id":"abc"}`. The frontend resolves a relation
// with `store.getById(data.type, data.id)`, so a capitalised shape is
// `undefined` — the relation reads as absent, with a 200 on the wire.
func adaptRelationshipData(data any) any {
	switch typed := data.(type) {
	case plugin.Identifier:
		return api.Identifier{Type: typed.Type, ID: typed.ID}
	case []plugin.Identifier:
		out := make([]api.Identifier, 0, len(typed))
		for _, identifier := range typed {
			out = append(out, api.Identifier{Type: identifier.Type, ID: identifier.ID})
		}
		return out
	case nil:
		return nil
	default:
		return data
	}
}

// HasFrontendBuild reports whether a front-end build was staged.
//
// The build output is gitignored, so a fresh clone has none: the
// tracked fallback shell is served instead, and this is how a caller
// tells the two apart.
func (m *Module) HasFrontendBuild() bool {
	return m.assets.HasBuild()
}
