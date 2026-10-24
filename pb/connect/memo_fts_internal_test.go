package connect

import (
	"context"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// listFTSMemoIDs is listMemoIDs with the FTS path enabled. It is the
// same handler; the only difference is the flag, which is why the two
// can be compared directly.
//
// It returns memo *contents* rather than names so a leak assertion can
// name the offending row directly. The equivalence tests map them back
// to names before comparing against listMemoIDs.
func listFTSMemoIDs(ctx context.Context, t *testing.T, app core.App, req *v1.ListMemosRequest) []string {
	t.Helper()
	srv := NewMemoServer(app)
	srv.SetFTSSearch(true)
	res, err := srv.ListMemos(ctx, req)
	require.NoError(t, err)
	out := make([]string, 0, len(res.GetMemos()))
	for _, m := range res.GetMemos() {
		out = append(out, m.GetContent())
	}
	return out
}

// listFTSMemoNames is listFTSMemoIDs keyed by resource name, so it can
// be compared element-wise against listMemoIDs.
func listFTSMemoNames(ctx context.Context, t *testing.T, app core.App, req *v1.ListMemosRequest) []string {
	t.Helper()
	srv := NewMemoServer(app)
	srv.SetFTSSearch(true)
	res, err := srv.ListMemos(ctx, req)
	require.NoError(t, err)
	out := make([]string, 0, len(res.GetMemos()))
	for _, m := range res.GetMemos() {
		out = append(out, m.GetName())
	}
	return out
}

// listFTSMemoIDsAnon is listFTSMemoIDs with no authenticated viewer,
// which is the case the security test depends on.
func listFTSMemoIDsAnon(t *testing.T, app core.App, filter string) []string {
	t.Helper()
	return listFTSMemoIDs(t.Context(), t, app, &v1.ListMemosRequest{Filter: filter})
}

// requireFTSIndex fails when the migration did not run, so these tests
// cannot pass vacuously against an instance without the index.
func requireFTSIndex(t *testing.T, app core.App) {
	t.Helper()
	rows := []struct {
		Name string `db:"name"`
	}{}
	err := app.DB().
		NewQuery(`SELECT [[name]] FROM sqlite_master WHERE [[type]]='table' AND [[name]]='memos_fts'`).
		All(&rows)
	require.NoError(t, err)
	require.Len(t, rows, 1, "memos_fts must exist; the FTS tests are meaningless without it")
}

// TestFTS_FindsTheSameRowsAsLIKE is the equivalence contract. Enabling
// FTS must not change which memos a user can find.
//
// The LIKE path is the reference: it is the behaviour that shipped, and
// TestListMemos_StringMatchFilters pins its semantics. FTS is an
// accelerator, not a new feature — except for case folding, which is
// the entire point and is asserted in TestFTS_FoldsCaseUnlikeLIKE.
func TestFTS_FindsTheSameRowsAsLIKE(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "the quarterly roadmap is drafted", nil)
	newFilterMemo(t, app, alice.Id, "roadmap notes for the review", nil)
	newFilterMemo(t, app, alice.Id, "old archive of notes", nil)
	newFilterMemo(t, app, alice.Id, "totally unrelated shopping list", nil)

	for _, filter := range []string{
		`content.contains("roadmap")`,
		`content.contains("drafted")`,
		`content.contains("shopping")`,
		`content.contains("nothingmatches")`,
		`content.contains("roadmap") && content.contains("drafted")`,
	} {
		like := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: filter})
		fts := listFTSMemoNames(t.Context(), t, app, &v1.ListMemosRequest{Filter: filter})
		assert.ElementsMatch(t, like, fts, "FTS and LIKE must agree for %q", filter)
	}
}

// TestFTS_FoldsCaseUnlikeLIKE is the divergence that justifies the whole
// change, asserted against both paths in one place so the difference
// cannot be quietly lost.
//
// SQLite's LIKE case-folds ASCII only, so a Cyrillic term needs an exact
// case to match under LIKE and is found regardless of case under FTS,
// whose unicode61 tokenizer folds across the full Unicode range.
func TestFTS_FoldsCaseUnlikeLIKE(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "Блины с яблоками на завтрак", nil)
	newFilterMemo(t, app, alice.Id, "a café in Zürich", nil)

	assert.Len(t, listFTSMemoIDsAnon(t, app, `content.contains("блины")`), 1,
		"FTS must fold Cyrillic case")
	assert.Len(t, listFTSMemoIDsAnon(t, app, `content.contains("cafe")`), 1,
		"FTS must fold diacritics")

	assert.Empty(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `content.contains("блины")`}),
		"LIKE is case-sensitive for non-ASCII; this is why FTS exists")
	assert.Empty(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `content.contains("cafe")`}),
		"LIKE cannot fold diacritics; this is why FTS exists")
}

