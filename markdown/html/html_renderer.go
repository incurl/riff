// Package html renders riff markdown to HTML.
//
// It is the counterpart of markdown/renderer, which renders the AST back
// to markdown text. The HTML renderer is what a client that displays
// rendered content needs: the riff forum serves it as a post's
// `contentHtml`, which the frontend trusts and inserts as-is.
//
// Layering: this package depends only on goldmark. It must not import
// anything PocketBase-specific.
package html

import (
	"bytes"

	"github.com/yuin/goldmark"
	rendererhtml "github.com/yuin/goldmark/renderer/html"
)

// Options configures the HTML renderer.
type Options struct {
	// AllowRawHTML keeps the raw HTML blocks that the source contains.
	//
	// It is OFF by default: the rendered output is inserted into a page
	// as trusted HTML, so a raw HTML block in a user's post would
	// otherwise be an injection vector. A caller that sanitises the
	// result afterwards can turn it on.
	AllowRawHTML bool
}

// GoldmarkOptions returns the goldmark options that configure the HTML
// renderer.
//
// A caller that owns a goldmark instance (markdown.Service does) passes
// these to goldmark.New alongside its own extensions, so the instance
// renders HTML with the SAME extensions it parses with. That is what
// keeps a table or a task list present in the HTML whenever it is
// present in the AST.
func (o Options) GoldmarkOptions() []goldmark.Option {
	if !o.AllowRawHTML {
		return nil
	}
	return []goldmark.Option{
		goldmark.WithRendererOptions(rendererhtml.WithUnsafe()),
	}
}

// Renderer renders markdown to HTML with its own goldmark instance.
//
// It is the standalone form. A caller that already owns a goldmark
// instance should use Options.GoldmarkOptions and convert through that
// instance instead, so the two paths cannot diverge.
type Renderer struct {
	// md is the configured goldmark instance.
	md goldmark.Markdown
}

// New returns a renderer for the given options.
func New(opts Options) *Renderer {
	return &Renderer{
		md: goldmark.New(opts.GoldmarkOptions()...),
	}
}

// Render renders markdown source to HTML.
//
// The output is a fragment: goldmark does not wrap it in a document,
// so the caller can place it inside its own page.
func (r *Renderer) Render(source []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.md.Convert(source, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderString renders markdown source to an HTML string.
func (r *Renderer) RenderString(source []byte) (string, error) {
	rendered, err := r.Render(source)
	if err != nil {
		return "", err
	}
	return string(rendered), nil
}
