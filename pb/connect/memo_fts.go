package connect

import (
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/search"

	"github.com/incurl/riff/internal/cel"
)

// FTS support for ListMemos.
//
// The index is created by pb/migrations/1910000000_create_memos_fts.go;
// this file is the read side. Two constraints shape it:
//
//  1. PocketBase's filter grammar cannot express `MATCH`. Its only
//     subquery escape, MultiMatchSubQuery, is populated exclusively for
//     *relations* (core/record_field_resolver_runner.go), so FTS cannot
//     be a FilterField in internal/cel. It has to be added as a raw
//     AndWhere on top of the compiled filter.
//
//  2. app.RecordQuery returns a bare *dbx.SelectQuery
//     (core/record_query.go:26), which is the same object
//     FindRecordsByFilter builds internally. We rebuild that function's
//     body here — resolver, sort, offset, limit — rather than calling
//     it, because the only way to inject the MATCH predicate is to
//     build the query ourselves. The rebuild is deliberate and
//     mirrored: records still come back as *core.Record, so
//     CanAccessRecord and the stub filter in ListMemos keep applying.
//
// **Security.** The FTS table is a derived index, never an authority.
// It narrows candidates; memos.ViewRule decides who sees them, in
// ListMemos, per row, exactly as it does for the LIKE path. There is no
// code path here that returns a memo the caller may not read.

// ftsMatchExpr renders the MATCH predicate that restricts `memos` to
// the rows whose content matches `query`.
//
// The subquery goes through `rowid` because an external-content FTS5
// table has no id column of its own; the join is left to the caller's
// SELECT so the shape stays identical to the non-FTS path.
func ftsMatchExpr(query string) dbx.Expression {
	// Identifiers are written unquoted on purpose. dbx substitutes
	// {{…}} / [[…]] only in NewQuery's SQL text (DB.processSQL); an
	// `Exp.Build` returns the fragment verbatim, so a `{{memos_fts}}`
	// here reaches SQLite literally and fails with "no such column:
	// memos_fts". These are riff's own constant identifiers, never user
	// input, so quoting them is not a security decision.
	return dbx.NewExp(
		`memos.rowid IN (SELECT rowid FROM memos_fts WHERE memos_fts MATCH {:riff_fts})`,
		dbx.Params{"riff_fts": query},
	)
}

// ftsRelevanceOrder is the relevance ordering. bm25 is only in scope
// when the FTS table is part of the query, which is why it is applied
// only on the FTS path and why a plain feed must never take it.
const ftsRelevanceOrder = `-bm25(memos_fts)`

// ftsSearchQuery turns user-supplied search text into an FTS5 MATCH
// expression.
//
// User text is *not* a literal in MATCH: it is FTS5 query syntax, where
// `"` opens a phrase, and bare AND/OR/NOT/*/:/^ change the meaning. A
// user's apostrophe would be a syntax error and a user's "OR" would
// silently widen the search. So the text is quoted into a single
// phrase, with an inner `"` doubled per the FTS5 string-literal rules.
//
// Each whitespace-separated word becomes its own quoted phrase joined
// with an implicit AND, which is what a user typing several words
// expects: "roadmap draft" should require both terms rather than either.
// Words are quoted *individually* so that a word containing a space
// cannot merge with its neighbour.
//
// Returns "" when the query has no usable term, meaning the caller
// should not restrict anything.
func ftsSearchQuery(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(fields))
	for _, w := range fields {
		if w == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, " ")
}

// findMemosWithOptionalFTS is ListMemos' row fetcher.
//
// ftsQuery is the already-escaped MATCH expression, or "" to skip the
// index entirely (the LIKE fallback, and every filter that carries no
// search term). When it is set, relevance ordering is applied so the
// best match leads; otherwise the caller's sort is used unchanged.
//
// The resolver/sort/limit/offset sequence mirrors
// BaseApp.FindRecordsByFilter. Diverging from it silently is how a
// relation-traversing filter (or an access-rule join) stops being
// applied, so keep the two in step if PocketBase changes.
func findMemosWithOptionalFTS(
	app core.App,
	collection *core.Collection,
	filter string,
	sort string,
	limit, offset int,
	params map[string]any,
	ftsQuery string,
) ([]*core.Record, error) {
	q := app.RecordQuery(collection)

	resolver := core.NewRecordFieldResolver(
		app,
		collection, // the base collection
		nil,        // no request data
		true,       // allow searching hidden/protected fields like "email"
	)

	if filter != "" {
		expr, err := search.FilterData(filter).BuildExpr(resolver, params)
		if err != nil {
			return nil, err
		}
		q.AndWhere(expr)
	}

	// The index narrows candidates; it never grants access. The caller's
	// per-row CanAccessRecord check is what enforces the ViewRule.
	if ftsQuery != "" {
		q.AndWhere(ftsMatchExpr(ftsQuery))
	}

	if sort != "" {
		for _, sortField := range search.ParseSortFromString(sort) {
			expr, err := sortField.BuildExpr(resolver)
			if err != nil {
				return nil, err
			}
			if expr != "" {
				q.AndOrderBy(expr)
			}
		}
	}

	// Relevance ordering needs the FTS table to be *in* the query, not
	// just referenced by a subquery: bm25 is an auxiliary function of
	// the virtual table, so it is only in scope once that table is
	// joined. Without the join the ORDER BY fails with "no such column".
	if ftsQuery != "" {
		q.InnerJoin("memos_fts", dbx.NewExp(
			`memos.rowid = memos_fts.rowid`,
		))
		q.AndOrderBy(ftsRelevanceOrder)
	}

	if err := resolver.UpdateQuery(q); err != nil {
		return nil, err
	}

	if offset > 0 {
		q.Offset(int64(offset))
	}
	if limit > 0 {
		q.Limit(int64(limit))
	}

	records := []*core.Record{}
	if err := q.All(&records); err != nil {
		return nil, err
	}
	return records, nil
}

