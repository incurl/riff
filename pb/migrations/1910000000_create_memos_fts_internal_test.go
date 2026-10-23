package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ftsSearchIDs runs a MATCH query against `memos_fts` and returns the
// ids of the matching memos, so a test can assert on behaviour rather
// than on the SQL string the compiler happened to produce.
//
// The join back to `memos` is by `rowid`: the FTS table is an external
// content table, so it has no `id` column of its own.
func ftsSearchIDs(t *testing.T, app core.App, match string) []string {
	t.Helper()
	rows := []struct {
		ID string `db:"id"`
	}{}
	err := app.DB().NewQuery(`
		SELECT m.[[id]]
		FROM {{memos_fts}}
		JOIN {{memos}} m ON m.[[rowid]] = {{memos_fts}}.[[rowid]]
		WHERE {{memos_fts}} MATCH {:match}
	`).Bind(map[string]any{"match": match}).All(&rows)
	require.NoError(t, err, "MATCH %q must not error", match)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// newFTSMemo inserts a memo directly so the test exercises the
// database triggers rather than any Go hook.
func newFTSMemo(t *testing.T, app core.App, author, content string) *core.Record {
	t.Helper()
	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	rec := core.NewRecord(memos)
	rec.Set("author", author)
	rec.Set("content", content)
	rec.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(rec))
	return rec
}

// TestCreateMemosFTSIndex_CreatedInsideMigrationTransaction is the
// answer to the question this migration was written to settle.
//
// PocketBase runs every migration inside `RunInTransaction`
// (core/migrations_runner.go:130), so `CREATE VIRTUAL TABLE ... USING
// FTS5` executes inside a SQLite transaction. FTS5 creates shadow
// tables and may reject DDL mid-transaction, so this is not
// assumable — the table simply being present after `RunAllMigrations`
// is the proof.
//
// If this ever fails with "cannot start a transaction within a
// transaction" or an FTS5-specific error, the DDL has to move to an
// OnBootstrap hook outside the migration tx and this migration becomes
// a no-op marker.
func TestCreateMemosFTSIndex_CreatedInsideMigrationTransaction(t *testing.T) {
	app := freshApp(t)

	exists, err := ftsTableExists(app, "memos_fts")
	require.NoError(t, err)
	assert.True(t, exists, "memos_fts must exist after migrations run inside a tx")
}

// TestCreateMemosFTSIndex_IsIdempotent pins the re-runnable property
// AGENTS.md requires of every migration: re-running against an
// existing install must not fail with "table already exists".
func TestCreateMemosFTSIndex_IsIdempotent(t *testing.T) {
	app := freshApp(t)
	require.NoError(t, app.RunAllMigrations(), "re-running migrations must be a no-op, not an error")
	exists, err := ftsTableExists(app, "memos_fts")
	require.NoError(t, err)
	assert.True(t, exists, "memos_fts must still be present after re-running migrations")
}

// TestCreateMemosFTSIndex_TriggersCreated pins that all three triggers
// exist. The update trigger is the one most easily forgotten, and
// without it an edited memo stays findable by text it no longer
// contains.
func TestCreateMemosFTSIndex_TriggersCreated(t *testing.T) {
	app := freshApp(t)
	for _, name := range []string{"memos_fts_ai", "memos_fts_ad", "memos_fts_au"} {
		var found string
		err := app.DB().
			NewQuery(`SELECT [[name]] FROM sqlite_master WHERE [[type]]='trigger' AND [[name]]={:n}`).
			Bind(map[string]any{"n": name}).Row(&found)
		require.NoError(t, err)
		assert.Equal(t, name, found, "trigger %s must exist", name)
	}
}

// TestCreateMemosFTSIndex_TracksWrites is the behavioural core: the
// triggers must keep the index in step with insert, update and delete.
// Each half is asserted separately because the delete half is the one
// that uses FTS5's 'delete' command form, and an external-content
// index cannot remove rows any other way.
func TestCreateMemosFTSIndex_TracksWrites(t *testing.T) {
	app := freshApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	author := core.NewRecord(users)
	author.Set("username", "fts_author")
	author.Set("email", "fts_author@example.test")
	author.Set("password", "test-password-not-used")
	require.NoError(t, app.Save(author))

	// Insert: the AFTER INSERT trigger indexes the row.
	rec := newFTSMemo(t, app, author.Id, "the quarterly roadmap is drafted")
	assert.Equal(t, []string{rec.Id}, ftsSearchIDs(t, app, "roadmap"), "insert must index the memo")

	// Update: both halves must run, or the old text stays findable.
	rec.Set("content", "the quarterly schedule is drafted")
	require.NoError(t, app.Save(rec))
	assert.Empty(t, ftsSearchIDs(t, app, "roadmap"), "the old text must not survive an update")
	assert.Equal(t, []string{rec.Id}, ftsSearchIDs(t, app, "schedule"), "the new text must be indexed")

	// Delete: the AFTER DELETE trigger must remove the row.
	require.NoError(t, app.Delete(rec))
	assert.Empty(t, ftsSearchIDs(t, app, "schedule"), "a deleted memo must not remain searchable")
}

