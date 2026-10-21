package connect

import (
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// newFilterSpace creates a `spaces` row with the given uid (the
// human-readable name the SPA sends as `spaces/<uid>`).
func newFilterSpace(t *testing.T, app core.App, ownerID, uid string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("uid", uid)
	rec.Set("title", uid)
	rec.Set("owner", ownerID)
	require.NoError(t, app.Save(rec))
	return rec
}

// newFilterMemo creates a `memos` row. state is set explicitly so the
// test does not depend on the backfill hook. Note that only live
// columns are set: `type` and the other flat PO-33 fields were
// dropped by pb/migrations/1860000001_drop_riff_only_memo_columns.go,
// and `Record.Set` on a missing field silently succeeds while storing
// nothing, which would make a fixture look meaningful and be a no-op.
func newFilterMemo(t *testing.T, app core.App, authorID string, content string, extra map[string]any) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("author", authorID)
	rec.Set("content", content)
	rec.Set("visibility", "PUBLIC")
	rec.Set("state", "NORMAL")
	for k, v := range extra {
		rec.Set(k, v)
	}
	require.NoError(t, app.Save(rec))
	return rec
}

// listMemoIDs runs ListMemos with the given filter and returns the
// memo names it produced, in order.
func listMemoIDs(t *testing.T, app core.App, req *v1.ListMemosRequest) []string {
	t.Helper()
	res, err := NewMemoServer(app).ListMemos(t.Context(), req)
	require.NoError(t, err)
	out := make([]string, 0, len(res.GetMemos()))
	for _, m := range res.GetMemos() {
		out = append(out, m.GetName())
	}
	return out
}

// TestListMemos_SpaceFilter narrows the feed to one space. This is the
// bug this whole change exists for: ListMemos passed "" as the filter,
// so every space filter was a no-op.
func TestListMemos_SpaceFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	team := newFilterSpace(t, app, alice.Id, "team")
	other := newFilterSpace(t, app, alice.Id, "other")

	newFilterMemo(t, app, alice.Id, "in team", map[string]any{"space": team.Id})
	newFilterMemo(t, app, alice.Id, "in other", map[string]any{"space": other.Id})
	newFilterMemo(t, app, alice.Id, "no space", nil)

	teamIDs := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `space == "spaces/team"`,
	})
	require.Len(t, teamIDs, 1, "space filter must narrow to the one memo in the space")

	otherIDs := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `space == "spaces/other"`,
	})
	require.Len(t, otherIDs, 1)

	// `space == null` is the unassigned probe the sidebar offers.
	unassigned := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `space == null`})
	require.Len(t, unassigned, 1)

	// The two spaces plus the unassigned memo: the unfiltered feed.
	all := listMemoIDs(t, app, &v1.ListMemosRequest{})
	require.Len(t, all, 3)
}

// Two spaces combined with OR, which is what the sidebar's
// combineCELFilters produces when more than one collection is selected.
func TestListMemos_SpaceFilterOr(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	team := newFilterSpace(t, app, alice.Id, "team")
	other := newFilterSpace(t, app, alice.Id, "other")

	newFilterMemo(t, app, alice.Id, "in team", map[string]any{"space": team.Id})
	newFilterMemo(t, app, alice.Id, "in other", map[string]any{"space": other.Id})
	newFilterMemo(t, app, alice.Id, "no space", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `space == "spaces/team" || space == "spaces/other"`,
	})
	require.Len(t, ids, 2)
}

// The profile feed and the space sidebar scope by *name* —
// `creator == "users/<username>"` — but `memos.author` is a relation
// holding the user's record id, so the name has to be resolved before
// the filter runs. Without that rewrite the compiled filter matches
// nothing and every profile page renders empty.
func TestListMemos_CreatorNameFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	team := newFilterSpace(t, app, alice.Id, "team")

	aliceTeam := newFilterMemo(t, app, alice.Id, "alice in team", map[string]any{"space": team.Id})
	newFilterMemo(t, app, alice.Id, "alice at home", nil)
	newFilterMemo(t, app, bob.Id, "bob in team", map[string]any{"space": team.Id})

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `creator == "users/alice"`})
	require.Len(t, ids, 2, "the profile feed must resolve the username to the author id")

	// The combined shape the SPA's combineCELFilters produces: every
	// clause parenthesised.
	ids = listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `(space == "spaces/team") && (creator == "users/alice")`,
	})
	require.Len(t, ids, 1)
	require.Equal(t, "memos/"+aliceTeam.Id, ids[0])

	// An unknown username narrows to nothing instead of widening.
	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `creator == "users/nobody"`})
	require.Len(t, ids, 0)
}

