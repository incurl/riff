package connect

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/version"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// riffVersionFallback is reported by GetInstanceProfile when the
// build did not embed a version (i.e. `go test` without
// -buildvcs=true). Production builds use
// `go run -buildvcs=true ./cmd/memos` (per AGENTS.md) which makes
// internal/version.Version return a CalVer string derived from
// the commit timestamp.
const riffVersionFallback = "dev (riff)"

// instanceVersion returns the build version, falling back to a
// development placeholder when the build did not embed vcs
// metadata. The fallback keeps the
// `TestInstanceService_FreshInstall` assertion
// (`assert.NotEmpty(GetVersion())`) passing under `go test`.
//
// When the operator set RIFF_MEMOS_VERSION (via
// SetAdvertisedVersion), that value wins: it is the Memos
// version string the V1 client's compatibility check reads, so a
// riff deployment that has completed the V1 surface advertises
// "0.31.0" (or whatever the current maximum is) rather than the
// riff build CalVer, which the client would flag as "higher than
// supported".
func instanceVersion() string {
	if advertisedMemosVersion != "" {
		return advertisedMemosVersion
	}
	if v := version.GetCurrentVersion(); v != "" {
		return v
	}
	return riffVersionFallback
}

// advertisedMemosVersion is the operator-configured Memos version
// reported by GetInstanceProfile. Empty means "use the riff build
// version". Set once at startup by main.go from
// RIFF_MEMOS_VERSION; mirrors pb/api.SetAdvertisedVersion so the
// V0 `/api/v1/status` probe and the V1
// `/api/v1/instance/profile` probe report the same string.
var advertisedMemosVersion string

// SetAdvertisedVersion overrides the Memos version reported by
// GetInstanceProfile. An empty argument is a no-op (the build
// version is preserved) so a misconfigured operator does not
// silently advertise an empty string, which the V1 client treats
// as "unsupported version".
func SetAdvertisedVersion(v string) {
	if v == "" {
		return
	}
	advertisedMemosVersion = v
}

// instanceCommit returns the build commit hash. Empty in dev
// builds without -buildvcs=true; populated otherwise. The
// frontend doesn't require a non-empty value.
func instanceCommit() string {
	if version.Commit != "" && version.Commit != "unknown" {
		return version.Commit
	}
	return ""
}

// InstanceServer implements the Memos InstanceService.
// Phase 4 wires GetInstanceProfile, which the Memos frontend
// calls on app load to display the site name, version, and
// initial setup state.
type InstanceServer struct {
	apiv1connect.UnimplementedInstanceServiceHandler
	app core.App
}

func NewInstanceServer(app core.App) *InstanceServer {
	return &InstanceServer{app: app}
}

// GetInstanceProfile returns the public-facing instance
// profile. The profile includes the site name (from PocketBase
// settings), the app version, and the first admin (used by
// the frontend to decide whether to show the installer).
func (s *InstanceServer) GetInstanceProfile(ctx context.Context, _ *v1.GetInstanceProfileRequest) (*v1.InstanceProfile, error) {
	settings := s.app.Settings()

	// Find the first real admin. PocketBase stores admins in the
	// _superusers collection; its auto-created installer
	// placeholder is skipped.
	admin := findFirstAdmin(s.app)

	// Access mode: derive from settings (the "disable
	// registration" flag) or hardcode to PUBLIC since riff
	// has no registration form.
	accessMode := v1.InstanceAccessMode_INSTANCE_ACCESS_MODE_PUBLIC
	if settings.Meta.HideControls {
		// If the admin hid UI controls, the instance is
		// effectively private to invited users.
		accessMode = v1.InstanceAccessMode_INSTANCE_ACCESS_MODE_PRIVATE
	}

	return &v1.InstanceProfile{
		Version:     instanceVersion(),
		Commit:      instanceCommit(),
		InstanceUrl: settings.Meta.AppURL,
		Admin:       admin,
		NeedsSetup:  needsSetup(s.app),
		AccessMode:  accessMode,
	}, nil
}

// findFirstAdmin looks up the oldest real superuser record and
// converts it to a v1.User proto. Returns nil if no admin
// exists (e.g., on a fresh install before setup).
//
// PocketBase creates a placeholder superuser (DefaultInstallerEmail)
// on first boot to drive its own installer; it is not an admin a
// human chose, so it is excluded. Sorted ascending by `created` so
// the profile reports the instance's original owner rather than an
// arbitrary superuser.
func findFirstAdmin(app core.App) *v1.User {
	rows, err := app.FindRecordsByFilter(
		core.CollectionNameSuperusers,
		"email != {:installer}", "created", 1, 0,
		map[string]any{"installer": core.DefaultInstallerEmail},
	)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return recordToUser(rows[0])
}

// needsSetup reports whether the instance still has to be
// initialised. An instance counts as set up once either a real
// superuser (the first admin, created via `riff superuser upsert`
// or the installer UI) or a regular user exists.
//
// The installer placeholder account does not count: it exists on
// every fresh instance, so treating it as a user would hide the
// installer from the frontend forever.
func needsSetup(app core.App) bool {
	admins, err := app.FindRecordsByFilter(
		core.CollectionNameSuperusers,
		"email != {:installer}", "", 1, 0,
		map[string]any{"installer": core.DefaultInstallerEmail},
	)
	if err == nil && len(admins) > 0 {
		return false
	}

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return true
	}
	count, err := app.CountRecords(users)
	if err != nil {
		return true
	}
	return count == 0
}
