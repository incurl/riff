package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addMemosPayloadColumn adds a `payload` JSON column to the `memos`
// collection. The column mirrors the upstream `proto/gen/store.MemoPayload`
// message shape:
//
//	{
//	  "property": { "has_link", "has_task_list", "has_code",
//	                "has_incomplete_tasks", "title" },
//	  "location": { "placeholder", "latitude", "longitude" }
//	}
//
// `tags` is intentionally NOT inside `payload` here. The top-level
// `tags` JSON column on `memos` (added in create_memos.go) is the
// source of truth: `pb/api/feed.go:89` filters on it via PocketBase's
// `tags ?= {:tag}` JSON operator, and four call sites read it via
// `GetStringSlice("tags")`. Moving it would force every filter and
// every reader to change for no real benefit.
//
// `location` lives inside `payload` as an empty shell on every row
// because riff does not have a geo feature yet. Keeping the
// upstream-shaped envelope means a future geo migration only has to
// populate the existing object instead of redesigning the column.
//
// No backfill is attempted. Riff is pre-release; the same
// reasoning the 1720000000_add_timestamps.go migration documented
// applies here: rows written before this column existed surface
// with an empty `payload` object and the property/location fields
// come back falsy. New writes go through the BeforeCreate/Update
// hook in pb/hooks/memos.go, which materializes property from
// markdown.Service.ExtractProperties on every save.
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		if memos.Fields.GetByName("payload") != nil {
			// Idempotent: the column already exists. Re-running
			// migrations is a supported PocketBase path; making
			// each one safe to re-apply avoids a class of "the
			// migration crashed halfway through" failures.
			return nil
		}
		memos.Fields.Add(&core.JSONField{
			Name:     "payload",
			Required: false,
		})
		return txApp.Save(memos)
	}, func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}
		memos.Fields.RemoveByName("payload")
		return txApp.Save(memos)
	})
}
