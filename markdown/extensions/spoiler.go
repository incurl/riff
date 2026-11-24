package extensions

import (
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// SpoilerNode is an inline spoiler: `||hidden text||`.
//
// It is the syntax upstream Flarum's `flarum/markdown` extension renders
// (s9e Litedown's inline spoiler), and the HTML is what its stylesheet
// reveals on click: `<span class="spoiler" data-spoiler="">`.
type SpoilerNode struct {
	gast.BaseInline
}

// KindSpoiler is the node kind of a [SpoilerNode].
var KindSpoiler = gast.NewNodeKind("Spoiler")

// Kind implements gast.Node.
func (*SpoilerNode) Kind() gast.NodeKind { return KindSpoiler }

// Dump implements gast.Node.
func (n *SpoilerNode) Dump(source []byte, level int) {
	gast.DumpHelper(n, source, level, nil, nil)
}

// NewSpoiler returns an empty spoiler node.
func NewSpoiler() *SpoilerNode { return &SpoilerNode{} }

// spoilerDelimiterProcessor pairs the opening and closing `||`.
//
// It mirrors goldmark's strikethrough processor: the delimiter run is the
// same character on both sides, and the node is produced when the match
// consumes exactly the two pipes the syntax uses.
type spoilerDelimiterProcessor struct{}

// IsDelimiter reports whether b can delimit a spoiler.
func (*spoilerDelimiterProcessor) IsDelimiter(b byte) bool { return b == '|' }

// CanOpenCloser reports whether the two runs can pair.
func (*spoilerDelimiterProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char && opener.OriginalLength == 2 && closer.OriginalLength == 2
}

// OnMatch builds the node when the match consumes a full `||` pair.
func (*spoilerDelimiterProcessor) OnMatch(consumes int) gast.Node {
	if consumes != 2 {
		return nil
	}
	return NewSpoiler()
}

// defaultSpoilerDelimiterProcessor is the single processor instance.
var defaultSpoilerDelimiterProcessor = &spoilerDelimiterProcessor{}

// spoilerParser recognises the opening `||`.
type spoilerParser struct{}

// Trigger implements parser.InlineParser.
func (*spoilerParser) Trigger() []byte { return []byte{'|'} }

// Parse implements parser.InlineParser.
//
// Only a run of EXACTLY two pipes opens a spoiler. One pipe is a table
// cell boundary and three is ambiguous, so both are left to the rest of
// the parser rather than turned into a half-open delimiter that would
// swallow the following text.
func (*spoilerParser) Parse(_ gast.Node, block text.Reader, pc parser.Context) gast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()

	node := parser.ScanDelimiter(line, before, 1, defaultSpoilerDelimiterProcessor)
	if node == nil || node.OriginalLength != 2 {
		return nil
	}

	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)

	return node
}

// spoilerHTMLRenderer renders a spoiler as the span the stylesheet
// reveals.
//
// The attributes are upstream's (`flarum/markdown`'s `ispoiler` template):
// `class="spoiler"` styles it, and `data-spoiler` is the hook the click
// handler removes.
type spoilerHTMLRenderer struct{}

// RegisterFuncs implements renderer.NodeRenderer.
func (r *spoilerHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindSpoiler, r.renderSpoiler)
}

// renderSpoiler writes the opening and closing tags.
func (*spoilerHTMLRenderer) renderSpoiler(w util.BufWriter, _ []byte, _ gast.Node, entering bool) (gast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<span class="spoiler" data-spoiler="">`)
	} else {
		_, _ = w.WriteString(`</span>`)
	}
	return gast.WalkContinue, nil
}

// spoiler is the goldmark extension that adds the syntax.
type spoiler struct{}

// SpoilerExtension adds `||hidden text||` inline spoilers.
var SpoilerExtension = &spoiler{}

// Extend implements goldmark.Extender.
func (*spoiler) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(
			// 500 is the priority goldmark gives its own emphasis-family
			// inline parsers, so a spoiler competes with them on equal
			// terms rather than pre-empting them.
			util.Prioritized(&spoilerParser{}, 500),
		),
	)
	m.Renderer().AddOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&spoilerHTMLRenderer{}, 500),
		),
	)
}