// A space AND a content search, which is the shape the SPA produces
// once a filter chip is added to the space scope.
func TestListMemos_SpaceAndContent(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	team := newFilterSpace(t, app, alice.Id, "team")

	newFilterMemo(t, app, alice.Id, "roadmap draft", map[string]any{"space": team.Id})
	newFilterMemo(t, app, alice.Id, "roadmap published", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `space == "spaces/team" && content.contains("roadmap")`,
	})
	require.Len(t, ids, 1)
}

func TestListMemos_ContentFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "alpha bravo", nil)
	newFilterMemo(t, app, alice.Id, "charlie delta", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `content.contains("bravo")`})
	require.Len(t, ids, 1)

	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `content.contains("nothing here")`})
	require.Len(t, ids, 0)
}

func TestListMemos_VisibilityFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "public one", map[string]any{"visibility": "PUBLIC"})
	newFilterMemo(t, app, alice.Id, "private one", map[string]any{"visibility": "PRIVATE"})

	// The per-row CanAccessRecord check still applies, so an
	// anonymous caller only ever sees the PUBLIC memo; the filter
	// selects among what the caller may read.
	pub := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `visibility == "PUBLIC"`})
	require.Len(t, pub, 1)

	both := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `visibility in ["PUBLIC", "PRIVATE"]`})
	require.Len(t, both, 1, "visibility filter must not bypass the per-row access check")
}

func TestListMemos_TagFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "tagged", map[string]any{"tags": []string{"work"}})
	newFilterMemo(t, app, alice.Id, "untagged", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `tags.exists("work")`})
	require.Len(t, ids, 1)

	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `tags.exists("missing")`})
	require.Len(t, ids, 0)
}

// The SPA sends the singular spelling; both must resolve.
func TestListMemos_TagFilterSingularAlias(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "tagged", map[string]any{"tags": []string{"work"}})

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `tag in ["work"]`})
	require.Len(t, ids, 1)
}

// The composer computes the derived property flags into the `payload`
// JSON column (pb/connect/memos_translate.go reads them back). The
// filter reads the same column, so the test sets it explicitly rather
// than depending on the hashtag/parsing hook.
func TestListMemos_DerivedFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "see [the docs](https://example.com)", map[string]any{
		"payload": map[string]any{"property": map[string]any{"has_link": true}},
	})
	newFilterMemo(t, app, alice.Id, "plain text", map[string]any{
		"payload": map[string]any{"property": map[string]any{"has_link": false}},
	})

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `has_link`})
	require.Len(t, ids, 1)

	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `has_link && has_task_list`})
	require.Len(t, ids, 0)

	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `has_code`})
	require.Len(t, ids, 0)
}

// The SPA's pinned chip emits the bare column name and wraps it in
// parentheses when it joins it to the other clauses
// (web/src/hooks/useMemoFilters.ts). Both spellings have to narrow the
// feed; before the compiler coalesced a bare boolean column the whole
// filter was rejected as a half-typed expression.
func TestListMemos_BarePinnedFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	pinned := newFilterMemo(t, app, alice.Id, "pinned", map[string]any{"pinned": true})
	newFilterMemo(t, app, alice.Id, "loose", map[string]any{"pinned": false})

	for _, filter := range []string{`pinned`, `(pinned)`, `pinned == true`, `pinned && visibility == "PUBLIC"`} {
		ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: filter})
		require.Len(t, ids, 1, "filter %q must narrow to the pinned memo", filter)
		require.Equal(t, "memos/"+pinned.Id, ids[0], "filter %q", filter)
	}

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `!pinned`})
	require.Len(t, ids, 1, "the negated form must select the unpinned memo")
}

