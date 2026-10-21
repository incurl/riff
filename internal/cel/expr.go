package cel

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// expr is the parser's intermediate representation: a rendered
// PocketBase fragment plus the parameters it binds. It is private;
// Compile is the only way a caller sees a compiled expression.
type expr struct {
	fragment string
	args     map[string]any
	// kind records what the node *is*, so the surrounding grammar
	// can tell a predicate from a bare value and `negate` can find
	// the operator to flip.
	kind nodeKind
	// op is the node operation: opAnd / opOr for a join,
	// opCompare for a comparison, opAtom for anything else.
	op nodeOp
	// join is "&&" or "||" for a join node.
	join string
	// parts holds the operands of a join node, kept individually so
	// negate can push a "!" down through them.
	parts []*expr
	// cmpOp is the comparison operator of an opCompare node: one
	// of = != < <= > >= ~ !~ ?= ?!=.
	cmpOp string
	// val is the Go value a literal node stands for, when it has
	// one (a string, int64, bool, time.Time or time.Duration). The
	// compiler folds `now - duration("24h")` on it; the rendered
	// fragment is still built from args, so folding never leaks a
	// value into the filter string.
	val any
	// notFragment is the pre-rendered negation of a derived (FieldBoolExpr)
	// predicate. It is empty for every other node, and negate falls
	// back to an error when neither an operator nor this is available.
	notFragment string
}

type nodeKind int

const (
	// nodePredicate is a fragment that is already a complete
	// boolean expression.
	nodePredicate nodeKind = iota
	// nodeValue is a column reference or a literal, awaiting an
	// operator.
	nodeValue
)

type nodeOp int

const (
	// opAtom is a leaf: a column, a literal or a derived predicate.
	opAtom nodeOp = iota
	opAnd
	opOr
	opCompare
	// opJSONList is a disjunction of LIKE patterns that tests
	// membership in a JSON-array column (jsonListMember). It is an
	// OR group, but its parts are not comparison nodes, so negate
	// refuses it rather than emitting an empty fragment.
	opJSONList
	// opFallback is a comparison against a field whose value may live
	// on a related column (Field.FallbackColumn). It renders as a
	// disjunction, so negate refuses it too.
	opFallback
)

func mergeExprs(a, b *expr) map[string]any {
	out := make(map[string]any, len(a.args)+len(b.args))
	for k, v := range a.args {
		out[k] = v
	}
	for k, v := range b.args {
		out[k] = v
	}
	return out
}

// join2 combines two nodes with "&&" or "||". The operands are
// parenthesised as whole expressions, which is the only grouping
// fexpr accepts.
func join2(left, right *expr, op string) *expr {
	k := opAnd
	if op == "||" {
		k = opOr
	}
	return &expr{
		fragment: "(" + left.fragment + " " + op + " " + right.fragment + ")",
		args:     mergeExprs(left, right),
		kind:     nodePredicate,
		op:       k,
		join:     op,
		parts:    []*expr{left, right},
	}
}

// literal binds v under a fresh parameter name and returns a value
// node. Every literal gets its own name; see parser.nextArg.
func (p *parser) literal(v any) *expr {
	name := p.nextArg()
	return &expr{fragment: "{:" + name + "}", args: map[string]any{name: v}, kind: nodeValue, val: v}
}

// timeLiteral binds t in PocketBase's date layout while keeping the
// instant itself on the node, so `now - duration("24h")` can be folded
// before anything is bound.
func (p *parser) timeLiteral(t time.Time) *expr {
	name := p.nextArg()
	return &expr{
		fragment: "{:" + name + "}",
		args:     map[string]any{name: pocketbaseDateTime(t)},
		kind:     nodeValue,
		val:      t,
	}
}

// -------------------------------------------------------------------------
// Grammar. Each level is one function; the chain is:
//
//   parseOr            '||' (lowest precedence)
//     parseAnd        '&&'
//       parseNot      '!'
//         parseCmp    == != < <= > >=
//           parseAddSub   + -
//             parseMulDiv  * /
//               parseUnary   unary -
//                 parsePrimary
// -------------------------------------------------------------------------

// Limits on a single filter expression.
//
// Every term in a filter compiles to its own clause in the emitted
// PocketBase filter, so both the length and the nesting depth are a
// direct multiplier on the work the database has to do. Both are bounded
// here so a hostile or accidentally pasted filter is reported as a clean
// error instead of being compiled at all.
//
// maxFilterLength mirrors the `ParserExpressionSizeLimit(8 << 10)`
// upstream Memos applies (filter/engine.go), so a filter that works
// against upstream also works here.
const (
	maxFilterLength = 8 << 10
	// maxParseDepth bounds the recursive descent. The only two
	// recursive productions are grouping (`( … )`) and negation
	// (`!`), so this is the only way an input can drive the goroutine
	// stack. Go's stack starts at 8 KiB and doubles up to 1 GiB, so a
	// very deep chain would otherwise grow the stack without bound
	// before any other limit could apply.
	maxParseDepth = 256
)

// enter records one level of nesting, refusing to descend further. It
// is paired with leave by the two recursive productions (parsePrimary's
// group and parseNot).
func (p *parser) enter() error {
	p.depth++
	if p.depth > maxParseDepth {
		return errors.Errorf("filter is nested more than %d levels deep", maxParseDepth)
	}
	return nil
}

func (p *parser) leave() { p.depth-- }

