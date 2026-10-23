package cel

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pocketbase/pocketbase/tools/types"
)

var frozenNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// memoFields mirrors the schema pb/connect registers for the
// `memos` collection: the SPA's CEL field names mapped onto real
// PocketBase columns.
func memoFields() []Field {
	return []Field{
		{Name: "content", Kind: FieldColumn, Column: "content"},
		{Name: "creator", Kind: FieldColumn, Column: "author"},
		{Name: "space", Kind: FieldColumn, Column: "space"},
		{Name: "visibility", Kind: FieldColumn, Column: "visibility", Const: []string{"PRIVATE", "PROTECTED", "PUBLIC", "SPACE"}},
		{Name: "state", Kind: FieldColumn, Column: "state", Const: []string{"NORMAL", "ARCHIVED"}},
		{Name: "tags", Kind: FieldJSONList, Column: "tags"},
		// The SPA's tag chip spells it with the singular name.
		{Name: "tag", Kind: FieldJSONList, Column: "tags"},
		{Name: "created_ts", Kind: FieldColumn, Column: "created"},
		{Name: "updated_ts", Kind: FieldColumn, Column: "updated"},
		// Boolean: the SPA's pinned chip emits a bare `pinned`.
		{Name: "pinned", Kind: FieldColumn, Column: "pinned", Boolean: true},
		{Name: "has_link", Kind: FieldBoolExpr, Expr: "json_extract(payload, '$.property.has_link') = 1"},
		{Name: "has_task_list", Kind: FieldBoolExpr, Expr: "json_extract(payload, '$.property.has_task_list') = 1"},
	}
}

func attachmentFields() []Field {
	return []Field{
		{Name: "memo_id", Kind: FieldColumn, Column: "memo"},
		// `space` is the linked memo's space, not a column on
		// attachments: PocketBase resolves the dotted relation path.
		{Name: "space", Kind: FieldColumn, Column: "memo.space"},
		{Name: "filename", Kind: FieldColumn, Column: "filename"},
		// `creator` is stamped only on rows uploaded before their
		// memo existed, so a bound row's creator is its memo's
		// author (pb/migrations/1890000000_…).
		{
			Name:           "creator",
			Kind:           FieldColumn,
			Column:         "creator",
			FallbackColumn: "memo.author",
			FallbackWhen:   "memo = null",
		},
	}
}

func compileExpr(t *testing.T, expr string, fields ...Field) (*Expression, error) {
	t.Helper()
	if fields == nil {
		fields = memoFields()
	}
	return Compile(expr, Options{Now: frozenNow, Fields: fields})
}

func mustCompile(t *testing.T, expr string, fields ...Field) *Expression {
	t.Helper()
	out, err := compileExpr(t, expr, fields...)
	require.NoError(t, err, "Compile(%q)", expr)
	return out
}

// ---------------------------------------------------------------------------
// The bug this package exists for: the SPA sends a space clause and the
// server used to ignore the whole filter string.
// ---------------------------------------------------------------------------

func TestCompile_SpaceAndCreator(t *testing.T) {
	got := mustCompile(t, `(space == "spaces/team") && (creator == "users/alice")`)
	assert.Equal(t, `(space = "spaces/team" && author = "users/alice")`, inline(got))
}

// A resolved space id — the shape pb/connect hands in after rewriting.
func TestCompile_SpaceByID(t *testing.T) {
	got := mustCompile(t, `space == "abc123xyz"`)
	assert.Equal(t, `space = "abc123xyz"`, inline(got))
}

func TestCompile_SpaceNull(t *testing.T) {
	assert.Equal(t, `space = null`, mustCompile(t, `space == null`).Fragment)
	assert.Equal(t, `space != null`, mustCompile(t, `space != null`).Fragment)
}

func TestCompile_Visibility(t *testing.T) {
	got := mustCompile(t, `visibility == "PUBLIC"`)
	assert.Equal(t, `visibility = "PUBLIC"`, inline(got))
}

func TestCompile_VisibilityIn(t *testing.T) {
	got := mustCompile(t, `visibility in ["PRIVATE", "PROTECTED"]`)
	assert.Equal(t, `(visibility = "PRIVATE" || visibility = "PROTECTED")`, inline(got))
}

func TestCompile_ContentContains(t *testing.T) {
	got := mustCompile(t, `content.contains("roadmap")`)
	assert.Equal(t, `content ~ "%roadmap%"`, inline(got))
}

// ---------------------------------------------------------------------------
// String matching. `.startsWith` / `.endsWith` share `.contains`'s `~`
// operator and differ only in where the compiler puts the wildcard,
// so they are expressible where the package used to claim they were not.
// ---------------------------------------------------------------------------

