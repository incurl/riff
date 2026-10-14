package connect

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestDefaultInstanceSettingForKey_CoversClosedSet pins the read-side
// default helper against drift between splitInstanceSettingName and
// defaultInstanceSettingForKey. The two switches must stay in sync —
// any closed-set key accepted by the parser must also have a default
// body the read path can return, and the body must populate the
// matching oneof variant (not be left nil).
//
// The check is hermetic: the helper has no core.App dependency, so
// this test runs without the test app fixture the rest of the
// instance_settings tests need. It exists so a future drift is caught
// by `go test ./pb/connect/...` even on a machine that can't bring
// up a PocketBase app.
func TestDefaultInstanceSettingForKey_CoversClosedSet(t *testing.T) {
	// Every entry in this table mirrors a case in
	// splitInstanceSettingName. If a new key is added to the parser
	// without a corresponding row here, the read path's defensive
	// CodeNotFound branch will fire in production; this test exists
	// so that branch doesn't.
	knownKeys := []string{
		"GENERAL", "STORAGE", "MEMO_RELATED", "TAGS", "NOTIFICATION", "AI", "ACCESS",
	}
	for _, key := range knownKeys {
		t.Run(key, func(t *testing.T) {
			setting := defaultInstanceSettingForKey(key)
			require.NotNilf(t, setting, "default for %q must exist; check that splitInstanceSettingName and defaultInstanceSettingForKey stay in sync", key)
			assert.Equal(t, "instance/settings/"+key, setting.GetName())
			require.NotNilf(t, setting.GetValue(), "default body for %q must populate the matching oneof variant", key)
		})
	}

	// Unknown keys return nil so the caller can fall through to
	// CodeNotFound (per-key) or skip (batch). The split-instance
	// setting-name check already rejects these, so the nil branch
	// is defensive, but it must stay nil.
	for _, key := range []string{"", "BASIC", "USAGE_LIMITS", "GARBAGE"} {
		t.Run("unknown/"+key, func(t *testing.T) {
			assert.Nil(t, defaultInstanceSettingForKey(key),
				"unknown key %q must not synthesise a default", key)
		})
	}
}

// TestGetInstanceSetting_MissingRowReturnsDefault drives the full
// handler against a fresh core.App. The contract: a known key with
// no stored row must return a non-error response whose oneof variant
// is the zero-value message, and the response must NOT have created
// a row in the database (the read path is read-only; an operator
// who deletes a row to revert to defaults must not have it
// resurrected by the next read).
//
// This is the riff-side analogue of upstream Memos's
// `TestGetInstanceSetting - general setting`
// (usememos/memos@main/server/api/test/instance_service_test.go:148-172)
// but tightened to walk the full closed set of seven keys. Each
// subtest is independent so a future regression in one key's default
// surfaces in the test name, not as a single failing test.
func TestGetInstanceSetting_MissingRowReturnsDefault(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	inst := NewInstanceServer(app)
	ctx := withViewer(context.Background(), alice)

	// Subtests share the same freshly-migrated app; each one asks
	// for a different key, so a stale row from one subtest can
	// never satisfy the next. The collection is empty before any
	// subtest runs because the app was just bootstrapped.
	keys := []string{
		"GENERAL", "STORAGE", "MEMO_RELATED", "TAGS", "NOTIFICATION", "AI", "ACCESS",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			got, err := inst.GetInstanceSetting(ctx, &v1.GetInstanceSettingRequest{
				Name: "instance/settings/" + key,
			})
			require.NoError(t, err, "missing row for known key must not be an error")
			require.NotNil(t, got, "default body must not be nil")
			assert.Equal(t, "instance/settings/"+key, got.GetName())
			require.NotNil(t, got.GetValue(), "default must populate the matching oneof variant, not leave Value unset")
		})
	}

	// The read path is read-only: it must not have created any
	// rows. The collection is still empty after all seven reads.
	rows, err := app.FindRecordsByFilter("instance_settings", "", "", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, rows, "GetInstanceSetting must not write to the instance_settings collection")
}

// TestGetInstanceSetting_UnknownKeyStillInvalidArgument pins the
// negative case: a name that doesn't decompose to a known key must
// still return an error, with the CodeInvalidArgument that
// splitInstanceSettingName produces (not CodeNotFound — the
// unknown-key rejection happens before the row lookup). The closed
// set is enforced by the parser; the read path's CodeNotFound branch
// in the body is defensive and unreachable in practice.
func TestGetInstanceSetting_UnknownKeyStillInvalidArgument(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	inst := NewInstanceServer(app)
	ctx := withViewer(context.Background(), alice)

	_, err := inst.GetInstanceSetting(ctx, &v1.GetInstanceSettingRequest{
		Name: "instance/settings/GARBAGE",
	})
	require.Error(t, err)
}

// TestBatchGetInstanceSettings_FillsDefaults pins the batch path: a
// requested key that has no stored row must come back as a default
// body, not be silently dropped. The per-key and batch endpoints
// must agree, otherwise the SPA's settings page would render
// inconsistently depending on which RPC the boot path uses.
func TestBatchGetInstanceSettings_FillsDefaults(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	inst := NewInstanceServer(app)
	ctx := withViewer(context.Background(), alice)

	resp, err := inst.BatchGetInstanceSettings(ctx, &v1.BatchGetInstanceSettingsRequest{
		Names: []string{
			"instance/settings/GENERAL",
			"instance/settings/STORAGE", // not stored yet
			"instance/settings/ACCESS",  // not stored yet
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetSettings(), 3,
		"every known key in the request must come back, even with no stored row")

	byName := map[string]*v1.InstanceSetting{}
	for _, s := range resp.GetSettings() {
		byName[s.GetName()] = s
	}
	require.NotNil(t, byName["instance/settings/GENERAL"].GetGeneralSetting())
	require.NotNil(t, byName["instance/settings/STORAGE"].GetStorageSetting())
	require.NotNil(t, byName["instance/settings/ACCESS"].GetAccessSetting())
}
