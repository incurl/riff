package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createMemoSharesCollection backs the Phase 4 Wave 6
// MemoService share RPCs (CreateMemoShare / ListMemoShares /
// DeleteMemoShare / GetSharedMemo). One row per active share
// link; the public `GetSharedMemo` RPC resolves the row by its
// `code` and returns the underlying memo.
//
// Fields:
//
//	memo        (relation → memos, single, required, CascadeDelete) —
//	            the memo this link grants read access to. Deleting
//	            the memo removes all of its share rows.
//	code        (text, required, max 32, unique) — the opaque
//	            bearer token embedded in the share URL. 22 chars
//	            from internal/random gives ~131 bits of entropy
//	            against guessing, well past the rate limit on the
//	            anonymous read path.
//	created_by  (relation → users, single, required) — the user
//	            who minted the link. The auth check is in the
//	            Connect handler (memo author or admin), so this
//	            column is informational, not authoritative.
//	expire_time (datetime, optional) — if set, the link stops
//	            working after this instant. Empty = never expires.
//	created     (autodate, onCreate) — output-only creation
//	            timestamp.
//
// Access rules: server-only on every verb. The Connect
// handlers are the sole authority for "who can create / list /
// delete", and `GetSharedMemo` queries the collection directly
// instead of going through the rule engine (the row's `code`
// is the bearer credential, not the caller's session). All
// `*Rule` fields are nil so PocketBase's superuser privilege
// is the only path that can mutate the table from the
// collection REST API — the Connect handler in pb/connect
// issues app.Save / app.Delete through the OnRecord*Request
// hook chain to keep the rules auditable.
//
// CascadeDelete on `memo` is intentional: deleting a memo
// hard-deletes its share rows, which matches the Wave 4
// memo-delete cascade rule in pb/hooks/spaces.go and
// multi-spaces.md:135.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		// Idempotency: re-running migrations is a supported
		// PocketBase path. Same shape as
		// 1740000000_create_memo_links.go.
		if existing, err := txApp.FindCollectionByNameOrId("memo_shares"); err == nil && existing != nil {
			return nil
		}

		s := core.NewBaseCollection("memo_shares")

		s.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		s.Fields.Add(&core.TextField{
			Name:     "code",
			Required: true,
			Max:      32,
			// The collection-level unique index is the
			// primary defence against code collision; the
			// handler retries on insert failure up to a
			// small cap and surfaces a 5xx if exhausted.
			Pattern: `^[A-Za-z0-9_-]{22,32}$`,
		})
		s.Fields.Add(&core.RelationField{
			Name:         "created_by",
			CollectionId: users.Id,
			Required:     true,
			MaxSelect:    1,
			MinSelect:    1,
		})
		s.Fields.Add(&core.DateField{
			Name:     "expire_time",
			Required: false,
		})
		s.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		// Server-only. See the file-level comment.
		s.ListRule = nil
		s.ViewRule = nil
		s.CreateRule = nil
		s.UpdateRule = nil
		s.DeleteRule = nil

		s.AddIndex("idx_memo_shares_code", true, "code", "")
		s.AddIndex("idx_memo_shares_memo", false, "memo", "")

		return txApp.Save(s)
	}, func(txApp core.App) error {
		s, err := txApp.FindCollectionByNameOrId("memo_shares")
		if err != nil {
			return err
		}
		return txApp.Delete(s)
	})
}