func TestCompile_StringMatchWildcardPadding(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{`content.contains("a")`, `content ~ "%a%"`},
		{`content.startsWith("a")`, `content ~ "a%"`},
		{`content.endsWith("a")`, `content ~ "%a"`},
	} {
		assert.Equal(t, tc.want, inline(mustCompile(t, tc.src)), tc.src)
	}
}

// PocketBase escapes a bound value and wraps it in `%…%` only when the
// value carries no unescaped `%`; a value that does passes through
// verbatim. Padding here means the emitted pattern always contains a
// wildcard, so the database's heuristic never overrides the operator.
// escapeLikeLiteral additionally neutralises the user's own metacharacters
// so they cannot widen or narrow the match.
func TestCompile_StringMatchEscapesMetacharacters(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{`content.contains("50%")`, `content ~ "%50\%%"`},
		{`content.startsWith("50%")`, `content ~ "50\%%"`},
		{`content.contains("a_b")`, `content ~ "%a\_b%"`},
		{`content.startsWith("a_b")`, `content ~ "a\_b%"`},
		// An escape the caller already wrote is preserved, not doubled.
		{`content.contains("a\%b")`, `content ~ "%a\%b%"`},
	} {
		assert.Equal(t, tc.want, inline(mustCompile(t, tc.src)), tc.src)
	}
}

// `content.contains("100%")` used to bind the raw value; PocketBase saw
// the unescaped `%`, skipped its own wrapping, and ran `LIKE '100%'` —
// a *prefix* match on a literal percent sign. Padding fixes that.
func TestCompile_ContainsPercentNoLongerBecomesPrefix(t *testing.T) {
	assert.Equal(t, `content ~ "%100\%%"`, inline(mustCompile(t, `content.contains("100%")`)))
}

// `matches` is a string-matching method but has no LIKE equivalent, so
// it must be refused by name rather than falling through to `.contains`
// and quietly returning a substring search.
func TestCompile_MatchesIsRefusedNotDegraded(t *testing.T) {
	for _, src := range []string{
		`content.matches("a")`,
		`filename.matches("a")`,
		`title.matches("a")`,
	} {
		_, err := compileExpr(t, src, attachmentFields()...)
		require.Errorf(t, err, "%q should be rejected", src)
		assert.Contains(t, err.Error(), "matches")
	}
}

// The three operators work on any registered scalar column, not just
// content, which is what the attachment contract's filename example
// already relied on for `.contains`.
func TestCompile_StringMatchOnAnyScalarField(t *testing.T) {
	assert.Equal(t, `filename ~ "po%"`, inline(mustCompile(t, `filename.startsWith("po")`, attachmentFields()...)))
	assert.Equal(t, `filename ~ "%.png"`, inline(mustCompile(t, `filename.endsWith(".png")`, attachmentFields()...)))
}

// A JSON-array column has no prefix/suffix semantics, so only
// `.contains` (membership) is accepted there.
func TestCompile_StringMatchRejectsOnListField(t *testing.T) {
	for _, src := range []string{`tags.startsWith("wo")`, `tags.endsWith("rk")`} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "%q should be rejected", src)
		assert.Contains(t, err.Error(), "list field")
	}
}

// Negation flips `~` to `!~`, which inverts the padding too — the
// complement of "starts with" is "does not start with".
func TestCompile_NegatesStringMatch(t *testing.T) {
	assert.Equal(t, `content !~ "a%"`, inline(mustCompile(t, `!content.startsWith("a")`)))
	assert.Equal(t, `content !~ "%a"`, inline(mustCompile(t, `!content.endsWith("a")`)))
}

// A non-literal or wrong-typed argument is still refused; the padding
// must not turn a runtime value into filter text.
func TestCompile_StringMatchRequiresStringLiteral(t *testing.T) {
	_, err := compileExpr(t, `content.startsWith(space)`)
	require.Error(t, err)
}

// tags.exists / `tag in [...]` compile to PocketBase's `?=`, which its
// filter builder resolves as "some element of the JSON array equals".
func TestCompile_TagExists(t *testing.T) {
	got := mustCompile(t, `tags.exists("work")`)
	// Membership in a JSON-array column expands to four anchored LIKE
	// patterns (see jsonListMember); a bare `?=` would substring-match
	// "workout".
	assert.Equal(t,
		`(tags = {:_a1}) || (tags ~ {:_a1_first}) || (tags ~ {:_a1_last}) || (tags ~ {:_a1_mid})`,
		got.Fragment)
	assert.Equal(t, `["work"]`, got.Argv["_a1"])
	assert.Equal(t, `["work",%`, got.Argv["_a1_first"])
	assert.Equal(t, `%,"work"]`, got.Argv["_a1_last"])
	assert.Equal(t, `%,"work",%`, got.Argv["_a1_mid"])
}

