package suspend

import (
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/repository"
)

// suspension reads an account's suspension.
//
// A missing account, or one that predates the columns, reads as "not
// suspended" rather than as an error: the attributes are rendered with
// every user in a feed, and a feed must not fail because one record is
// odd.
func suspension(app core.App, userID string) (until time.Time, message, reason string, err error) {
	collection, err := app.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return time.Time{}, "", "", errors.Wrap(err, "suspend: the shared users collection is missing")
	}

	record, err := app.FindRecordById(collection, userID)
	if err != nil {
		return time.Time{}, "", "", nil //nolint:nilerr // an unknown account is simply not suspended
	}

	return record.GetDateTime(suspendedUntilField).Time(), record.GetString(messageField), record.GetString(reasonField), nil
}
