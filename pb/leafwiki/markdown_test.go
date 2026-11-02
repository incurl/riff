package leafwiki

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMarkdown_NoFrontmatter(t *testing.T) {
	fm, body, err := ParseMarkdown("# Hello\n\nSome text.\n")
	require.NoError(t, err)
	assert.Empty(t, fm.ID)
	assert.Empty(t, fm.Title)
	assert.Equal(t, "# Hello\n\nSome text.\n", body)
}

func TestParseMarkdown_MalformedFrontmatterKeepsBody(t *testing.T) {
	raw := "---\ntags: [unclosed\n---\n# Title\n"
	fm, body, err := ParseMarkdown(raw)
	require.Error(t, err)
	assert.Empty(t, fm.ID)
	assert.Equal(t, raw, body, "a malformed block must not truncate the file")
}

func TestParseMarkdown_UnterminatedBlockIsBody(t *testing.T) {
	raw := "---\nleafwiki_id: abc\nstill going\n"
	fm, body, err := ParseMarkdown(raw)
	require.NoError(t, err)
	assert.Empty(t, fm.ID)
	assert.Equal(t, raw, body)
}

func TestBuildMarkdown_RoundTripsManagedAndExtraKeys(t *testing.T) {
	fm := Frontmatter{
		ID:           "page-1",
		Title:        "Getting Started",
		CreatedAt:    "2025-01-02T03:04:05.000000001Z",
		UpdatedAt:    "2025-01-02T03:04:06.000000001Z",
		CreatorID:    "user-1",
		LastAuthorID: "user-2",
		Pinned:       true,
		Extra: map[string]any{
			"tags":  []any{"Go", "wiki"},
			"owner": "peter",
		},
	}

	raw, err := BuildMarkdown(fm, "# Getting Started\n\nBody.\n")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(raw, "---\n"), "frontmatter must open the file")

	parsed, body, err := ParseMarkdown(raw)
	require.NoError(t, err)

	assert.Equal(t, fm.ID, parsed.ID)
	assert.Equal(t, fm.Title, parsed.Title)
	assert.Equal(t, fm.CreatedAt, parsed.CreatedAt)
	assert.Equal(t, fm.UpdatedAt, parsed.UpdatedAt)
	assert.Equal(t, fm.CreatorID, parsed.CreatorID)
	assert.Equal(t, fm.LastAuthorID, parsed.LastAuthorID)
	assert.True(t, parsed.Pinned)
	assert.Equal(t, "peter", parsed.Extra["owner"])
	assert.Equal(t, []string{"go", "wiki"}, ExtractTags(parsed))
	assert.Equal(t, "# Getting Started\n\nBody.\n", body)
}

func TestBuildMarkdown_DeterministicExtraOrder(t *testing.T) {
	fm := Frontmatter{ID: "id", Title: "T", Extra: map[string]any{"zulu": "1", "alpha": "2"}}
	first, err := BuildMarkdown(fm, "")
	require.NoError(t, err)
	second, err := BuildMarkdown(fm, "")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Less(t, strings.Index(first, "alpha"), strings.Index(first, "zulu"))
}

func TestExtractTags_NormalisesAndDeduplicates(t *testing.T) {
	fm := Frontmatter{Extra: map[string]any{"tags": []any{" Go ", "go", "", "Wiki"}}}
	assert.Equal(t, []string{"go", "wiki"}, ExtractTags(fm))
	assert.Equal(t, []string{}, ExtractTags(Frontmatter{Extra: map[string]any{}}))
}

func TestExtractProperties_SkipsSystemKeysAndFlattensNested(t *testing.T) {
	fm := Frontmatter{Extra: map[string]any{
		"tags":    []any{"go"},
		"owner":   "peter",
		"draft":   false,
		"empty":   "",
		"nested":  map[string]any{"a": "b"},
		"list":    []any{"x"},
		keyTitle:  "ignored",
		keyPinned: true,
	}}
	properties := ExtractProperties(fm)
	assert.Equal(t, map[string]string{
		"owner":    "peter",
		"draft":    "false",
		"nested.a": "b",
	}, properties)
}

func TestExcerpt_StripsMarkdownSyntax(t *testing.T) {
	body := "# Heading\n\nSome **bold** text with a [link](/guides/setup) and `code`.\n\n```\nfence\n```\n"
	excerpt := Excerpt(body)
	assert.Contains(t, excerpt, "Heading")
	assert.Contains(t, excerpt, "link")
	assert.Contains(t, excerpt, "bold")
	assert.NotContains(t, excerpt, "**")
	assert.NotContains(t, excerpt, "](")
	assert.NotContains(t, excerpt, "fence")
}

func TestExcerpt_Truncates(t *testing.T) {
	excerpt := Excerpt(strings.Repeat("a", maxExcerptLength+50))
	assert.True(t, strings.HasSuffix(excerpt, "…"))
	assert.Equal(t, maxExcerptLength+1, len([]rune(excerpt)))
}

func TestExtractLinks_InternalTargetsOnly(t *testing.T) {
	body := `[a](/guides/setup) [a again](/guides/setup#section) [b](guides/setup)
[external](https://example.com) [mail](mailto:x@example.com) [anchor](#top)
[asset](/assets/abc/pic.png) [[Wiki Link]] [[Other|Alias]]`
	links := ExtractLinks(body)

	paths := make([]string, 0, len(links))
	titles := make([]string, 0, len(links))
	for _, link := range links {
		if link.Path != "" {
			paths = append(paths, link.Path)
		}
		if link.Title != "" {
			titles = append(titles, link.Title)
		}
	}

	assert.Equal(t, []string{"guides/setup"}, paths, "duplicates, fragments and asset links are dropped")
	assert.Equal(t, []string{"Wiki Link", "Other"}, titles)
}

func TestTitleFromBody(t *testing.T) {
	assert.Equal(t, "Hello World", TitleFromBody("intro\n\n# Hello World\n"))
	assert.Empty(t, TitleFromBody("no heading here"))
}