func (p *parser) parseOr() (*expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isPunct("||") {
		if err := requirePredicate(left); err != nil {
			return nil, err
		}
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if err := requirePredicate(right); err != nil {
			return nil, err
		}
		left = join2(left, right, "||")
	}
	return left, nil
}

func (p *parser) parseAnd() (*expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isPunct("&&") {
		if err := requirePredicate(left); err != nil {
			return nil, err
		}
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		if err := requirePredicate(right); err != nil {
			return nil, err
		}
		left = join2(left, right, "&&")
	}
	return left, nil
}

// requirePredicate rejects a bare value in a position that needs a
// boolean. Without it `duration("1h") && pinned` would build a join
// around an empty fragment and reach PocketBase as a syntax error
// instead of a message naming the offending operand.
func requirePredicate(e *expr) error {
	if e.kind != nodePredicate {
		return errors.New("operand is not a boolean expression; compare it with == or !=")
	}
	return nil
}

// parseNot handles the `!` prefix. fexpr has no negation operator
// (`!x` parses as "invalid sign operator"), so the negation is
// pushed down through De Morgan's laws:
//
//	!(a && b)  ->  !a || !b
//	!(a || b)  ->  !a && !b
//	!f         ->  the operator's negation (= -> !=, ~ -> !~, …)
//
// A negated derived predicate (`!has_link`) has no such dual, so it
// is rejected instead of being silently turned into something else.
//
// `!` binds to the comparison that follows it, so `!space == "a"`
// negates the whole test rather than the column. The SPA does not
// currently emit `!`; it is supported so a hand-written saved view
// does not simply fail.
func (p *parser) parseNot() (*expr, error) {
	if !p.isPunct("!") {
		return p.parseCmp()
	}
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()
	p.advance()
	// Recurse through parseNot so `!!x` cancels out; parseCmp for the
	// single case so `!x == y` negates the comparison, not the column.
	var inner *expr
	var err error
	if p.isPunct("!") {
		inner, err = p.parseNot()
	} else {
		inner, err = p.parseCmp()
	}
	if err != nil {
		return nil, err
	}
	return negate(inner)
}

// jsonListMember builds the filter that tests whether a JSON-array
// column contains one exact element.
//
// PocketBase's `?=` operator ("some element of this multi-value field
// equals") only applies to fields it models as multi-match, which are
// relations — not to a plain JSON column. For a JSON column the
// filter language offers only `=`, `~` (LIKE) and comparisons, so
// membership is expressed as a disjunction of LIKE patterns covering
// every position an element can occupy in the serialised array:
//
//	["work"]        the whole array is the single element
//	["work", …]     the first element
//	…,"work"]       the last element
//	…,"work",…      a middle element
//
// The patterns rely on the serialiser emitting no whitespace after the
// commas, which is what PocketBase's JSONField does (it marshals with
// go-json). A `LIKE '%"work"%'` alone would be wrong: it also matches
// "workout" and "xwork", and the anchors here are what keep those out.
//
// Each pattern is bound as a parameter rather than inlined, so the tag
// text never reaches the filter string; four parameters are added to
// args, named <key>, <key>first, <key>last and <key>mid.
func jsonListMember(column, key, element string, args map[string]any) string {
	// The JSON-escaped form is what the serialiser writes, so a tag
	// containing a quote or a backslash matches the same way the
	// stored value does.
	quoted := strconv.Quote(element)
	single := key
	first := key + "_first"
	last := key + "_last"
	mid := key + "_mid"
	args[single] = "[" + quoted + "]"
	args[first] = "[" + quoted + ",%"
	args[last] = "%," + quoted + "]"
	args[mid] = "%," + quoted + ",%"
	return fmt.Sprintf(
		"(%[1]s = {:%[2]s}) || (%[1]s ~ {:%[3]s}) || (%[1]s ~ {:%[4]s}) || (%[1]s ~ {:%[5]s})",
		column, single, first, last, mid)
}

// negate applies De Morgan to a parsed node.
func negate(e *expr) (*expr, error) {
	switch e.op {
	case opCompare:
		var flipped string
		switch e.cmpOp {
		case "=":
			flipped = "!="
		case "!=":
			flipped = "="
		case "~":
			flipped = "!~"
		case "!~":
			flipped = "~"
		case "?=":
			// `?=` ("some element equals") -> `?!=` ("no element equals").
			flipped = "?!="
		case "?!=":
			flipped = "?="
		default:
			// <, <=, >, >= have no direct negation in fexpr, and
			// inverting the bounds needs the value's type, which the
			// caller owns.
			return nil, errors.Errorf("negating `%s` is not supported", e.cmpOp)
		}
		// Re-render: the cached fragment still carries the original
		// operator.
		out := *e
		out.cmpOp = flipped
		out.join = ""
		out.fragment = e.parts[0].fragment + " " + flipped + " " + rightFragment(e.parts)
		return &out, nil
	case opAnd:
		return negateGroup(e, "||")
	case opOr:
		return negateGroup(e, "&&")
	case opJSONList:
		// The parts are LIKE patterns rather than comparisons, so
		// De Morgan has nothing to flip. `!(a IN b)` would need the
		// negated LIKE patterns, which the filter language cannot
		// express as a single clause.
		return nil, errors.Errorf("negating a list-membership test is not supported")
	default:
		// A derived predicate (has_link, has_task_list, …) is a
		// complete boolean with no operator to flip. It can still be
		// negated when the caller supplied its complement
		// (Field.NegatedExpr); otherwise there is nothing to emit.
		if e.notFragment != "" {
			return &expr{fragment: e.notFragment, args: map[string]any{}, kind: nodePredicate}, nil
		}
		return nil, errors.Errorf("this expression cannot be negated")
	}
}

