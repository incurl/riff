package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
)

// testPlugin is a plugin that records its lifecycle calls.
type testPlugin struct {
	// manifest is the plugin's manifest.
	manifest Manifest
	// calls records the lifecycle phases the plugin went through.
	calls *[]string
	// panicOn is the phase in which the plugin panics, empty for none.
	panicOn string
}

// Manifest describes the plugin.
func (p *testPlugin) Manifest() Manifest { return p.manifest }

// Register records the register phase.
func (p *testPlugin) Register(_ context.Context, _ *Extenders) error {
	*p.calls = append(*p.calls, "register:"+p.manifest.ID)
	if p.panicOn == "register" {
		panic("plugin exploded during register")
	}
	return nil
}

// Boot records the boot phase.
func (p *testPlugin) Boot(_ context.Context, _ *Extenders) error {
	*p.calls = append(*p.calls, "boot:"+p.manifest.ID)
	if p.panicOn == "boot" {
		panic("plugin exploded during boot")
	}
	return nil
}

// Shutdown records the shutdown phase.
func (p *testPlugin) Shutdown(_ context.Context, _ *Extenders) error {
	*p.calls = append(*p.calls, "shutdown:"+p.manifest.ID)
	return nil
}

// newManager returns a manager with a fresh bus and container.
func newTestManager(t *testing.T, coreVersion string) *Manager {
	t.Helper()
	return NewManager(coreVersion, flarumcore.NewEventBus(nil), flarumcore.NewContainer(), nil, nil)
}

// manifestFor builds a manifest for a test plugin.
func manifestFor(id string, deps ...string) Manifest {
	return Manifest{
		ID:          id,
		Name:        id,
		Version:     "1.0.0",
		CoreRequire: ">=2.0.0",
		Deps:        deps,
		BackendKind: BackendStaticGo,
	}
}

// TestManager_LifecycleOrderInDependencyOrder pins that the plugins boot
// in dependency order: a dependency boots before its dependent.
func TestManager_LifecycleOrderInDependencyOrder(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/base"), calls: &calls}))
	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/feature", "acme/base"), calls: &calls}))

	require.NoError(t, manager.Load(context.Background(), map[string]bool{
		"acme/base":    true,
		"acme/feature": true,
	}))

	// Every plugin registers before any plugin boots, which is what
	// upstream Flarum does: the Register phase declares the extenders,
	// and the Boot phase runs once all of them are declared. The
	// DEPENDENCY order still holds within each phase.
	require.Equal(t, []string{
		"register:acme/base",
		"register:acme/feature",
		"boot:acme/base",
		"boot:acme/feature",
	}, calls)

	require.Equal(t, []string{"acme/base", "acme/feature"}, manager.Order())
}

// TestManager_ShutdownRunsInReverseOrder pins that the shutdown phase
// unwinds the dependency order.
func TestManager_ShutdownRunsInReverseOrder(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/base"), calls: &calls}))
	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/feature", "acme/base"), calls: &calls}))
	require.NoError(t, manager.Load(context.Background(), map[string]bool{
		"acme/base":    true,
		"acme/feature": true,
	}))

	calls = nil
	manager.Shutdown(context.Background())

	require.Equal(t, []string{"shutdown:acme/feature", "shutdown:acme/base"}, calls)
}

// TestManager_MissingDependencyIsRejected pins that a plugin whose
// dependency is not enabled blocks the WHOLE batch, with an explicit
// error rather than a partial startup.
func TestManager_MissingDependencyIsRejected(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/base"), calls: &calls}))
	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/feature", "acme/base"), calls: &calls}))

	err := manager.Load(context.Background(), map[string]bool{
		"acme/feature": true,
	})
	require.Error(t, err, "a missing dependency must abort the batch")
	require.Contains(t, err.Error(), "acme/base")
	require.Contains(t, err.Error(), "not enabled")

	require.Empty(t, calls, "no plugin may run when the batch is invalid")
}

