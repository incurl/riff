package markdown

import (
	"strings"
	"testing"
)

// TestRenderHTML_AMentionIsNeverDropped pins the bug this renderer
// exists to fix.
//
// A mention node carries NO source segment, so an HTML renderer that
// emits nothing for it DELETES the mention from the output. The text
// "hey @alice" used to render as "hey " — the word was gone, and
// nothing in the output said so.
//
// Every configuration that enables the mention parser must keep the
// text.
func TestRenderHTML_AMentionIsNeverDropped(t *testing.T) {
	cases := []struct {
		name    string
		service Service
	}{
		{
			name:    "the parser alone",
			service: NewService(WithMentionExtension()),
		},
		{
			name: "the parser and the link builder",
			service: NewService(WithMentionLinks(func(username string) string {
				return "/forum/u/" + username
			})),
		},
		{
			name: "a link builder that declines to build a link",
			service: NewService(WithMentionLinks(func(string) string {
				return ""
			})),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rendered, err := testCase.service.RenderHTML([]byte("hey @alice, look"))
			if err != nil {
				t.Fatalf("RenderHTML returned an error: %v", err)
			}
			if !strings.Contains(rendered, "@alice") {
				t.Fatalf("the mention must survive the rendering: %q", rendered)
			}
		})
	}
}

// TestRenderHTML_AMentionIsALink pins the link the builder supplies.
func TestRenderHTML_AMentionIsALink(t *testing.T) {
	service := NewService(WithMentionLinks(func(username string) string {
		return "/forum/u/" + username
	}))

	rendered, err := service.RenderHTML([]byte("hey @alice, look"))
	if err != nil {
		t.Fatalf("RenderHTML returned an error: %v", err)
	}

	want := `<a class="UserMention" href="/forum/u/alice">@alice</a>`
	if !strings.Contains(rendered, want) {
		t.Fatalf("the mention should be a link to the builder's URL: %q", rendered)
	}
}

// TestRenderHTML_AMentionWithoutALinkIsASpan pins the degraded form.
//
// The span keeps the text: a caller that enables the extension and
// forgets the builder gets a mention that is not a link, not a mention
// that is gone.
func TestRenderHTML_AMentionWithoutALinkIsASpan(t *testing.T) {
	service := NewService(WithMentionExtension())

	rendered, err := service.RenderHTML([]byte("hey @alice, look"))
	if err != nil {
		t.Fatalf("RenderHTML returned an error: %v", err)
	}

	want := `<span class="UserMention">@alice</span>`
	if !strings.Contains(rendered, want) {
		t.Fatalf("the mention should be a span: %q", rendered)
	}
}

// TestRenderHTML_AMentionHrefIsEscaped pins that the href is escaped.
//
// The rendered fragment is inserted into a page as trusted HTML, so a
// URL carrying a quote must not break out of the attribute.
func TestRenderHTML_AMentionHrefIsEscaped(t *testing.T) {
	service := NewService(WithMentionLinks(func(string) string {
		return `/forum/u/"onmouseover="alert(1)`
	}))

	rendered, err := service.RenderHTML([]byte("hey @alice"))
	if err != nil {
		t.Fatalf("RenderHTML returned an error: %v", err)
	}
	if strings.Contains(rendered, `"onmouseover="alert(1)"`) {
		t.Fatalf("the href must be escaped: %q", rendered)
	}
}

// TestExtractAll_AMentionsAreStillExtracted pins that the renderer
// change did not disturb the extraction.
//
// The memos layer reads the mentions out of the AST, and it must keep
// working: the renderer only adds an HTML node renderer.
func TestExtractAll_AMentionsAreStillExtracted(t *testing.T) {
	service := NewService(WithMentionExtension())

	extracted, err := service.ExtractAll([]byte("hey @alice and @bob"))
	if err != nil {
		t.Fatalf("ExtractAll returned an error: %v", err)
	}
	if len(extracted.Mentions) != 2 {
		t.Fatalf("both mentions should be extracted: %v", extracted.Mentions)
	}
	if extracted.Mentions[0] != "alice" || extracted.Mentions[1] != "bob" {
		t.Fatalf("the mentions should be extracted in order: %v", extracted.Mentions)
	}
}
