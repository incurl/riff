package likes

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// likeCollection returns the plugin's collection.
func likeCollection(app core.App) (*core.Collection, error) {
	collection, err := app.FindCollectionByNameOrId("forum_likes")
	if err != nil {
		return nil, errors.Wrap(err, "likes: the forum_likes collection is missing")
	}
	return collection, nil
}

// likeCount returns how many users liked a post.
func likeCount(app core.App, postID string) (int, error) {
	collection, err := likeCollection(app)
	if err != nil {
		return 0, err
	}
	records, err := app.FindRecordsByFilter(collection, "post = {:post}", "", 0, 0,
		map[string]any{"post": postID})
	if err != nil {
		return 0, err
	}
	return len(records), nil
}

// isLiked reports whether a user liked a post.
func isLiked(app core.App, postID, userID string) (bool, error) {
	collection, err := likeCollection(app)
	if err != nil {
		return false, err
	}
	record, err := app.FindFirstRecordByFilter(collection, "post = {:post} && user = {:user}",
		map[string]any{"post": postID, "user": userID})
	if err != nil {
		// The record is missing: the user has not liked the post. That
		// is an answer, not a failure.
		return false, nil //nolint:nilerr // a missing like is "not liked"
	}
	return record != nil, nil
}

// likerIDs returns the ids of a post's likers.
//
// The current user's like comes first, so the frontend can highlight it.
func likerIDs(app core.App, postID string, limit int) ([]string, error) {
	collection, err := likeCollection(app)
	if err != nil {
		return nil, err
	}
	records, err := app.FindRecordsByFilter(collection, "post = {:post}", "created_at", 0, 0,
		map[string]any{"post": postID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.GetString("user"))
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	return ids, nil
}

// addLike likes a post.
//
// It is idempotent: liking a post the user already liked leaves the
// like count unchanged. The unique index is the final guard, so a
// concurrent double-click cannot create a second row.
func addLike(app core.App, postID, userID string) error {
	collection, err := likeCollection(app)
	if err != nil {
		return err
	}
	existing, err := app.FindFirstRecordByFilter(collection, "post = {:post} && user = {:user}",
		map[string]any{"post": postID, "user": userID})
	if err == nil && existing != nil {
		return nil
	}

	record := core.NewRecord(collection)
	record.Set("post", postID)
	record.Set("user", userID)
	return app.Save(record)
}

// removeLike unlikes a post.
//
// It is idempotent too: unliking a post that was never liked succeeds.
func removeLike(app core.App, postID, userID string) error {
	collection, err := likeCollection(app)
	if err != nil {
		return err
	}
	record, err := app.FindFirstRecordByFilter(collection, "post = {:post} && user = {:user}",
		map[string]any{"post": postID, "user": userID})
	if err != nil {
		// The like is already gone: unliking twice is not a failure.
		return nil //nolint:nilerr // an absent like is already unliked
	}
	return app.Delete(record)
}
