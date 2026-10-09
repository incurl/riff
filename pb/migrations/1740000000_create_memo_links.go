package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createMemoLinksCollection backs the Phase 3 link-preview
// auto-fetch. Every time a memo with a link is saved, the
// memo_links hook in pb/hooks/memo_links.go writes one row per
// distinct URL into this collection, then fills in title /
// description / image / error by running linkmeta against the
// URL. Rows are upserted on each save; rows whose URL no longer
// appears in the memo content are deleted on update.
//
// Fields:
//
//	memo       (relation → memos, single, required, indexed,
//	            cascade delete: yes — link rows die with the memo)
//	url        (url, required, indexed) — the link target
//	title      (text, optional, max 500) — linkmeta's resolved title
//	description(text, optional, max 2000) — linkmeta's resolved description
//	image      (url, optional) — linkmeta's resolved image
//	fetched_at (date, optional, indexed) — last successful fetch time
//	error      (text, optional, max 500) — last fetch error if any
//
// Access rules. Anyone who can read a memo can read its
// memo_links; only the hook (which runs server-side) writes.
//
//	viewRule   = visibility-respecting rule via the memo relation
//	createRule = nil (server-only via the connect layer; the public
//	             API does not expose this collection for writes)
//	updateRule = nil
//	deleteRule = nil
//
// A unique index on (memo, url) keeps the upsert path a single
// query instead of "SELECT then INSERT-or-UPDATE".
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		if existing, err := txApp.FindCollectionByNameOrId("memo_links"); err == nil && existing != nil {
			// Idempotent: re-running migrations is a supported
			// PocketBase path. Same reasoning as
			// 1700000021_add_memos_payload.go.
			_ = existing
			return nil
		}

		col := core.NewBaseCollection("memo_links")

		col.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		col.Fields.Add(&core.URLField{
			Name:     "url",
			Required: true,
		})
		col.Fields.Add(&core.TextField{
			Name: "title",
			Max:  500,
		})
		col.Fields.Add(&core.TextField{
			Name: "description",
			Max:  2000,
		})
		col.Fields.Add(&core.URLField{
			Name:     "image",
			Required: false,
		})
		col.Fields.Add(&core.DateField{
			Name: "fetched_at",
		})
		col.Fields.Add(&core.TextField{
			Name: "error",
			Max:  500,
		})

		// Mirror the memos visibility rule. The relation
		// back-traversal `memo.<visibility>` evaluates the
		// referenced memo's own rules. Anonymous users still
		// hit the public path; signed-in non-owners hit the
		// protected path; space members hit the space path.
		col.ListRule = mustPointer(
			`memo.visibility = "PUBLIC"` +
				` || memo.visibility = "PROTECTED"` +
				` || (memo.visibility = "SPACE" && memo.space != null && memo.space.space_members_via_space.user.id ?= @request.auth.id && memo.space.space_members_via_space.status = "ACTIVE")`,
		)
		col.ViewRule = col.ListRule

		// Server-only writes; the hook is the sole writer.
		col.CreateRule = mustPointer("")
		col.UpdateRule = mustPointer("")
		col.DeleteRule = mustPointer("")

		// Indexes. The unique (memo, url) pair is the upsert
		// key. fetched_at is indexed so a future
		// "stale-link" sweeper can find rows older than N days
		// without a full table scan.
		col.AddIndex("idx_memo_links_memo_url", true, "memo,url", "")
		col.AddIndex("idx_memo_links_fetched_at", false, "fetched_at", "")

		return txApp.Save(col)
	}, func(txApp core.App) error {
		col, err := txApp.FindCollectionByNameOrId("memo_links")
		if err != nil {
			return err
		}
		return txApp.Delete(col)
	})
}

// mustPointer is a tiny helper that turns a string literal into
// a *types.Pointer (the value type the field-setters want). Kept
// here instead of imported from another migration to avoid
// pulling `pb/tools/types` into the import surface for a one-liner.
func mustPointer(s string) *string { return &s }