// rightFragment renders the right-hand operand of a comparison node.
// Every comparison this package builds has exactly two operands; the
// `null` comparison has one and never reaches negation (its operator
// is flipped from a one-operand node).
func rightFragment(parts []*expr) string {
	if len(parts) < 2 {
		return "null"
	}
	return parts[1].fragment
}

// negateGroup applies De Morgan to the operands of a join node.
func negateGroup(e *expr, join string) (*expr, error) {
	parts := make([]string, 0, len(e.parts))
	args := map[string]any{}
	for _, part := range e.parts {
		neg, err := negate(part)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "("+neg.fragment+")")
		for k, v := range neg.args {
			args[k] = v
		}
	}
	return &expr{fragment: "(" + strings.Join(parts, join) + ")", args: args, kind: nodePredicate}, nil
}

var cmpOps = map[string]string{
	"==": "=",
	"!=": "!=",
	">":  ">",
	">=": ">=",
	"<":  "<",
	"<=": "<=",
}

func (p *parser) parseCmp() (*expr, error) {
	left, err := p.parseAddSub()
	if err != nil {
		return nil, err
	}
	if err := rejectDurationOperand(left); err != nil {
		return nil, err
	}
	// CEL's membership operator with a list *field* on the right:
	// `"work" in tags`. This is the spelling the proto documents and
	// the SPA's quick-find hint shows; `tag in ["work"]` is the other
	// one and is handled in parseIdentOrCall, where the right side is
	// a literal list.
	if t := p.peek(); t.kind == tkIdent && t.lit == "in" {
		return p.parseValueIn(left)
	}
	t := p.peek()
	sqlOp, isCmp := cmpOps[t.lit]
	if t.kind != tkPunct || !isCmp {
		return p.bareBoolean(left), nil
	}
	p.advance()

	// `space == null` and `space != null` are the only places the
	// `null` keyword is legal. PocketBase's filter builder spells them
	// `IS NULL` / `IS NOT NULL`; every other comparison binds a
	// real parameter.
	if p.peek().kind == tkKeyword && p.peek().lit == "null" {
		p.advance()
		if left.kind != nodeValue {
			return nil, errors.Errorf("null comparison needs a column on the left, got %q", left.fragment)
		}
		// PocketBase's filter builder special-cases an equality
		// against the `null` token: it emits `col IS NULL` (and
		// `IS NOT NULL` for `!=`), including the empty-string
		// variant, so the null-vs-empty normalisation is handled
		// for us. `null` is a reserved identifier the backend
		// resolves even when no such field exists.
		return &expr{
			fragment: left.fragment + " " + sqlOp + " null",
			args:     left.args,
			kind:     nodePredicate,
			op:       opCompare,
			cmpOp:    sqlOp,
			parts:    []*expr{left},
		}, nil
	}

	right, err := p.parseAddSub()
	if err != nil {
		return nil, err
	}
	if err := rejectDurationOperand(right); err != nil {
		return nil, err
	}
	// `size(tags) == 0` / `!= 0` is the one length test the filter
	// language can express, through the empty-array / NULL check.
	if ref, ok := left.val.(sizeRef); ok {
		return p.parseSizeComparison(ref, sqlOp, right)
	}
	// Enforce the field's closed value set. Without this,
	// `visibility == "PUBLIK"` compiles to a query that matches no
	// rows, which the user reads as "the filter does nothing".
	if f, ok := p.fieldByFragment(left); ok {
		// An array column has no scalar equality, but `tag == "work"`
		// is a spelling older saved views carry and the only reading
		// it can have is membership, so it compiles to the same test
		// as `"work" in tags`. `!=` is rejected: the complement of the
		// four anchored LIKE patterns has no expression here.
		if f.Kind == FieldJSONList {
			if sqlOp != "=" {
				return nil, errors.Errorf(
					"%q is a list; only `%s in […]` and `\"…\" in %s` are supported (a negated membership test is not)", f.Name, f.Name, f.Name)
			}
			value, ok := right.singleValue().(string)
			if !ok {
				return nil, errors.Errorf("%q needs a string literal", f.Name)
			}
			return jsonListPredicate(f.Column, value, p)
		}
		if err := validateConst(f.Name, f, []*expr{right}); err != nil {
			return nil, err
		}
		if f.FallbackColumn != "" {
			return p.fallbackComparison(f, sqlOp, right)
		}
	} else if left.kind == nodePredicate {
		// A derived field is already a complete boolean
		// predicate; comparing it to a value is a type error, not
		// something to silently coerce.
		return nil, errors.Errorf("cannot compare the result of a derived predicate against a value")
	}
	// No parentheses around the operands: fexpr's grammar is
	// `operand <sign> operand`, and a parenthesised identifier is a
	// parse error ("invalid or incomplete filter expression").
	return &expr{
		fragment: left.fragment + " " + sqlOp + " " + right.fragment,
		args:     mergeExprs(left, right),
		kind:     nodePredicate,
		op:       opCompare,
		cmpOp:    sqlOp,
		parts:    []*expr{left, right},
	}, nil
}

