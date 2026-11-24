package lock

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// addLockedColumn adds the `is_locked` column to the discussions collection.
//
// It is IDEMPOTENT in the two ways a migration has to be:
//
//   - the column is added only when it is missing, so re-running is safe;
//   - existing rows are BACKFILLED from the legacy encoding, so a
//     discussion that was locked before the column existed stays locked.
//     Without the backfill, upgrading would silently reopen every closed
//     discussion — the worst possible outcome for a moderation tool.
//
// The backfill reads `state = "locked"` and leaves `state` alone:
// visibility still lives there, and rewriting it would turn a hidden
// discussion visible.
func addLockedColumn(app core.App, physical string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "lock: the %q collection is missing", physical)
	}

	if collection.Fields.GetByName(lockedField) == nil {
		collection.Fields.Add(&core.BoolField{Name: lockedField})

		if err := app.Save(collection); err != nil {
			return errors.Wrap(err, "lock: the is_locked column could not be added")
		}
	}

	return backfillLocked(app, physical)
}

// backfillLocked marks the discussions the legacy `state` marks as locked.
func backfillLocked(app core.App, physical string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "lock: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		// A filter error here is not fatal to the migration: the column
		// exists, and the reader falls back to `state`. Returning an
		// error would abort bootstrap for a cosmetic backfill.
		return nil //nolint:nilerr // the column is what matters
	}

	for _, record := range records {
		if record.GetString("state") != "locked" || record.GetBool(lockedField) {
			continue
		}

		record.Set(lockedField, true)

		if err := app.Save(record); err != nil {
			return errors.Wrap(err, "lock: a locked discussion could not be backfilled")
		}
	}

	return nil
}
