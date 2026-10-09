package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createMemosCollection creates the `memos` collection, the central
// post type for riff.
//
// Per the Memos 0.31 design doc, a memo's audience (visibility) and
// its placement (space) are independent. Visibility values are named
// domains: PRIVATE, PROTECTED, PUBLIC, SPACE. Space placement is
// nullable (Unassigned) and adds the read gate only when visibility
// is SPACE.
//
// Fields:
//
//	author      (relation → users, single, required, indexed)
//	space       (relation → spaces, single, optional, indexed)
//	content     (editor, required) — markdown body
//	visibility  (select, required, no default;
//	             values: PRIVATE, PROTECTED, PUBLIC, SPACE)
//	type        (select, required, default: "note";
//	             values: note, patch, sample_pack, link)
//	— PO-33 metadata (per system-prompt domain model) —
//	bpm          (number, optional, onlyInt, 0-300)
//	key          (text,   optional, max 16)
//	sample_source(text,   optional, max 80)
//	tags         (json,   optional) — array of strings
//	difficulty   (select, optional; values: beginner, intermediate, advanced)
//	license      (text,   optional, max 80)
//	attribution  (text,   optional, max 200)
//	video_url    (url,    optional)
//	— timestamps —
//	created      (autodate, onCreate)
//	updated      (autodate, onCreate + onUpdate)
//
// Access rules. The visibility rule (Memos 0.31 audience table):
//
//	author = @request.auth.id                                       — author always
//	OR visibility = "PROTECTED"                                     — any active user
//	OR visibility = "PUBLIC"                                        — any caller (incl. anonymous, but PocketBase's auth middleware applies first)
//	OR (visibility = "SPACE" && space != null && space.space_members_via_space.user.id ?= @request.auth.id && space.space_members_via_space.status = "ACTIVE")
//
// PocketBase's relation-traversal syntax `<target>_via_<relfield>`
// refers to the back-relation on the target collection. Here we
// traverse from a memo to its space's space_members rows where the
// user is the authenticated caller and the status is ACTIVE.
//
// Update and delete are author-only. The author can move a memo
// between spaces by updating `space` (which Phase 3 will validate:
// if visibility=SPACE, the new space must have the author as an
// active member; if visibility != SPACE, space must be cleared).
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		spaces, err := txApp.FindCollectionByNameOrId("spaces")
		if err != nil {
			return err
		}

		memos := core.NewBaseCollection("memos")

		memos.Fields.Add(&core.RelationField{
			Name:          "author",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: false, // a user with memos cannot be hard-deleted until memos are moved or deleted
		})
		memos.Fields.Add(&core.RelationField{
			Name:          "space",
			CollectionId:  spaces.Id,
			Required:      false,
			MaxSelect:     1,
			MinSelect:     0,
			CascadeDelete: false, // Phase 3 owns space-deletion cascading
		})
		memos.Fields.Add(&core.EditorField{
			Name:     "content",
			Required: true,
		})
		memos.Fields.Add(&core.SelectField{
			Name:     "visibility",
			Required: true,
			Values:   []string{"PRIVATE", "PROTECTED", "PUBLIC", "SPACE"},
		})
		memos.Fields.Add(&core.SelectField{
			Name:     "type",
			Required: true,
			Values:   []string{"note", "patch", "sample_pack", "link"},
		})

		// PO-33 metadata fields.
		memos.Fields.Add(&core.NumberField{
			Name:    "bpm",
			OnlyInt: true,
		})
		memos.Fields.Add(&core.TextField{
			Name: "key",
			Max:  16,
		})
		memos.Fields.Add(&core.TextField{
			Name: "sample_source",
			Max:  80,
		})
		memos.Fields.Add(&core.JSONField{
			Name: "tags",
		})
		memos.Fields.Add(&core.SelectField{
			Name:   "difficulty",
			Values: []string{"beginner", "intermediate", "advanced"},
		})
		memos.Fields.Add(&core.TextField{
			Name: "license",
			Max:  80,
		})
		memos.Fields.Add(&core.TextField{
			Name: "attribution",
			Max:  200,
		})
		memos.Fields.Add(&core.URLField{
			Name: "video_url",
		})

		// Timestamps.
		memos.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		memos.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnCreate: true,
			OnUpdate: true,
		})

		// Access rules.
		visibilityRule := `author = @request.auth.id` +
			` || visibility = "PROTECTED"` +
			` || visibility = "PUBLIC"` +
			` || (visibility = "SPACE" && space != null && space.space_members_via_space.user.id ?= @request.auth.id && space.space_members_via_space.status = "ACTIVE")`
		memos.ListRule = types.Pointer(visibilityRule)
		memos.ViewRule = types.Pointer(visibilityRule)
		memos.CreateRule = types.Pointer("")
		memos.UpdateRule = types.Pointer("author = @request.auth.id")
		memos.DeleteRule = types.Pointer("author = @request.auth.id")

		// Indexes.
		memos.AddIndex("idx_memos_author", false, "author", "")
		memos.AddIndex("idx_memos_space", false, "space", "")
		memos.AddIndex("idx_memos_created", false, "created", "")

		return txApp.Save(memos)
	}, func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		return txApp.Delete(memos)
	})
}
