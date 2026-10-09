package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createUserSettingsCollection backs UserSetting in proto/api/v1/user_service.proto.
//
// Memos models user settings as a flat list of (key, value) records per user.
// The setting body is one of the proto's `oneof` variants (general_setting,
// webhooks_setting, tags_setting); we don't try to discriminate at the column
// level — we store the full proto JSON in a `payload` field. That keeps Wave 5
// free of bespoke serializers and means new setting kinds land without a
// schema change; the trade-off is that the columns aren't searchable, which
// matches what the frontend actually does (it loads the row, then renders).
//
// Fields:
//
//	user     (relation → users, single, required, indexed)
//	key      (text, required) — one of "GENERAL", "WEBHOOKS", "TAGS"
//	payload  (json, required) — proto JSON of the matching setting body
//
// Access rules: a user can read and update only their own settings.
//
//	viewRule   = "user = @request.auth.id"
//	updateRule = "user = @request.auth.id"
//	deleteRule = "user = @request.auth.id"
//	createRule = nil (server-only via Connect; `user_settings` is not
//	             exposed to the REST collection API)
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		us := core.NewBaseCollection("user_settings")

		us.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting a user removes their settings
		})
		us.Fields.Add(&core.TextField{
			Name:     "key",
			Required: true,
			Max:      32,
		})
		us.Fields.Add(&core.JSONField{
			Name:     "payload",
			Required: true,
		})

		us.ListRule = types.Pointer("user = @request.auth.id")
		us.ViewRule = types.Pointer("user = @request.auth.id")
		us.CreateRule = nil // server-only
		us.UpdateRule = types.Pointer("user = @request.auth.id")
		us.DeleteRule = types.Pointer("user = @request.auth.id")

		us.AddIndex("idx_user_settings_user_key", true, "user, key", "")

		return txApp.Save(us)
	}, func(txApp core.App) error {
		us, err := txApp.FindCollectionByNameOrId("user_settings")
		if err != nil {
			return err
		}
		return txApp.Delete(us)
	})
}
