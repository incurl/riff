package gamification

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/plugin"
)

// The vote values, as upstream's `Vote` model uses them.
const (
	// VoteUp is an upvote.
	VoteUp = 1
	// VoteDown is a downvote.
	VoteDown = -1
)

// postScore returns the sum of a post's votes.
//
// A post with no votes scores zero, which is what the frontend shows.
//
// The rows are filtered by the post in the QUERY: the score is asked for
// once per post on a page (and once more per discussion in a list), so
// summing every vote in the forum for each of them would make a listing
// cost the whole vote table per row.
func postScore(app core.App, physical, postID string) (int, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return 0, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	records, err := app.FindRecordsByFilter(collection, "post = {:post}", "", 0, 0, map[string]any{"post": postID})
	if err != nil {
		return 0, errors.Wrap(err, "gamification: the votes could not be read")
	}

	score := 0
	for _, record := range records {
		score += record.GetInt("value")
	}

	return score, nil
}

// hasVote reports whether the actor's vote on a post has the given sign.
//
// A caller with no session has no vote, which is false rather than an
// error: the attribute is rendered on every post, including for a guest.
func hasVote(app core.App, physical, postID, actorID string, value int) (bool, error) {
	if actorID == "" {
		return false, nil
	}

	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return false, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	record, err := app.FindFirstRecordByFilter(collection, "post = {:post} && user = {:user}", map[string]any{
		"post": postID,
		"user": actorID,
	})
	if err != nil {
		return false, nil //nolint:nilerr // no vote is not an error
	}

	return record.GetInt("value") == value, nil
}

// voterIDs returns the accounts that voted on a post with the given sign.
func voterIDs(app core.App, physical, postID string, value int) ([]plugin.Identifier, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	records, err := app.FindRecordsByFilter(collection, "post = {:post} && value = {:value}", "created_at", 0, 0, map[string]any{
		"post":  postID,
		"value": value,
	})
	if err != nil {
		return nil, errors.Wrap(err, "gamification: the votes could not be read")
	}

	ids := make([]plugin.Identifier, 0, len(records))
	for _, record := range records {
		ids = append(ids, plugin.Identifier{Type: "users", ID: record.GetString("user")})
	}

	return ids, nil
}

// userScore returns the points an account has earned.
//
// It is the sum of the votes RECEIVED on the account's posts, which is
// upstream's `Vote::updateUserVotes`: a post's author is scored by the
// votes that post attracts, not by the votes they cast.
func userScore(app core.App, votesPhysical, postsPhysical, userID string) (int, error) {
	collection, err := app.FindCollectionByNameOrId(votesPhysical)
	if err != nil {
		return 0, errors.Wrapf(err, "gamification: the %q collection is missing", votesPhysical)
	}

	posts, err := postsByAuthor(app, postsPhysical, userID)
	if err != nil {
		return 0, err
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return 0, errors.Wrap(err, "gamification: the votes could not be read")
	}

	score := 0
	for _, record := range records {
		if _, ok := posts[record.GetString("post")]; !ok {
			continue
		}
		score += record.GetInt("value")
	}

	return score, nil
}

// userRank returns the name of the HIGHEST rank an account has earned.
//
// Upstream attaches EVERY rank whose threshold the account has passed
// (`Rank::where('points', '<=', $user->votes)`) and its frontend renders
// them all as a stack of badges. There is no stored "current rank", so it
// is derived here: the rank with the largest `points` the account
// qualifies for.
//
// The HIGHEST one is the singular answer. A lowest-first reading would
// report "Rookie" for an account that has passed "Veteran", which is the
// opposite of what the badge is for.
func userRank(app core.App, ranksPhysical, votesPhysical, postsPhysical, userID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId(ranksPhysical)
	if err != nil {
		return "", errors.Wrapf(err, "gamification: the %q collection is missing", ranksPhysical)
	}

	score, err := userScore(app, votesPhysical, postsPhysical, userID)
	if err != nil {
		return "", err
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return "", errors.Wrap(err, "gamification: the ranks could not be read")
	}

	name := ""
	best := -1

	for _, record := range records {
		points := record.GetInt("points")

		// A rank the account has not reached does not count, and a rank
		// with FEWER points than the best one found does not either.
		if points > score || points < best {
			continue
		}

		// Equal points: the first by name wins, so the answer is stable
		// whatever order the records come back in.
		if points == best && name != "" && record.GetString("name") >= name {
			continue
		}

		best = points
		name = record.GetString("name")
	}

	return name, nil
}