// The calendar's day filter: two timestamp comparisons.
func TestListMemos_TimestampRangeFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "old", nil)

	// Everything written before now is inside a range that started
	// in the past; a range that starts in the future excludes it.
	past := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `created_ts >= timestamp("2020-01-01T00:00:00Z") && created_ts < timestamp("2999-01-01T00:00:00Z")`,
	})
	require.Len(t, past, 1, "the calendar range must match a memo written today")

	future := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `created_ts >= timestamp("2999-01-01T00:00:00Z")`,
	})
	require.Len(t, future, 0)

	// The literal shape the SPA actually sends: buildTimestampRangeFilter
	// in web/src/lib/calendar-utils.ts emits epoch seconds, not RFC 3339.
	spaFilter := fmt.Sprintf(
		`created_ts >= timestamp(%d) && created_ts < timestamp(%d)`,
		time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix(),
	)
	require.Len(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: spaFilter}), 1,
		"the calendar's integer timestamp form must compile: %s", spaFilter)
}

// `state` is a request field, not part of the CEL expression; the
// Archived page depends on it and it used to be ignored.
func TestListMemos_StateFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "normal memo", map[string]any{"state": "NORMAL"})
	archived := newFilterMemo(t, app, alice.Id, "archived memo", nil)
	archived.Set("state", "ARCHIVED")
	require.NoError(t, app.Save(archived))

	normal := listMemoIDs(t, app, &v1.ListMemosRequest{State: v1.State_NORMAL})
	require.Len(t, normal, 1)

	archivedIDs := listMemoIDs(t, app, &v1.ListMemosRequest{State: v1.State_ARCHIVED})
	require.Len(t, archivedIDs, 1)

	// Unspecified means "no state constraint", not "NORMAL".
	unspecified := listMemoIDs(t, app, &v1.ListMemosRequest{})
	require.Len(t, unspecified, 2)
}

// state and the CEL filter are ANDed rather than one replacing the other.
func TestListMemos_StateAndFilterCombine(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "normal bravo", nil)
	archived := newFilterMemo(t, app, alice.Id, "archived bravo", nil)
	archived.Set("state", "ARCHIVED")
	require.NoError(t, app.Save(archived))

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `content.contains("bravo")`,
		State:  v1.State_NORMAL,
	})
	require.Len(t, ids, 1)
}

// An unsupported expression is a 400, not a silently unfiltered feed.
func TestListMemos_RejectsUnsupportedFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "content", nil)

	for _, filter := range []string{
		`nonsense == "x"`,
		`visibility == "PUBLIK"`,
		`sets.equivalent(tags, ["a"])`,
		`content.startsWith("a")`,
		`space == "spaces/nope" || nonsense == "y"`,
	} {
		_, err := NewMemoServer(app).ListMemos(t.Context(), &v1.ListMemosRequest{Filter: filter})
		require.Error(t, err, "filter %q should be rejected", filter)
	}
}

// ---------------------------------------------------------------------------
// The space-name rewriting step, tested directly because it is the piece
// that bridges the SPA's resource names and the relation column.
// ---------------------------------------------------------------------------

type stubResolver struct {
	ids  map[string]string
	errs map[string]bool
}

func (s stubResolver) RecordID(resourceName string) (string, error) {
	if s.errs[resourceName] {
		return "", errSpaceNotFound
	}
	return s.ids[resourceName], nil
}

var errSpaceNotFound = errors.New("space not found")