// TestFTS_PrivateMemoIsNeverLeaked is the security test, and the reason
// this file exists.
//
// The FTS table is a derived index with no access control of its own. If
// the handler returned its rows directly, every PRIVATE memo would be
// readable by an anonymous caller who merely guessed a word in it — the
// same class of leak as the /api/files bug in AGENTS.md. The index may
// only ever *narrow* candidates; the ViewRule decides visibility, and
// that check lives in ListMemos and must keep applying.
func TestFTS_PrivateMemoIsNeverLeaked(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	newFilterMemo(t, app, alice.Id, "confidential launch codes", map[string]any{"visibility": "PRIVATE"})
	newFilterMemo(t, app, bob.Id, "confidential budget", map[string]any{"visibility": "PRIVATE"})
	newFilterMemo(t, app, alice.Id, "confidential public note", map[string]any{"visibility": "PUBLIC"})

	assert.ElementsMatch(t, []string{"confidential public note"},
		listFTSMemoIDsAnon(t, app, `content.contains("confidential")`),
		"an anonymous caller must see only the PUBLIC memo")

	assert.ElementsMatch(t,
		[]string{"confidential launch codes", "confidential public note"},
		listFTSMemoIDs(withViewer(t.Context(), alice), t, app, &v1.ListMemosRequest{
			Filter: `content.contains("confidential")`,
		}), "alice sees her own PRIVATE memo and the PUBLIC one, never bob's")

	// Bob sees his own PRIVATE memo *and* the PUBLIC one, but never
	// alice's PRIVATE memo.
	assert.ElementsMatch(t,
		[]string{"confidential budget", "confidential public note"},
		listFTSMemoIDs(withViewer(t.Context(), bob), t, app, &v1.ListMemosRequest{
			Filter: `content.contains("confidential")`,
		}), "bob must see his own PRIVATE memo and the PUBLIC one, never alice's")
}

// TestFTS_SearchSyntaxIsTreatedAsALiteral pins that user text is never
// interpreted as an FTS5 query.
//
// MATCH is a query language: a bare `"` opens a phrase, and bare
// AND/OR/NOT/* change the meaning. Passing the user's text through raw
// would make a search for O'Brien a syntax error and a search for
// "a OR b" silently return twice as much.
func TestFTS_SearchSyntaxIsTreatedAsALiteral(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "O'Brien wrote the memo", nil)
	newFilterMemo(t, app, alice.Id, "the cat sat on the mat", nil)
	newFilterMemo(t, app, alice.Id, `a "quoted" phrase in the body`, nil)

	for _, filter := range []string{
		`content.contains("O'Brien")`,
		`content.contains("a OR b")`,
		`content.contains("\"")`,
		`content.contains("AND")`,
		`content.contains("NEAR(a b)")`,
		`content.contains("cat NOT mat")`,
	} {
		assert.NotPanics(t, func() {
			_ = listFTSMemoIDsAnon(t, app, filter)
		}, "filter %q must not panic on the FTS path", filter)
	}

	assert.Len(t, listFTSMemoIDsAnon(t, app, `content.contains("O'Brien")`), 1,
		"an apostrophe is a literal, not a quote delimiter")
	assert.Len(t, listFTSMemoIDsAnon(t, app, `content.contains("cat NOT mat")`), 0,
		"those words are absent, so nothing matches — but nothing errored either")
}

// TestFTS_NegatedSearchKeepsTheLikePath pins the correctness rule that
// collectTextMatches exists to enforce.
//
// `!content.contains("x")` means "every memo that does NOT contain x".
// An index narrows to "contains x" and cannot express the complement,
// so substituting it would invert the filter and return exactly the
// wrong rows. The compiler refuses to report a negated term, so this
// falls through to LIKE and stays correct.
func TestFTS_NegatedSearchKeepsTheLikePath(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "roadmap notes", nil)
	newFilterMemo(t, app, alice.Id, "unrelated content", nil)
	newFilterMemo(t, app, alice.Id, "another roadmap entry", nil)

	filter := `!content.contains("roadmap")`
	like := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: filter})
	fts := listFTSMemoNames(t.Context(), t, app, &v1.ListMemosRequest{Filter: filter})

	assert.ElementsMatch(t, like, fts, "a negated search must not be served by the index")
	assert.Len(t, fts, 1, "exactly the memo without the term")
}

// TestFTS_FilterWithoutSearchIsUnchanged pins that enabling FTS does not
// perturb the ordinary feed. A filter with no content term takes the
// pre-existing path, and relevance ordering — only valid when the FTS
// table is in the query — must not be applied, or the whole feed would
// reorder or fail.
func TestFTS_FilterWithoutSearchIsUnchanged(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "first memo body", nil)
	newFilterMemo(t, app, alice.Id, "second memo body", nil)

	for _, filter := range []string{
		"",
		`space == "nope"`,
		`visibility == "PUBLIC"`,
		`"work" in tags`,
		`size(tags) == 0`,
	} {
		plain := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: filter})
		withFTS := listFTSMemoNames(t.Context(), t, app, &v1.ListMemosRequest{Filter: filter})
		assert.ElementsMatch(t, plain, withFTS, "filter %q must be unaffected by FTS", filter)
	}
}

