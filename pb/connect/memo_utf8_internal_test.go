package connect

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestMemoContent_UTF8SurvivesTheResponse is the regression guard for a
// production 500.
//
// The error was "marshal message: string field contains invalid UTF-8":
// protojson refuses to marshal a string field that is not valid UTF-8,
// so one bad string turned the whole ListMemos response into a 500 for
// every reader.
//
// There were two independent sources.
//
// The first was snippetOf, which truncated the content with a *byte*
// cut (`content[:n]`). When the cut landed inside a multi-byte
// character it left a lone continuation byte in the snippet. That is
// not invalid input — it is ordinary CJK, Cyrillic or emoji content
// whose 120th byte happens to fall mid-character — and it made the
// response unmarshalable.
//
// The second was the stored content itself: PocketBase does not
// validate text fields on save, and the ZIP import reads raw archive
// bytes with no UTF-8 check, so a row can hold invalid bytes. The
// read path now sanitises those rather than letting one bad row
// poison the feed for everyone.
func TestMemoContent_UTF8SurvivesTheResponse(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	// Every multi-byte character, positioned so the 120-byte cut lands
	// inside it.
	for _, ch := range []string{"é", "東", "🎵"} {
		content := strings.Repeat("a", 119) + ch + strings.Repeat("b", 20)
		newFilterMemo(t, app, alice.Id, content, nil)
	}

	// And a row whose stored bytes are genuinely invalid.
	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	bad := core.NewRecord(memos)
	bad.Set("author", alice.Id)
	bad.Set("content", "valid start \xc3 invalid tail")
	bad.Set("visibility", "PUBLIC")
	require.NoError(t, app.Save(bad), "PocketBase does not validate text fields")

	srv := NewMemoServer(app)
	srv.SetFTSSearch(true)
	res, err := srv.ListMemos(t.Context(), &v1.ListMemosRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, res.GetMemos())

	for _, m := range res.GetMemos() {
		assert.True(t, utf8.ValidString(m.GetContent()),
			"content must be valid UTF-8: %q", m.GetContent())
		assert.True(t, utf8.ValidString(m.GetSnippet()),
			"snippet must be valid UTF-8: %q", m.GetSnippet())

		// The exact operation that failed in production.
		_, merr := protojson.Marshal(m)
		require.NoError(t, merr, "the memo must be marshalable")
	}
}

// TestSnippetOf_CutsOnARuneBoundary pins the truncation itself, which
// is the primary bug.
func TestSnippetOf_CutsOnARuneBoundary(t *testing.T) {
	for _, ch := range []string{"é", "東", "🎵", "한", "😀"} {
		for _, prefix := range []int{118, 119, 120} {
			content := strings.Repeat("a", prefix) + ch + strings.Repeat("b", 20)
			got := snippetOf(content, 120)
			assert.True(t, utf8.ValidString(got),
				"snippet of content with %q at byte %d must be valid UTF-8: %q",
				ch, prefix, got)
		}
	}
}
