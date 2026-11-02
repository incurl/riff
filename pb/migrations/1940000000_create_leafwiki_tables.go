package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createLeafWikiTables replaces the SQLite databases LeafWiki ships with
// PocketBase collections.
//
// Upstream LeafWiki keeps its Markdown pages on disk (the source of truth) and
// derives a handful of SQLite databases from them: `search.db`, `tags.db`,
// `links.db`, `properties.db`, plus the user/session/settings databases. The
// riff port (pb/leafwiki) keeps the Markdown tree, rebuilds the derived data on
// every load, and stores it here instead:
//
//	leafwiki_pages — one row per Markdown page or section. Holds the body and
//	                excerpt (search), the tag and property arrays (facets), the
//	                optimistic-lock version and the authorship block. It
//	                replaces search.db + tags.db + properties.db and the page
//	                metadata half of LeafWiki's in-memory tree index.
//
//	leafwiki_links — one row per internal link. `state` is resolved or broken;
//	                a `wikilink:Title` to_path marks a [[WikiLink]] that has no
//	                page yet. It replaces links.db.
//
// User/session/API-key/settings storage is NOT recreated: the port authenticates
// against PocketBase's own `users` / `_superusers` collections, so there is no
// second user table to keep in sync.
//
// Both collections have all access rules nil. The wiki is served by
// pb/leafwiki's handlers, which do their own authorization, and the index is a
// cache that must not be writable through PocketBase's generated REST API.
//
// The collections are safe to drop and rebuild at any time: pb/leafwiki
// reconciles them with the Markdown tree on load.
func init() {
	migrations.Register(func(txApp core.App) error {
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
	}, func(txApp core.App) error {
		for _, name := range []string{"leafwiki_links", "leafwiki_pages"} {
			collection, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}
			if err := txApp.Delete(collection); err != nil {
				return err
			}
		}
		return nil
	})
}
