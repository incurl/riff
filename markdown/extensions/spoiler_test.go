package extensions_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/markdown"
)

// The inline spoiler is upstream Flarum's `flarum/markdown` syntax
// (`||hidden||`, s9e Litedown's `ispoiler`). It renders to the span the
// stylesheet reveals on click, and it must not disturb the constructs that
// also use the pipe character — a table is the one that would break
// silently, by losing its cells rather than by erroring.
func TestSpoiler_RendersTheSpanTheStylesheetReveals(t *testing.T) {
	service := markdown.NewService()

	rendered, err := service.RenderHTML([]byte("before ||the answer is 42|| after"))
	require.NoError(t, err)

	require.Contains(t, rendered, `<span class="spoiler" data-spoiler="">the answer is 42</span>`)
	require.Contains(t, rendered, "before")
	require.Contains(t, rendered, "after")
}

// TestSpoiler_LeavesTablesAlone is the regression pin for the delimiter
// choice: `|` is the table separator, so a spoiler parser that claimed a
// single pipe would eat the table.
func TestSpoiler_LeavesTablesAlone(t *testing.T) {
	service := markdown.NewService()

	source := "| a | b |\n| - | - |\n| 1 | 2 |\n"

	rendered, err := service.RenderHTML([]byte(source))
	require.NoError(t, err)

	require.Contains(t, rendered, "<table>")
	require.Contains(t, rendered, "<th>a</th>")
	require.Contains(t, rendered, "<td>2</td>")
	require.NotContains(t, rendered, "spoiler", "a table must not be turned into a spoiler")
}

// TestSpoiler_ALoneDelimiterIsLiteral pins the other half: a single pipe,
// and an unclosed pair, stay as text rather than opening a span that would
// silently swallow the rest of the post.
func TestSpoiler_ALoneDelimiterIsLiteral(t *testing.T) {
	service := markdown.NewService()

	for _, source := range []string{"a || b", "||never closed", "| a |"} {
		rendered, err := service.RenderHTML([]byte(source))
		require.NoError(t, err)
		require.NotContains(t, rendered, "spoiler", "%q must not open a spoiler", source)
	}
}

// TestSpoiler_ParsesInlineMarkdownInside pins that the content is parsed,
// not copied: a bold run inside a spoiler is bold, which is what makes the
// syntax useful for hint text rather than only for plain words.
func TestSpoiler_ParsesInlineMarkdownInside(t *testing.T) {
	service := markdown.NewService()

	rendered, err := service.RenderHTML([]byte("||**big** reveal||"))
	require.NoError(t, err)

	require.True(t, strings.Contains(rendered, `<span class="spoiler" data-spoiler=""><strong>big</strong> reveal</span>`), rendered)
}
