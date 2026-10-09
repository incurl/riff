package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createSamplesCollection models a sample pack attached to a memo of
// type=sample_pack. Sample packs can contain multiple .wav/.mp3
// files. Metadata (bpm, key, license, attribution) lives on the
// parent memo per Phase 2 Q7 — search "samples by BPM" is via
// memo.bpm, not duplicated here.
//
// Fields:
//
//	memo        (relation → memos, single, required)
//	files       (file, multi, max 50 files, max 32 MB each,
//	             allowed MIME: audio/wav, audio/x-wav, audio/mpeg,
//	             audio/mp3, application/zip)
//	description (text, max 500)
//
// Access rules mirror patches: inherit from parent memo visibility.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		s := core.NewBaseCollection("samples")

		s.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // deleting the parent memo removes its sample pack
		})
		s.Fields.Add(&core.FileField{
			Name:      "files",
			Required:  true,
			MaxSelect: 50,
			MaxSize:   32 * 1024 * 1024, // 32 MB per file
			MimeTypes: []string{
				"audio/wav",
				"audio/x-wav",
				"audio/mpeg",
				"audio/mp3",
				"application/zip",
			},
		})
		s.Fields.Add(&core.TextField{
			Name: "description",
			Max:  500,
		})

		visibilityRule := `memo.author = @request.auth.id` +
			` || memo.visibility = "PROTECTED"` +
			` || memo.visibility = "PUBLIC"` +
			` || (memo.visibility = "SPACE"` +
			`     && memo.space != null` +
			`     && memo.space.space_members_via_space.user.id ?= @request.auth.id` +
			`     && memo.space.space_members_via_space.status = "ACTIVE")`
		s.ListRule = types.Pointer(visibilityRule)
		s.ViewRule = types.Pointer(visibilityRule)
		s.CreateRule = types.Pointer("memo.author = @request.auth.id")
		s.UpdateRule = types.Pointer("memo.author = @request.auth.id")
		s.DeleteRule = types.Pointer("memo.author = @request.auth.id")

		// Indexes
		s.AddIndex("idx_samples_memo", false, "memo", "")

		return txApp.Save(s)
	}, func(txApp core.App) error {
		s, err := txApp.FindCollectionByNameOrId("samples")
		if err != nil {
			return err
		}
		return txApp.Delete(s)
	})
}
