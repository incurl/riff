package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateLeafWikiTables pins the two collections that replace LeafWiki's
// SQLite databases.
//
// pb/leafwiki keeps the Markdown pages on disk and rebuilds its index from
// them, so these collections are a cache: they must exist after a migration
// run, carry the columns the index writes, and be closed to the generated
// PocketBase REST API (the wiki handlers are the only authority).
func TestCreateLeafWikiTables(t *testing.T) {
	app := newTestApp(t)

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
