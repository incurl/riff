package leafwiki

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The index used to be reconciled by reading every page file and rewriting
// every link row in the PocketBase collections on each mutation. It now lives
// in memory and is synced from body-free node projections, so a page is only
// reopened when it actually changed. These tests pin that property directly by
// counting loader calls — a regression to "read everything" fails here even
// though the search results would still be correct.

func newIndexForTest(t *testing.T) *index {
	t.Helper()
	// newTestAPI builds a real app with riff's migrations, which is what the
	// index needs (a bootstrapped app.Store()); it also gives each test its
	// own app, which is what the scoping test relies on.
	_, app, _ := newTestAPI(t)
	return newIndex(app)
}

// countingLoader returns a loader that records which page ids it was asked for.
func countingLoader(pages map[string]string) (func(string) (indexedPage, bool), *[]string) {
	var loaded []string
	return func(pageID string) (indexedPage, bool) {
		loaded = append(loaded, pageID)
		content, ok := pages[pageID]
		if !ok {
			return indexedPage{}, false
		}
		return indexedPage{PageID: pageID, Content: content}, true
	}, &loaded
}

func TestIndexSyncLoadsOnlyChangedPages(t *testing.T) {
	idx := newIndexForTest(t)
	pages := map[string]string{"a": "alpha", "b": "beta"}
	load, loaded := countingLoader(pages)

	refs := []nodeRef{
		{ID: "a", Path: "a", Slug: "a", Title: "A", Kind: NodeKindPage, Version: "v1"},
		{ID: "b", Path: "b", Slug: "b", Title: "B", Kind: NodeKindPage, Version: "v1"},
	}

	idx.syncTree(refs, load)
	assert.Equal(t, []string{"a", "b"}, *loaded, "a cold index must load every page")

	// Nothing changed: no page may be reopened.
	*loaded = nil
	idx.syncTree(refs, load)
	assert.Empty(t, *loaded, "an unchanged wiki must not re-read a single page")

	// One page changed: exactly that page is reopened.
	*loaded = nil
	refs[0].Version = "v2"
	idx.syncTree(refs, load)
	assert.Equal(t, []string{"a"}, *loaded, "only the edited page may be re-read")

	entry, ok := idx.pageByID("a")
	require.True(t, ok)
	assert.Equal(t, "v2", entry.Version)
	assert.Equal(t, "alpha", entry.Content)
}

func TestIndexSyncDropsVanishedPages(t *testing.T) {
	idx := newIndexForTest(t)
	pages := map[string]string{"a": "alpha", "b": "beta"}
	load, _ := countingLoader(pages)

	refs := []nodeRef{
		{ID: "a", Path: "a", Title: "A", Version: "v1"},
		{ID: "b", Path: "b", Title: "B", Version: "v1"},
	}
	idx.syncTree(refs, load)
	assert.Len(t, idx.allPages(), 2)

	idx.syncTree(refs[:1], load)
	remaining := idx.allPages()
	require.Len(t, remaining, 1)
	assert.Equal(t, "a", remaining[0].PageID)

	_, ok := idx.pageByID("b")
	assert.False(t, ok, "a deleted page must leave the index")
}

// TestIndexReResolvesLinksWithoutReloading is the correctness half of the
// incremental design: creating a page heals links that point at it, and that
// re-resolution must not require re-reading the linking pages.
func TestIndexReResolvesLinksWithoutReloading(t *testing.T) {
	idx := newIndexForTest(t)
	pages := map[string]string{
		// "source" links to the path "target", which does not exist yet.
		"source": "[t](/target)",
	}
	load, loaded := countingLoader(pages)

	idx.syncTree([]nodeRef{
		{ID: "source", Path: "source", Title: "Source", Version: "v1"},
	}, load)

	broken := idx.brokenLinks()
	require.Len(t, broken, 1, "the link must start out broken")
	assert.Equal(t, "target", broken[0].ToPath)

	// Adding the target heals the link. Only the new page is loaded.
	*loaded = nil
	pages["target"] = "target body"
	idx.syncTree([]nodeRef{
		{ID: "source", Path: "source", Title: "Source", Version: "v1"},
		{ID: "target", Path: "target", Title: "Target", Version: "v1"},
	}, load)

	assert.Equal(t, []string{"target"}, *loaded, "creating a page must not re-read the pages linking to it")
	assert.Empty(t, idx.brokenLinks(), "the link must resolve once its target exists")

	incoming := idx.linksTo("target")
	require.Len(t, incoming, 1)
	assert.Equal(t, "source", incoming[0].FromPageID)
	assert.Equal(t, linkStateResolved, incoming[0].State)
}

// TestIndexResolvesWikiLinksByTitle covers the [[WikiLink]] branch, which
// resolves against page titles rather than paths.
func TestIndexResolvesWikiLinksByTitle(t *testing.T) {
	idx := newIndexForTest(t)
	pages := map[string]string{
		"source": "see [[Target Page]]",
		"target": "body",
	}
	load, _ := countingLoader(pages)

	idx.syncTree([]nodeRef{
		{ID: "source", Path: "source", Title: "Source", Version: "v1"},
		{ID: "target", Path: "target", Title: "Target Page", Version: "v1"},
	}, load)

	rows := idx.linksFrom("source")
	require.Len(t, rows, 1)
	assert.Equal(t, "target", rows[0].ToPageID)
	assert.Equal(t, linkStateResolved, rows[0].State)
	assert.Equal(t, "Target Page", rows[0].ToTitle)
}

// TestIndexIsScopedToTheApp pins the app.Store() scoping: two apps in one
// process (the test suite boots many) must not share an index.
func TestIndexIsScopedToTheApp(t *testing.T) {
	first := newIndexForTest(t)
	second := newIndexForTest(t)

	pages := map[string]string{"a": "alpha", "b": "beta"}
	load, _ := countingLoader(pages)
	first.syncTree([]nodeRef{{ID: "a", Path: "a", Title: "A", Version: "v1"}}, load)

	assert.Len(t, first.allPages(), 1)
	assert.Empty(t, second.allPages(), "a second app must not see the first app's index")
}

// TestIndexSurvivesFullReload checks the rebuild path: a fresh Load() must
// repopulate an index that was never written to disk.
func TestIndexSurvivesFullReload(t *testing.T) {
	server, _, handler := newTestAPI(t)
	_ = handler

	tree := server.Tree()
	require.NotNil(t, tree)

	actor := Actor{ID: testUser(t, server.app, "indexer").Id}
	page, err := tree.CreatePage(actor, rootID, "Reloaded", "reloaded", NodeKindPage)
	require.NoError(t, err)

	require.NoError(t, tree.Load())

	entry, ok := tree.index.pageByID(page.ID)
	require.True(t, ok, "the index must be rebuilt from the tree on a full load")
	assert.Equal(t, "reloaded", entry.Path)
	assert.Equal(t, "Reloaded", entry.Title)
}
