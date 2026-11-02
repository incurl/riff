package leafwiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTree_MovePageRehomesTheFile(t *testing.T) {
	tree := newTestTree(t)

	section, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Archive", "", NodeKindSection)
	require.NoError(t, err)
	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Loose", "", NodeKindPage)
	require.NoError(t, err)

	moved, err := tree.MovePage(Actor{ID: "user-1"}, page.ID, page.Version, section.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, "archive/loose", moved.Path)

	assert.False(t, fileExists(filepath.Join(tree.store.rootDir, "loose.md")))
	assert.True(t, fileExists(filepath.Join(tree.store.rootDir, "archive", "loose.md")))
}

func TestTree_MovePageRefusesItsOwnSubtree(t *testing.T) {
	tree := newTestTree(t)

	section, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Section", "", NodeKindSection)
	require.NoError(t, err)
	child, err := tree.CreatePage(Actor{ID: "user-1"}, section.ID, "Child", "", NodeKindSection)
	require.NoError(t, err)

	_, err = tree.MovePage(Actor{ID: "user-1"}, section.ID, section.Version, child.ID, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArgument)
}

func TestTree_SortChildrenPersistsOrder(t *testing.T) {
	tree := newTestTree(t)

	first, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "First", "", NodeKindPage)
	require.NoError(t, err)
	second, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Second", "", NodeKindPage)
	require.NoError(t, err)

	require.NoError(t, tree.SortChildren(Actor{ID: "user-1"}, "root", []string{second.ID, first.ID}))

	root := tree.Root()
	require.Len(t, root.Children, 2)
	assert.Equal(t, "second", root.Children[0].Slug)
	assert.Equal(t, "first", root.Children[1].Slug)

	// The order survives a reload: it is on disk, not in memory.
	require.NoError(t, tree.Load())
	root = tree.Root()
	assert.Equal(t, "second", root.Children[0].Slug)
}

func TestTree_DeletePageRequiresRecursiveForSubtrees(t *testing.T) {
	tree := newTestTree(t)

	section, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Zone", "", NodeKindSection)
	require.NoError(t, err)
	_, err = tree.CreatePage(Actor{ID: "user-1"}, section.ID, "Child", "", NodeKindPage)
	require.NoError(t, err)

	err = tree.DeletePage(Actor{ID: "user-1"}, section.ID, false, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArgument)

	require.NoError(t, tree.DeletePage(Actor{ID: "user-1"}, section.ID, true, ""))
	root := tree.Root()
	assert.Empty(t, root.Children)
	assert.False(t, dirExists(filepath.Join(tree.store.rootDir, "zone")))
}

func TestTree_ConvertPage(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Convertible", "", NodeKindPage)
	require.NoError(t, err)

	converted, err := tree.ConvertPage(Actor{ID: "user-1"}, page.ID, page.Version, NodeKindSection)
	require.NoError(t, err)
	assert.Equal(t, NodeKindSection, converted.Kind)
	assert.True(t, fileExists(filepath.Join(tree.store.rootDir, "convertible", "index.md")))

	// A section with children refuses to become a page.
	_, err = tree.CreatePage(Actor{ID: "user-1"}, converted.ID, "Child", "", NodeKindPage)
	require.NoError(t, err)
	current, err := tree.Page(converted.ID)
	require.NoError(t, err)
	_, err = tree.ConvertPage(Actor{ID: "user-1"}, current.ID, current.Version, NodeKindPage)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArgument)
}

func TestTree_CopyPageDuplicatesContentAndChildren(t *testing.T) {
	tree := newTestTree(t)

	section, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Playbook", "", NodeKindSection)
	require.NoError(t, err)
	child, err := tree.CreatePage(Actor{ID: "user-1"}, section.ID, "Step", "", NodeKindPage)
	require.NoError(t, err)

	content := "hello\n"
	_, err = tree.UpdatePage(Actor{ID: "user-1"}, child.ID, child.Version, "Step", "", &content, nil, nil)
	require.NoError(t, err)

	copied, err := tree.CopyPage(Actor{ID: "user-1"}, section.ID, "root", "Playbook Copy", "")
	require.NoError(t, err)
	assert.Equal(t, "playbook-copy", copied.Path)
	assert.NotEqual(t, section.ID, copied.ID)

	root := tree.Root()
	require.Len(t, root.Children, 2)
	copiedNode := root.Children[1]
	require.Len(t, copiedNode.Children, 1)
	assert.Equal(t, "step", copiedNode.Children[0].Slug)

	copiedChild, err := tree.Page(copiedNode.Children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", copiedChild.Content)
}

func TestTree_ReloadKeepsIdentifiersStable(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Stable", "", NodeKindPage)
	require.NoError(t, err)

	require.NoError(t, tree.Load())
	reloaded, err := tree.Page(page.ID)
	require.NoError(t, err)
	assert.Equal(t, page.ID, reloaded.ID)
	assert.Equal(t, page.Path, reloaded.Path)
}

func TestTree_AdoptsFilesWrittenByHand(t *testing.T) {
	tree := newTestTree(t)

	// A Markdown file dropped into the directory by an editor (no
	// frontmatter) becomes a page with a stable, generated id.
	path := filepath.Join(tree.store.rootDir, "hand-written.md")
	require.NoError(t, os.WriteFile(path, []byte("# Hand Written\n\nbody\n"), 0o640))

	require.NoError(t, tree.Load())
	page, err := tree.PageByPath("hand-written")
	require.NoError(t, err)
	assert.Equal(t, "Hand Written", page.Title)
	assert.NotEmpty(t, page.ID)

	// The id is persisted, so the next load adopts the same one.
	assert.Contains(t, readTreeFile(t, tree, "hand-written.md"), "leafwiki_id: "+page.ID)
}

func TestRewriteLinkDestinations(t *testing.T) {
	content := "[a](/guides/setup) [b](guides/setup#x) [c](/guides/setup-old)"
	rewritten := rewriteLinkDestinations(content, "guides/setup", "guides/install")
	assert.Equal(t, "[a](/guides/install) [b](guides/install#x) [c](/guides/setup-old)", rewritten)
	assert.True(t, strings.Contains(rewritten, "setup-old"), "a sibling with a shared prefix is untouched")
}