func TestCELIdentityRewriter(t *testing.T) {
	r := CELIdentityRewriter{
		App: stubResolver{ids: map[string]string{
			"spaces/team":   "rec123",
			"users/alice":   "usr_alice",
			"users/two/one": "usr_odd",
		}},
		Rules: memoIdentityRules(),
	}

	for _, tc := range []struct{ in, want string }{
		{`space == "spaces/team"`, `space == "rec123"`},
		{`space != "spaces/team"`, `space != "rec123"`},
		{`space in ["spaces/team"]`, `space in ["rec123"]`},
		{`space in ["spaces/team", "spaces/other"]`, `space in ["rec123", "spaces/other"]`},
		{`space == "spaces/team" && visibility == "PUBLIC"`, `space == "rec123" && visibility == "PUBLIC"`},
		{`(space == "spaces/team") && (creator == "users/alice")`, `(space == "rec123") && (creator == "usr_alice")`},
		// The profile feed sends the creator name, not the id.
		{`creator == "users/alice"`, `creator == "usr_alice"`},
		{`creator != "users/alice" && pinned`, `creator != "usr_alice" && pinned`},
		{`creator in ["users/alice", "users/bob"]`, `creator in ["usr_alice", "users/bob"]`},
		// A username may itself contain a slash; the resolver
		// owns that split, the rewriter only matches the prefix.
		{`creator == "users/two/one"`, `creator == "usr_odd"`},
		// A raw record id is left alone.
		{`space == "rec123"`, `space == "rec123"`},
		{`creator == "usr_alice"`, `creator == "usr_alice"`},
		// An unrelated field is untouched.
		{`visibility == "PUBLIC"`, `visibility == "PUBLIC"`},
		// `null` comparisons carry no name to resolve.
		{`space == null`, `space == null`},
		// The field name inside a string literal is a *value*: the
		// word-search must not touch it. A substring rewrite used to
		// drop it entirely, turning a search for "space" into
		// `content.contains("")`, which matches every row.
		{`content.contains("space")`, `content.contains("space")`},
		{`content.contains("creator")`, `content.contains("creator")`},
		// A dotted path is not the column.
		{`payload.space == "spaces/team"`, `payload.space == "spaces/team"`},
		// A longer identifier that ends in the field name is not it.
		{`myspace == "spaces/team"`, `myspace == "spaces/team"`},
		{`creator_name == "users/alice"`, `creator_name == "users/alice"`},
		// A non-literal operand is left for the compiler to reject.
		{`space == other`, `space == other`},
		// Whitespace is normalised around a rewritten comparison.
		{`space=="spaces/team"`, `space == "rec123"`},
	} {
		assert.Equal(t, tc.want, r.Rewrite(tc.in), "Rewrite(%q)", tc.in)
	}
}

// An unresolvable space name stays as typed. The filter then matches no
// rows, which is what "a space that does not exist" should mean; it
// must not turn into a filter that silently matches everything.
func TestCELIdentityRewriter_UnknownSpaceKeepsValue(t *testing.T) {
	r := CELIdentityRewriter{
		App:   stubResolver{ids: map[string]string{}},
		Rules: memoIdentityRules(),
	}
	assert.Equal(t, `space == "spaces/ghost"`, r.Rewrite(`space == "spaces/ghost"`))
	assert.Equal(t, `creator == "users/ghost"`, r.Rewrite(`creator == "users/ghost"`))
}

func TestCELIdentityRewriter_NilResolverIsPassthrough(t *testing.T) {
	r := CELIdentityRewriter{Rules: memoIdentityRules()}
	assert.Equal(t, `space == "spaces/team"`, r.Rewrite(`space == "spaces/team"`))
	assert.Equal(t, `creator == "users/alice"`, r.Rewrite(`creator == "users/alice"`))
}

func TestCELIdentityRewriter_ResolverErrorKeepsValue(t *testing.T) {
	r := CELIdentityRewriter{
		App:   stubResolver{ids: map[string]string{}, errs: map[string]bool{"spaces/team": true}},
		Rules: memoIdentityRules(),
	}
	assert.Equal(t, `space == "spaces/team"`, r.Rewrite(`space == "spaces/team"`))
}

// memoStateFilter maps the proto enum onto the stored value.
func TestMemoStateFilter(t *testing.T) {
	sql, args, err := memoStateFilter(v1.State_NORMAL)
	require.NoError(t, err)
	assert.Equal(t, "state = {:__state}", sql)
	assert.Equal(t, "NORMAL", args["__state"])

	_, args, err = memoStateFilter(v1.State_ARCHIVED)
	require.NoError(t, err)
	assert.Equal(t, "ARCHIVED", args["__state"])

	sql, args, err = memoStateFilter(v1.State_STATE_UNSPECIFIED)
	require.NoError(t, err)
	assert.Empty(t, sql)
	assert.Empty(t, args)
}

// A content search whose term happens to be a field name must search
// for the term, not silently widen. The identity rewriter walks the
// expression by word and skips string literals for exactly this reason:
// a substring pass dropped the word from inside the literal and turned
// `content.contains("space")` into `content.contains("")`, which
// matches every memo.
func TestListMemos_ContentSearchForFieldName(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "our space is ready", nil)
	newFilterMemo(t, app, alice.Id, "unrelated memo", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `content.contains("space")`})
	require.Len(t, ids, 1, "the search term must survive the rewriter: %v", ids)
}

