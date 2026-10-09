package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createNotificationsCollection adds the `notifications` inbox used
// for server-pushed events like space invitations.
//
// We use a custom collection rather than PocketBase's `mails` table
// because the Memos 0.31 design doc specifies in-app inbox
// notifications the invitee can act on (accept/decline), and
// PocketBase's `mails` is for transactional email only.
//
// Fields:
//
//	user      (relation → users, single, required, indexed)
//	type      (select, required, indexed; values: SPACE_INVITATION, MEMO_COMMENT, MEMO_REACTION)
//	payload   (json, required) — type-specific shape:
//	          SPACE_INVITATION: { space: { id, uid, title, icon, role } }
//	          MEMO_COMMENT:     { memo: { id, author, snippet }, from_user }
//	          MEMO_REACTION:    { memo: { id, author }, emoji, from_user }
//	read      (bool, default false, indexed)
//	created   (autodate, onCreate, indexed)
//
// Access rules: a user can see and act on their own notifications.
// `createRule = nil` (superuser only) — server hooks are the only
// path to insert rows, which keeps the type/payload shape controlled.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		n := core.NewBaseCollection("notifications")

		n.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting a user removes their inbox
		})
		n.Fields.Add(&core.SelectField{
			Name:     "type",
			Required: true,
			Values:   []string{"SPACE_INVITATION", "MEMO_COMMENT", "MEMO_REACTION"},
		})
		n.Fields.Add(&core.JSONField{
			Name:     "payload",
			Required: true,
		})
		n.Fields.Add(&core.BoolField{
			Name: "read",
		})
		n.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		n.ListRule = types.Pointer("user = @request.auth.id")
		n.ViewRule = types.Pointer("user = @request.auth.id")
		n.CreateRule = nil // server-only via hooks
		n.UpdateRule = types.Pointer("user = @request.auth.id")
		n.DeleteRule = types.Pointer("user = @request.auth.id")

		// Indexes
		n.AddIndex("idx_notifications_user", false, "user, read", "")
		n.AddIndex("idx_notifications_created", false, "created", "")

		return txApp.Save(n)
	}, func(txApp core.App) error {
		n, err := txApp.FindCollectionByNameOrId("notifications")
		if err != nil {
			return err
		}
		return txApp.Delete(n)
	})
}