// bareBoolean turns a bare reference to a boolean column into the
// predicate PocketBase spells `column = true`. The SPA's pinned chip
// emits `pinned` with no comparison, while the V0 shaper and the proto
// docs spell the same filter `pinned == true`; both must compile. A
// bare reference to any other column is a half-typed filter, so it
// stays a nodeValue and Compile rejects it with "does not produce a
// boolean" rather than guessing between `= null` and `= ""`.
//
// This is applied in parseCmp, exactly where a comparison operator
// would have followed, so a boolean column also works inside a group
// (`(pinned) && …` — the shape combineCELFilters emits) and as either
// operand of `&&` / `||`.
func (p *parser) bareBoolean(e *expr) *expr {
	if e.kind != nodeValue {
		return e
	}
	// CEL's boolean literals are complete predicates on their own.
	// Saved views spell "match everything" as `true`, so the literal
	// has to compile rather than being rejected as a half-typed
	// filter.
	if v, ok := e.val.(bool); ok {
		return p.constantPredicate(v)
	}
	f, ok := p.fieldByFragment(e)
	if !ok || !f.Boolean {
		return e
	}
	return &expr{
		fragment: e.fragment + " = true",
		args:     map[string]any{},
		kind:     nodePredicate,
		op:       opCompare,
		cmpOp:    "=",
		parts: []*expr{
			e,
			{fragment: "true", kind: nodeValue},
		},
	}
}

// constantPredicate renders a bare boolean literal. PocketBase's
// filter grammar has no comparison-free `true`, so the literal
// becomes a test on the record id every collection has:
// `id != ""` matches every row, `id = ""` matches none. The empty
// string is bound like any other literal, which keeps negate able to
// flip the comparison (`!true` -> `id = ""`).
func (p *parser) constantPredicate(v bool) *expr {
	op := "!="
	if !v {
		op = "="
	}
	lit := p.literal("")
	return &expr{
		fragment: "id " + op + " " + lit.fragment,
		args:     lit.args,
		kind:     nodePredicate,
		op:       opCompare,
		cmpOp:    op,
		parts:    []*expr{{fragment: "id", kind: nodeValue}, lit},
	}
}

// parseValueIn compiles CEL's membership operator with the list on
// the right — `"work" in tags` — which is the spelling both the proto
// and the SPA's quick-find hint document. Only a JSON-array field can
// be the right-hand side: there is nothing else in the schema that
// holds a list, and `"x" in visibility` is a type error rather than a
// filter.
func (p *parser) parseValueIn(left *expr) (*expr, error) {
	p.advance() // consume `in`
	if p.isPunct("[") {
		return nil, errors.Errorf(
			"`in` needs a list field on the right, not a literal list; write `tag in […]` instead")
	}
	right, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	f, ok := p.fieldByFragment(right)
	if !ok {
		return nil, errors.New("`in` needs a list field on the right, e.g. `\"work\" in tags`")
	}
	if f.Kind != FieldJSONList {
		return nil, errors.Errorf("%q is not a list; compare it with == instead", f.Name)
	}
	value, ok := left.val.(string)
	if !ok {
		return nil, errors.Errorf("only a string literal can be tested for membership in %q", f.Name)
	}
	return jsonListPredicate(f.Column, value, p)
}

// sizeRef marks a `size(<json-list field>)` call so the surrounding
// comparison can be turned into the emptiness test. It is never
// rendered itself.
type sizeRef struct{ column string }

// parseSizeComparison compiles `size(tags) == 0` (and `!= 0`), the
// only length test the filter language can express: an empty JSON
// array is stored as `[]`, and PocketBase normalises a comparison
// against `null` into an empty-string / IS NULL test, so the pair
// covers rows created before the tag hook as well.
func (p *parser) parseSizeComparison(ref sizeRef, sqlOp string, right *expr) (*expr, error) {
	if sqlOp != "=" && sqlOp != "!=" {
		return nil, errors.Errorf("size(%s) only supports == 0 or != 0", ref.column)
	}
	n, ok := right.singleValue().(int64)
	if !ok || n != 0 {
		return nil, errors.Errorf(
			"size(%s) is only supported against 0; the filter language has no length operator", ref.column)
	}
	empty := p.nextArg()
	args := map[string]any{empty: "[]"}
	fragment := fmt.Sprintf("((%s = null) || (%s = {:%s}))", ref.column, ref.column, empty)
	if sqlOp == "!=" {
		fragment = fmt.Sprintf("((%s != null) && (%s != {:%s}))", ref.column, ref.column, empty)
	}
	return &expr{
		fragment: fragment,
		args:     args,
		kind:     nodePredicate,
		op:       opJSONList,
		join:     "||",
	}, nil
}

// fallbackComparison compiles a comparison against a field whose value
// may live on a related row. riff's `attachments.creator` is only
// stamped on rows uploaded before their memo existed, so "the creator
// of this attachment" is the memo's author for a bound row and the
// uploader for an unlinked one:
//
//	((memo.author = x) || ((creator = x) && (memo = null)))
//
// Only equality is supported — the proto documents exactly that, and
// a negated form would need the disjunction's complement.
func (p *parser) fallbackComparison(f Field, sqlOp string, right *expr) (*expr, error) {
	if sqlOp != "=" {
		return nil, errors.Errorf("%q only supports == (it reads %s or %s)", f.Name, f.FallbackColumn, f.Column)
	}
	value, ok := right.singleValue().(string)
	if !ok {
		return nil, errors.Errorf("%q needs a string literal", f.Name)
	}
	primary, fallback := p.nextArg(), p.nextArg()
	return &expr{
		fragment: fmt.Sprintf("((%s = {:%s}) || ((%s = {:%s}) && (%s)))",
			f.FallbackColumn, primary, f.Column, fallback, f.FallbackWhen),
		args: map[string]any{primary: value, fallback: value},
		kind: nodePredicate,
		op:   opFallback,
	}, nil
}