// rankIDs returns the ranks an account has earned.
func rankIDs(app core.App, ranksPhysical, votesPhysical, postsPhysical, userID string) ([]plugin.Identifier, error) {
	collection, err := app.FindCollectionByNameOrId(ranksPhysical)
	if err != nil {
		return nil, errors.Wrapf(err, "gamification: the %q collection is missing", ranksPhysical)
	}

	score, err := userScore(app, votesPhysical, postsPhysical, userID)
	if err != nil {
		return nil, err
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "gamification: the ranks could not be read")
	}

	ids := make([]plugin.Identifier, 0)
	for _, record := range records {
		if record.GetInt("points") > score {
			continue
		}
		ids = append(ids, plugin.Identifier{Type: TypeRank, ID: record.Id})
	}

	return ids, nil
}

// setVote records (or changes) a vote.
//
// It returns the vote's new value and whether a row was created, so the
// caller can tell a first vote from a change.
//
// The unique index on (post, user) is what makes this safe: a concurrent
// second vote for the same pair cannot add a second row.
func setVote(app core.App, physical, postID, userID string, value int) (int, bool, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return 0, false, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	record, err := app.FindFirstRecordByFilter(collection, "post = {:post} && user = {:user}", map[string]any{
		"post": postID,
		"user": userID,
	})

	created := false
	if err != nil {
		record = core.NewRecord(collection)
		record.Set("post", postID)
		record.Set("user", userID)
		created = true
	}

	record.Set("value", value)

	if err := app.Save(record); err != nil {
		return 0, false, errors.Wrap(err, "gamification: the vote could not be saved")
	}

	return record.GetInt("value"), created, nil
}

// postsByAuthor returns the ids of the posts an account wrote.
//
// The collection is the PLUGIN's resolved one, not the literal
// `forum_posts`: an instance with a different forum prefix would otherwise
// read a collection that does not exist, take the fallback below, and
// score every account at zero — silently.
func postsByAuthor(app core.App, postsPhysical, userID string) (map[string]struct{}, error) {
	collection, err := app.FindCollectionByNameOrId(postsPhysical)
	if err != nil {
		return map[string]struct{}{}, nil //nolint:nilerr // a missing collection scores zero
	}

	records, err := app.FindRecordsByFilter(collection, "user = {:user}", "", 0, 0, map[string]any{"user": userID})
	if err != nil {
		return nil, errors.Wrap(err, "gamification: the posts could not be read")
	}

	ids := make(map[string]struct{}, len(records))
	for _, record := range records {
		ids[record.Id] = struct{}{}
	}

	return ids, nil
}

// discussionOfPost returns the discussion a post belongs to.
func discussionOfPost(app core.App, postsPhysical, postID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId(postsPhysical)
	if err != nil {
		return "", errors.Wrapf(err, "gamification: the %q collection is missing", postsPhysical)
	}

	record, err := app.FindRecordById(collection, postID)
	if err != nil {
		return "", err
	}

	return record.GetString("discussion"), nil
}

// firstPostOfDiscussion returns a discussion's first post id.
//
// It is the `first_post` column the post-creation path stamps, which is
// what upstream's `$discussion->first_post_id` is.
func firstPostOfDiscussion(app core.App, discussionsPhysical, discussionID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId(discussionsPhysical)
	if err != nil {
		return "", errors.Wrapf(err, "gamification: the %q collection is missing", discussionsPhysical)
	}

	record, err := app.FindRecordById(collection, discussionID)
	if err != nil {
		return "", err
	}

	return record.GetString("first_post"), nil
}

// postAuthor returns the id of the account that wrote a post.
func postAuthor(app core.App, postsPhysical, postID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId(postsPhysical)
	if err != nil {
		return "", errors.Wrapf(err, "gamification: the %q collection is missing", postsPhysical)
	}

	record, err := app.FindRecordById(collection, postID)
	if err != nil {
		return "", err
	}

	return record.GetString("user"), nil
}
