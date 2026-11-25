package tags

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
)

// createTaggedEventPost records that a discussion's tags changed.
//
// It is upstream's `DiscussionTaggedPost`: a POST of the type
// `discussionTagged`, whose content is `[oldTagIds, newTagIds]` — the two
// SETS, as arrays of ids. The frontend diffs them to say which tags were
// added and which removed.
//
// The content is a JSON ARRAY, not a string, and the post's `content` column
// is an editor field (text). So the array is stored as its JSON text, and
// the API hands it to the frontend as an ARRAY — upstream's frontend reads
// `post.content()[0]`, and a string would make that the first CHARACTER.
//
// The post is NOT created when the set did not change: upstream returns
// early when the old and new ids are equal, and a post saying "nothing
// changed" is noise in the stream.
func createTaggedEventPost(app core.App, discussionID, actorID string, oldIDs, newIDs []string) error {
	posts, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return err
	}

	discussions, err := app.FindCollectionByNameOrId("forum_discussions")
	if err != nil {
		return err
	}

	discussion, err := app.FindRecordById(discussions, discussionID)
	if err != nil {
		return err
	}

	content, err := json.Marshal([][]string{oldIDs, newIDs})
	if err != nil {
		return err
	}

	post := core.NewRecord(posts)
	post.Set("discussion", discussionID)
	post.Set("user", actorID)
	post.Set("number", discussion.GetInt("last_post_number")+1)
	post.Set("content", string(content))
	post.Set("content_html", "")
	post.Set("content_type", "text/html")
	post.Set("type", "discussionTagged")

	return app.Save(post)
}

// sameTagSet reports whether two tag sets are the same set.
//
// The comparison is order-insensitive: a client that re-sends the same tags
// in a different order has not changed anything, and upstream returns early
// on equal id LISTS — which it compares after sorting.
func sameTagSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	seen := make(map[string]int, len(a))
	for _, id := range a {
		seen[id]++
	}

	for _, id := range b {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}

	return true
}