// memoFTSRewrite removes the LIKE clause a positive content-match term
// compiled to, and returns the MATCH expression that replaces it.
//
// The LIKE clause is located by its bound parameter rather than by
// re-parsing the fragment: the compiler reports the Argv key of every
// text term, and `content ~ {:_a3}` is exactly the shape it emits. If
// the fragment does not match the expected shape the rewrite is
// abandoned and the LIKE clause is kept — an unrewritten filter is
// merely slow, whereas a wrongly rewritten one returns the wrong rows.
//
// It refuses when the filter has more than one text term. Two positive
// terms are an intersection of two LIKE scans; one MATCH expression
// cannot express that faithfully (a two-word query is an AND, but a
// filter like `contains("a") && startsWith("b")` is not), so both are
// left to LIKE rather than approximated.
//
// ftsQuery is "" when nothing was rewritten.
func memoFTSRewrite(
	filter string,
	filterSQL string,
	filterArgs map[string]any,
) (string, map[string]any, string) {
	if strings.TrimSpace(filter) == "" {
		return filterSQL, filterArgs, ""
	}
	matches := memoCELTextMatches(filter)
	if len(matches) != 1 {
		// Zero: nothing to accelerate. More than one: not expressible
		// as one MATCH. Both keep the LIKE clause.
		return filterSQL, filterArgs, ""
	}
	m := matches[0]
	if m.Field != "content" || m.Op != "contains" || m.ArgKey == "" {
		// Only a plain substring search maps onto MATCH. startsWith and
		// endsWith have no equivalent in a token index, and a query on
		// any other column is not what this index covers.
		return filterSQL, filterArgs, ""
	}

	needle, ok := filterArgs[m.ArgKey].(string)
	if !ok || needle == "" {
		return filterSQL, filterArgs, ""
	}
	// The bound value is the padded, LIKE-escaped pattern. Undo both so
	// the MATCH expression sees the text the user typed.
	term := strings.TrimSuffix(strings.TrimPrefix(needle, "%"), "%")
	term = unescapeLikeLiteral(term)
	query := ftsSearchQuery(term)
	if query == "" {
		return filterSQL, filterArgs, ""
	}

	// The compiler emits an *unquoted* column (`stringMatch` builds
	// `column + " ~ " + literal`, and the column is the bare column
	// name). Quoting it here would never match, and a mismatch silently
	// disables FTS — so match exactly what the compiler writes.
	clause := fmt.Sprintf(`content ~ {:%s}`, m.ArgKey)
	if !strings.Contains(filterSQL, clause) {
		// The fragment does not have the documented shape. Keep the LIKE
		// clause rather than guess: unrewritten is merely slow, wrongly
		// rewritten returns the wrong rows.
		return filterSQL, filterArgs, ""
	}

	remaining := strings.Replace(filterSQL, clause, "", 1)
	remaining = strings.TrimSpace(remaining)
	// The surrounding `&&` / `||` leaves a dangling operator once the
	// clause is removed. Strip it, and drop a filter that is now empty.
	trimmed := trimDanglingJoin(remaining)
	if trimmed == "" {
		return "", nil, query
	}
	return trimmed, filterArgs, query
}

// memoCELTextMatches re-derives the text-match report for a filter that
// has already been rewritten once (the identity rewriter resolves
// space/creator names to ids). Compiling again is cheap relative to the
// query it guards, and re-using the first compiled result would mean
// threading a field the LIKE path never needed.
func memoCELTextMatches(filter string) []cel.TextMatch {
	compiled, err := cel.Compile(filter, cel.Options{Now: time.Now(), Fields: memoCELFields()})
	if err != nil {
		return nil
	}
	return compiled.TextMatches
}

// trimDanglingJoin removes the connective that a removed clause left
// behind: "a && " -> "a", " && b" -> "b", "a || " -> "a".
func trimDanglingJoin(s string) string {
	s = strings.TrimSpace(s)
	for _, op := range []string{" && ", " || "} {
		if rest, cut := strings.CutSuffix(s, op); cut {
			return strings.TrimSpace(rest)
		}
		if rest, cut := strings.CutPrefix(s, op); cut {
			return strings.TrimSpace(rest)
		}
	}
	return s
}

// unescapeLikeLiteral reverses the escaping likeWildcards applies for
// PocketBase's `ESCAPE '\'`: a `\%` or `\_` becomes a literal `%`/`_`.
// A trailing lone backslash is left alone, since likeWildcards never
// produces one.
func unescapeLikeLiteral(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		default:
			b.WriteRune(r)
		}
	}
	if esc {
		b.WriteRune('\\')
	}
	return b.String()
}

// findMemos fetches the rows for a memo list.
//
// With ftsQuery empty this is exactly FindRecordsByFilter with
// `-created` sorting, so the disabled path is the pre-existing
// behaviour rather than a reimplementation of it. With ftsQuery set it
// routes through findMemosWithOptionalFTS, which adds the MATCH
// predicate and relevance ordering.
//
// Either way the caller still applies CanAccessRecord per row: the
// index narrows candidates, the ViewRule decides visibility.
func (s *MemoServer) findMemos(
	collection *core.Collection,
	filter string,
	limit, offset int,
	params map[string]any,
	ftsQuery string,
) ([]*core.Record, error) {
	if ftsQuery == "" {
		return s.app.FindRecordsByFilter(collection, filter, "-created", limit, offset, params)
	}
	return findMemosWithOptionalFTS(
		s.app, collection, filter, "-created", limit, offset, params, ftsQuery,
	)
}
