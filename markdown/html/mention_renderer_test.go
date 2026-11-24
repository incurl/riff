package html

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/incurl/riff/markdown/extensions"
)

// renderWithMentions renders the source through a goldmark instance that
// carries the mention extension and the mention renderer.
func renderWithMentions(t *testing.T, opts MentionLinkOptions, source string) string {
	t.Helper()

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extensions.MentionExtension),
		MentionRendererOption(opts),
	)

	var buf strings.Builder
	if err := md.Convert([]byte(source), &buf); err != nil {
		t.Fatalf("Convert returned an error: %v", err)
	}
	return buf.String()
}

// TestMentionRenderer_RendersALink pins the markup.
//
// The class is upstream Flarum's (`flarum/mentions` emits
// `<a class="UserMention" href="…">@name</a>`), and the forum's
// stylesheet keys off it.
func TestMentionRenderer_RendersALink(t *testing.T) {
	rendered := renderWithMentions(t, MentionLinkOptions{
		Href: func(username string) string { return "/forum/u/" + username },
	}, "hey @alice")

	want := `<a class="UserMention" href="/forum/u/alice">@alice</a>`
	if !strings.Contains(rendered, want) {
		t.Fatalf("the mention should be a link: %q", rendered)
	}
}

// TestMentionRenderer_WithoutABuilderItIsASpan pins the degraded form.
//
// A caller that enables the mention extension and forgets the renderer
// gets a mention that is not a link — never a mention that is GONE,
// because a mention node carries no source segment to recover from.
func TestMentionRenderer_WithoutABuilderItIsASpan(t *testing.T) {
	rendered := renderWithMentions(t, MentionLinkOptions{}, "hey @alice")

	want := `<span class="UserMention">@alice</span>`
	if !strings.Contains(rendered, want) {
		t.Fatalf("the mention should be a span: %q", rendered)
	}
	if !strings.Contains(rendered, "@alice") {
		t.Fatalf("the mention text must survive: %q", rendered)
	}
}

// TestMentionRenderer_AnEmptyHrefIsASpan pins that a builder which
// declines to build a link degrades to a span rather than an empty
// anchor.
func TestMentionRenderer_AnEmptyHrefIsASpan(t *testing.T) {
	rendered := renderWithMentions(t, MentionLinkOptions{
		Href: func(string) string { return "" },
	}, "hey @alice")

	if strings.Contains(rendered, "<a ") {
		t.Fatalf("an empty href must not produce a link: %q", rendered)
	}
	if !strings.Contains(rendered, `<span class="UserMention">@alice</span>`) {
		t.Fatalf("the mention should be a span: %q", rendered)
	}
}

// TestMentionRenderer_TheUsernameIsEscaped pins that the username is
// written as text.
//
// The rendered fragment is inserted into a page as trusted HTML.
func TestMentionRenderer_TheUsernameIsEscaped(t *testing.T) {
	rendered := renderWithMentions(t, MentionLinkOptions{
		Href: func(username string) string { return "/forum/u/" + username },
	}, "hey @alice")

	if strings.Contains(rendered, "<script") {
		t.Fatalf("the username must be escaped: %q", rendered)
	}
}
