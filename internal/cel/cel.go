package cel

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/errors"
)

// FieldKind enumerates the kind of filter fragment a field
// expression produces. The caller maps every name the SPA sends onto
// one of these kinds so the parser never has to guess what a name
// means.
type FieldKind int

const (
	// FieldColumn maps the field to a PocketBase column name; the
	// compiled fragment compares that column against the rhs.
	FieldColumn FieldKind = iota
	// FieldJSONList maps the field to a column holding a JSON array
	// of strings (riff's `tags`). `?=` does not resolve against such
	// a column — only against relations — so membership compiles to
	// the anchored LIKE disjunction in jsonListMember, and `in [...]`
	// means "the array contains one of these".
	FieldJSONList
	// FieldBoolExpr maps the field to a complete predicate the caller
	// renders itself (e.g. `has_link` is
	// `payload.property.has_link = true`). The expression is already
	// boolean; comparing it against a value is an error.
	FieldBoolExpr
)

// Field describes one name the parser is allowed to encounter. Every
// field the SPA sends must appear in the schema passed to Compile; an
// unknown name is an error rather than a silently dropped filter.
type Field struct {
	// Name is the CEL name the parser accepts (e.g. "space",
	// "creator", "has_link").
	Name string
	// Kind selects how the field is rendered.
	Kind FieldKind
	// Column is the PocketBase column the value is rendered against
	// (Kind is FieldColumn or FieldJSONList). Ignored when Kind ==
	// FieldBoolExpr.
	Column string
	// FallbackColumn, when non-empty, is a second column consulted
	// when Column carries no value. riff's `attachments.creator` is
	// only stamped on rows uploaded before their memo existed, so a
	// bound row's creator is its memo's author; a comparison against
	// this field compiles to
	// `(FallbackColumn = x) || ((Column = x) && (FallbackWhen))`.
	FallbackColumn string
	// FallbackWhen is the predicate that says FallbackColumn applies,
	// e.g. `memo = null` for an attachment with no linked memo. Only
	// meaningful together with FallbackColumn.
	FallbackWhen string
	// Expr is the complete predicate emitted for this name (Kind ==
	// FieldBoolExpr). The caller is responsible for keeping it
	// injection-safe; it never contains user input.
	Expr string
	// NegatedExpr is the logical negation of Expr (Kind ==
	// FieldBoolExpr). A derived predicate carries no operator to
	// flip, so `!has_location` can only compile when the caller
	// supplies its complement. Empty means the field cannot be
	// negated, which is reported as an error rather than
	// approximated.
	NegatedExpr string
	// Const, when non-empty, is the closed set of string values the
	// field accepts. Compile validates the right-hand side against
	// it so `visibility == "PUBLIK"` is a 400 rather than a query
	// that silently matches nothing.
	Const []string
	// Boolean marks a FieldColumn holding a boolean. A bare reference
	// to such a field is a complete predicate and compiles to
	// `column = true`; the SPA's pinned chip emits `pinned` with no
	// comparison (web/src/hooks/useMemoFilters.ts), while the V0
	// shaper and the proto docs spell the same thing `pinned == true`.
	// Both forms have to compile. Ignored for FieldBoolExpr, which is
	// already a predicate.
	Boolean bool
}

// Options bundles the call-time inputs Compile needs.
type Options struct {
	// Now pins the value of `now`. Pinning it keeps the compiled
	// fragment and its parameters consistent within one request and
	// makes the whole package testable without a clock.
	Now time.Time
	// Fields enumerates every name the caller is willing to see.
	Fields []Field
}

// Expression is the compiled output: a PocketBase filter fragment
// plus the parameters it binds.
type Expression struct {
	// Fragment is the PocketBase filter, e.g.
	// `space = {:_a0} && creator = {:_a1}`.
	Fragment string
	// Argv is the bound parameters, keyed by the placeholder names
	// used in Fragment.
	Argv map[string]any
}

// Compile parses expr against the field schema and returns the
// PocketBase filter it compiles to. An empty expression is an error
// so the caller cannot confuse "no filter" with "empty filter".
func Compile(expr string, opts Options) (*Expression, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, errors.New("empty expression")
	}
	if len(expr) > maxFilterLength {
		return nil, errors.Errorf("filter is %d bytes, over the %d byte limit", len(expr), maxFilterLength)
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	p := &parser{
		input:  expr,
		fields: indexFields(opts.Fields),
		now:    opts.Now,
	}
	p.prime()
	out, err := p.parseOr()
	if err != nil {
		return nil, errors.Wrapf(err, "compile %q", expr)
	}
	if p.peek().kind != tkEOF {
		return nil, errors.Errorf("unexpected %q at position %d", p.peek().lit, p.peek().off)
	}
	// A complete expression has to be a predicate. A bare column
	// reference (`space`) is almost always a half-typed filter; it
	// would compile to a truthy test on a non-empty string and
	// silently return the wrong rows.
	if out.kind != nodePredicate {
		return nil, errors.Errorf("expression does not produce a boolean; compare it with == or !=")
	}
	return &Expression{Fragment: out.fragment, Argv: out.args}, nil
}