func TestCompile_TagIn(t *testing.T) {
	got := mustCompile(t, `tags in ["work", "personal"]`)
	// Two membership tests, OR-ed: eight LIKE clauses in total.
	assert.Equal(t,
		`((tags = {:_a2}) || (tags ~ {:_a2_first}) || (tags ~ {:_a2_last}) || (tags ~ {:_a2_mid}) || `+
			`(tags = {:_a3}) || (tags ~ {:_a3_first}) || (tags ~ {:_a3_last}) || (tags ~ {:_a3_mid}))`,
		got.Fragment)
	assert.Equal(t, `["work"]`, got.Argv["_a2"])
	assert.Equal(t, `["personal"]`, got.Argv["_a3"])
}

func TestCompile_AndOrPrecedence(t *testing.T) {
	got := mustCompile(t, `has_link || visibility == "PUBLIC" && space == "spaces/a"`)
	assert.Equal(t,
		`((json_extract(payload, '$.property.has_link') = 1) || (visibility = "PUBLIC" && space = "spaces/a"))`,
		inline(got))
	assert.Equal(t, map[string]any{"_a0": "PUBLIC", "_a1": "spaces/a"}, got.Argv)
}

func TestCompile_DerivedBooleanField(t *testing.T) {
	got := mustCompile(t, `has_link`)
	assert.Equal(t, `(json_extract(payload, '$.property.has_link') = 1)`, got.Fragment)
	assert.Empty(t, got.Argv)
}

func TestCompile_BareBooleanField(t *testing.T) {
	got := mustCompile(t, `pinned == true`)
	assert.Equal(t, `pinned = true`, inline(got))
	assert.Equal(t, map[string]any{"_a0": true}, got.Argv)
}

// The SPA's pinned chip emits the bare column name, not a comparison
// (web/src/hooks/useMemoFilters.ts builds `pinned` and
// combineCELFilters wraps it in parentheses when it joins clauses).
// A bare reference to a boolean column therefore has to compile the
// same way `pinned == true` does — otherwise the chip's whole filter
// is rejected as a half-typed expression.
func TestCompile_BareBooleanColumnIsCoalesced(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want string
		argv map[string]any
	}{
		{"bare", `pinned`, `pinned = true`, map[string]any{}},
		{"grouped", `(pinned)`, `pinned = true`, map[string]any{}},
		{"andDerived", `pinned && has_link`,
			`(pinned = true && (json_extract(payload, '$.property.has_link') = 1))`, map[string]any{}},
		{"orWithCompare", `pinned || visibility == "PUBLIC"`,
			`(pinned = true || visibility = "PUBLIC")`, map[string]any{"_a0": "PUBLIC"}},
		{"negatedBare", `!pinned`, `pinned != true`, map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustCompile(t, tc.expr)
			assert.Equal(t, tc.want, inline(got))
			// Only the comparison's literal is bound; the boolean
			// column itself never becomes a parameter.
			assert.Equal(t, tc.argv, got.Argv)
		})
	}
}

