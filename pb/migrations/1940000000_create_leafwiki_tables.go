package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createLeafWikiTables replaced the SQLite databases LeafWiki ships with
// PocketBase collections.
//
// Upstream LeafWiki keeps its Markdown pages on disk (the source of truth) and
// derives a handful of SQLite databases from them: `search.db`, `tags.db`,
// `links.db`, `properties.db`, plus the user/session/settings databases. This
// migration modelled that derived data as two collections — `leafwiki_pages`
// (one row per page or section: body, excerpt, tag and property arrays, the
// optimistic-lock version, the authorship block) and `leafwiki_links` (one row
// per internal link, `resolved` or `broken`, with a `wikilink:Title` to_path
// marking a [[WikiLink]] with no page yet).
//
// That design is now retired: 1950000000_drop_leafwiki_tables drops both
// collections, because pb/leafwiki keeps the index in memory on the app's own
// store. The tables were only ever written and never read across a restart
// (the tree is re-read from disk and the index rebuilt from it on every load),
// while the per-mutation reconcile rewrote every link row inside a transaction.
//
// This migration is kept rather than deleted so the history of an existing
// instance still matches its recorded migrations; see
// 1950000000_drop_leafwiki_tables.go.
func init() {
	migrations.Register(createLeafWikiCollections, dropLeafWikiCollections)
}

// createLeafWikiCollections builds the two index collections. It is a named
// function rather than an inline closure so the migration tests can apply it
// on its own — the collections no longer survive a full migration run (see
// 1950000000_drop_leafwiki_tables.go), but the schema still has a test.
func createLeafWikiCollections(txApp core.App) error {
	pages := core.NewBaseCollection("leafwiki_pages")
	pages.Fields.Add(&core.TextField{
		Name:     "page_id",
		Required: true,
		Max:      64,
	})
	pages.Fields.Add(&core.TextField{
		Name: "path",
		Max:  2048,
	})
	pages.Fields.Add(&core.TextField{
		Name: "slug",
		Max:  255,
	})
	pages.Fields.Add(&core.TextField{
		Name: "title",
		Max:  512,
	})
	pages.Fields.Add(&core.SelectField{
		Name:   "kind",
		Values: []string{"page", "section"},
	})
	pages.Fields.Add(&core.TextField{
		Name: "content",
	})
	pages.Fields.Add(&core.TextField{
		Name: "excerpt",
		Max:  1024,
	})
	pages.Fields.Add(&core.JSONField{
		Name: "tags",
	})
	pages.Fields.Add(&core.JSONField{
		Name: "properties",
	})
	pages.Fields.Add(&core.BoolField{
		Name: "pinned",
	})
	pages.Fields.Add(&core.NumberField{
		Name:    "position",
		OnlyInt: true,
	})
	pages.Fields.Add(&core.TextField{
		Name: "created_at",
		Max:  64,
	})
	pages.Fields.Add(&core.TextField{
		Name: "updated_at",
		Max:  64,
	})
	pages.Fields.Add(&core.TextField{
		Name: "version",
		Max:  64,
	})
	pages.Fields.Add(&core.TextField{
		Name: "creator_id",
		Max:  64,
	})
	pages.Fields.Add(&core.TextField{
		Name: "last_author_id",
		Max:  64,
	})

	pages.AddIndex("idx_leafwiki_pages_page_id", true, "page_id", "")
	pages.AddIndex("idx_leafwiki_pages_path", false, "path", "")

	// All rules nil: the Connect/REST surface in pb/leafwiki is the only
	// authority, and a derived cache must not be mutable over the
	// generated API.
	pages.ListRule = nil
	pages.ViewRule = nil
	pages.CreateRule = nil
	pages.UpdateRule = nil
	pages.DeleteRule = nil

	if err := txApp.Save(pages); err != nil {
		return err
	}

	links := core.NewBaseCollection("leafwiki_links")
	links.Fields.Add(&core.TextField{
		Name:     "from_page_id",
		Required: true,
		Max:      64,
	})
	links.Fields.Add(&core.TextField{
		Name: "to_page_id",
		Max:  64,
	})
	links.Fields.Add(&core.TextField{
		Name: "to_path",
		Max:  2048,
	})
	links.Fields.Add(&core.TextField{
		Name: "to_title",
		Max:  512,
	})
	links.Fields.Add(&core.SelectField{
		Name:   "state",
		Values: []string{"resolved", "broken"},
	})

	links.AddIndex("idx_leafwiki_links_from", false, "from_page_id", "")
	links.AddIndex("idx_leafwiki_links_to", false, "to_page_id", "")

	links.ListRule = nil
	links.ViewRule = nil
	links.CreateRule = nil
	links.UpdateRule = nil
	links.DeleteRule = nil
	return txApp.Save(links)
}
