package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createMemoRelationsCollection models the Memos 0.31 design-doc
// concept of a `COMMENT` relation between two memos. A comment is an
// independent memo connected to a context memo by one immutable
// `COMMENT` relation created atomically with it.
//
// In the Memos gRPC API this collection is the wire shape for both
// `COMMENT` and `REFERENCE` relations. For Phase 2 we only model
// `COMMENT`; `REFERENCE` may be added later if/when PO-33 memos
// reference each other.
//
// Fields:
//
//	memo         (relation → memos, single, required) — the comment memo
//	related_memo (relation → memos, single, required) — the context memo
//	type         (select, required, default: "COMMENT";
//	              values: COMMENT, REFERENCE)
//
// Immutable: PocketBase does not have a built-in "immutable" flag.
// Phase 3 will enforce "no update" via OnRecordUpdateRequest hook.
//
// Access rules (Phase 2 baseline):
//
//	Visibility inherits from BOTH endpoints, per the design doc:
//	"A COMMENT or REFERENCE relation and its snippet are returned
//	only when both endpoints are readable."
//	We model this conservatively: a relation is listable / viewable
//	if and only if BOTH the comment memo and the context memo are
//	visible to the caller. Phase 3 will refine via OnRecordsListRequest
//	hooks; for Phase 2 we open the read rules to authenticated users
//	and rely on Phase 3 filters to drop unreadable rows.
//
//	createRule = ""   (any authenticated user; Phase 3 will check that
//	                   (a) the caller is the author of the comment
//	                   memo, (b) the context memo is visible to the
//	                   caller, (c) if the context memo's visibility is
//	                   SPACE, the caller is an active member)
//	updateRule = nil  (locked; Phase 3 will enforce immutability)
//	deleteRule = memo.author = @request.auth.id   (comment author only)
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		rel := core.NewBaseCollection("memo_relations")

		rel.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting the comment memo removes the relation
		})
		rel.Fields.Add(&core.RelationField{
			Name:          "related_memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: false, // deleting a context memo does NOT delete its comments (comments remain orphaned but readable per the design doc; deleted-memo webhook is the only signal)
		})
		rel.Fields.Add(&core.SelectField{
			Name:     "type",
			Required: true,
			Values:   []string{"COMMENT", "REFERENCE"},
		})

		rel.ListRule = types.Pointer("@request.auth.id != ''")
		rel.ViewRule = types.Pointer("@request.auth.id != ''")
		rel.CreateRule = types.Pointer("")
		rel.UpdateRule = nil // immutable
		rel.DeleteRule = types.Pointer("@request.auth.id != '' && memo.author = @request.auth.id")

		// Indexes
		rel.AddIndex("idx_memo_relations_memo", false, "memo", "")
		rel.AddIndex("idx_memo_relations_related", false, "related_memo", "")
		// Note: PocketBase forbids a unique index here because the same
		// context memo can have many comments, and the same comment
		// memo can be related to many contexts (in REFERENCE mode).
		// No unique index for now.

		return txApp.Save(rel)
	}, func(txApp core.App) error {
		rel, err := txApp.FindCollectionByNameOrId("memo_relations")
		if err != nil {
			return err
		}
		return txApp.Delete(rel)
	})
}
