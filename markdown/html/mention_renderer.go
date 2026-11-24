package html

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"

	mast "github.com/incurl/riff/markdown/ast"
)

// MentionHrefBuilder returns the URL a mention links to.
//
// The caller owns the URL: the markdown package decides the MARKUP and
// the forum decides where an account lives. That is what keeps this
// package free of any PocketBase- or forum-specific knowledge — the
// layering rule the package documents.
//
// A builder that returns an empty string renders the mention as a span
// instead of a link.
type MentionHrefBuilder func(username string) string

// MentionLinkOptions configures the mention node renderer.
type MentionLinkOptions struct {
	// Href builds the URL a mention links to. It is optional: without
	// it the mention renders as a span.
	Href MentionHrefBuilder
}

// MentionRenderer renders a mention node as HTML.
//
// The markup is upstream Flarum's: the `flarum/mentions` extension emits
// `<a class="UserMention" href="…">@name</a>`, and the forum's stylesheet
// keys off that class.
//
// **THE DEFAULT MUST NOT DROP TEXT.** A mention node carries no source
// segment the renderer could recover, so a renderer that emits nothing
// DELETES the mention from the output. Before this renderer existed, a
// service with the mention extension enabled rendered "hey @alice" as
// "hey " — the word was gone. Without a builder the renderer therefore
// falls back to a span, which degrades the link but keeps the text.
type MentionRenderer struct {
	// href builds the URL a mention links to.
	href MentionHrefBuilder
}

// NewMentionRenderer returns a mention node renderer.
func NewMentionRenderer(opts MentionLinkOptions) *MentionRenderer {
	return &MentionRenderer{href: opts.Href}
}

// RegisterFuncs implements renderer.NodeRenderer.
func (r *MentionRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(mast.KindMention, r.renderMention)
}

// renderMention writes the mention.
//
// The node is an inline leaf, so the whole thing is written on the way
// in: there is no closing tag to write on the way out.
func (r *MentionRenderer) renderMention(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	mention, ok := node.(*mast.MentionNode)
	if !ok {
		return ast.WalkContinue, nil
	}

	// The username is written as text, so it is escaped: the rendered
	// fragment is inserted into a page as trusted HTML.
	username := util.EscapeHTML(mention.Username)

	if r.href == nil {
		_, _ = w.WriteString(`<span class="UserMention">@`)
		_, _ = w.Write(username)
		_, _ = w.WriteString(`</span>`)
		return ast.WalkContinue, nil
	}

	href := r.href(string(mention.Username))
	if href == "" {
		_, _ = w.WriteString(`<span class="UserMention">@`)
		_, _ = w.Write(username)
		_, _ = w.WriteString(`</span>`)
		return ast.WalkContinue, nil
	}

	_, _ = w.WriteString(`<a class="UserMention" href="`)
	_, _ = w.Write(util.EscapeHTML([]byte(href)))
	_, _ = w.WriteString(`">@`)
	_, _ = w.Write(username)
	_, _ = w.WriteString(`</a>`)

	return ast.WalkContinue, nil
}

// MentionRendererOption returns the goldmark options that register the
// mention node renderer.
//
// It is a function rather than a value so the caller can attach it to
// the SAME goldmark instance that parses the content: a second instance
// would parse with one set of extensions and render with another, and
// the HTML would not carry the AST's extensions.
func MentionRendererOption(opts MentionLinkOptions) goldmark.Option {
	return goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(NewMentionRenderer(opts), 500),
		),
	)
}
