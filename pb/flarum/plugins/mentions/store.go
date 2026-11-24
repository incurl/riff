package mentions

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// mentionCollection returns the plugin's collection.
func mentionCollection(app core.App) (*core.Collection, error) {
	collection, err := app.FindCollectionByNameOrId("forum_post_mentions_user")
	if err != nil {
		return nil, errors.Wrap(err, "mentions: the mentions collection is missing")
	}
	return collection, nil
}

// mentionRows returns the rows of a post's mentions, oldest first.
//
// A row is (mentioning post, mentioned account): the post that carries
// the `@username` and the account it names. That is upstream's
// `post_mentions_user` table, and it is what makes the post's OWN
// mentions (`mentionsUsers`) a lookup by the post.
func mentionRows(app core.App, postID string) ([]*core.Record, error) {
	collection, err := mentionCollection(app)
	if err != nil {
		return nil, err
	}
	return app.FindRecordsByFilter(collection, "post = {:post}", "created_at", 0, 0,
		map[string]any{"post": postID})
}

// mentionedUserIDs returns the accounts a post mentions.
func mentionedUserIDs(app core.App, postID string) ([]string, error) {
	rows, err := mentionRows(app, postID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.GetString("user"))
	}
	return ids, nil
}

// mentionedByPostIDs returns the posts that mention a post's author.
//
// It is upstream's `mentionedBy` relation: the posts in which the
// account that wrote this post was mentioned — "here are the other
// places you were mentioned".
//
// The rows are looked up by the MENTIONED ACCOUNT, so the row's `post`
// column already names the mentioning post: no scan of the author's
// posts is needed.
//
// The result is OLDEST FIRST: upstream's relation is `oldest('id')`, and
// the frontend shows the first few of them.
//
// Only the posts the caller may see are counted. A mention in a hidden
// discussion must not reveal that the discussion exists, so the
// visibility is applied HERE rather than by the caller.
func mentionedByPostIDs(app core.App, authorID string, limit int, visible func(postID string) bool) ([]string, error) {
	if authorID == "" {
		return nil, nil
	}

	collection, err := mentionCollection(app)
	if err != nil {
		return nil, err
	}

	rows, err := app.FindRecordsByFilter(collection, "user = {:user}", "created_at", 0, 0,
		map[string]any{"user": authorID})
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	seen := map[string]struct{}{}

	for _, row := range rows {
		mentioning := row.GetString("post")
		if mentioning == "" {
			continue
		}
		if _, duplicate := seen[mentioning]; duplicate {
			continue
		}
		if visible != nil && !visible(mentioning) {
			continue
		}
		seen[mentioning] = struct{}{}
		ids = append(ids, mentioning)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}

	return ids, nil
}

// mentionedByCount returns how many posts mention a post's author.
//
// The count is over the posts the caller may see, so it cannot reveal
// a post the caller cannot read.
func mentionedByCount(app core.App, authorID string, visible func(postID string) bool) (int, error) {
	ids, err := mentionedByPostIDs(app, authorID, 0, visible)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// syncMentions replaces a post's mentions with the given set.
//
// It REPLACES rather than merges: upstream's
// `UpdateMentionsMetadataWhenVisible` syncs on Posted/Revised/Restored,
// so a mention the edit removed is gone. A merge would keep it.
//
// It returns the mentions that are NEW — the ones the post did not have
// before. That is what the notification is driven from: a mention the
// post already had is not a new mention, and notifying it again would
// fill the reader's inbox with copies.
//
// The write is idempotent: a post whose mention set did not change ends
// up with the same rows, and the unique index is the final guard
// against a duplicate.
func syncMentions(app core.App, postID string, userIDs []string) ([]string, error) {
	collection, err := mentionCollection(app)
	if err != nil {
		return nil, err
	}

	existing, err := mentionRows(app, postID)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		wanted[userID] = struct{}{}
	}

	// The rows the edit dropped are deleted; the rows that stay are
	// left alone, so their `created_at` — and therefore the order the
	// mentions are listed in — survives an unrelated edit.
	for _, row := range existing {
		userID := row.GetString("user")
		if _, keep := wanted[userID]; keep {
			delete(wanted, userID)
			continue
		}
		if err := app.Delete(row); err != nil {
			return nil, errors.Wrap(err, "mentions: a stale mention could not be removed")
		}
	}

	// What is left is a mention the post did not have yet.
	added := make([]string, 0, len(wanted))
	for userID := range wanted {
		record := core.NewRecord(collection)
		record.Set("post", postID)
		record.Set("user", userID)
		if err := app.Save(record); err != nil {
			return nil, errors.Wrap(err, "mentions: a mention could not be stored")
		}
		added = append(added, userID)
	}

	return added, nil
}

// resolveUsername returns the id of the account with that username.
//
// A username that does not resolve is NOT an error: a mention of a
// missing account is inert, and it must not fail the save.
func resolveUsername(app core.App, username string) (string, bool) {
	if username == "" {
		return "", false
	}

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return "", false
	}

	record, err := app.FindFirstRecordByFilter(users, "username = {:username}",
		map[string]any{"username": username})
	if err != nil || record == nil {
		return "", false
	}
	return record.Id, true
}
