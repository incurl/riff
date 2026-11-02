package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// dropLeafWikiTables removes the leafwiki_pages / leafwiki_links collections.
//
// They were introduced by 1940000000_create_leafwiki_tables as a
// PocketBase-backed replacement for LeafWiki's SQLite indexes. pb/leafwiki now
// keeps that index in memory on the app's own store instead, because the
// collections were never actually read across a restart: the Markdown tree is
// the source of truth and the index is rebuilt from it on every load.
//
// Keeping them cost real time on the hot path. Every page mutation went
// through a full reconcile that dropped and re-inserted every link row inside
// a transaction, and the search path read the whole collection back and
// filtered in Go — the SQLite query engine was never used. The in-memory index
// makes the same reconcile a map update.
//
// The creating migration is deliberately left in place rather than deleted: on
// an existing instance it is already recorded as applied, and removing the file
// would look like a missing migration to PocketBase's runner. A fresh install
// therefore creates the two collections and immediately drops them, which costs
// one no-op migration on a brand-new data directory and keeps the history
// honest. Both directions are idempotent: the drop skips a collection that is
// already gone, so re-running on any state is safe.
func init() {
	migrations.Register(dropLeafWikiCollections, func(_ core.App) error {
		// Irreversible by design: the rows were derived from the Markdown
		// tree and can be rebuilt at any time. Recreating the empty tables
		// on rollback would leave an instance in the state this migration
		// exists to fix, so the down migration is a no-op.
		return nil
	})
}

// dropLeafWikiCollections deletes the two index collections if they exist.
func dropLeafWikiCollections(txApp core.App) error {
	// Links first: it has no relation to pages, but keeping the order stable
	// makes the migration easy to reason about.
	for _, name := range []string{"leafwiki_links", "leafwiki_pages"} {
		collection, err := txApp.FindCollectionByNameOrId(name)
		if err != nil {
			// Already gone — this is the fresh-install and
			// re-run path, not an error.
			continue
		}
		if err := txApp.Delete(collection); err != nil {
			return err
		}
	}
	return nil
}
