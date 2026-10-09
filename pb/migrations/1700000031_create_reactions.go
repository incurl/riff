package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createReactionsCollection models likes/reactions on a memo.
//
// Per the Memos 0.31 design doc, "A reaction belongs to one memo and
// has no independent audience." Reads inherit from the parent memo
// (Phase 3 will layer this in via OnRecordsListRequest); for Phase 2
// we expose listable to authenticated users and let Phase 3 filter
// out rows whose parent memo is unreadable.
//
// Fields:
//
//	memo   (relation → memos, single, required)
//	user   (relation → users, single, required) — the reactor
//	emoji  (text, required, max 16) — the reaction glyph; the system
//	      uses emoji but the field is text so that custom short
//	      strings ("+1", "🔥", "PO-33") also work.
//
// Access rules (Phase 2 baseline; Phase 3 will tighten):
//
//	listRule   = memo = "" OR @request.auth.id != ""   (any auth user; Phase 3 will narrow to "parent memo is readable")
//	viewRule   = @request.auth.id != ""
//	createRule = @request.auth.id != "" && user = @request.auth.id
//	deleteRule = user = @request.auth.id
//
// Per the design doc: "An active reaction creator can withdraw their
// own reaction even after losing memo access or Space membership."
// So delete is self-only and is NOT gated by the parent memo's
// current visibility.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		r := core.NewBaseCollection("reactions")

		r.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting the parent memo removes its reactions
		})
		r.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: false,
		})
		r.Fields.Add(&core.TextField{
			Name:     "emoji",
			Required: true,
			Max:      16,
		})

		r.ListRule = types.Pointer("@request.auth.id != ''")
		r.ViewRule = types.Pointer("@request.auth.id != ''")
		r.CreateRule = types.Pointer("@request.auth.id != '' && user = @request.auth.id")
		r.UpdateRule = types.Pointer("user = @request.auth.id")
		r.DeleteRule = types.Pointer("user = @request.auth.id")

		// Indexes
		r.AddIndex("idx_reactions_memo", false, "memo", "")
		r.AddIndex("idx_reactions_user", false, "user", "")

		return txApp.Save(r)
	}, func(txApp core.App) error {
		r, err := txApp.FindCollectionByNameOrId("reactions")
		if err != nil {
			return err
		}
		return txApp.Delete(r)
	})
}
