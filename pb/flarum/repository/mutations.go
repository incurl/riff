package repository

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
)

// slugPattern matches the characters a slug may contain.
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// lower lowercases a string.
func lower(text string) string { return strings.ToLower(text) }

// trim trims the cutset from both ends of a string.
func trim(text, cutset string) string { return strings.Trim(text, cutset) }

// CreateDiscussionWithPost creates a discussion and its first post.
//
// The two rows are written in one transaction: a discussion whose first
// post is missing renders as an empty thread in the frontend, so the
// pair must appear together or not at all.
//
// It returns the discussion's id and the post's id.
func (r *Repository) CreateDiscussionWithPost(userID, title, slug, content, contentHTML string) (string, string, error) {
	discussions, err := r.collection("discussions")
	if err != nil {
		return "", "", err
	}
	posts, err := r.collection("posts")
	if err != nil {
		return "", "", err
	}

	var discussionID, postID string

	err = r.app.RunInTransaction(func(txApp core.App) error {
		now := time.Now().UTC()

		discussion := core.NewRecord(discussions)
		discussion.Set("title", title)
		discussion.Set("slug", slug)
		discussion.Set("user", userID)
		discussion.Set("last_posted_user", userID)
		discussion.Set("last_post_number", 1)
		discussion.Set("comment_count", 1)
		discussion.Set("participant_count", 1)
		discussion.Set("state", "open")
		discussion.Set("created_at", now)
		discussion.Set("last_posted_at", now)
		if err := txApp.Save(discussion); err != nil {
			return err
		}
		discussionID = discussion.Id

		post := core.NewRecord(posts)
		post.Set("discussion", discussion.Id)
		post.Set("user", userID)
		post.Set("number", 1)
		post.Set("content", content)
		post.Set("content_html", contentHTML)
		post.Set("content_type", "text/html")
		post.Set("type", "comment")
		post.Set("created_at", now)
		if err := txApp.Save(post); err != nil {
			return err
		}
		postID = post.Id

		// The discussion points at its first and last post.
		discussion.Set("first_post", post.Id)
		discussion.Set("last_post", post.Id)
		return txApp.Save(discussion)
	})
	if err != nil {
		return "", "", errors.Wrap(err, "forum: could not create the discussion")
	}

	return discussionID, postID, nil
}

// CreatePost adds a reply to a discussion.
//
// The post number, the discussion's counters and its last-post pointer
// are updated in one transaction.
func (r *Repository) CreatePost(userID, discussionID, content, contentHTML string) (string, error) {
	posts, err := r.collection("posts")
	if err != nil {
		return "", err
	}
	discussions, err := r.collection("discussions")
	if err != nil {
		return "", err
	}

	var postID string

	err = r.app.RunInTransaction(func(txApp core.App) error {
		now := time.Now().UTC()

		discussion, err := txApp.FindRecordById(discussions, discussionID)
		if err != nil {
			return err
		}

		number := discussion.GetInt("last_post_number") + 1

		post := core.NewRecord(posts)
		post.Set("discussion", discussionID)
		post.Set("user", userID)
		post.Set("number", number)
		post.Set("content", content)
		post.Set("content_html", contentHTML)
		post.Set("content_type", "text/html")
		post.Set("type", "comment")
		post.Set("created_at", now)
		if err := txApp.Save(post); err != nil {
			return err
		}
		postID = post.Id

		discussion.Set("last_post_number", number)
		discussion.Set("last_post", post.Id)
		discussion.Set("last_posted_user", userID)
		discussion.Set("last_posted_at", now)
		discussion.Set("comment_count", discussion.GetInt("comment_count")+1)
		return txApp.Save(discussion)
	})
	if err != nil {
		return "", errors.Wrap(err, "forum: could not create the post")
	}

	return postID, nil
}

// UpdateDiscussionTitle renames a discussion.
//
// Upstream Flarum keeps the rename in the POST HISTORY: it writes a post of
// the type `discussionRenamed` whose content is `[oldTitle, newTitle]`, so
// the stream shows that the discussion was renamed and when.
//
// The port had the post TYPE and the frontend COMPONENT, and never created
// the post — so a rename was invisible in the stream. The content is an
// ARRAY, and the frontend reads `post.content()[0]` and
// `post.content()[1]`: the first ELEMENT, not the first character.
//
// The event post does not count towards the discussion's comment count and
// does not become its last post: it is a record of an event, not a reply.
// Upstream's `AbstractEventPost` is excluded from the comment count for
// the same reason.
func (r *Repository) UpdateDiscussionTitle(id, title, editorID string) error {
	collection, err := r.collection("discussions")
	if err != nil {
		return err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return err
	}

	oldTitle := record.GetString("title")

	record.Set("title", title)
	record.Set("slug", slugForTitle(title))

	return r.app.RunInTransaction(func(txApp core.App) error {
		if err := txApp.Save(record); err != nil {
			return err
		}

		return createRenamedEventPost(txApp, id, editorID, oldTitle, title)
	})
}