// fieldByFragment finds the Field whose column produced fragment.
// A node with no bound parameters is unambiguously a bare column
// reference, so matching on fragment plus an empty args map is
// safe. It lets parseCmp apply the Const set without threading the
// field name through every grammar level.
func (p *parser) fieldByFragment(e *expr) (Field, bool) {
	if len(e.args) != 0 {
		return Field{}, false
	}
	for _, f := range p.fields {
		if f.Kind != FieldBoolExpr && f.Column == e.fragment {
			return f, true
		}
	}
	return Field{}, false
}

// parseAddSub exists only to reject arithmetic explicitly. PocketBase's
// filter language has no arithmetic operators, so `now - duration("24h")`
// cannot be compiled into it: there is nowhere to put the subtraction.
//
// The timestamps the SPA filters on (the calendar's day range) are
// already written as two comparisons against `timestamp(...)` literals,
// which do compile. `now - duration(...)` does not, and is reported as
// unsupported rather than silently dropped.
// parseAddSub folds `now ± duration(...)` and rejects every other
// arithmetic operator. PocketBase's filter language has no
// arithmetic, so the subtraction cannot be handed to the query — but
// when one operand is a pinned instant and the other is a fixed
// duration, the result *is* a literal and can be computed here. That
// is the shape CEL's relative-date idiom uses (`created_ts > now -
// duration("168h")`, which the proto documents and the saved-view
// examples ship); `size(x) * 2` has nowhere to go and stays an error.
func (p *parser) parseAddSub() (*expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tkPunct || (t.lit != "+" && t.lit != "-" && t.lit != "*" && t.lit != "/") {
			return left, nil
		}
		if t.lit == "*" || t.lit == "/" {
			return nil, arithmeticError(t.lit)
		}
		base, ok := left.val.(time.Time)
		if !ok {
			return nil, arithmeticError(t.lit)
		}
		p.advance()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		d, ok := right.val.(time.Duration)
		if !ok {
			return nil, errors.Errorf(
				"only a duration(...) can be added to or subtracted from a timestamp (got %q)", right.fragment)
		}
		if t.lit == "+" {
			base = base.Add(d)
		} else {
			base = base.Add(-d)
		}
		left = p.timeLiteral(base)
	}
}

// arithmeticError is the error for an operator the filter language
// cannot carry.
func arithmeticError(op string) error {
	return errors.Errorf(
		"arithmetic (%q) is not supported by the PocketBase filter language; "+
			"only `now ± duration(…)` is folded at compile time. "+
			"Write an explicit range such as `created_ts >= timestamp(\"…\") && created_ts < timestamp(\"…\")`",
		op)
}

// rejectDurationOperand refuses a bare duration where a value is
// expected. `created_ts > duration("24h")` has no meaning, and folding
// only happens in parseAddSub; without this check the duration would
// be bound as a parameter and handed to PocketBase.
func rejectDurationOperand(e *expr) error {
	if _, ok := e.val.(time.Duration); ok {
		return errors.New(
			"duration(...) must be added to or subtracted from a timestamp, e.g. `created_ts > now - duration(\"24h\")`")
	}
	return nil
}

func (p *parser) parsePrimary() (*expr, error) {
	t := p.peek()
	switch t.kind {
	case tkString:
		p.advance()
		return p.literal(t.lit), nil
	case tkInt:
		p.advance()
		v, err := strconv.ParseInt(t.lit, 10, 64)
		if err != nil {
			return nil, errors.Wrapf(err, "bad integer %q", t.lit)
		}
		return p.literal(v), nil
	case tkFloat:
		p.advance()
		v, err := strconv.ParseFloat(t.lit, 64)
		if err != nil {
			return nil, errors.Wrapf(err, "bad float %q", t.lit)
		}
		return p.literal(v), nil
	case tkKeyword:
		switch t.lit {
		case "true":
			p.advance()
			return p.literal(true), nil
		case "false":
			p.advance()
			return p.literal(false), nil
		case "null":
			return nil, errors.Errorf("bare null at position %d; compare against it with == or !=", t.off)
		default:
			return nil, errors.Errorf("unexpected keyword %q at position %d", t.lit, t.off)
		}
	case tkPunct:
		if t.lit == "(" {
			if err := p.enter(); err != nil {
				return nil, err
			}
			defer p.leave()
			p.advance()
			inner, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.isPunct(")") {
				return nil, errors.Errorf("expected ) at position %d, got %q", p.peek().off, p.peek().lit)
			}
			p.advance()
			return inner, nil
		}
		// A `[...]` list is only legal as the rhs of `in`.
		if t.lit == "[" {
			return nil, errors.Errorf("list at position %d is only valid on the right of `in`", t.off)
		}
		return nil, errors.Errorf("unexpected %q at position %d", t.lit, t.off)
	case tkIdent:
		return p.parseIdentOrCall()
	default:
		return nil, errors.Errorf("unexpected %q at position %d", t.lit, t.off)
	}
}

