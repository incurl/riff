package plugin

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/pkg/errors"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
)

// Phase names the lifecycle step a plugin is in.
type Phase int

const (
	// PhaseRegistered means the plugin's Register phase has run.
	PhaseRegistered Phase = iota
	// PhaseBooted means the plugin's Boot phase has run.
	PhaseBooted
	// PhaseShutDown means the plugin's Shutdown phase has run.
	PhaseShutDown
)

// Plugin is a registered extension.
//
// A plugin implements the three lifecycle hooks. Register declares what
// the plugin extends (routes, services, events, migrations, assets) and
// must not have side effects; Boot does the startup work; Shutdown
// releases resources.
type Plugin interface {
	// Manifest describes the plugin.
	Manifest() Manifest
	// Register declares the plugin's extenders.
	Register(ctx context.Context, ext *Extenders) error
	// Boot runs after every plugin has registered.
	Boot(ctx context.Context, ext *Extenders) error
	// Shutdown releases the plugin's resources.
	Shutdown(ctx context.Context, ext *Extenders) error
}

// registered is a plugin the manager knows about.
type registered struct {
	// plugin is the plugin itself.
	plugin Plugin
	// manifest is the plugin's manifest, cached for cheap access.
	manifest Manifest
	// phase is the lifecycle phase the plugin reached.
	phase Phase
	// enabled reports whether the plugin is enabled in the store.
	enabled bool
	// extenders are the extenders THIS plugin declared. They are kept
	// per plugin so its migrations can be tracked per plugin.
	extenders *Extenders
}

// Manager owns the plugin lifecycle.
//
// It resolves dependencies, runs the Register/Boot/Shutdown phases in
// dependency order, isolates plugin panics, and persists the enabled
// state.
type Manager struct {
	mu sync.RWMutex

	// coreVersion is the running core version, checked against each
	// plugin's coreRequire range.
	coreVersion Version
	// logger logs plugin activity.
	logger *slog.Logger
	// bus is the event bus the plugins subscribe to.
	bus *flarumcore.EventBus
	// container is the DI container the plugins bind into.
	container *flarumcore.Container
	// resolver resolves logical names to physical collection names.
	resolver NameResolver
	// extenders accumulates what the plugins declared.
	extenders *Extenders

	// plugins holds every registered plugin, keyed by ID.
	plugins map[string]*registered
	// order is the load order (dependency order).
	order []string
}

// NewManager returns a plugin manager.
//
// coreVersion is the running forum core version; it is checked against
// each plugin's coreRequire range.
// resolver resolves the logical model names a plugin declares to the
// physical collection names they live in. It may be nil, in which case
// a plugin that asks for a name gets the default prefix.
func NewManager(coreVersion string, bus *flarumcore.EventBus, container *flarumcore.Container, logger *slog.Logger, resolver NameResolver) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	version, err := ParseVersion(coreVersion)
	if err != nil {
		// An unparseable core version must not silently disable every
		// plugin's range check. Treat it as 0.0.0 and let the range
		// check report the mismatch.
		version = Version{}
	}
	return &Manager{
		coreVersion: version,
		logger:      logger,
		bus:         bus,
		container:   container,
		resolver:    resolver,
		extenders:   NewExtenders(),
		plugins:     map[string]*registered{},
	}
}

// Register adds a plugin to the manager.
//
// Registration does not run the plugin's Register phase: that happens in
// [Manager.Load], after every plugin is known, so a plugin may depend on
// another that has not been registered yet.
func (m *Manager) Register(p Plugin) error {
	if p == nil {
		return errors.New("plugin: cannot register a nil plugin")
	}
	manifest := p.Manifest()
	if err := manifest.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.plugins[manifest.ID]; exists {
		return errors.Errorf("plugin: %q is already registered", manifest.ID)
	}
	m.plugins[manifest.ID] = &registered{plugin: p, manifest: manifest}
	return nil
}

// Enabled reports whether a plugin is enabled.
//
// The enabled state is supplied by the caller because it lives in the
// plugin_state collection; the manager does not read the database itself.
func (m *Manager) Enabled(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.plugins[id]
	if !ok {
		return false
	}
	return entry.enabled
}

// SetEnabled marks a plugin enabled or disabled.
//
// It does not run any lifecycle phase: enabling a plugin at runtime is
// the host's decision, and the manager only reflects the persisted state
// so that [Manager.Load] honours it.
func (m *Manager) SetEnabled(id string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.plugins[id]; ok {
		entry.enabled = enabled
	}
}