// createRenamedEventPost writes the `discussionRenamed` event post.
//
// The content is `[oldTitle, newTitle]` as a JSON array, stored as text
// because the `content` column is an editor field — and decoded back into
// an array by the API (`api.postContent`), because the frontend indexes
// into it.
func createRenamedEventPost(app core.App, discussionID, editorID, oldTitle, newTitle string) error {
	posts, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return err
	}

	content, err := json.Marshal([]string{oldTitle, newTitle})
	if err != nil {
		return err
	}

	post := core.NewRecord(posts)
	post.Set("discussion", discussionID)
	post.Set("user", editorID)
	post.Set("number", nextPostNumber(app, discussionID))
	post.Set("content", string(content))
	post.Set("content_html", "")
	post.Set("content_type", "text/html")
	post.Set("type", "discussionRenamed")

	return app.Save(post)
}

// nextPostNumber returns the number the next post in a discussion takes.
//
// An EVENT post takes a number too — it sits in the stream — but it does
// not become the discussion's last post and does not raise its comment
// count, so the number is computed rather than taken from the discussion's
// own counter.
func nextPostNumber(app core.App, discussionID string) int {
	discussions, err := app.FindCollectionByNameOrId("forum_discussions")
	if err != nil {
		return 1
	}

	discussion, err := app.FindRecordById(discussions, discussionID)
	if err != nil {
		return 1
	}

	return discussion.GetInt("last_post_number") + 1
}

// UpdatePostContent edits a post's content.
//
// The edit marker and the editor are recorded, which is what the
// frontend's "edited" badge reads.
func (r *Repository) UpdatePostContent(id, content, contentHTML, editorID string) (*domain.Post, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return nil, err
	}
	record.Set("content", content)
	record.Set("content_html", contentHTML)
	record.Set("edited_at", time.Now().UTC())
	record.Set("edited_user", editorID)
	if err := r.app.Save(record); err != nil {
		return nil, err
	}
	return postFromRecord(record), nil
}

// HidePost hides a post.
func (r *Repository) HidePost(id, actorID string) error {
	collection, err := r.collection("posts")
	if err != nil {
		return err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return err
	}
	record.Set("is_hidden", true)
	record.Set("hidden_at", time.Now().UTC())
	record.Set("hidden_user", actorID)
	return r.app.Save(record)
}

// DeletePost removes a post.
//
// The discussion's counters are corrected, because the frontend renders
// the comment count next to the thread.
func (r *Repository) DeletePost(id string) error {
	posts, err := r.collection("posts")
	if err != nil {
		return err
	}
	discussions, err := r.collection("discussions")
	if err != nil {
		return err
	}

	return r.app.RunInTransaction(func(txApp core.App) error {
		record, err := txApp.FindRecordById(posts, id)
		if err != nil {
			return err
		}
		discussionID := record.GetString("discussion")
		if err := txApp.Delete(record); err != nil {
			return err
		}

		discussion, err := txApp.FindRecordById(discussions, discussionID)
		if err != nil {
			return err
		}
		count := discussion.GetInt("comment_count")
		if count > 0 {
			discussion.Set("comment_count", count-1)
		}
		return txApp.Save(discussion)
	})
}

// DeleteDiscussion removes a discussion.
//
// The posts go with it: the posts collection cascades on the discussion
// relation, so a single delete removes the whole thread.
func (r *Repository) DeleteDiscussion(id string) error {
	collection, err := r.collection("discussions")
	if err != nil {
		return err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return err
	}
	return r.app.Delete(record)
}

// HideDiscussion hides a discussion.
func (r *Repository) HideDiscussion(id, actorID string) error {
	collection, err := r.collection("discussions")
	if err != nil {
		return err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return err
	}
	record.Set("state", "hidden")
	record.Set("hidden_at", time.Now().UTC())
	record.Set("hidden_user", actorID)
	return r.app.Save(record)
}

// slugForTitle turns a title into a slug.
//
// It mirrors the API layer's slugify, so a renamed discussion keeps the
// same URL shape.
func slugForTitle(title string) string {
	slug := slugPattern.ReplaceAllString(lower(title), "-")
	slug = trim(slug, "-")
	if slug == "" {
		return "discussion"
	}
	if len(slug) > 100 {
		slug = trim(slug[:100], "-")
	}
	return slug
}