// A bare reference to a non-boolean column stays a value, so Compile
// still reports the half-typed filter instead of guessing between
// `= null` and `= ""`.
func TestCompile_BareNonBooleanColumnStillRejected(t *testing.T) {
	_, err := compileExpr(t, `content`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not produce a boolean")
}

// Two literals in one expression get two parameter names, so the
// single Argv map cannot collide.
func TestCompile_DistinctParameterNames(t *testing.T) {
	got := mustCompile(t, `space == "x" && space == "x"`)
	assert.Equal(t, `(space = "x" && space = "x")`, inline(got))
	assert.Equal(t, map[string]any{"_a0": "x", "_a1": "x"}, got.Argv)
}

// ---------------------------------------------------------------------------
// Timestamps
// ---------------------------------------------------------------------------

// PocketBase stores AutodateField columns as TEXT in this exact
// layout; a mismatch here makes every calendar filter return nothing.
func TestCompile_TimestampRange(t *testing.T) {
	got := mustCompile(t, `created_ts >= timestamp("2025-12-01T00:00:00Z") && created_ts < timestamp("2026-01-01T00:00:00Z")`)
	assert.Equal(t,
		`(created >= "2025-12-01 00:00:00.000Z" && created < "2026-01-01 00:00:00.000Z")`,
		inline(got))
}

// The SPA's calendar (web/src/lib/calendar-utils.ts) writes the
// conversion with an integer: `created_ts >= timestamp(1749340800)`.
// CEL defines timestamp(int) as Unix seconds, and rejecting it made
// every day/month filter a 400.
func TestCompile_TimestampFromEpochSeconds(t *testing.T) {
	got := mustCompile(t, `created_ts >= timestamp(1749340800) && created_ts < timestamp(1749427200)`)
	assert.Equal(t,
		`(created >= "2025-06-08 00:00:00.000Z" && created < "2025-06-09 00:00:00.000Z")`,
		inline(got))
}

func TestCompile_TimestampRejectsBadArityAndType(t *testing.T) {
	for _, src := range []string{
		`created_ts >= timestamp()`,
		`created_ts >= timestamp("2025-01-01T00:00:00Z", "2025-01-02T00:00:00Z")`,
		`created_ts >= timestamp("not a timestamp")`,
		`created_ts >= timestamp(content)`,
	} {
		_, err := Compile(src, Options{Fields: []Field{{Name: "content", Kind: FieldColumn, Column: "content"}, {Name: "created_ts", Kind: FieldColumn, Column: "created"}}})
		require.Error(t, err, "Compile(%q) should fail", src)
	}
}

func TestCompile_NowIsPinned(t *testing.T) {
	got := mustCompile(t, `created_ts < now`)
	assert.Equal(t, `created < "2026-01-02 03:04:05.000Z"`, inline(got))
}

// The format must track PocketBase's own constant, not a guess.
func TestPocketbaseDateTimeMatchesPocketBaseLayout(t *testing.T) {
	assert.Equal(t,
		types.DefaultDateLayout,
		"2006-01-02 15:04:05.000Z",
		"internal/cel duplicates tools/types.DefaultDateLayout; the two must not drift")
	assert.Equal(t, "2026-01-02 03:04:05.000Z", pocketbaseDateTime(frozenNow))
}

// ---------------------------------------------------------------------------
// Rejections. Every one of these used to be silently ignored.
// ---------------------------------------------------------------------------

func TestCompile_RejectsVisibilityOutsideConst(t *testing.T) {
	// Without the Const check this compiles to a query matching no
	// rows, which reads to the user as "the filter does nothing".
	_, err := compileExpr(t, `visibility == "PUBLIK"`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PUBLIK")
}

func TestCompile_RejectsVisibilityOutsideConstInList(t *testing.T) {
	_, err := compileExpr(t, `visibility in ["PUBLIC", "PUBLIK"]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PUBLIK")
}

func TestCompile_RejectsUnknownField(t *testing.T) {
	for _, src := range []string{`evil == "x"`, `sort_by == "x"`, `has_incomplete_tasks == "x"`} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, src)
		assert.Contains(t, err.Error(), "unknown field")
	}
}

func TestCompile_RejectsUnknownFunction(t *testing.T) {
	_, err := compileExpr(t, `rand("x")`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rand")
}

// Constructs the PocketBase filter language cannot express are
// refused rather than approximated with a different (wrong) query.
func TestCompile_RejectsInexpressibleConstructs(t *testing.T) {
	for _, src := range []string{
		`sets.intersects(tags, ["a"])`,
		`sets.equivalent(tags, ["a"])`,
		`tags.all("y")`,
		`tags.exists_one("z")`,
		`size(content) > 10`,
		// `matches` is still refused: LIKE has no regex engine.
		`content.matches("c")`,
		`created_ts.getFullYear() == 2025`,
		`created_ts > duration("24h")`,
		`created_ts > duration("24h") * 2`,
	} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "%q should be rejected", src)
	}
}

// An array column has no scalar equality; `tags == "x"` is a mistake
// for `tags.exists("x")`.
func TestCompile_ListEqualityMeansMembership(t *testing.T) {
	// `tag == "work"` is what older saved views carry; the only
	// reading it can have is membership, so it compiles to the same
	// test as `"work" in tags`.
	assert.Equal(t, mustCompile(t, `"work" in tags`).Fragment,
		mustCompile(t, `tag == "work"`).Fragment)

	// The complement of the four LIKE patterns has no expression.
	_, err := compileExpr(t, `tags != "work"`)
	require.Error(t, err)

	// A list field only compares against a string.
	_, err = compileExpr(t, `tags == 1`)
	require.Error(t, err)
}

// A bare column reference is a half-typed filter.
func TestCompile_RejectsBareColumn(t *testing.T) {
	_, err := compileExpr(t, `space`)
	require.Error(t, err)
}

func TestCompile_RejectsBareNull(t *testing.T) {
	_, err := compileExpr(t, `null`)
	require.Error(t, err)
}

func TestCompile_RejectsBareValueExpression(t *testing.T) {
	// `now` and `timestamp(...)` alone are values, not predicates.
	for _, src := range []string{`now`, `timestamp("2025-01-01T00:00:00Z")`, `duration("5m")`} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "%q should be rejected", src)
	}
}

func TestCompile_RejectsEmpty(t *testing.T) {
	for _, src := range []string{"", "   "} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "Compile(%q)", src)
	}
}

// A filter is bounded in both length and nesting. Every term becomes its
// own clause in the emitted PocketBase filter, so an unbounded filter is
// an unbounded amount of work for the database — and the recursive
// descent has no other recursion, so deep grouping is also the only way
// an input could drive the goroutine stack.
//
// Both limits are what make the worst case cheap: before them, a 256 KB
// filter took 7.16s and a 1.6 MB one exhausted memory.
func TestCompile_RejectsOverlongFilter(t *testing.T) {
	src := strings.TrimSuffix(strings.Repeat(`space == "a" || `, 2000), "|| ")
	require.Greater(t, len(src), maxFilterLength)
	_, err := compileExpr(t, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
}

// A filter right at the limit must still compile — the guard is a
// ceiling, not a cliff.
func TestCompile_AcceptsFilterAtLimit(t *testing.T) {
	src := strings.TrimSuffix(strings.Repeat(`space == "a" || `, maxFilterLength/16), "|| ")
	require.LessOrEqual(t, len(src), maxFilterLength)
	_, err := compileExpr(t, src)
	require.NoError(t, err)
}

func TestCompile_RejectsDeepNesting(t *testing.T) {
	// A depth of maxParseDepth+1 costs 2 bytes per level, so this stays
	// under maxFilterLength — otherwise the length guard would fire
	// first and the depth guard would go untested.
	for _, depth := range []int{maxParseDepth + 1, maxFilterLength / 4} {
		src := strings.Repeat("(", depth) + `space == "a"` + strings.Repeat(")", depth)
		require.LessOrEqual(t, len(src), maxFilterLength, "length guard must not mask the depth guard")
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "depth %d should be rejected", depth)
		assert.Contains(t, err.Error(), "nested")
	}
}

// Negation is the other recursive production, so it needs the same guard:
// `!!!!…x` must not be able to recurse without bound either.
func TestCompile_RejectsDeepNegation(t *testing.T) {
	src := strings.Repeat("!", maxFilterLength/2) + `space == "a"`
	require.LessOrEqual(t, len(src), maxFilterLength, "length guard must not mask the depth guard")
	_, err := compileExpr(t, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested")
}

// Both guards are reachable: a filter over the length ceiling is refused
// on length even when it is shallow, and one under it can still be
// refused on depth.
func TestCompile_LengthAndDepthGuardsAreIndependent(t *testing.T) {
	// Real nesting, at 2 bytes per level, so this exceeds maxParseDepth
	// but stays under maxFilterLength and the depth guard is what fires.
	deep := strings.Repeat("(", maxParseDepth+1) + `space == "a"` + strings.Repeat(")", maxParseDepth+1)
	require.LessOrEqual(t, len(deep), maxFilterLength)
	_, err := compileExpr(t, deep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested")

	// Shallow but long, so the length guard is what fires.
	long := strings.TrimSuffix(strings.Repeat(`space == "a" || `, 2000), "|| ")
	require.Greater(t, len(long), maxFilterLength)
	_, err = compileExpr(t, long)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
}

// Nesting at exactly the limit is accepted, and the depth counter is
// unwound on the way out so sibling groups are not penalised.
func TestCompile_AcceptsNestingAtLimit(t *testing.T) {
	src := strings.Repeat("(", maxParseDepth) + `space == "a"` + strings.Repeat(")", maxParseDepth)
	_, err := compileExpr(t, src)
	require.NoError(t, err)

	// Sibling groups reuse the counter: this is 2×maxParseDepth levels
	// of nesting overall, but no single path is deeper than the limit.
	// They must all still compile.
	pairs := strings.Repeat(`(space == "a") && `, maxParseDepth)
	pairs = strings.TrimSuffix(pairs, "&& ")
	_, err = compileExpr(t, pairs)
	require.NoError(t, err)
}

func TestCompile_RejectsDerivedFieldComparison(t *testing.T) {
	_, err := compileExpr(t, `has_link == "x"`)
	require.Error(t, err)
}

func TestCompile_RejectsUnterminatedString(t *testing.T) {
	_, err := compileExpr(t, `space == "unterminated`)
	require.Error(t, err)
}

// A string literal is bound, never concatenated, so a quote in the
// payload cannot escape into the filter.
func TestCompile_LiteralsAreBound(t *testing.T) {
	got := mustCompile(t, `space == "x'; DROP TABLE memos; --"`)
	assert.NotContains(t, got.Fragment, "DROP TABLE")
	assert.Equal(t, "x'; DROP TABLE memos; --", got.Argv["_a0"])
}

// ---------------------------------------------------------------------------
// The attachment library filter the Attachments page sends.
// ---------------------------------------------------------------------------

func TestCompile_AttachmentMemoIdNull(t *testing.T) {
	got := mustCompile(t, `memo_id == null`, attachmentFields()...)
	assert.Equal(t, `memo = null`, got.Fragment)
}

func TestCompile_AttachmentSpaceFiltered(t *testing.T) {
	got := mustCompile(t, `(space == "spaces/team") && (memo_id == null)`, attachmentFields()...)
	// `space` is the linked memo's space, not a column on
	// attachments, so the compiled fragment follows the relation.
	assert.Equal(t, `(memo.space = "spaces/team" && memo = null)`, inline(got))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// inline replaces every bound placeholder with its quoted value so a
// test can pin the exact filter text without depending on the
// parameter names the parser allocates.
func inline(got *Expression) string {
	out := got.Fragment
	for name, v := range got.Argv {
		out = strings.ReplaceAll(out, "{:"+name+"}", quoteValue(v))
	}
	return out
}

func quoteValue(v any) string {
	switch t := v.(type) {
	case string:
		return `"` + t + `"`
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return "?"
	}
}

// ---------------------------------------------------------------------------
// Negation. fexpr has no `!` operator, so `!x` is pushed down through
// De Morgan's laws by negate().
// ---------------------------------------------------------------------------

func TestCompile_NegatesComparison(t *testing.T) {
	assert.Equal(t, `space != "a"`, inline(mustCompile(t, `!space == "a"`)))
	assert.Equal(t, `space = "a"`, inline(mustCompile(t, `!!space == "a"`)))
	assert.Equal(t, `content !~ "%x%"`, inline(mustCompile(t, `!content.contains("x")`)))
}

func TestCompile_NegatesThroughAnd(t *testing.T) {
	// !(a && b) -> !a || !b
	got := mustCompile(t, `!(space == "a" && visibility == "PUBLIC")`)
	assert.Equal(t, `((space != "a")||(visibility != "PUBLIC"))`, inline(got))
}

func TestCompile_NegatesThroughOr(t *testing.T) {
	// !(a || b) -> !a && !b
	got := mustCompile(t, `!(space == "a" || space == "b")`)
	assert.Equal(t, `((space != "a")&&(space != "b"))`, inline(got))
}

// A derived predicate has no negated form in the filter language.
func TestCompile_RejectsNegatingDerivedPredicate(t *testing.T) {
	_, err := compileExpr(t, `!has_link`)
	require.Error(t, err)
}

// The membership expansion is a disjunction of LIKE patterns, not
// comparisons, so De Morgan has nothing to flip.
func TestCompile_RejectsNegatingListMembership(t *testing.T) {
	_, err := compileExpr(t, `!tags.exists("x")`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negating")
}

// Ordering comparisons have no negation either: !x > y would need the
// value's bounds inverted, and the compiler does not know the type.
func TestCompile_RejectsNegatingOrdering(t *testing.T) {
	_, err := compileExpr(t, `!(created_ts > now)`)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// The CEL spellings the public contract documents. Each of these used
// to be a CodeInvalidArgument on a filter the proto, the MCP tool
// description or a shipped saved-view example told the user to write.
// ---------------------------------------------------------------------------

// `"work" in tags` — the proto's documented membership form, and the
// placeholder of the SPA's quick-find CEL mode. The mirror spelling
// `tag in ["work"]` already compiled.
func TestCompile_ValueInListField(t *testing.T) {
	got := mustCompile(t, `"work" in tags`)
	assert.Equal(t, mustCompile(t, `tag in ["work"]`).Fragment, got.Fragment)

	// Inside a conjunction, and with CEL's single-quote form.
	got = mustCompile(t, `'work' in tags && visibility == "PUBLIC"`)
	assert.Contains(t, got.Fragment, "visibility = {:")
	bound := false
	for _, v := range got.Argv {
		if v == `["work"]` {
			bound = true
		}
	}
	assert.True(t, bound, "the tag must be bound as a JSON element, got %v", got.Argv)
}

func TestCompile_RejectsValueInNonList(t *testing.T) {
	for _, src := range []string{
		`"x" in visibility`,
		`"x" in ["x"]`,
		`1 in tags`,
		`space in tags`,
	} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "%q should be rejected", src)
	}
}

// `tags.exists(t, t == "work")` is the element-binding form the proto
// documents. It has no per-element expression to evaluate, so only the
// equality predicate is accepted — and it compiles to the same
// membership test as `tags.exists("work")`.
func TestCompile_TagsExistsElementBinding(t *testing.T) {
	one := mustCompile(t, `tags.exists("work")`)

	got := mustCompile(t, `tags.exists(t, t == "work")`)
	assert.Equal(t, one.Fragment, got.Fragment)
	assert.Equal(t, one.Argv, got.Argv)

	// CEL allows either operand order.
	assert.Equal(t, one.Fragment, mustCompile(t, `tags.exists(t, "work" == t)`).Fragment)
}

func TestCompile_RejectsTagsExistsNonEquality(t *testing.T) {
	for _, src := range []string{
		`tags.exists(t, t.startsWith("a"))`,
		`tags.exists(t, t != "a")`,
		`tags.exists(t, t == "a" && t == "b")`,
	} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "%q should be rejected", src)
	}
}

// `now - duration("24h")` is CEL's relative-date idiom: the proto
// documents it and the saved-view examples ship it. The subtraction is
// folded into a bound timestamp here, because the filter language has
// no arithmetic of its own.
func TestCompile_FoldsRelativeDuration(t *testing.T) {
	got := mustCompile(t, `created_ts > now - duration("24h")`)
	assert.Equal(t, `created > "2026-01-01 03:04:05.000Z"`, inline(got))

	got = mustCompile(t, `created_ts <= now + duration("1h")`)
	assert.Equal(t, `created <= "2026-01-02 04:04:05.000Z"`, inline(got))

	got = mustCompile(t, `created_ts >= timestamp("2026-01-01T00:00:00Z") - duration("30m")`)
	assert.Equal(t, `created >= "2025-12-31 23:30:00.000Z"`, inline(got))
}

// CEL's boolean literals are complete predicates. Saved views spell
// "match everything" as `true`; PocketBase's grammar has no
// comparison-free boolean, so it becomes a test on the record id.
func TestCompile_BooleanLiterals(t *testing.T) {
	assert.Equal(t, `id != ""`, inline(mustCompile(t, `true`)))
	assert.Equal(t, `id = ""`, inline(mustCompile(t, `false`)))
	assert.Equal(t, `id = ""`, inline(mustCompile(t, `!true`)))
	assert.Equal(t, `(id != "" && pinned = true)`, inline(mustCompile(t, `true && pinned`)))
}

// `filename.contains("test")` is the attachment contract's own
// example. The `~` operator is the same one content.contains uses,
// just bound to whatever column the schema names.
func TestCompile_FieldContains(t *testing.T) {
	// The pattern arrives pre-padded (`%test%`); see likeWildcards.
	assert.Equal(t, `filename ~ "%test%"`, inline(mustCompile(t, `filename.contains("test")`, attachmentFields()...)))

	// A list field membership-tests instead of substring-matching.
	assert.Equal(t, mustCompile(t, `tags.exists("work")`).Fragment,
		mustCompile(t, `tags.contains("work")`).Fragment)

	_, err := compileExpr(t, `title.contains("x")`)
	require.Error(t, err)
}

// `size(tags) == 0` is a shipped saved-view example ("Untagged"). The
// filter language has no length operator, but emptiness is expressible
// through the JSON column's empty-array / NULL representation.
func TestCompile_SizeOfListField(t *testing.T) {
	assert.Equal(t, `((tags = null) || (tags = "[]"))`, inline(mustCompile(t, `size(tags) == 0`)))
	assert.Equal(t, `((tags != null) && (tags != "[]"))`, inline(mustCompile(t, `size(tags) != 0`)))

	for _, src := range []string{`size(tags) == 1`, `size(tags) > 0`, `size(content) == 0`} {
		_, err := compileExpr(t, src)
		require.Errorf(t, err, "%q should be rejected", src)
	}
}

// A derived predicate carries no operator to flip, so `!has_location`
// only compiles when the caller supplies its complement.
func TestCompile_NegatedDerivedField(t *testing.T) {
	fields := append(memoFields(), Field{
		Name:        "has_location",
		Kind:        FieldBoolExpr,
		Expr:        "payload.location.placeholder != null && payload.location.latitude != null",
		NegatedExpr: "payload.location.placeholder = null || payload.location.latitude = null",
	})

	got := mustCompile(t, `!has_location`, fields...)
	assert.Equal(t, `(payload.location.placeholder = null || payload.location.latitude = null)`, got.Fragment)

	// Without a complement there is nothing to emit.
	_, err := compileExpr(t, `!has_link`)
	require.Error(t, err)
}

// `creator` on attachments is the linked memo's author for a bound row
// and the uploader for an unlinked one: the column is only stamped on
// orphans (pb/migrations/1890000000_…), so a plain column comparison
// would never match a bound attachment.
func TestCompile_AttachmentCreatorFallback(t *testing.T) {
	got := mustCompile(t, `creator == "users/alice"`, attachmentFields()...)
	assert.Equal(t,
		`((memo.author = "users/alice") || ((creator = "users/alice") && (memo = null)))`,
		inline(got))

	// Only equality has a meaning here; the complement of the
	// disjunction is not expressible.
	_, err := compileExpr(t, `creator != "users/alice"`, attachmentFields()...)
	require.Error(t, err)
}

// TestCompile_ReportsTextMatches pins the TextMatches report that
// pb/connect/memo_fts.go uses to decide whether an FTS index can
// replace a LIKE clause.
//
// The report has to carry the *unpadded* literal: the fragment only
// ever contains the padded, escaped pattern, so a caller cannot recover
// the caller's own text from it.
//
// A negated term must NOT be reported. `!content.contains("x")`
// compiles to `content !~ …`, and an index narrows to "contains x";
// substituting it would turn a negative filter into a positive one and
// return exactly the wrong rows. Absence is the safe signal — the
// caller keeps the LIKE clause and stays correct.
func TestCompile_ReportsTextMatches(t *testing.T) {
	t.Run("plain contains is reported", func(t *testing.T) {
		got := mustCompile(t, `content.contains("roadmap")`)
		require.Len(t, got.TextMatches, 1)
		assert.Equal(t, "content", got.TextMatches[0].Field)
		assert.Equal(t, "contains", got.TextMatches[0].Op)
		assert.Equal(t, "roadmap", got.TextMatches[0].Value)
		assert.NotEmpty(t, got.TextMatches[0].ArgKey)
		assert.Equal(t, "%roadmap%", got.Argv[got.TextMatches[0].ArgKey],
			"ArgKey must point at the padded LIKE pattern")
	})

	t.Run("starts and ends are reported with their operator", func(t *testing.T) {
		sw := mustCompile(t, `content.startsWith("ab")`).TextMatches
		require.Len(t, sw, 1)
		assert.Equal(t, "startsWith", sw[0].Op)
		assert.Equal(t, "ab", sw[0].Value)

		ew := mustCompile(t, `content.endsWith("yz")`).TextMatches
		require.Len(t, ew, 1)
		assert.Equal(t, "endsWith", ew[0].Op)
	})

	t.Run("negated term is not reported", func(t *testing.T) {
		assert.Empty(t, mustCompile(t, `!content.contains("x")`).TextMatches,
			"an index would invert the meaning; the caller must keep LIKE")
		assert.Empty(t, mustCompile(t, `!(content.contains("x"))`).TextMatches)
		// Negation pushed down through a conjunction.
		assert.Empty(t, mustCompile(t, `!(content.contains("x") && space == "s")`).TextMatches)
	})

	t.Run("conjunction reports each term in source order", func(t *testing.T) {
		got := mustCompile(t, `content.contains("alpha") && content.contains("beta")`)
		require.Len(t, got.TextMatches, 2)
		assert.Equal(t, "alpha", got.TextMatches[0].Value)
		assert.Equal(t, "beta", got.TextMatches[1].Value)
	})

	t.Run("non-string terms are ignored", func(t *testing.T) {
		assert.Empty(t, mustCompile(t, `space == "s"`).TextMatches)
		assert.Empty(t, mustCompile(t, `size(tags) == 0`).TextMatches)
		assert.Empty(t, mustCompile(t, `"work" in tags`).TextMatches)
	})

	t.Run("a non-text conjunct does not suppress a text one", func(t *testing.T) {
		got := mustCompile(t, `content.contains("roadmap") && space == "s"`)
		require.Len(t, got.TextMatches, 1, "the index narrows rows; the conjunct still has to apply")
		assert.Equal(t, "roadmap", got.TextMatches[0].Value)
	})

	t.Run("escaped metacharacters are reported unpadded", func(t *testing.T) {
		got := mustCompile(t, `content.contains("100%")`)
		require.Len(t, got.TextMatches, 1)
		assert.Equal(t, "100%", got.TextMatches[0].Value,
			"the caller wrote 100%, not the escaped LIKE pattern")
	})
}