// parseIdentOrCall handles the identifier-shaped primaries:
//
//	ident                    a field reference
//	now                      the request time, bound as a literal
//	ident(args)              a top-level builtin (duration,
//	                         timestamp)
//	ident.ident(args)        a method builtin (content.contains,
//	                         tags.exists, sets.contains)
//
// It also handles the `field in [...]` form, which the SPA uses
// verbatim (`tag in ["work"]`, `visibility in ["PRIVATE", …]`) and
// which is *not* a general CEL `in` macro.
func (p *parser) parseIdentOrCall() (*expr, error) {
	name := p.peek().lit
	p.advance()

	// `now` is a CEL variable, not a call: `created_ts > now - duration("24h")`.
	// Bound in PocketBase's own date layout, same as timestamp().
	if name == "now" && !p.isPunct("(") {
		return p.timeLiteral(p.now), nil
	}

	// `field in [a, b, c]` — checked before the call path so a
	// field that shadows a builtin name still wins. `in` lexes as
	// an identifier, so match on the token kind as well.
	if t := p.peek(); t.kind == tkIdent && t.lit == "in" {
		return p.parseIn(name)
	}

	// `receiver.method(` — the receiver must be a registered
	// field (or the `tags` pseudo-field the caller registers).
	if p.isPunct(".") {
		p.advance()
		method := p.peek()
		if method.kind != tkIdent {
			return nil, errors.Errorf("expected a method name after %q., got %q", name, method.lit)
		}
		p.advance()
		qualified := name + "." + method.lit
		if !p.isPunct("(") {
			return nil, errors.Errorf("expected ( after %s", qualified)
		}
		p.advance()
		args, err := p.parseCallArgs(qualified)
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		return p.callBuiltin(qualified, args)
	}

	if p.isPunct("(") {
		p.advance()
		args, err := p.parseArgs()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		return p.callBuiltin(name, args)
	}

	return p.fieldReference(name)
}

// parseIn compiles `field in [a, b, c]`. A list of strings against
// an array column becomes one json_array_contains per element OR-ed
// together; a list against a scalar column becomes a chain of
// equality tests.
func (p *parser) parseIn(fieldName string) (*expr, error) {
	p.advance() // consume `in`
	if !p.isPunct("[") {
		return nil, errors.Errorf("expected [ after `in`, got %q", p.peek().lit)
	}
	p.advance()
	values := make([]*expr, 0, 4)
	for {
		v, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		if p.isPunct(",") {
			p.advance()
			continue
		}
		break
	}
	if err := p.expectPunct("]"); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.Errorf("`in []` matches nothing; write the field directly if that is what you want")
	}

	f, ok := p.fields[fieldName]
	if !ok {
		return nil, errors.Errorf("unknown field %q at position %d", fieldName, p.peek().off)
	}
	if err := validateConst(fieldName, f, values); err != nil {
		return nil, err
	}

	args := map[string]any{}
	parts := make([]string, 0, len(values))
	nodes := make([]*expr, 0, len(values))
	for _, v := range values {
		value, ok := v.singleValue().(string)
		if !ok {
			return nil, errors.Errorf("%q takes string values only", fieldName)
		}
		key := p.nextArg()
		if f.Kind == FieldJSONList {
			frag := jsonListMember(f.Column, key, value, args)
			parts = append(parts, frag)
			nodes = append(nodes, &expr{fragment: frag, args: args, kind: nodePredicate, op: opJSONList, join: "||"})
			continue
		}
		// A scalar column: one equality per value.
		col := &expr{fragment: f.Column, kind: nodeValue}
		lit := &expr{fragment: "{:" + key + "}", kind: nodeValue}
		frag := f.Column + " = " + "{:" + key + "}"
		args[key] = value
		parts = append(parts, frag)
		nodes = append(nodes, &expr{
			fragment: frag,
			args:     map[string]any{key: value},
			kind:     nodePredicate,
			op:       opCompare,
			cmpOp:    "=",
			parts:    []*expr{col, lit},
		})
	}
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	op := opOr
	if f.Kind == FieldJSONList {
		op = opJSONList
	}
	return &expr{
		fragment: "(" + strings.Join(parts, " || ") + ")",
		args:     args,
		kind:     nodePredicate,
		op:       op,
		join:     "||",
		parts:    nodes,
	}, nil
}

// singleValue returns the one value a literal node binds. It is only
// valid on a node produced by parser.literal; a node carrying zero or
// several parameters is a programming error and yields nil, which the
// caller turns into a filter parameter that matches nothing.
func (e *expr) singleValue() any {
	for _, v := range e.args {
		return v
	}
	return nil
}

// parseCallArgs parses a call's argument list, with one special case:
// CEL's element-binding `exists` macro — `tags.exists(t, t == "work")`
// — which names an element variable the rest of the grammar does not
// know about. Only the equality predicate is accepted: there is no
// per-element expression to evaluate, so `t.startsWith(…)` or a
// conjunction is rejected rather than approximated.
func (p *parser) parseCallArgs(name string) ([]*expr, error) {
	if name == "tags.exists" || name == "tag.exists" {
		save := p.tok
		if t := p.peek(); t.kind == tkIdent {
			p.advance()
			if p.isPunct(",") {
				p.advance()
				value, err := p.parseExistsEquality(name, t.lit)
				if err != nil {
					return nil, err
				}
				if !p.isPunct(")") {
					return nil, errors.Errorf("%s(...) only supports `%s == \"value\"`", name, t.lit)
				}
				return []*expr{p.literal(value)}, nil
			}
		}
		// Not the element-binding form: replay the token and parse
		// the ordinary `exists("value")` arguments.
		p.tok = save
	}
	return p.parseArgs()
}

