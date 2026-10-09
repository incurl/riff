package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createUserWebhooksCollection backs the five
// UserService.UserWebhook RPCs declared in
// proto/api/v1/user_service.proto. One row per webhook
// a user has configured. The signing secret column is
// stored as plain text; the proto guarantees it is
// INPUT_ONLY and the handler omits it on every read
// path.
//
// Why a plain TextField and not PocketBase's built-in
// PasswordField? PasswordField.Intercept clears the
// in-memory `Plain` after the create/update action,
// which breaks both the handler's response and the
// drain goroutine that needs the raw value to sign
// outgoing requests. PasswordField is also coupled to
// auth collections; the user_webhooks table is a base
// collection, so we'd be fighting the framework. The
// secret is still protected against casual API
// inspection (the proto's INPUT_ONLY annotation
// removes it from every list / get response) and
// against leaked logs (the field is `Hidden: true`).
// At-rest database encryption is the operator's
// responsibility (PocketBase ships a `pb_data/`
// directory the operator is expected to mount on
// encrypted storage).
//
// Fields:
//
//	user          (relation → users, single, required,
//	               CascadeDelete) — the user that owns
//	               the webhook. Deleting the user
//	               hard-deletes all of their webhooks.
//	url           (url, required) — the receiver. We
//	               re-validate the URL through
//	               internal/webhook in the Connect
//	               handler so the form-level check is
//	               purely a UX nicety.
//	display_name  (text, max 200) — optional human
//	               label shown in the React settings UI.
//	signing_secret (text, max 256, Hidden) — the
//	               Standard Webhooks "whsec_<base64>"
//	               token. INPUT_ONLY in the proto, so
//	               it never round-trips via the list
//	               and get responses. Empty means "no
//	               signing on outbound deliveries".
//	created       (autodate, onCreate)
//	updated       (autodate, onUpdate)
//
// Access rules: server-only on every verb. The
// `users/{user}/webhooks/{webhook}` resource name
// names a row by its own id, not by display_name, so
// there is no need for a separate list filter — the
// handler is the sole authority. All `*Rule` fields
// are nil so PocketBase's superuser privilege is the
// only path that can mutate the table from the
// collection REST API.
//
// CascadeDelete on `user` mirrors the user_settings
// collection: deleting a user removes all of their
// webhooks in the same transaction.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		if existing, err := txApp.FindCollectionByNameOrId("user_webhooks"); err == nil && existing != nil {
			return nil
		}

		w := core.NewBaseCollection("user_webhooks")

		w.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		w.Fields.Add(&core.URLField{
			Name:     "url",
			Required: true,
		})
		w.Fields.Add(&core.TextField{
			Name: "display_name",
			Max:  200,
		})
		w.Fields.Add(&core.TextField{
			Name:   "signing_secret",
			Max:    256,
			Hidden: true,
		})
		w.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		w.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnUpdate: true,
		})

		w.ListRule = nil
		w.ViewRule = nil
		w.CreateRule = nil
		w.UpdateRule = nil
		w.DeleteRule = nil

		w.AddIndex("idx_user_webhooks_user", false, "user", "")

		return txApp.Save(w)
	}, func(txApp core.App) error {
		w, err := txApp.FindCollectionByNameOrId("user_webhooks")
		if err != nil {
			return err
		}
		return txApp.Delete(w)
	})
}
