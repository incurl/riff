package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createMemosFTSIndex adds an FTS5 external-content index over
// `memos.content`.
//
// This migration is the *index only*. Nothing in riff queries
// `memos_fts` yet: `ListMemos` still compiles `content.contains(…)`
// to a `LIKE` scan (`internal/cel/expr.go::likeWildcards`, the LIKE
// path this index will eventually replace). Building the index ahead
// of the query is deliberate: it lets a deployment populate the index
// on its own schedule, and it settles the DDL-in-a-transaction
// question (yes — see
// TestCreateMemosFTSIndex_CreatedInsideMigrationTransaction) without
// a query-path change in flight.
//
// Why FTS5 at all. `LIKE '%…%'` cannot use an index and scans every
// row, and SQLite's LIKE case-folds ASCII only, so a non-ASCII search
// term matches nothing. The `unicode61` tokenizer folds case over the
// full Unicode range and `remove_diacritics 2` makes `café` match
// `cafe`, neither of which the LIKE path can do.
//
// Why external content (`content='memos'`). FTS5 then stores only
// the inverted index; the memo body is not duplicated, and the index
// cannot drift from the source text because the text lives in exactly
// one place. `content_rowid='rowid'` pins the row identity to the
// `memos` table's own rowid so the triggers can address rows without
// a separate mapping.
//
// Why triggers rather than `pb/hooks`. An `AFTER INSERT/UPDATE/DELETE`
// trigger runs inside the same transaction as the row write, so the
// index cannot desynchronise from `memos`. riff's detached-hook
// workers have needed six separate teardown fixes (see AGENTS.md,
// "Known Issues"), so a mechanism that is structurally incapable of
// racing is worth more here than the convenience of Go callbacks.
//
// The delete half uses FTS5's 'delete' command form. That is the
// subtle part: for an external-content table the index rows are not
// stored, so a plain DELETE cannot find them. Writing the old values
// back with the literal 'delete' command in the first column is what
// removes them. The update trigger must do both halves — remove the
// old token set, insert the new — or an edited memo stays findable by
// text it no longer contains.
//
// The migration is idempotent across fresh installs and existing
// instances: it skips when `memos_fts` already exists, and it skips
// entirely when there is no `memos` collection to index.
func init() {
	migrations.Register(func(txApp core.App) error {
		// Nothing to index yet. The `memos` collection is created by an
		// earlier migration, so this only happens on a partially applied
		// schema; skipping keeps the rest of the chain running instead of
		// aborting the whole bootstrap.
		if _, err := txApp.FindCollectionByNameOrId("memos"); err != nil {
			return nil //nolint:nilerr // a missing `memos` means "nothing to do", not a failure
		}

		exists, err := ftsTableExists(txApp, "memos_fts")
		if err != nil {
			return err
		}
		if exists {
			return nil
		}

		for _, stmt := range memosFTSDDL {
			if _, err := txApp.DB().NewQuery(stmt).Execute(); err != nil {
				return err
			}
		}

		// Backfill every row that already exists. 'rebuild' re-reads the
		// whole content table, which is what an external-content index
		// needs: it is the documented way to bring the index in sync
		// after rows were inserted without the triggers in place.
		_, err = txApp.DB().NewQuery(
			`INSERT INTO {{memos_fts}}({{memos_fts}}) VALUES ('rebuild')`,
		).Execute()

		return err
	}, func(txApp core.App) error {
		if _, err := txApp.FindCollectionByNameOrId("memos"); err != nil {
			return nil //nolint:nilerr // nothing was created, so nothing to revert
		}
		// Dropping the virtual table drops its shadow tables with it.
		// The triggers live on `memos` and must go explicitly, or they
		// would fire against a table that no longer exists and fail every
		// subsequent write.
		for _, stmt := range []string{
			`DROP TRIGGER IF EXISTS {{memos_fts_ai}}`,
			`DROP TRIGGER IF EXISTS {{memos_fts_ad}}`,
			`DROP TRIGGER IF EXISTS {{memos_fts_au}}`,
			`DROP TABLE IF EXISTS {{memos_fts}}`,
		} {
			if _, err := txApp.DB().NewQuery(stmt).Execute(); err != nil {
				return err
			}
		}
		return nil
	})
}

// memosFTSDDL is the schema the index needs, applied in order.
//
// `remove_diacritics 2` folds every character that has a canonical
// decomposition, including the Latin-1/Latin Extended range that `café`
// and `naïve` live in. Mode 2 also folds combining marks, so a
// decomposed `é` (e + U+0301) matches the precomposed one.
//
// The delete trigger uses FTS5's 'delete' command form, which is the
// subtle part: an external-content index stores no rows of its own, so
// a plain DELETE cannot find them. Writing the old values back with the
// literal 'delete' command in the first column is what removes them.
// The update trigger must do both halves — remove the old token set,
// insert the new — or an edited memo stays findable by text it no
// longer contains.
var memosFTSDDL = []string{
	// The index itself. `content={{memos}}` makes it external-content:
	// only the inverted index is stored, so the memo body is not
	// duplicated and cannot drift from the source text.
	`CREATE VIRTUAL TABLE {{memos_fts}} USING FTS5 (
		[[content]],
		content={{memos}},
		content_rowid='rowid',
		tokenize="unicode61 remove_diacritics 2"
	)`,

	`CREATE TRIGGER {{memos_fts_ai}} AFTER INSERT ON {{memos}} BEGIN
		INSERT INTO {{memos_fts}}([[rowid]], [[content]])
		VALUES (new.[[rowid]], new.[[content]]);
	END`,

	`CREATE TRIGGER {{memos_fts_ad}} AFTER DELETE ON {{memos}} BEGIN
		INSERT INTO {{memos_fts}}({{memos_fts}}, [[rowid]], [[content]])
		VALUES ('delete', old.[[rowid]], old.[[content]]);
	END`,

	`CREATE TRIGGER {{memos_fts_au}} AFTER UPDATE ON {{memos}} BEGIN
		INSERT INTO {{memos_fts}}({{memos_fts}}, [[rowid]], [[content]])
		VALUES ('delete', old.[[rowid]], old.[[content]]);
		INSERT INTO {{memos_fts}}([[rowid]], [[content]])
		VALUES (new.[[rowid]], new.[[content]]);
	END`,
}

// ftsTableExists reports whether `name` is present in sqlite_master.
//
// The scan targets a one-element struct rather than a bare string or a
// []sql.NullString: dbx rejects a non-struct, non-NullStringMap target
// outright ("must be a slice of struct or NullStringMap rows"), and a
// plain string would return sql.ErrNoRows on no match, which is the
// *expected* answer on a fresh instance and would be indistinguishable
// from a real failure. The error is returned rather than swallowed so
// that "query failed" never reads as "table is missing".
func ftsTableExists(txApp core.App, name string) (bool, error) {
	rows := []struct {
		Name string `db:"name"`
	}{}
	err := txApp.DB().
		NewQuery(`SELECT [[name]] FROM sqlite_master WHERE [[type]]='table' AND [[name]]={:name}`).
		Bind(map[string]any{"name": name}).
		All(&rows)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}
