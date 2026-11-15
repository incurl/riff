package html

import (
	"strings"
	"testing"
)

// TestRenderer_RendersMarkdown pins the basic rendering.
func TestRenderer_RendersMarkdown(t *testing.T) {
	renderer := New(Options{})

	cases := []struct {
		name     string
		source   string
		expected string
	}{
		{"heading", "# Title", "<h1>Title</h1>"},
		{"paragraph", "Hello", "<p>Hello</p>"},
		{"emphasis", "a **bold** word", "<strong>bold</strong>"},
		{"italic", "an *italic* word", "<em>italic</em>"},
		{"link", "[text](https://example.com)", `<a href="https://example.com">text</a>`},
		{"code", "`code`", "<code>code</code>"},
		{"list", "- one\n- two", "<ul>"},
		{"quote", "> quoted", "<blockquote>"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rendered, err := renderer.RenderString([]byte(testCase.source))
			if err != nil {
				t.Fatalf("RenderString(%q) returned an error: %v", testCase.source, err)
			}
			if !strings.Contains(rendered, testCase.expected) {
				t.Fatalf("RenderString(%q) = %q, want it to contain %q", testCase.source, rendered, testCase.expected)
			}
		})
	}
}

// TestRenderer_RawHTMLIsEscapedByDefault pins that a raw HTML block is
// not passed through.
//
// The rendered output is inserted into a page as trusted HTML, so an
// unescaped script tag would be an injection vector.
func TestRenderer_RawHTMLIsEscapedByDefault(t *testing.T) {
	renderer := New(Options{})

	rendered, err := renderer.RenderString([]byte("<script>alert(1)</script>"))
	if err != nil {
		t.Fatalf("RenderString returned an error: %v", err)
	}
	if strings.Contains(rendered, "<script>") {
		t.Fatalf("the raw script tag must not be passed through: %q", rendered)
	}
}

// TestRenderer_RawHTMLCanBeAllowed pins the opt-in.
func TestRenderer_RawHTMLCanBeAllowed(t *testing.T) {
	renderer := New(Options{AllowRawHTML: true})

	rendered, err := renderer.RenderString([]byte("<b>bold</b>"))
	if err != nil {
		t.Fatalf("RenderString returned an error: %v", err)
	}
	if !strings.Contains(rendered, "<b>bold</b>") {
		t.Fatalf("the raw HTML should be passed through when it is allowed: %q", rendered)
	}
}

// TestRenderer_RenderReturnsAFragment pins that the output is a
// fragment, not a whole document: the caller places it inside its own
// page.
func TestRenderer_RenderReturnsAFragment(t *testing.T) {
	renderer := New(Options{})

	rendered, err := renderer.Render([]byte("Hello"))
	if err != nil {
		t.Fatalf("Render returned an error: %v", err)
	}
	if strings.Contains(string(rendered), "<html") || strings.Contains(string(rendered), "<body") {
		t.Fatalf("the output must be a fragment: %q", rendered)
	}
}