// parseExistsEquality reads the predicate of the element-binding form,
// which names the element variable and compares it with a literal:
// `t == "work"` or `"work" == t`.
func (p *parser) parseExistsEquality(fn, elem string) (string, error) {
	unsupported := errors.Errorf("%s(...) only supports `%s == \"value\"`", fn, elem)
	if p.peek().kind == tkString {
		v := p.peek().lit
		p.advance()
		if !p.isPunct("==") {
			return "", unsupported
		}
		p.advance()
		if p.peek().kind != tkIdent || p.peek().lit != elem {
			return "", unsupported
		}
		p.advance()
		return v, nil
	}
	if p.peek().kind != tkIdent || p.peek().lit != elem {
		return "", unsupported
	}
	p.advance()
	if !p.isPunct("==") {
		return "", unsupported
	}
	p.advance()
	if p.peek().kind != tkString {
		return "", unsupported
	}
	v := p.peek().lit
	p.advance()
	return v, nil
}

func (p *parser) parseArgs() ([]*expr, error) {
	if p.isPunct(")") {
		return nil, nil
	}
	out := []*expr{}
	for {
		v, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.isPunct(",") {
			p.advance()
			continue
		}
		return out, nil
	}
}

func (p *parser) isPunct(s string) bool {
	t := p.peek()
	return t.kind == tkPunct && t.lit == s
}

func (p *parser) expectPunct(s string) error {
	if !p.isPunct(s) {
		return errors.Errorf("expected %q at position %d, got %q", s, p.peek().off, p.peek().lit)
	}
	p.advance()
	return nil
}

// fieldReference resolves a bare identifier. A FieldBoolExpr field is
// a complete predicate on its own; a FieldColumn field needs an
// operator, so a bare reference to one is an error rather than a
// guess between `= null` and `= ""`.
func (p *parser) fieldReference(name string) (*expr, error) {
	f, ok := p.fields[name]
	if !ok {
		return nil, errors.Errorf("unknown field %q", name)
	}
	switch f.Kind {
	case FieldBoolExpr:
		return &expr{
			fragment:    "(" + f.Expr + ")",
			args:        map[string]any{},
			kind:        nodePredicate,
			notFragment: negatedExprFragment(f),
		}, nil
	case FieldColumn, FieldJSONList:
		return &expr{fragment: f.Column, args: nil, kind: nodeValue}, nil
	default:
		return nil, errors.Errorf("field %q has an unknown kind", name)
	}
}

// negatedExprFragment renders Field.NegatedExpr as a parenthesised
// predicate, or "" when the field declares no complement.
func negatedExprFragment(f Field) string {
	if f.NegatedExpr == "" {
		return ""
	}
	return "(" + f.NegatedExpr + ")"
}

// validateConst enforces a Field's Const set against the values on
// the right of the comparison. Without it a typo silently becomes a
// query that matches no rows, which reads to the user as "the filter
// does nothing".
func validateConst(fieldName string, f Field, values []*expr) error {
	if len(f.Const) == 0 {
		return nil
	}
	for _, v := range values {
		raw, ok := v.singleValue().(string)
		if !ok {
			continue
		}
		found := false
		for _, allowed := range f.Const {
			if raw == allowed {
				found = true
				break
			}
		}
		if !found {
			return errors.Errorf("field %q does not accept %q; allowed: %s", fieldName, raw, strings.Join(f.Const, ", "))
		}
	}
	return nil
}

// -------------------------------------------------------------------------
// Builtins
// -------------------------------------------------------------------------

// pocketbaseDateTime renders t the way PocketBase stores an
// AutodateField: UTC, "2006-01-02 15:04:05.000Z". The constant is
// duplicated from tools/types.DefaultDateLayout rather than
// imported because the depguard rule keeps internal/ free of module
// imports; internal/cel's test asserts the two agree.
func pocketbaseDateTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000Z")
}

