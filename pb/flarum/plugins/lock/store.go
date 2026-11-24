package lock

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// isLocked reports whether a discussion is locked.
//
// A missing row reads as not locked rather than as an error: the flag is
// drawn on every discussion the caller can see, and a row that vanished
// between the list query and the attribute read is not a failed request.
func isLocked(app core.App, physical, discussionID string) (bool, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return false, errors.Wrapf(err, "lock: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, discussionID)
	if err != nil {
		return false, nil //nolint:nilerr // a gone discussion is simply not locked
	}

	return record.GetBool(lockedField), nil
}

// setLocked writes the flag.
//
// A repeat is a no-op rather than an error: locking an already-locked
// discussion is what a retried request looks like, and the frontend
// should not surface it as a failure.
func setLocked(app core.App, physical, discussionID string, locked bool) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "lock: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, discussionID)
	if err != nil {
		return errors.Wrap(err, "lock: the discussion does not exist")
	}

	if record.GetBool(lockedField) == locked {
		return nil
	}

	record.Set(lockedField, locked)

	if err := app.Save(record); err != nil {
		return errors.Wrap(err, "lock: the discussion could not be saved")
	}

	return nil
}