// A tag filter must match the whole element, not a substring. The
// membership expansion compiles to four anchored LIKE patterns precisely
// because `tags ~ '"work"'` alone also matches "workout"; this pins
// every position an element can occupy in the stored JSON array.
func TestListMemos_TagFilterMatchesWholeElements(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	withTags := []struct {
		content string
		tags    []string
	}{
		{"only", []string{"work"}},
		{"first", []string{"work", "life"}},
		{"last", []string{"life", "work"}},
		{"middle", []string{"a", "work", "b"}},
	}
	for _, tc := range withTags {
		newFilterMemo(t, app, alice.Id, tc.content, map[string]any{"tags": tc.tags})
	}
	// Near misses that a substring match would wrongly include.
	for _, tags := range [][]string{{"workout"}, {"xwork"}, {"home work"}, {"workish"}} {
		newFilterMemo(t, app, alice.Id, "nearmiss", map[string]any{"tags": tags})
	}
	newFilterMemo(t, app, alice.Id, "untagged", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `tags.exists("work")`})
	require.Len(t, ids, 4, "every array position must match; got %v", ids)
	for _, id := range ids {
		require.NotContains(t, id, "nearmiss", "a substring match must be excluded")
		require.NotContains(t, id, "untagged")
	}
}

// ---------------------------------------------------------------------------
// Attachments. The Attachments page sends `memo_id == null` to build the
// unused-attachment library; ListAttachments used to ignore it.
// ---------------------------------------------------------------------------

// newFilterAttachment creates an `attachments` row. The MIME column is
// `mime_type` (pb/migrations/1850000000_create_attachments.go); setting
// `type` would be a silent no-op.
func newFilterAttachment(t *testing.T, app core.App, creatorID, memoID string, extra map[string]any) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("attachments")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	if creatorID != "" {
		rec.Set("creator", creatorID)
	}
	if memoID != "" {
		rec.Set("memo", memoID)
	}
	rec.Set("filename", "sample.wav")
	rec.Set("mime_type", "audio/wav")
	rec.Set("size", 1024)
	for k, v := range extra {
		rec.Set(k, v)
	}
	require.NoError(t, app.Save(rec))
	return rec
}

func TestListAttachments_FilterIsHonoured(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	memo := newFilterMemo(t, app, alice.Id, "with attachment", nil)
	newFilterAttachment(t, app, alice.Id, memo.Id, nil)
	newFilterAttachment(t, app, alice.Id, "", nil)

	server := NewAttachmentServer(app)
	// The orphan branch of canAccessAttachment needs a viewer: an
	// attachment with no memo is readable only by the uploader
	// recorded in `creator`.
	ctx := withViewer(t.Context(), alice)
	list := func(filter string) []*v1.Attachment {
		res, err := server.ListAttachments(ctx, &v1.ListAttachmentsRequest{Filter: filter})
		require.NoError(t, err, "filter %q", filter)
		return res.GetAttachments()
	}

	all := list("")
	require.Len(t, all, 2)

	// The unused-attachment library filter.
	unused := list(`memo_id == null`)
	require.Len(t, unused, 1)

	bound := list(`memo_id != null`)
	require.Len(t, bound, 1)

	require.Len(t, list(`filename == "sample.wav"`), 2)
	require.Len(t, list(`filename == "nothing.wav"`), 0)

	// The MIME column is `mime_type`. `type` never existed on this
	// collection, and an alias that compiled to the dead name would
	// make PocketBase reject the filter.
	require.Len(t, list(`mime_type == "audio/wav"`), 2)
	require.Len(t, list(`mime_type == "image/png"`), 0)
	require.Len(t, list(`type == "audio/wav"`), 2, "`type` is an alias of mime_type")
}

