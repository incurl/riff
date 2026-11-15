package repository

import (
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
// The editor is not recorded on the discussion itself: upstream Flarum
// keeps the rename in the post history, which the forum's v1 scope does
// not carry yet.
func (r *Repository) UpdateDiscussionTitle(id, title, _ string) error {
	collection, err := r.collection("discussions")
	if err != nil {
		return err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return err
	}
	record.Set("title", title)
	record.Set("slug", slugForTitle(title))
	return r.app.Save(record)
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
