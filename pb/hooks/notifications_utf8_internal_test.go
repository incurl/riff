package hooks

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// TestSnippetOf_CutsOnARuneBoundary pins the same bug in the hooks'
// copy of the helper.
//
// A byte cut (`content[:n]`) can land inside a multi-byte character
// and leave a lone continuation byte in the result. The identical bug
// in pb/connect/memos_translate.go made every ListMemos response a
// 500 ("marshal message: string field contains invalid UTF-8") for any
// memo whose 120th byte fell inside a Cyrillic, CJK or emoji
// character. The notification templates interpolate the snippet, so
// the same broken bytes reach an email there.
func TestSnippetOf_CutsOnARuneBoundary(t *testing.T) {
	for _, ch := range []string{"é", "東", "🎵", "한", "😀"} {
		for _, prefix := range []int{118, 119, 120} {
			content := strings.Repeat("a", prefix) + ch + strings.Repeat("b", 20)
			got := snippetOf(content, 120)
			assert.True(t, utf8.ValidString(got),
				"snippet with %q at byte %d must be valid UTF-8: %q", ch, prefix, got)
		}
	}
}

// TestSnippetOf_SanitisesInvalidInput covers a content that is already
// invalid UTF-8 in the database.
func TestSnippetOf_SanitisesInvalidInput(t *testing.T) {
	got := snippetOf("valid start \xc3 invalid tail", 120)
	assert.True(t, utf8.ValidString(got),
		"a short content is returned verbatim, so it must be sanitised: %q", got)
}