// Load resolves the dependency order and runs the lifecycle.
//
// Only enabled plugins take part. The whole batch is validated first: a
// dependency conflict, an incompatible core version, or a missing
// dependency aborts the load with an explicit error rather than starting
// a partial batch.
//
// The returned error lists every problem it found, so an operator does not
// have to fix them one restart at a time.
func (m *Manager) Load(ctx context.Context, enabled map[string]bool) error {
	m.mu.Lock()
	for id, entry := range m.plugins {
		entry.enabled = enabled[id]
	}
	m.mu.Unlock()

	_, order, err := m.resolve()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.order = order
	m.extenders = NewExtenders()
	m.mu.Unlock()

	// --- Register phase ------------------------------------------------
	// Each plugin gets its own extenders, so the manager knows which
	// migrations belong to which plugin.
	for _, id := range order {
		entry := m.plugin(id)
		if entry == nil {
			continue
		}
		extenders := m.newExtenders()
		m.mu.Lock()
		entry.extenders = extenders
		m.mu.Unlock()

		if err := m.runPhase(ctx, id, PhaseRegistered, entry.plugin.Register, extenders); err != nil {
			return err
		}
		m.mu.Lock()
		entry.phase = PhaseRegistered
		m.mu.Unlock()
	}

	// --- Boot phase ----------------------------------------------------
	for _, id := range order {
		entry := m.plugin(id)
		if entry == nil {
			continue
		}
		if err := m.runPhase(ctx, id, PhaseBooted, entry.plugin.Boot, entry.extenders); err != nil {
			return err
		}
		m.mu.Lock()
		entry.phase = PhaseBooted
		m.mu.Unlock()

		m.bus.DispatchAfter(ctx, flarumcore.Event{
			Name:   flarumcore.EventPluginBooted,
			Target: id,
			Data:   map[string]any{"version": entry.manifest.Version},
		})
	}

	return nil
}

// Shutdown runs every booted plugin's Shutdown phase, in reverse
// dependency order.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		entry := m.plugin(id)
		if entry == nil || entry.phase != PhaseBooted {
			continue
		}
		if err := m.runPhase(ctx, id, PhaseShutDown, entry.plugin.Shutdown, entry.extenders); err != nil {
			m.logger.Error("forum: plugin shutdown failed",
				slog.String("plugin", id),
				slog.String("error", err.Error()),
			)
		}
		m.mu.Lock()
		entry.phase = PhaseShutDown
		m.mu.Unlock()
	}
}

// runPhase runs one lifecycle hook with panic isolation.
//
// A plugin that panics must not take the forum down, so the panic is
// recovered and returned as an error naming the plugin.
func (m *Manager) runPhase(ctx context.Context, id string, phase Phase, hook func(context.Context, *Extenders) error, extenders *Extenders) (err error) {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("forum: plugin panicked",
				slog.String("plugin", id),
				slog.String("phase", phaseName(phase)),
				slog.Any("panic", r),
			)
			err = errors.Errorf("plugin %q panicked during %s", id, phaseName(phase))
		}
	}()

	if err := hook(ctx, extenders); err != nil {
		return errors.Wrapf(err, "plugin %q failed during %s", id, phaseName(phase))
	}
	return nil
}

// newExtenders returns an extender accumulator bound to the manager's
// container, event bus and name resolver.
func (m *Manager) newExtenders() *Extenders {
	extenders := NewExtenders()
	extenders.Container = m.container
	extenders.Bus = m.bus
	extenders.Logger = m.logger
	extenders.Registry = m.resolver
	return extenders
}

// PluginExtenders returns the extenders a plugin declared.
//
// It is what the module reads to wire the plugins' contributions into
// the API and the frontend.
func (m *Manager) PluginExtenders() map[string]*Extenders {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]*Extenders, len(m.plugins))
	for id, entry := range m.plugins {
		if entry.extenders == nil {
			continue
		}
		out[id] = entry.extenders
	}
	return out
}

// Extenders returns the accumulated extenders.
func (m *Manager) Extenders() *Extenders {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.extenders
}

// Order returns the resolved load order.
func (m *Manager) Order() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// Manifests returns the manifests of the loaded plugins, in load order.
//
// The SPA boot sequence and the asset manifest endpoint both need this.
func (m *Manager) Manifests() []Manifest {
	m.mu.RLock()
	order := make([]string, len(m.order))
	copy(order, m.order)
	m.mu.RUnlock()

	manifests := make([]Manifest, 0, len(order))
	for _, id := range order {
		entry := m.plugin(id)
		if entry == nil {
			continue
		}
		manifests = append(manifests, entry.manifest)
	}
	return manifests
}

// RegisteredManifests returns the manifests of EVERY registered plugin,
// whether it is enabled or not.
//
// The module reads this to learn the full plugin set before the load,
// so it can decide which plugins are enabled.
func (m *Manager) RegisteredManifests() []Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	manifests := make([]Manifest, 0, len(m.plugins))
	for _, entry := range m.plugins {
		manifests = append(manifests, entry.manifest)
	}
	slices.SortFunc(manifests, func(a, b Manifest) int {
		return strings.Compare(a.ID, b.ID)
	})
	return manifests
}