// `space` is documented on ListAttachmentsRequest ("the linked memo's
// space ... or null when the attachment is unlinked") but there is no
// `space` column on `attachments`: the compiled filter has to follow
// the `memo` relation. This pins that PocketBase accepts the dotted
// path instead of answering 500 unknown field.
func TestListAttachments_SpaceFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	team := newFilterSpace(t, app, alice.Id, "team")
	other := newFilterSpace(t, app, alice.Id, "other")

	inTeam := newFilterMemo(t, app, alice.Id, "in team", map[string]any{"space": team.Id})
	inOther := newFilterMemo(t, app, alice.Id, "in other", map[string]any{"space": other.Id})
	loose := newFilterMemo(t, app, alice.Id, "no space", nil)

	newFilterAttachment(t, app, alice.Id, inTeam.Id, nil)
	newFilterAttachment(t, app, alice.Id, inOther.Id, nil)
	newFilterAttachment(t, app, alice.Id, loose.Id, nil)
	newFilterAttachment(t, app, alice.Id, "", nil) // orphan

	server := NewAttachmentServer(app)
	ctx := withViewer(t.Context(), alice)
	list := func(filter string) []*v1.Attachment {
		t.Helper()
		res, err := server.ListAttachments(ctx, &v1.ListAttachmentsRequest{Filter: filter})
		require.NoError(t, err, "filter %q", filter)
		return res.GetAttachments()
	}

	assert.Len(t, list(""), 4)

	teamOnly := list(`space == "spaces/team"`)
	require.Len(t, teamOnly, 1, "the space filter must follow the memo relation")
	assert.Equal(t, "memos/"+inTeam.Id, teamOnly[0].GetMemo())

	assert.Len(t, list(`space == "spaces/other"`), 1)

	// "No space" covers both the unlinked attachment and an
	// attachment whose memo sits outside every space.
	assert.Len(t, list(`space == null`), 2)
}

func TestListAttachments_RejectsUnsupportedFilter(t *testing.T) {
	app := newTestApp(t)
	_, err := NewAttachmentServer(app).ListAttachments(t.Context(), &v1.ListAttachmentsRequest{
		Filter: `nonexistent_field == "x"`,
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// The CEL spellings the public contract and the shipped saved-view
// examples use. Each of these used to be a CodeInvalidArgument on a
// filter the UI offered to the user.
// ---------------------------------------------------------------------------

// `"work" in tags` is the proto's documented membership form and the
// quick-find hint; `tags.exists(t, t == "work")` is its element-binding
// equivalent. Both must produce the same rows as `tag in ["work"]`,
// and neither may match the near-miss tag "workout".
func TestListMemos_TagMembershipDocumentedForms(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	want := newFilterMemo(t, app, alice.Id, "tagged work", map[string]any{"tags": []string{"work"}})
	newFilterMemo(t, app, alice.Id, "tagged workout", map[string]any{"tags": []string{"workout"}})

	for _, filter := range []string{
		`"work" in tags`,
		`tags.exists(t, t == "work")`,
		`tags.exists("work")`,
		`tag in ["work"]`,
		// The spelling older saved views carry.
		`tag == "work"`,
	} {
		ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: filter})
		require.Equal(t, []string{"memos/" + want.Id}, ids, "filter %q", filter)
	}
}

// `now - duration("24h")` is the "Recent notes" example the saved-view
// dialog ships. The subtraction is folded into a bound timestamp.
func TestListMemos_RelativeDurationFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	fresh := newFilterMemo(t, app, alice.Id, "fresh", nil)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `created_ts >= now - duration("24h")`,
	})
	require.Equal(t, []string{"memos/" + fresh.Id}, ids)

	// A window that closed before the row was written matches nothing.
	ids = listMemoIDs(t, app, &v1.ListMemosRequest{
		Filter: `created_ts < now - duration("24h")`,
	})
	require.Empty(t, ids)
}

// A saved view may store the bare CEL literal `true` ("match
// everything"), which the compiler turns into a test on the record id.
func TestListMemos_BooleanLiteralFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	newFilterMemo(t, app, alice.Id, "one", nil)
	newFilterMemo(t, app, alice.Id, "two", nil)

	require.Len(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: "true"}), 2)
	require.Empty(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: "false"}))
}

// `!has_location` and `size(tags) == 0` are two more shipped examples:
// a negated derived predicate (the caller supplies its complement) and
// the only length test the filter language can express.
func TestListMemos_NegatedDerivedAndUntaggedFilters(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	tagged := newFilterMemo(t, app, alice.Id, "#work plan", map[string]any{
		"tags":    []string{"work"},
		"payload": map[string]any{"property": map[string]any{"has_link": true}},
	})
	untagged := newFilterMemo(t, app, alice.Id, "plain", nil)

	// Neither memo has a location, so the negation matches both.
	require.Len(t, listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `!has_location`}), 2)

	ids := listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `size(tags) == 0`})
	require.Equal(t, []string{"memos/" + untagged.Id}, ids)

	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `has_link && size(tags) != 0`})
	require.Equal(t, []string{"memos/" + tagged.Id}, ids)

	// Negation of a derived predicate: the memo with no payload at all
	// has no `has_link` key, which the `= null` half covers.
	ids = listMemoIDs(t, app, &v1.ListMemosRequest{Filter: `!has_link`})
	require.Equal(t, []string{"memos/" + untagged.Id}, ids)
}

