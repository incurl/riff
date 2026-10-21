// Package cel compiles a subset of CEL (the Common Expression
// Language) expressions into the filter language PocketBase's
// query builder understands.
//
// The SPA sends CEL in `ListMemosRequest.filter`,
// `ListAttachmentsRequest.filter` and the `filter` field of every
// saved MemoView. Before this package existed the server ignored
// those fields entirely, so every filter — space, creator, tag,
// search, visibility, calendar range — returned the unfiltered
// feed and the user saw nothing wrong.
//
// # Target language
//
// The output is NOT raw SQL. It is PocketBase's own filter
// grammar, parsed by github.com/ganigeorgiev/fexpr:
//
//	operand <sign> operand        a comparison
//	<expr> && <expr>              AND
//	<expr> || <expr>              OR
//	( ... )                       a group
//
// The signs are = != < <= > >= ~ !~ ?= ?!= ?< ?<= ?> ?>=
// (~ is a "contains" LIKE; ?= is "some element of this
// multi-value field equals"). A literal is written as text and
// bound through the params map; a param placeholder is {:name}.
//
// The vocabulary is much smaller than CEL's, and the mismatch is
// deliberate: anything the grammar cannot express is rejected with
// an error the caller surfaces as a 400, rather than being
// approximated with a query that returns the wrong rows.
//
// # Grammar
//
//	expr   := orExpr
//	orExpr := andExpr ('||' andExpr)*
//	andExpr:= notExpr ('&&' notExpr)*
//	notExpr:= '!' notExpr | cmpExpr
//	cmpExpr:= primary (('=='|'!='|'>='|'<='|'>'|'<') primary)?
//	primary:= literal | field | field 'in' list | value 'in' field
//	          | builtin | '(' expr ')'
//
// Atoms are string literals (single or double quoted), integers,
// floats, true / false / null, and the identifier `now`. A bare
// `true` / `false` is a complete predicate ("match everything" /
// "match nothing"), so a saved view may store it.
//
// # Functions
//
//	content.contains(s)      -> `content ~ s`
//	<field>.contains(s)      -> `column ~ s` for any scalar field
//	tags.exists(s)           -> JSON-array membership, see below
//	tags.exists(e, e == s)   -> the same membership test
//	sets.contains(col, s)    -> JSON-array membership
//	size(list)               -> only against 0, see below
//	now                      -> the request time, pinned by the caller
//	timestamp(rfc3339)       -> that instant, pinned format
//	timestamp(unixSeconds)   -> that instant (CEL's int conversion)
//	duration("24h")          -> only as `now ± duration(…)`
//
// `now ± duration(...)` is folded at compile time: the filter language
// has no arithmetic, but when one operand is a pinned instant and the
// other a fixed duration the result is a literal, so
// `created_ts > now - duration("168h")` compiles to a bound timestamp.
// Every other arithmetic operator is rejected — `size(x) * 2` has
// nowhere to put the result. Write an explicit range instead —
// `created_ts >= timestamp("...") && created_ts < timestamp("...")`
// — which is what the SPA's calendar emits.
//
// `size(...)` has no length operator to compile to. The one useful
// case, emptiness (`size(tags) == 0`), is expressed through the JSON
// column's empty-array / NULL representation.
//
// Rejected, because the filter language has no equivalent:
//
//	content.startsWith / endsWith / matches
//	  There is no wildcard escaping and no "starts with" operator,
//	  so these would silently degrade to a contains-search.
//	size(x) other than `== 0` / `!= 0` on a list field
//	  No length function.
//	sets.intersects / sets.equivalent
//	  Set algebra has no counterpart.
//	tags.all / tags.exists_one
//	  No quantifier and no way to count matches.
//	tags.exists(e, <non-equality predicate>)
//	  The element-binding form has no expression evaluator; only
//	  `e == "value"` is compiled.
//
// `timestamp()` takes either form CEL defines: a string (parsed as
// RFC 3339) or an integer (Unix seconds). Both spellings arrive in
// practice — the SPA's calendar builds `timestamp(1749340800)` in
// web/src/lib/calendar-utils.ts while saved views and the docs use
// the string form — and a compiler that only accepted one of them
// turned every filter written the other way into a 400.
//
// # JSON-array fields
//
// A JSON-array column (riff's `tags`) is not a multi-value field in
// PocketBase's model, so `?=` does not apply to it: `?=` resolves
// only for relations. Membership is therefore expressed as four
// anchored LIKE patterns covering every position an element can
// occupy in the serialised array. See jsonListMember for why the
// anchors are required.
//
// Every CEL spelling of membership compiles to that same test:
// `"work" in tags`, `tag in ["work"]`, `tags.exists("work")`,
// `tags.exists(t, t == "work")`, and the older `tag == "work"` (the
// only meaning a scalar equality can have here). A negated membership
// test has no expression and is rejected.
//
// # Fields
//
// The caller supplies a Field schema mapping each name in the
// documented grammar onto either a column (FieldColumn, FieldJSONList)
// or a complete pre-rendered predicate (FieldBoolExpr). An unknown
// name is an error. A Field may carry a Const set, which turns
// `visibility == "PUBLIK"` into an error instead of a query that
// matches nothing.
//
// Two optional knobs describe fields that are not a single column:
//
//   - NegatedExpr is the complement of a FieldBoolExpr's Expr, so
//     `!has_location` can be compiled instead of reported as
//     inexpressible.
//   - FallbackColumn / FallbackWhen describe a value that lives on a
//     related row when the field's own column is empty, e.g.
//     `attachments.creator` for a bound attachment (its creator is the
//     memo's author; the column is only stamped on orphans). A
//     comparison then compiles to
//     `(FallbackColumn = x) || ((Column = x) && (FallbackWhen))` and
//     supports equality only.
//
// # Safety
//
// Every literal is bound through the returned Argv map and never
// concatenated into the filter string, so no user input reaches the
// filter parser. The parser is a hand-written recursive descent over
// the grammar above with no backtracking and no recursion beyond the
// nesting of the input's own parentheses.
//
// Two bounds keep the cost of a hostile or pasted filter finite, since
// every term compiles to its own clause in the emitted filter:
//
//	maxFilterLength  8 KiB, mirroring the ParserExpressionSizeLimit
//	                 upstream Memos applies (filter/engine.go)
//	maxParseDepth    256 levels of grouping or negation, the only two
//	                 recursive productions, and therefore the only way
//	                 an input could drive the goroutine stack
//
// Without them a 256 KB filter took 7.16s to compile and a 1.6 MB one
// exhausted memory; both are now rejected in microseconds.
package cel
