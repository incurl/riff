package hooks

import (
	"errors"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
)

// registerSpaces binds hooks for the `spaces` collection.
//
//  1. OnRecordCreateRequest: enforce owner = @request.auth.id and
//     atomically add the creator as an ACTIVE ADMIN member. Per
//     the Memos 0.31 design doc: "The creator becomes the first
//     ADMIN."
//
//  2. OnRecordDeleteRequest: cascade the deletion to all directly
//     assigned memos, plus their owned resources (patches,
//     samples, memo_relations endpoints, reactions). This
//     implements the design doc's:
//     "Hard-deleting a Space atomically deletes the Space, its
//     memberships, every directly assigned memo, those memos'
//     owned database resources, and relations having a deleted
//     endpoint."
func registerSpaces(app core.App) {
	app.OnRecordCreateRequest("spaces").BindFunc(func(e *core.RecordRequestEvent) error {
		// Always re-derive owner from the authenticated caller.
		// The CreateRule is open, so the body may include a
		// forged owner field; we overwrite it here.
		if e.Auth == nil {
			return errors.New("authenticated user required to create a space")
		}
		e.Record.Set("owner", e.Auth.Id)
		if err := e.Next(); err != nil {
			return err
		}
		// After the space is persisted, add the creator as
		// ACTIVE ADMIN. We do this here (not in an After hook)
		// so the membership row is in the same transaction.
		sm, err := e.App.FindCollectionByNameOrId("space_members")
		if err != nil {
			return err
		}
		member := core.NewRecord(sm)
		member.Set("space", e.Record.Id)
		member.Set("user", e.Auth.Id)
		member.Set("status", "ACTIVE")
		member.Set("role", "ADMIN")
		return e.App.Save(member)
	})

	app.OnRecordDeleteRequest("spaces").BindFunc(func(e *core.RecordRequestEvent) error {
		spaceID := e.Record.Id

		// Collect the assigned memos BEFORE deleting the space.
		// memos.space is a non-cascading relation, so PocketBase clears
		// the value on every memo that referenced this space as part of
		// its own delete handling. Running `space = <id>` afterwards
		// therefore matches nothing, and the memos would be left orphaned
		// (detached but alive) instead of deleted. Reading them first and
		// deleting by id afterwards is what actually implements the
		// design doc's "hard-deleting a Space deletes every directly
		// assigned memo".
		memos, err := e.App.FindRecordsByFilter("memos",
			"space={:id}", "", 0, 0, map[string]any{"id": spaceID})
		if err != nil {
			return err
		}
		memoIDs := make([]string, 0, len(memos))
		for _, m := range memos {
			memoIDs = append(memoIDs, m.Id)
		}

		// Run the original delete first (so CascadeDelete on
		// space_members runs), then cascade the rest.
		if err := e.Next(); err != nil {
			return err
		}
		return cascadeSpaceDeletion(e.App, spaceID, memoIDs)
	})
}

// cascadeSpaceDeletion deletes every memo assigned to the space, plus their
// owned resources. Runs in a single transaction so partial failure rolls back.
//
// memoIDs is captured before the space row was deleted; see the caller.
func cascadeSpaceDeletion(app core.App, spaceID string, memoIDs []string) error {
	return app.RunInTransaction(func(txApp core.App) error {
		for _, memoID := range memoIDs {
			m, err := txApp.FindRecordById("memos", memoID)
			if err != nil {
				// Already gone (deleted concurrently); nothing to cascade.
				continue
			}
			if err := deleteByFilter(txApp, "patches", "memo={:id}", map[string]any{"id": memoID}); err != nil {
				return err
			}
			if err := deleteByFilter(txApp, "samples", "memo={:id}", map[string]any{"id": memoID}); err != nil {
				return err
			}
			if err := deleteByFilter(txApp, "reactions", "memo={:id}", map[string]any{"id": memoID}); err != nil {
				return err
			}
			// memo_relations where this memo is EITHER endpoint.
			if err := deleteByFilter(txApp, "memo_relations", "memo={:id}", map[string]any{"id": memoID}); err != nil {
				return err
			}
			if err := deleteByFilter(txApp, "memo_relations", "related_memo={:id}", map[string]any{"id": memoID}); err != nil {
				return err
			}
			if err := txApp.Delete(m); err != nil {
				return err
			}
		}
		slog.Info("cascaded space deletion", "space_id", spaceID, "memos_deleted", len(memoIDs))
		return nil
	})
}

// deleteByFilter is a small helper that finds records matching a
// filter and deletes each.
func deleteByFilter(txApp core.App, collection string, filter string, params map[string]any) error {
	records, err := txApp.FindRecordsByFilter(collection, filter, "", 0, 0, params)
	if err != nil {
		return err
	}
	for _, r := range records {
		if err := txApp.Delete(r); err != nil {
			return err
		}
	}
	return nil
}
