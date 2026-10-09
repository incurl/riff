package hooks

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/markdown"
)

// mentionsInContent returns the unique @username tokens found in
// the given content. The function intentionally lives in the
// hooks package (rather than the connect package) so both code
// paths can call it without a cross-package import; the underlying
// markdown service is package-internal to riff/markdown anyway.
//
// A token is captured only when the markdown AST surfaces a
// MentionNode; raw text matches like the literal "@bob" inside a
// code fence are deliberately ignored, matching the way the
// mention extension replaces them in the rendered output.
func mentionsInContent(content string) []string {
	if content == "" {
		return nil
	}
	svc := newMentionsService()
	extracted, err := svc.ExtractAll([]byte(content))
	if err != nil || extracted == nil {
		return nil
	}
	if len(extracted.Mentions) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(extracted.Mentions))
	for _, m := range extracted.Mentions {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

// newMentionsService builds a markdown service that walks both tags
// and mentions. It is cheap (one goldmark.New per call) but called
// per memo save, which is fine at riff's traffic level; if it
// becomes a hot path the service should move to a package-level
// singleton.
func newMentionsService() markdown.Service {
	return markdown.NewService(
		markdown.WithTagExtension(),
		markdown.WithMentionExtension(),
	)
}

// lookupUserByNameBestEffort resolves a username to a record,
// returning (nil, nil) when the user is missing — the @-mention
// path is best-effort and a deleted user between the post and
// the scan is the most common reason for a miss.
func lookupUserByNameBestEffort(app core.App, username string) (*core.Record, error) {
	if username == "" {
		return nil, nil
	}
	rows, err := app.FindRecordsByFilter("users", "username={:u}", "", 1, 0, map[string]any{"u": username})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// lookupUsername resolves a user record id to a username, falling
// back to the record id when the user is missing. Mirrors the
// helper in pb/connect/translate.go: kept duplicated rather than
// moved because the hooks package cannot import the connect package
// (a cycle), and the helper is six lines.
func lookupUsername(app core.App, userID string) string {
	if userID == "" {
		return ""
	}
	rec, err := app.FindRecordById("users", userID)
	if err != nil {
		return userID
	}
	if u := rec.GetString("username"); u != "" {
		return u
	}
	return userID
}

// memoParentID returns the related_memo id when this memo is a
// comment, or "" when it is a top-level memo. Mirrors
// pb/connect/memos_translate.go::memoParentID; duplicated to keep
// the hooks package free of an import cycle.
func memoParentID(app core.App, memoID string) string {
	rows, err := app.FindRecordsByFilter("memo_relations",
		"memo={:id} && type='COMMENT'", "", 1, 0, map[string]any{"id": memoID})
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].GetString("related_memo")
}

// snippetOf returns the first n bytes of content, trimmed at a
// word boundary if possible. Mirrors pb/connect/memos_translate.go.
// Ellipsis suffix is a single Unicode character so the result
// always ends in a visible marker even when the content is short.
func snippetOf(content string, n int) string {
	if n <= 0 || len(content) <= n {
		return content
	}
	cut := content[:n]
	if idx := lastSpace(cut); idx > n/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}

func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' || s[i] == '\n' || s[i] == '\t' {
			return i
		}
	}
	return -1
}
