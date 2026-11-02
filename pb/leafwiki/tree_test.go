package leafwiki

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTree returns a tree backed by a temporary directory and no index.
func newTestTree(t *testing.T) *Tree {
	t.Helper()
	tree, err := newTree(t.TempDir(), nil)
	require.NoError(t, err)
	return tree
}

// readTreeFile reads a file relative to the pages directory.
func readTreeFile(t *testing.T, tree *Tree, relative string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(tree.store.rootDir, filepath.FromSlash(relative)))
	require.NoError(t, err)
	return string(raw)
}

func TestTree_CreatePageWritesLeafWikiLayout(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Getting Started", "", NodeKindPage)
	require.NoError(t, err)

	assert.Equal(t, "getting-started", page.Slug)
	assert.Equal(t, "getting-started", page.Path)
	assert.Equal(t, NodeKindPage, page.Kind)
	assert.Equal(t, "user-1", page.Metadata.CreatorID)
	assert.NotEmpty(t, page.ID)

	raw := readTreeFile(t, tree, "getting-started.md")
	assert.Contains(t, raw, "leafwiki_id: "+page.ID)
	assert.Contains(t, raw, "leafwiki_title: Getting Started")

	// LeafWiki records the explicit child order in the parent directory.
	order := readTreeFile(t, tree, orderFilename)
	assert.Contains(t, order, page.ID)
}

func TestTree_CreateSectionAndNestedPage(t *testing.T) {
	tree := newTestTree(t)

	section, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Guides", "", NodeKindSection)
	require.NoError(t, err)
	assert.Equal(t, "guides", section.Path)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, section.ID, "Setup", "", NodeKindPage)
	require.NoError(t, err)
	assert.Equal(t, "guides/setup", page.Path)

	assert.Contains(t, readTreeFile(t, tree, "guides/index.md"), "leafwiki_title: Guides")
	assert.Contains(t, readTreeFile(t, tree, "guides/setup.md"), "leafwiki_title: Setup")

	root := tree.Root()
	require.Len(t, root.Children, 1)
	assert.Equal(t, NodeKindSection, root.Children[0].Kind)
	require.Len(t, root.Children[0].Children, 1)
	assert.Equal(t, NodeKindPage, root.Children[0].Children[0].Kind)
}

func TestTree_CreateChildUnderPageConvertsItToSection(t *testing.T) {
	tree := newTestTree(t)

	parent, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Notes", "", NodeKindPage)
	require.NoError(t, err)
	require.True(t, fileExists(filepath.Join(tree.store.rootDir, "notes.md")))

	child, err := tree.CreatePage(Actor{ID: "user-1"}, parent.ID, "First", "", NodeKindPage)
	require.NoError(t, err)
	assert.Equal(t, "notes/first", child.Path)

	assert.False(t, fileExists(filepath.Join(tree.store.rootDir, "notes.md")), "the page file becomes a section index")
	assert.True(t, fileExists(filepath.Join(tree.store.rootDir, "notes", "index.md")))
	assert.True(t, fileExists(filepath.Join(tree.store.rootDir, "notes", "first.md")))
}

func TestTree_UpdatePage_EnforcesVersionAndSavesContent(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Draft", "", NodeKindPage)
	require.NoError(t, err)

	content := "# Draft\n\nHello.\n"
	stale := "2000-01-01T00:00:00.000000001Z"
	_, err = tree.UpdatePage(Actor{ID: "user-2"}, page.ID, stale, "Draft", "", &content, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVersionConflict)

	updated, err := tree.UpdatePage(Actor{ID: "user-2"}, page.ID, page.Version, "Draft", "", &content, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, content, updated.Content)
	assert.Equal(t, "user-2", updated.Metadata.LastAuthorID)
	assert.NotEqual(t, page.Version, updated.Version)
}

func TestTree_UpdatePage_WritesTagsAndProperties(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Tagged", "", NodeKindPage)
	require.NoError(t, err)

	tags := []string{"Go", "wiki"}
	properties := map[string]string{"owner": "peter"}
	updated, err := tree.UpdatePage(Actor{ID: "user-1"}, page.ID, page.Version, "Tagged", "", nil, &tags, &properties)
	require.NoError(t, err)

	assert.Equal(t, []string{"go", "wiki"}, updated.Tags)
	assert.Equal(t, map[string]string{"owner": "peter"}, updated.Properties)
	assert.Contains(t, readTreeFile(t, tree, "tagged.md"), "owner: peter")

	// Clearing the properties must remove the key, not leave it behind.
	empty := map[string]string{}
	cleared, err := tree.UpdatePage(Actor{ID: "user-1"}, page.ID, updated.Version, "Tagged", "", nil, nil, &empty)
	require.NoError(t, err)
	assert.Empty(t, cleared.Properties)
}

func TestTree_UpdatePage_RenamesSlug(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Old Name", "", NodeKindPage)
	require.NoError(t, err)

	updated, err := tree.UpdatePage(Actor{ID: "user-1"}, page.ID, page.Version, "New Name", "new-name", nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "new-name", updated.Path)
	assert.False(t, fileExists(filepath.Join(tree.store.rootDir, "old-name.md")))
	assert.True(t, fileExists(filepath.Join(tree.store.rootDir, "new-name.md")))
}