// callBuiltin dispatches the documented function set. Every branch
// returns a node whose fragment is valid PocketBase filter syntax.
func (p *parser) callBuiltin(name string, args []*expr) (*expr, error) {
	switch name {
	// now / timestamp() / duration().
	//
	// The three are evaluated here rather than in the filter
	// language, because the filter language has no date functions
	// beyond strftime and no arithmetic. `now` is pinned by the
	// caller (Options.Now) so the value is stable for the request.
	//
	// The bound value is formatted exactly the way PocketBase
	// stores its AutodateField columns — "2006-01-02 15:04:05.000Z"
	// in UTC (tools/types.DefaultDateLayout). Autodate columns are
	// TEXT, so binding a Unix epoch integer would compare a string
	// against a number and match nothing.
	case "now":
		if len(args) != 0 {
			return nil, errors.Errorf("now() takes no arguments")
		}
		return p.timeLiteral(p.now), nil
	case "duration":
		// Only meaningful as the right-hand operand of
		// `now ± duration(...)`, where parseAddSub folds it into a
		// literal. A bare use is rejected by rejectDurationOperand
		// inside parseCmp.
		s, err := oneStringArg(name, args)
		if err != nil {
			return nil, err
		}
		d, perr := time.ParseDuration(s)
		if perr != nil {
			return nil, errors.Wrapf(perr, "duration(%q) wants a Go duration such as \"24h\"", s)
		}
		return &expr{kind: nodeValue, val: d}, nil
	case "timestamp":
		if len(args) != 1 {
			return nil, errors.Errorf("timestamp(...) takes exactly one argument")
		}
		// CEL defines two conversions: timestamp(string) parses an
		// RFC 3339 timestamp, timestamp(int) reads the argument as
		// Unix seconds. Both are in use here: the SPA's calendar
		// emits the integer form (`created_ts >= timestamp(1749340800)`
		// in web/src/lib/calendar-utils.ts) while saved views and the
		// docs spell the string form. Accepting only one of them made
		// the other a 400 on every feed that used it.
		if secs, ok := args[0].singleValue().(int64); ok {
			return p.timeLiteral(time.Unix(secs, 0).UTC()), nil
		}
		s, err := oneStringArg(name, args)
		if err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339, s)
		if perr != nil {
			return nil, errors.Wrapf(perr, "timestamp(%q) wants RFC3339", s)
		}
		return p.timeLiteral(t), nil
	case "size":
		// The filter language has no length operator. The one useful
		// case — "this list is empty" — is expressible through the
		// JSON column's empty-array / NULL representation, so the
		// call is turned into that test by parseCmp.
		if len(args) != 1 {
			return nil, errors.New("size(...) takes exactly one argument")
		}
		f, ok := p.fieldByFragment(args[0])
		if !ok || f.Kind != FieldJSONList {
			return nil, errors.New(
				"size(...) is only supported for a list field, as `size(tags) == 0`")
		}
		return &expr{kind: nodeValue, val: sizeRef{column: f.Column}}, nil
	}

	// content.contains(...) -> the `~` operator, which PocketBase's
	// filter builder expands to `LIKE ('%' || param || '%')`.
	//
	// startsWith / endsWith / matches are NOT expressible: the filter
	// language has no wildcard escaping, no "starts with" operator and
	// no regex. They are rejected rather than approximated, because a
	// `LIKE 'a%'` that silently becomes a contains-search would return
	// the wrong rows.
	if _, isContent := strings.CutPrefix(name, "content."); isContent {
		if !strings.HasSuffix(name, ".contains") {
			return nil, errors.Errorf(
				"%s is not supported; the filter language only offers `content.contains(…)`", name)
		}
		s, err := oneStringArg(name, args)
		if err != nil {
			return nil, err
		}
		lit := p.literal(s)
		return &expr{
			fragment: "content ~ " + lit.fragment,
			args:     lit.args,
			kind:     nodePredicate,
			op:       opCompare,
			cmpOp:    "~",
			parts:    []*expr{{fragment: "content"}, lit},
		}, nil
	}

	// Any other registered column supports `field.contains("text")` on
	// the same `~` operator. This is what the attachment contract
	// documents (`filename.contains("test")`) and what `content.contains`
	// already did for one hard-coded name.
	if recv, ok := strings.CutSuffix(name, ".contains"); ok {
		if f, ok := p.fields[recv]; ok {
			s, err := oneStringArg(name, args)
			if err != nil {
				return nil, err
			}
			if f.Kind == FieldJSONList {
				return jsonListPredicate(f.Column, s, p)
			}
			lit := p.literal(s)
			return &expr{
				fragment: f.Column + " ~ " + lit.fragment,
				args:     lit.args,
				kind:     nodePredicate,
				op:       opCompare,
				cmpOp:    "~",
				parts:    []*expr{{fragment: f.Column, kind: nodeValue}, lit},
			}, nil
		}
	}

	// tags.exists(...) maps onto PocketBase's `?=` operator, which
	// its filter builder resolves as "some element of the JSON array
	// equals this value".
	//
	// tags.all / tags.exists_one / the element-binding forms
	// (`tags.exists(t, t == "x")`) are not expressible: there is no
	// quantifier and no way to count matches. They are rejected so a
	// saved view that uses them reports an error instead of quietly
	// matching everything.
	switch name {
	case "tags.exists":
		s, err := oneStringArg(name, args)
		if err != nil {
			return nil, err
		}
		return jsonListPredicate("tags", s, p)
	case "tags.all", "tags.exists_one":
		return nil, errors.Errorf("%s(...) is not supported; only `tags.exists(…)` has an equivalent in the filter language", name)
	}

	// sets.contains(list, x) -> `list ?= x`.
	switch name {
	case "sets.contains":
		if len(args) != 2 {
			return nil, errors.Errorf("sets.contains() takes (list, string)")
		}
		col := args[0]
		if col.kind != nodeValue {
			return nil, errors.New("sets.contains() takes a column as its first argument")
		}
		s, err := oneStringArgAt(name, args, 1)
		if err != nil {
			return nil, err
		}
		return jsonListPredicate(col.fragment, s, p)
	case "sets.intersects", "sets.equivalent":
		return nil, errors.Errorf("%s(...) is not supported by the filter language", name)
	}
	return nil, errors.Errorf("unknown function %q", name)
}

// jsonListPredicate is the one-element form of the `in [...]` clause,
// shared by tags.exists(…) and sets.contains(…).
func jsonListPredicate(column, element string, p *parser) (*expr, error) {
	args := map[string]any{}
	key := p.nextArg()
	return &expr{
		fragment: jsonListMember(column, key, element, args),
		args:     args,
		kind:     nodePredicate,
		op:       opJSONList,
		join:     "||",
	}, nil
}

func oneStringArg(name string, args []*expr) (string, error) {
	return oneStringArgAt(name, args, 0)
}

func oneStringArgAt(name string, args []*expr, idx int) (string, error) {
	if len(args) <= idx {
		return "", errors.Errorf("%s() is missing argument %d", name, idx+1)
	}
	v := args[idx]
	raw, ok := v.singleValue().(string)
	if !ok {
		return "", errors.Errorf("%s() argument %d must be a string literal", name, idx+1)
	}
	return raw, nil
}