func indexFields(fs []Field) map[string]Field {
	out := make(map[string]Field, len(fs))
	for _, f := range fs {
		out[f.Name] = f
	}
	return out
}

type tokenKind int

const (
	tkEOF tokenKind = iota
	tkIdent
	tkString
	tkInt
	tkFloat
	tkPunct
	tkKeyword
)

type token struct {
	kind tokenKind
	lit  string
	off  int
	// end is the byte offset just past the token in the source.
	// It is tracked explicitly because lit is the *decoded*
	// value: a string literal's decoded length differs from
	// the number of bytes it occupied (quotes, escapes), and
	// advance() must skip source bytes, not decoded ones.
	end int
}

// parser holds exactly one piece of mutable cursor state: `tok`.
// `prime` seeds it and `advance` moves it. Nothing else in the file
// touches the byte offset directly — an earlier revision mixed a
// `pos` field with `tok.off` and the two desynchronised the moment a
// method call (`content.contains(…)`) consumed a dot.
type parser struct {
	input  string
	tok    token
	fields map[string]Field
	now    time.Time
	// seq names the bound parameters. Every literal in one
	// expression gets its own name so two equal values in an
	// `AND` do not collide in the single Argv map.
	seq int
	// depth counts nested grouping and negation. The recursive descent
	// has no other recursion, so this is the only place a hostile input
	// could exhaust the goroutine stack; see maxParseDepth.
	depth int
}

func (p *parser) peek() token { return p.tok }

func (p *parser) prime() { p.tok = lex(p.input, 0) }

func (p *parser) advance() {
	n := p.tok.end
	if n > len(p.input) {
		n = len(p.input)
	}
	p.tok = lex(p.input, n)
}

// nextArg allocates a fresh parameter name and returns it. The
// caller stores the value under it.
func (p *parser) nextArg() string {
	name := "_a" + strconv.Itoa(p.seq)
	p.seq++
	return name
}

// lex returns the token starting at off. The scanner is deliberately
// context-free: a `.` is punctuation, and a float is recognised by
// the parser seeing `<digits> . <digits>` rather than by the lexer
// peeking ahead. That keeps the dot unambiguous for method calls.
func lex(input string, off int) token {
	for off < len(input) {
		switch c := input[off]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			off++
			continue
		case isIdentStart(c):
			end := off
			for end < len(input) && isIdentPart(input[end]) {
				end++
			}
			lit := input[off:end]
			k := tkIdent
			if lit == "true" || lit == "false" || lit == "null" {
				k = tkKeyword
			}
			return token{kind: k, lit: lit, off: off, end: end}
		case isDigit(c):
			end := off
			for end < len(input) && isDigit(input[end]) {
				end++
			}
			k := tkInt
			// Only treat the dot as a decimal point when a digit
			// follows it; `1.` alone stays an int.
			if end < len(input) && input[end] == '.' && end+1 < len(input) && isDigit(input[end+1]) {
				end++
				for end < len(input) && isDigit(input[end]) {
					end++
				}
				k = tkFloat
			}
			return token{kind: k, lit: input[off:end], off: off, end: end}
		case c == '"' || c == '\'':
			quote := c
			end := off + 1
			var sb strings.Builder
			for end < len(input) {
				d := input[end]
				if d == '\\' && end+1 < len(input) {
					sb.WriteByte(input[end+1])
					end += 2
					continue
				}
				if d == quote {
					return token{kind: tkString, lit: sb.String(), off: off, end: end + 1}
				}
				sb.WriteByte(d)
				end++
			}
			// Unterminated string: report it at the opening quote
			// rather than silently treating the rest as an ident.
			return token{kind: tkEOF, lit: "unterminated string literal", off: off, end: len(input)}
		default:
			for _, op := range []string{"==", "!=", ">=", "<=", "&&", "||"} {
				if strings.HasPrefix(input[off:], op) {
					return token{kind: tkPunct, lit: op, off: off, end: off + len(op)}
				}
			}
			if strings.IndexByte("!><()[],.+-", c) >= 0 {
				return token{kind: tkPunct, lit: string(c), off: off, end: off + 1}
			}
			return token{kind: tkEOF, lit: string(c), off: off, end: off + 1}
		}
	}
	return token{kind: tkEOF, lit: "", off: off, end: off}
}

func isIdentStart(c byte) bool { return c == '_' || unicode.IsLetter(rune(c)) }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
