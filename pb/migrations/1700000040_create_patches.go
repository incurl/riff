package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createPatchesCollection models a single PO-33 patch / backup file
// attached to a memo of type=patch.
//
// PO-33 patch files are typically TE Pocket-Operator micro-grayscale
// format. The MIME type we accept is application/octet-stream (the
// file is opaque binary); we set a MaxSize of 8 MB which is well
// above any real patch file.
//
// Fields:
//
//	memo                  (relation → memos, single, required)
//	file                  (file, single, max 8 MB, octet-stream)
//	description           (text, max 500)
//	compatible_po_version (text, max 16) — e.g. "PO-33", "PO-33 v1.2"
//
// Access rules inherit from the parent memo. Per the design doc,
// a patch is readable if its memo is readable, so the rule
// dereferences `memo` and applies the same audience logic as the
// memos collection:
//
//	(memo.author = @request.auth.id
//	 OR memo.visibility = "PROTECTED"
//	 OR memo.visibility = "PUBLIC"
//	 OR (memo.visibility = "SPACE"
//	     && memo.space != null
//	     && memo.space.space_members_via_space.user.id ?= @request.auth.id
//	     && memo.space.space_members_via_space.status = "ACTIVE"))
//
// Mutation (upload, replace) is the memo's author only. Deletion is
// the memo's author only. Phase 3 will tighten write rules to also
// require active space membership when the memo is SPACE-audience.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		p := core.NewBaseCollection("patches")

		p.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting the parent memo removes its patch
		})
		p.Fields.Add(&core.FileField{
			Name:      "file",
			Required:  true,
			MaxSelect: 1,
			MaxSize:   8 * 1024 * 1024, // 8 MB
			MimeTypes: []string{"application/octet-stream"},
		})
		p.Fields.Add(&core.TextField{
			Name: "description",
			Max:  500,
		})
		p.Fields.Add(&core.TextField{
			Name: "compatible_po_version",
			Max:  16,
		})

		// Visibility inherits from parent memo.
		visibilityRule := `memo.author = @request.auth.id` +
			` || memo.visibility = "PROTECTED"` +
			` || memo.visibility = "PUBLIC"` +
			` || (memo.visibility = "SPACE"` +
			`     && memo.space != null` +
			`     && memo.space.space_members_via_space.user.id ?= @request.auth.id` +
			`     && memo.space.space_members_via_space.status = "ACTIVE")`
		p.ListRule = types.Pointer(visibilityRule)
		p.ViewRule = types.Pointer(visibilityRule)
		p.CreateRule = types.Pointer("memo.author = @request.auth.id")
		p.UpdateRule = types.Pointer("memo.author = @request.auth.id")
		p.DeleteRule = types.Pointer("memo.author = @request.auth.id")

		// Indexes
		p.AddIndex("idx_patches_memo", false, "memo", "")

		return txApp.Save(p)
	}, func(txApp core.App) error {
		p, err := txApp.FindCollectionByNameOrId("patches")
		if err != nil {
			return err
		}
		return txApp.Delete(p)
	})
}
