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
	// The tag and mention extensions are deliberately NOT enabled. They
	// are riff's memos syntax (a #tag becomes a memo tag, an @user a
	// memo mention), and they build AST nodes with no HTML renderer, so
	// enabling them would silently drop every `#tag` and `@mention`
	// from the rendered body. In a forum post both are literal text,
	// which is what Flarum core shows too.
	//
	// The service is stateless and safe to share across goroutines.
	markdownService := markdown.NewService()

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

	return module, nil
}

// boot runs the plugin lifecycle.
func (m *Module) boot(ctx context.Context) error {
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
			m.api.AddAttribute(attribute.Resource, attribute.Name, func(ctx context.Context, req api.AttributeRequest) (any, error) {
				return attribute.Value(ctx, &plugin.AttributeRequest{
					App:      m.app,
					ActorID:  req.ActorID,
					RecordID: req.RecordID,
					Type:     req.Type,
				})
			})
		}
		for _, relationship := range extenders.Relationships() {
			m.api.AddRelationship(relationship.Resource, relationship.Name, relationship.Type, relationship.Many,
				func(ctx context.Context, req api.RelationshipRequest) ([]api.Identifier, error) {
					resolved, err := relationship.Resolve(ctx, &plugin.RelationshipRequest{
						App:      m.app,
						ActorID:  req.ActorID,
						RecordID: req.RecordID,
						Type:     req.Type,
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
	return nil
}

// Shutdown releases the module's resources.
//
// The host calls it when the app terminates.
func (m *Module) Shutdown(ctx context.Context) {
	m.plugins.Shutdown(ctx)
}

// enabledPlugins reads the persisted enabled state of the plugins.
//
// A plugin with no state row is enabled: the forum ships its plugins
// enabled, and an operator disables one explicitly.
func (m *Module) enabledPlugins() (map[string]bool, error) {
	enabled := map[string]bool{}
	for _, manifest := range m.plugins.RegisteredManifests() {
		enabled[manifest.ID] = true
	}

	physical := m.registry.Physical("plugin_state")
	collection, err := m.app.FindCollectionByNameOrId(physical)
	if err != nil {
		// The state table is missing: every plugin is enabled.
		return enabled, nil //nolint:nilerr // a missing table means "all enabled"
	}

	records, err := m.app.FindAllRecords(collection)
	if err != nil {
		// The state table cannot be read: every plugin is enabled.
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

// HasFrontendBuild reports whether a front-end build was staged.
//
// The build output is gitignored, so a fresh clone has none: the
// tracked fallback shell is served instead, and this is how a caller
// tells the two apart.
func (m *Module) HasFrontendBuild() bool {
	return m.assets.HasBuild()
}