// plugin returns a registered plugin entry.
func (m *Manager) plugin(id string) *registered {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plugins[id]
}

// phaseName renders a phase for messages.
func phaseName(phase Phase) string {
	switch phase {
	case PhaseRegistered:
		return "register"
	case PhaseBooted:
		return "boot"
	case PhaseShutDown:
		return "shutdown"
	default:
		return "unknown"
	}
}

// resolve validates the batch and computes the dependency order.
//
// It returns the enabled plugins in load order. Every problem found is
// collected so the operator sees them all at once.
func (m *Manager) resolve() ([]*registered, []string, error) {
	m.mu.RLock()
	enabled := make([]*registered, 0, len(m.plugins))
	for _, entry := range m.plugins {
		if entry.enabled {
			enabled = append(enabled, entry)
		}
	}
	m.mu.RUnlock()

	slices.SortFunc(enabled, func(a, b *registered) int {
		return strings.Compare(a.manifest.ID, b.manifest.ID)
	})

	var problems []string

	// --- core version compatibility ------------------------------------
	for _, entry := range enabled {
		ok, err := SatisfiesRange(m.coreVersion, entry.manifest.CoreRequire)
		if err != nil {
			problems = append(problems, errors.Wrapf(err,
				"plugin %q has an invalid coreRequire range %q", entry.manifest.ID, entry.manifest.CoreRequire).Error())
			continue
		}
		if !ok {
			problems = append(problems, errors.Errorf(
				"plugin %q requires core %s, but the running core is %s",
				entry.manifest.ID, entry.manifest.CoreRequire, m.coreVersion).Error())
		}
	}

	// --- backend kind ---------------------------------------------------
	for _, entry := range enabled {
		if entry.manifest.BackendKind != BackendStaticGo {
			problems = append(problems, errors.Errorf(
				"plugin %q uses backendKind %q, which is not supported in v1 (only %q is)",
				entry.manifest.ID, entry.manifest.BackendKind, BackendStaticGo).Error())
		}
	}

	// --- dependency resolution -----------------------------------------
	byID := map[string]*registered{}
	for _, entry := range enabled {
		byID[entry.manifest.ID] = entry
	}

	for _, entry := range enabled {
		deps, err := ParseDeps(entry.manifest.Deps)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for _, dep := range deps {
			target, ok := byID[dep.ID]
			if !ok {
				problems = append(problems, errors.Errorf(
					"plugin %q depends on %q, which is not enabled", entry.manifest.ID, dep.ID).Error())
				continue
			}
			version, err := ParseVersion(target.manifest.Version)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			ok, err = SatisfiesRange(version, dep.Range)
			if err != nil {
				problems = append(problems, errors.Wrapf(err,
					"plugin %q has an invalid version range for %q", entry.manifest.ID, dep.ID).Error())
				continue
			}
			if !ok {
				problems = append(problems, errors.Errorf(
					"plugin %q depends on %q %s, but %s is installed",
					entry.manifest.ID, dep.ID, dep.Range, target.manifest.Version).Error())
			}
		}
	}

	if len(problems) > 0 {
		return nil, nil, errors.Errorf("plugin: cannot load the plugin batch:\n  - %s",
			joinProblems(problems))
	}

	order, err := topologicalOrder(enabled)
	if err != nil {
		return nil, nil, err
	}

	batch := make([]*registered, 0, len(order))
	for _, id := range order {
		batch = append(batch, byID[id])
	}
	return batch, order, nil
}

// topologicalOrder sorts the plugins so that every plugin comes after the
// plugins it depends on.
//
// A cycle is an error: the batch cannot be loaded in any order.
func topologicalOrder(entries []*registered) ([]string, error) {
	byID := map[string]*registered{}
	for _, entry := range entries {
		byID[entry.manifest.ID] = entry
	}

	// state: 0 = unvisited, 1 = in progress, 2 = done.
	state := map[string]int{}
	order := make([]string, 0, len(entries))
	var visit func(id string, path []string) error

	visit = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return errors.Errorf("plugin: dependency cycle detected: %s",
				joinProblems(append(append([]string{}, path...), id)))
		case 2:
			return nil
		}
		state[id] = 1

		entry := byID[id]
		deps, err := ParseDeps(entry.manifest.Deps)
		if err != nil {
			return err
		}
		for _, dep := range deps {
			if _, ok := byID[dep.ID]; !ok {
				continue
			}
			if err := visit(dep.ID, append(append([]string{}, path...), id)); err != nil {
				return err
			}
		}

		state[id] = 2
		order = append(order, id)
		return nil
	}

	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.manifest.ID)
	}
	slices.Sort(ids)

	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// joinProblems renders a list of problem strings as one indented block.
func joinProblems(problems []string) string {
	out := ""
	for i, problem := range problems {
		if i > 0 {
			out += "\n  - "
		}
		out += problem
	}
	return out
}
