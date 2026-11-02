package leafwiki

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTree_LookupPathAndEnsurePath(t *testing.T) {
	tree := newTestTree(t)

	lookup := tree.LookupPath("guides/setup")
	assert.False(t, lookup.Exists)
	assert.True(t, lookup.CanCreate)
	assert.Equal(t, "guides/setup", lookup.Path)
	require.Len(t, lookup.Segments, 2)

	page, err := tree.EnsurePath(Actor{ID: "user-1"}, "guides/setup", "Setup")
	require.NoError(t, err)
	assert.Equal(t, "guides/setup", page.Path)
	assert.Equal(t, "Setup", page.Title)

	// Ensuring the same path twice is idempotent.
	again, err := tree.EnsurePath(Actor{ID: "user-1"}, "guides/setup", "Setup")
	require.NoError(t, err)
	assert.Equal(t, page.ID, again.ID)

	assert.True(t, tree.LookupPath("guides/setup").Exists)
}

func TestTree_SuggestSlugAvoidsCollisions(t *testing.T) {
	tree := newTestTree(t)

	first, err := tree.SuggestSlug("root", "Hello World", "")
	require.NoError(t, err)
	assert.Equal(t, "hello-world", first)

	_, err = tree.CreatePage(Actor{ID: "user-1"}, "root", "Hello World", "", NodeKindPage)
	require.NoError(t, err)

	second, err := tree.SuggestSlug("root", "Hello World", "")
	require.NoError(t, err)
	assert.Equal(t, "hello-world-1", second)
}

func TestTree_PermalinkResolvesCurrentPath(t *testing.T) {
	tree := newTestTree(t)

	page, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Deep", "", NodeKindPage)
	require.NoError(t, err)

	target, err := tree.Permalink(page.ID)
	require.NoError(t, err)
	assert.Equal(t, page.ID, target.ID)
	assert.Equal(t, "deep", target.Path)
}

func TestSlugValidation(t *testing.T) {
	assert.NoError(t, validateSlug("hello-world"))
	assert.NoError(t, validateSlug("a1"))
	assert.Error(t, validateSlug(""))
	assert.Error(t, validateSlug("api"))
	assert.Error(t, validateSlug("-leading"))
	assert.Error(t, validateSlug("trailing-"))
	assert.Error(t, validateSlug("with space"))
	assert.Equal(t, "hello-world", normaliseSlug("Hello, World!"))

	// A title with no route-safe characters yields no slug at all; the
	// caller falls back to the default instead.
	assert.Equal(t, "", normaliseSlug("🎛️"))
	assert.Equal(t, "page", uniqueChildSlug(nil, "", "🎛️"))
}

func TestTree_PageByPathIsCaseInsensitive(t *testing.T) {
	tree := newTestTree(t)

	_, err := tree.CreatePage(Actor{ID: "user-1"}, "root", "Mixed Case", "", NodeKindPage)
	require.NoError(t, err)

	page, err := tree.PageByPath("Mixed-Case")
	require.NoError(t, err)
	assert.Equal(t, "mixed-case", page.Path)

	_, err = tree.PageByPath("does/not/exist")
	assert.ErrorIs(t, err, ErrNotFound)
}
