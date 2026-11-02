package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLeafWikiIndexCollectionsAreNotLeftBehind pins the outcome of the two
// LeafWiki migrations together.
//
// 1940000000 creates leafwiki_pages / leafwiki_links as a PocketBase-backed
// replacement for LeafWiki's SQLite indexes; 1950000000 drops them again,
// because pb/leafwiki now keeps that index in memory on the app's own store.
// The creating migration is left in the history rather than deleted (an
// existing instance has it recorded as applied), so a fresh install creates the
// collections and immediately drops them.
//
// What matters after a full migration run is that neither collection survives:
// a leftover table would be a dead index that nothing writes and nothing reads.
func TestLeafWikiIndexCollectionsAreNotLeftBehind(t *testing.T) {
	app := newTestApp(t)

	for _, name := range []string{"leafwiki_pages", "leafwiki_links"} {
		_, err := app.FindCollectionByNameOrId(name)
		assert.Error(t, err, "%s must not survive the migration run", name)
	}
}

// TestCreateLeafWikiTables still exercises the creating migration on its own,
// so its schema is not lost to history: the columns the old index wrote, the
// unique page id index, and the closed access rules are all still asserted by
// applying the up migration against a fresh app.
func TestCreateLeafWikiTables(t *testing.T) {
	app := newTestApp(t)

	require.NoError(t, createLeafWikiCollections(app))

	pages, err := app.FindCollectionByNameOrId("leafwiki_pages")
	require.NoError(t, err, "leafwiki_pages must be created by the migration")
	for _, field := range []string{
		"page_id", "path", "slug", "title", "kind", "content", "excerpt",
		"tags", "properties", "pinned", "position", "created_at", "updated_at",
		"version", "creator_id", "last_author_id",
	} {
		assert.NotNilf(t, pages.Fields.GetByName(field), "leafwiki_pages is missing %q", field)
	}

	// The page id is the wiki's own opaque identifier, not the record id, so
	// it needs its own unique index.
	assert.True(t, hasIndex(pages, "idx_leafwiki_pages_page_id"), "page_id must be uniquely indexed")
	assert.True(t, hasIndex(pages, "idx_leafwiki_pages_path"), "path must be indexed")

	// Nothing may reach these collections over /api/collections/*.
	assert.Nil(t, pages.ListRule)
	assert.Nil(t, pages.ViewRule)
	assert.Nil(t, pages.CreateRule)
	assert.Nil(t, pages.UpdateRule)
	assert.Nil(t, pages.DeleteRule)

	links, err := app.FindCollectionByNameOrId("leafwiki_links")
	require.NoError(t, err, "leafwiki_links must be created by the migration")
	for _, field := range []string{"from_page_id", "to_page_id", "to_path", "to_title", "state"} {
		assert.NotNilf(t, links.Fields.GetByName(field), "leafwiki_links is missing %q", field)
	}
	assert.True(t, hasIndex(links, "idx_leafwiki_links_from"), "from_page_id must be indexed")
	assert.True(t, hasIndex(links, "idx_leafwiki_links_to"), "to_page_id must be indexed")
	assert.Nil(t, links.ListRule)
	assert.Nil(t, links.ViewRule)
	assert.Nil(t, links.CreateRule)
	assert.Nil(t, links.UpdateRule)
	assert.Nil(t, links.DeleteRule)

	// The kind and state selects only accept the documented values, so a
	// typo in the index writer fails loudly instead of storing junk.
	kindField, ok := pages.Fields.GetByName("kind").(*core.SelectField)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"page", "section"}, kindField.Values)

	stateField, ok := links.Fields.GetByName("state").(*core.SelectField)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"resolved", "broken"}, stateField.Values)
}

// TestDropLeafWikiTablesIsIdempotent runs the drop twice: once against a data
// directory that has the collections and once against one that no longer does.
// A migration that fails on the second run would break every re-run and every
// fresh install.
func TestDropLeafWikiTablesIsIdempotent(t *testing.T) {
	app := newTestApp(t)

	require.NoError(t, createLeafWikiCollections(app))
	require.NoError(t, dropLeafWikiCollections(app))
	require.NoError(t, dropLeafWikiCollections(app), "a second drop must be a no-op")

	for _, name := range []string{"leafwiki_pages", "leafwiki_links"} {
		_, err := app.FindCollectionByNameOrId(name)
		assert.Error(t, err, "%s must be gone after the drop", name)
	}
}

// hasIndex reports whether a collection carries an index with the given name.
// PocketBase stores each index as its full `CREATE INDEX …` statement, so a
// container check would never match the bare name.
func hasIndex(collection *core.Collection, name string) bool {
	for _, statement := range collection.Indexes {
		if strings.Contains(statement, name) {
			return true
		}
	}
	return false
}
