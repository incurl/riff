package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createMemoViewsCollection backs MemoView in proto/api/v1/user_service.proto.
//
// A MemoView is a saved CEL filter the user can pin to the sidebar; the
// `filter` column carries the expression verbatim and `icon` carries either a
// Unicode emoji or a canonical Lucide name (the proto's oneof).
//
// Fields:
//
//	user     (relation → users, single, required, indexed)
//	title    (text, required)
//	filter   (text, required)
//	icon_kind (select, optional; "emoji" or "lucide")
//	icon_value (text, optional; the emoji or Lucide name)
//	created  (autodate, onCreate)
//
// Access rules: a user can CRUD only their own views. ListRule and ViewRule
// gate reads, UpdateRule/DeleteRule gate writes. CreateRule is nil because
// the REST API would let any authenticated user create rows on behalf of
// anyone; Connect goes through the record-request bridge and stamps the
// owner, which the rule engine doesn't enforce.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		mv := core.NewBaseCollection("memo_views")

		mv.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		mv.Fields.Add(&core.TextField{
			Name:     "title",
			Required: true,
			Max:      120,
		})
		mv.Fields.Add(&core.TextField{
			Name:     "filter",
			Required: true,
			Max:      1000,
		})
		mv.Fields.Add(&core.SelectField{
			Name:     "icon_kind",
			Required: false,
			Values:   []string{"emoji", "lucide"},
		})
		mv.Fields.Add(&core.TextField{
			Name: "icon_value",
			Max:  64,
		})
		mv.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		mv.ListRule = types.Pointer("user = @request.auth.id")
		mv.ViewRule = types.Pointer("user = @request.auth.id")
		mv.CreateRule = nil
		mv.UpdateRule = types.Pointer("user = @request.auth.id")
		mv.DeleteRule = types.Pointer("user = @request.auth.id")

		mv.AddIndex("idx_memo_views_user", false, "user", "")

		return txApp.Save(mv)
	}, func(txApp core.App) error {
		mv, err := txApp.FindCollectionByNameOrId("memo_views")
		if err != nil {
			return err
		}
		return txApp.Delete(mv)
	})
}