// TestCreateMemosFTSIndex_FoldsCaseAndDiacritics is the reason this
// work exists, and it is the behaviour the LIKE path could not
// provide.
//
// SQLite's LIKE folds ASCII case only, so before this index a search
// for a Cyrillic or a Japanese term matched nothing. The
// `unicode61` tokenizer folds case across the full range, and
// `remove_diacritics 2` additionally makes a decomposed spelling
// match its precomposed one.
func TestCreateMemosFTSIndex_FoldsCaseAndDiacritics(t *testing.T) {
	app := freshApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	author := core.NewRecord(users)
	author.Set("username", "fold_author")
	author.Set("email", "fold_author@example.test")
	author.Set("password", "test-password-not-used")
	require.NoError(t, app.Save(author))

	newFTSMemo(t, app, author.Id, "Блины с яблоками на завтрак")
	newFTSMemo(t, app, author.Id, "カフェで-incident-report を書いた")
	newFTSMemo(t, app, author.Id, "a café in Zürich")

	assert.Len(t, ftsSearchIDs(t, app, "блины"), 1,
		"a lowercased Cyrillic query must match a capitalised document — LIKE could not do this")
	assert.Len(t, ftsSearchIDs(t, app, "БЛИНЫ"), 1,
		"case folding must be symmetric")
	assert.Len(t, ftsSearchIDs(t, app, "cafe"), 1,
		"remove_diacritics must fold café to cafe")
	assert.Len(t, ftsSearchIDs(t, app, "ZURICH"), 1,
		"remove_diacritics must fold ü as well as é")

	// A CJK run is not split into per-character tokens: `unicode61`
	// classifies ideographs and kana as alphanumeric, so `カフェで`
	// indexes as one token. Measured behaviour (not assumed):
	//
	//	"written"     -> 0 hits   (the word is 書いた, hiragana)
	//	"incident"    -> 1 hit    (the ASCII run after the hyphen)
	//	"カフェで"      -> 1 hit    (the whole run)
	//	"カフェ"        -> 0 hits   (a prefix is a different token)
	//	"written*"    -> n/a here, but prefix search works via `term*`
	//
	// So a Latin-script query inside a CJK document is findable, and a
	// CJK query has to name a whole run unless it uses the `term*`
	// prefix form. Both are tokenizer behaviour, not riff's; a user
	// searching Japanese will need the prefix form for partial matches.
	assert.Len(t, ftsSearchIDs(t, app, "incident"), 1,
		"an ASCII term in a CJK document must be findable")
	assert.Len(t, ftsSearchIDs(t, app, "written"), 0,
		"the hiragana spelling 書いた is not the Latin token 'written'")
	assert.Len(t, ftsSearchIDs(t, app, "カフェで"), 1,
		"the whole CJK run is one token and matches")
	assert.Empty(t, ftsSearchIDs(t, app, "カフェ"),
		"a bare prefix of a CJK run is a different token; the tokenizer does not segment ideographs")
	assert.Len(t, ftsSearchIDs(t, app, "カフェ*"), 1,
		"the prefix form must match a partial CJK run")
}

// TestCreateMemosFTSIndex_BackfillsExistingRows pins the 'rebuild'
// step. The index is created after the memos already exist, so without
// the rebuild every pre-existing memo would be unsearchable — which is
// the whole population on any upgrade.
//
// This simulates the upgrade honestly: a memo is inserted *before* the
// index exists, so the AFTER INSERT trigger cannot possibly have seen
// it. That is the only way to test the rebuild.
//
// Note that `RunAllMigrations` cannot be used to re-apply an
// already-recorded migration (the runner skips applied files), so the
// migration's own function is invoked directly — the same closure the
// registry holds.
func TestCreateMemosFTSIndex_BackfillsExistingRows(t *testing.T) {
	app := freshApp(t)

	// Tear the index down to model a pre-upgrade instance: rows
	// present, index absent. The triggers must go too, or the memo
	// insert below would fail against the missing table.
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS {{memos_fts_ai}}`,
		`DROP TRIGGER IF EXISTS {{memos_fts_ad}}`,
		`DROP TRIGGER IF EXISTS {{memos_fts_au}}`,
		`DROP TABLE IF EXISTS {{memos_fts}}`,
	} {
		_, err := app.DB().NewQuery(stmt).Execute()
		require.NoError(t, err)
	}
	exists, err := ftsTableExists(app, "memos_fts")
	require.NoError(t, err)
	require.False(t, exists, "sanity: the index must be gone before the rebuild test")

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	author := core.NewRecord(users)
	author.Set("username", "backfill_author")
	author.Set("email", "backfill_author@example.test")
	author.Set("password", "test-password-not-used")
	require.NoError(t, app.Save(author))

	// Inserted with no triggers and no index: the only path to
	// findability is the 'rebuild'.
	rec := newFTSMemo(t, app, author.Id, "a memo that predates the index")

	// Re-apply the migration body directly.
	for _, item := range core.AppMigrations.Items() {
		if item.File == "1910000000_create_memos_fts.go" {
			require.NoError(t, item.Up(app),
				"re-applying the migration body must rebuild the index")
			break
		}
	}

	assert.Equal(t, []string{rec.Id}, ftsSearchIDs(t, app, "predates"),
		"'rebuild' must backfill rows that predate the index")
}