// TestFTS_DisabledIsTheDefault pins that an instance only changes search
// behaviour when an operator asks for it. NewMemoServer leaves the flag
// off, so every test in the suite that does not opt in exercises LIKE.
func TestFTS_DisabledIsTheDefault(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "Блины на завтрак", nil)

	assert.Empty(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `content.contains("блины")`}),
		"the default server must keep LIKE semantics")
	assert.False(t, NewMemoServer(app).ftsSearch, "FTS must be opt-in")
}

// TestFTS_EditedMemoIsNotFoundByItsOldText pins the update trigger's
// both-halves requirement through the read path: an index that only
// inserted on update would keep an edited memo findable by text it no
// longer contains.
func TestFTS_EditedMemoIsNotFoundByItsOldText(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "the original heading", nil)
	assert.Len(t, listFTSMemoIDsAnon(t, app, `content.contains("original")`), 1)

	rec := newFilterMemo(t, app, alice.Id, "the replacement heading", nil)
	require.NoError(t, app.Delete(rec))

	assert.Empty(t, listFTSMemoIDsAnon(t, app, `content.contains("replacement")`),
		"a deleted memo must stop matching")
	assert.Len(t, listFTSMemoIDsAnon(t, app, `content.contains("original")`), 1,
		"the surviving memo must still match")
}

// TestFTS_SearchCombinedWithOtherFilters is the regression guard for
// the bug that shipped to production.
//
// Removing the LIKE clause from a compiled filter has to remove the
// *connective* that joined it too. The compiler wraps a conjunction in
// parentheses — `(content ~ {:_a1} && pinned = true)` — so cutting the
// clause out alone leaves `( && pinned = true)`, and PocketBase's filter
// parser rejects that with "expected left operand (identifier,
// function, text or number), got \"&&\" (join)". Every search combined
// with any other condition was a 500.
//
// This test failed on the first release of the FTS path, because the
// original suite only ever combined a search with *another search* — and
// two text terms fall back to LIKE, so the rewrite never ran. The
// shapes the SPA actually sends combine a search with a non-text
// condition, which is exactly the case that broke.
//
// It asserts both that the filter parses and that the remaining
// conjunct still filters: a rewrite that dropped the conjunct as well
// as the LIKE clause would return the wrong rows, not an error.
func TestFTS_SearchCombinedWithOtherFilters(t *testing.T) {
	app := newTestApp(t)
	requireFTSIndex(t, app)
	alice := newUser(t, app, "alice")

	newFilterMemo(t, app, alice.Id, "the quarterly roadmap is drafted", nil)
	newFilterMemo(t, app, alice.Id, "unrelated shopping list", nil)
	newFilterMemo(t, app, alice.Id, "another roadmap entry", nil)

	plain := NewMemoServer(app)
	fts := NewMemoServer(app)
	fts.SetFTSSearch(true)

	contents := func(srv *MemoServer, filter string) []string {
		t.Helper()
		res, err := srv.ListMemos(t.Context(), &v1.ListMemosRequest{Filter: filter})
		require.NoError(t, err, "filter %q must not error", filter)
		out := make([]string, 0, len(res.GetMemos()))
		for _, m := range res.GetMemos() {
			out = append(out, m.GetContent())
		}
		return out
	}

	// Every shape the SPA can produce: a search plus a non-text
	// condition, in both orders, and a search plus a second search.
	for _, filter := range []string{
		`content.contains("roadmap") && pinned`,
		`content.contains("roadmap") && has_link`,
		`content.contains("roadmap") && has_code`,
		`content.contains("roadmap") && has_location`,
		`content.contains("roadmap") && has_task_list`,
		`content.contains("roadmap") && tag in ["work"]`,
		`content.contains("roadmap") && visibility in ["PUBLIC"]`,
		`content.contains("roadmap") && visibility in ["PRIVATE"]`,
		`content.contains("roadmap") && created_ts > now - duration("168h")`,
		`content.contains("roadmap") && space == "nope"`,
		`pinned && content.contains("roadmap")`,
		`content.contains("roadmap") && content.contains("drafted")`,
	} {
		like := contents(plain, filter)
		got := contents(fts, filter)
		assert.ElementsMatch(t, like, got,
			"FTS and LIKE must agree for %q — the surviving conjunct must still filter", filter)
	}

	// Spot-check that the conjunct really did narrow, rather than being
	// dropped along with the LIKE clause.
	assert.Len(t, contents(fts, `content.contains("roadmap") && visibility in ["PRIVATE"]`), 0,
		"the visibility conjunct must still apply")
	assert.Len(t, contents(fts, `content.contains("roadmap") && content.contains("drafted")`), 1,
		"the second search conjunct must still apply")
}
