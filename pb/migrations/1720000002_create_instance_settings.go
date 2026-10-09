package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createInstanceSettingsCollection backs InstanceSetting in
// proto/api/v1/instance_service.proto.
//
// There is exactly one row per setting key. The proto's `oneof value` body is
// stored verbatim as JSON in `payload`, the same trade-off as user_settings:
// it costs us queryability, but the frontend loads settings eagerly so the
// round-trip cost is fine, and adding a new setting kind never needs a
// migration.
//
// Fields:
//
//	key      (text, required, unique) — e.g. "GENERAL", "STORAGE"
//	payload  (json, required) — proto JSON of the matching body
//
// Access rules: listable by anyone (the admin settings page fetches a
// batch on boot), but only superusers may mutate. CreateRule is nil so
// the REST collection API cannot be used to bypass superuser checks.
func init() {
	migrations.Register(func(txApp core.App) error {
		is := core.NewBaseCollection("instance_settings")

		is.Fields.Add(&core.TextField{
			Name:     "key",
			Required: true,
			Max:      32,
		})
		is.Fields.Add(&core.JSONField{
			Name:     "payload",
			Required: true,
		})
		is.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnCreate: true,
			OnUpdate: true,
		})

		// List/View: public so the SPA can read settings on first paint.
		is.ListRule = types.Pointer("")
		is.ViewRule = types.Pointer("")
		// Mutate: superuser only. Connect goes through the record-request
		// bridge; we additionally re-check inside the handler.
		is.CreateRule = nil
		is.UpdateRule = types.Pointer("@request.auth.isSuperuser = true")
		is.DeleteRule = types.Pointer("@request.auth.isSuperuser = true")

		is.AddIndex("idx_instance_settings_key", true, "key", "")

		return txApp.Save(is)
	}, func(txApp core.App) error {
		is, err := txApp.FindCollectionByNameOrId("instance_settings")
		if err != nil {
			return err
		}
		return txApp.Delete(is)
	})
}