// TestManager_IncompatibleCoreVersionIsRejected pins the core version
// check.
func TestManager_IncompatibleCoreVersionIsRejected(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "1.0.0")

	manifest := manifestFor("acme/base")
	manifest.CoreRequire = ">=2.0.0"
	require.NoError(t, manager.Register(&testPlugin{manifest: manifest, calls: &calls}))

	err := manager.Load(context.Background(), map[string]bool{"acme/base": true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires core")
	require.Empty(t, calls)
}

// TestManager_DependencyVersionConflictIsRejected pins that a dependency
// outside the declared range aborts the batch.
func TestManager_DependencyVersionConflictIsRejected(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	base := manifestFor("acme/base")
	base.Version = "1.0.0"
	require.NoError(t, manager.Register(&testPlugin{manifest: base, calls: &calls}))

	feature := manifestFor("acme/feature", "acme/base >=2.0.0")
	require.NoError(t, manager.Register(&testPlugin{manifest: feature, calls: &calls}))

	err := manager.Load(context.Background(), map[string]bool{
		"acme/base":    true,
		"acme/feature": true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "acme/base")
	require.Empty(t, calls)
}

// TestManager_DisabledPluginDoesNotRun pins that a disabled plugin
// leaves no listener, route or hook behind.
func TestManager_DisabledPluginDoesNotRun(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/base"), calls: &calls}))
	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/feature"), calls: &calls}))

	require.NoError(t, manager.Load(context.Background(), map[string]bool{
		"acme/base":    true,
		"acme/feature": false,
	}))

	require.Equal(t, []string{"register:acme/base", "boot:acme/base"}, calls)
	require.Equal(t, []string{"acme/base"}, manager.Order())
}

// TestManager_PluginPanicIsIsolated pins that a plugin panic does not
// crash the core: the load fails with an error naming the plugin, and
// the process survives.
func TestManager_PluginPanicIsIsolated(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{
		manifest: manifestFor("acme/boom"),
		calls:    &calls,
		panicOn:  "register",
	}))

	err := manager.Load(context.Background(), map[string]bool{"acme/boom": true})
	require.Error(t, err, "a plugin panic must be reported")
	require.Contains(t, err.Error(), "acme/boom")
	require.Contains(t, err.Error(), "panicked")
}

// TestManager_DuplicateRegistrationIsRejected pins that a plugin id is
// unique.
func TestManager_DuplicateRegistrationIsRejected(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/base"), calls: &calls}))
	err := manager.Register(&testPlugin{manifest: manifestFor("acme/base"), calls: &calls})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already registered")
}

// TestManager_JSSandboxBackendIsRejected pins that the deferred backend
// kind is refused with an explicit message rather than silently
// ignored.
func TestManager_JSSandboxBackendIsRejected(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	manifest := manifestFor("acme/script")
	manifest.BackendKind = BackendJSSandbox
	require.NoError(t, manager.Register(&testPlugin{manifest: manifest, calls: &calls}))

	err := manager.Load(context.Background(), map[string]bool{"acme/script": true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "js-sandbox")
	require.Empty(t, calls)
}

// TestManager_CycleIsRejected pins that a dependency cycle is an error
// rather than an arbitrary order.
func TestManager_CycleIsRejected(t *testing.T) {
	calls := []string{}
	manager := newTestManager(t, "2.0.0")

	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/a", "acme/b"), calls: &calls}))
	require.NoError(t, manager.Register(&testPlugin{manifest: manifestFor("acme/b", "acme/a"), calls: &calls}))

	err := manager.Load(context.Background(), map[string]bool{
		"acme/a": true,
		"acme/b": true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle")
}

// TestSatisfiesRange pins the semver range semantics the manifest
// depends on.
func TestSatisfiesRange(t *testing.T) {
	version, err := ParseVersion("1.2.3")
	require.NoError(t, err)

	cases := []struct {
		rangeText string
		expected  bool
	}{
		{"", true},
		{"1.2.3", true},
		{"1.2.4", false},
		{">=1.2.0", true},
		{">=1.3", false},
		{">1.2", true},
		{">1.2.3", false},
		{"<=1.2.3", true},
		{"<1.2.3", false},
		{"^1.2.0", true},
		{"^2.0.0", false},
		{"~1.2.0", true},
		{"~1.3.0", false},
		{"1.0.0 - 2.0.0", true},
		{"2.0.1 - 3.0.0", false},
	}

	for _, testCase := range cases {
		actual, err := SatisfiesRange(version, testCase.rangeText)
		require.NoErrorf(t, err, "range %q", testCase.rangeText)
		require.Equalf(t, testCase.expected, actual, "range %q against 1.2.3", testCase.rangeText)
	}
}

// TestParseVersion_PreReleaseOrdersBeforeRelease pins the semver
// pre-release rule.
func TestParseVersion_PreReleaseOrdersBeforeRelease(t *testing.T) {
	alpha, err := ParseVersion("1.0.0-alpha")
	require.NoError(t, err)
	release, err := ParseVersion("1.0.0")
	require.NoError(t, err)

	require.Negative(t, alpha.Compare(release))
	require.Positive(t, release.Compare(alpha))
}