// `filename.contains("test")` is the attachment contract's own example.
func TestListAttachments_FilenameContains(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	memo := newFilterMemo(t, app, alice.Id, "with attachment", nil)
	newFilterAttachment(t, app, alice.Id, memo.Id, nil)

	server := NewAttachmentServer(app)
	ctx := withViewer(t.Context(), alice)

	res, err := server.ListAttachments(ctx, &v1.ListAttachmentsRequest{Filter: `filename.contains("sample")`})
	require.NoError(t, err)
	require.Len(t, res.GetAttachments(), 1)

	res, err = server.ListAttachments(ctx, &v1.ListAttachmentsRequest{Filter: `filename.contains("nothing")`})
	require.NoError(t, err)
	require.Empty(t, res.GetAttachments())
}

// `creator` is "the linked memo's creator, or the uploader for an
// unlinked attachment": the `creator` column is only stamped on
// orphans, so the filter has to follow the memo relation for bound
// rows (pb/connect/attachment_upload.go).
func TestListAttachments_CreatorFollowsMemoAuthor(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	aliceMemo := newFilterMemo(t, app, alice.Id, "alice memo", nil)
	bobMemo := newFilterMemo(t, app, bob.Id, "bob memo", nil)

	aliceBound := newFilterAttachment(t, app, "", aliceMemo.Id, nil) // no creator column
	aliceOrphan := newFilterAttachment(t, app, alice.Id, "", nil)    // orphan: creator stamped
	bobBound := newFilterAttachment(t, app, "", bobMemo.Id, nil)
	newFilterAttachment(t, app, bob.Id, "", nil) // bob's orphan is not readable by alice

	server := NewAttachmentServer(app)
	ctx := withViewer(t.Context(), alice)
	names := func(filter string) map[string]bool {
		t.Helper()
		res, err := server.ListAttachments(ctx, &v1.ListAttachmentsRequest{Filter: filter})
		require.NoError(t, err, "filter %q", filter)
		out := make(map[string]bool, len(res.GetAttachments()))
		for _, a := range res.GetAttachments() {
			out[a.GetName()] = true
		}
		return out
	}

	mine := names(`creator == "users/alice"`)
	require.Len(t, mine, 2, "a bound attachment must match its memo's author: %v", mine)
	assert.True(t, mine["attachments/"+aliceBound.Id])
	assert.True(t, mine["attachments/"+aliceOrphan.Id])

	theirs := names(`creator == "users/bob"`)
	require.Len(t, theirs, 1, "bob's orphan is private to him: %v", theirs)
	assert.True(t, theirs["attachments/"+bobBound.Id])
}

// A saved view whose filter cannot compile is rejected at write time:
// otherwise it is stored and every feed that selects it 400s.
func TestMemoView_ValidatesFilter(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)
	ctx := withViewer(t.Context(), alice)

	// A filter the feed can evaluate passes validate_only.
	_, err := users.CreateMemoView(ctx, &v1.CreateMemoViewRequest{
		Parent:       "users/alice",
		MemoView:     &v1.MemoView{Title: "ok", Filter: `"work" in tags`},
		ValidateOnly: true,
	})
	require.NoError(t, err)

	// The same call with a filter the feed would reject answers the
	// question the SPA's Validate button asks.
	_, err = users.CreateMemoView(ctx, &v1.CreateMemoViewRequest{
		Parent:       "users/alice",
		MemoView:     &v1.MemoView{Title: "bad", Filter: `content.startsWith("x")`},
		ValidateOnly: true,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// The write path refuses it too, and validate_only stored nothing.
	_, err = users.CreateMemoView(ctx, &v1.CreateMemoViewRequest{
		Parent:   "users/alice",
		MemoView: &v1.MemoView{Title: "bad", Filter: `nonsense == "x"`},
	})
	require.Error(t, err)

	list, err := users.ListMemoViews(ctx, &v1.ListMemoViewsRequest{Parent: "users/alice"})
	require.NoError(t, err)
	assert.Empty(t, list.GetMemoViews())
}
