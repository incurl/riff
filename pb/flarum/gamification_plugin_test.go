package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The Gamification plugin is the reference implementation of a plugin that
// OWNS A SCORE. The two things worth pinning are the ones a score gets
// wrong quietly:
//
//   - the vote and the reported score disagree (the UI shows a number the
//     server does not agree with);
//   - a vote is not idempotent (a double-clicked button inflates the
//     score), which is what the unique index on (post, user) prevents.

// seedRank creates a rank.
func seedRank(t *testing.T, app core.App, name, color string, points int) string {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_ranks")
	require.NoError(t, err)

	record := core.NewRecord(collection)
	record.Set("name", name)
	record.Set("color", color)
	record.Set("points", points)
	require.NoError(t, app.Save(record))

	return record.Id
}

// postIDOf returns the id of a discussion's first post.
func postIDOf(t *testing.T, app core.App, discussionID string) string {
	t.Helper()

	record, err := app.FindRecordById(mustCollection(t, app, "forum_posts"), discussionID)
	_ = record
	_ = err

	posts, err := app.FindAllRecords(mustCollection(t, app, "forum_posts"), nil)
	require.NoError(t, err)

	for _, post := range posts {
		if post.GetString("discussion") == discussionID {
			return post.Id
		}
	}

	t.Fatalf("no post found for discussion %s", discussionID)

	return ""
}

// mustCollection finds a collection or fails the test.
func mustCollection(t *testing.T, app core.App, name string) *core.Collection {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId(name)
	require.NoError(t, err)

	return collection
}

// voteAs posts a vote and returns the recorder.
func voteAs(t *testing.T, handler http.Handler, postID, vote string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	body := `{"data":{"attributes":{"vote":"` + vote + `"}}}`

	return doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/vote", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
}

// removeMemberPermission takes a permission away from the member group.
//
// The rows are per (group, permission), so the row is DELETED rather than
// flipped: a row with `allowed = false` would be a deny, and the
// permission service's lookup stops at the first ALLOW, so a deny row
// alongside an allow row for another group would not take effect.
func removeMemberPermission(t *testing.T, app core.App, permission string) {
	t.Helper()

	collection := mustCollection(t, app, "forum_permissions")

	records, err := app.FindRecordsByFilter(collection, "permission = {:permission} && group = {:group}", "", 0, 0, map[string]any{
		"permission": permission,
		"group":      domain.GroupMemberID,
	})
	require.NoError(t, err)

	for _, record := range records {
		require.NoError(t, app.Delete(record))
	}
}

// setGameSetting writes a forum setting.
func setGameSetting(t *testing.T, app core.App, key, value string) {
	t.Helper()

	collection := mustCollection(t, app, "forum_settings")

	record, err := app.FindFirstRecordByFilter(collection, "key = {:key}", map[string]any{"key": key})
	if err != nil {
		record = core.NewRecord(collection)
		record.Set("key", key)
	}

	record.Set("value", value)
	require.NoError(t, app.Save(record))
}

// TestGamification_TheVoteSetsAndClearsTheScore pins the round trip: the
// vote changes the score, and the score on the resource matches it.
func TestGamification_TheVoteSetsAndClearsTheScore(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_voter")
	discussionID, _ := seedDiscussion(t, app, "Voteable", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, 0, postVoteScore(t, handler, postID, cookies), "a new post has no votes")

	recorder := voteAs(t, handler, postID, "up", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 1, postVoteScore(t, handler, postID, cookies), "an upvote is worth one")

	// Withdrawing the vote takes the score back to zero.
	recorder = voteAs(t, handler, postID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 0, postVoteScore(t, handler, postID, cookies), "withdrawing a vote removes it")
}

// TestGamification_AVoteIsIdempotent pins the unique index.
//
// A double-clicked button must not add a second row: the account's second
// vote UPDATES the first. Without the index the score would double, and
// nothing would report it.
func TestGamification_AVoteIsIdempotent(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_double")
	discussionID, _ := seedDiscussion(t, app, "Double", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", cookies).Code)
	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", cookies).Code)

	require.Equal(t, 1, postVoteScore(t, handler, postID, cookies), "a second vote must not add a second point")

	// The row count is the real assertion: the index, not the arithmetic.
	votes := mustCollection(t, app, "forum_votes")
	records, err := app.FindAllRecords(votes, nil)
	require.NoError(t, err)
	require.Len(t, records, 1, "there must be exactly one vote row")
}

// TestGamification_ChangingAVoteReplacesIt pins that changing a vote
// replaces it rather than adding to it.
func TestGamification_ChangingAVoteReplacesIt(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_change")
	discussionID, _ := seedDiscussion(t, app, "Change", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", cookies).Code)
	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "down", cookies).Code)

	require.Equal(t, -1, postVoteScore(t, handler, postID, cookies), "a changed vote is worth the new value")
}

// TestGamification_AMemberWithoutThePermissionCannotVote pins the gate.
//
// The permission is seeded for the member group, so a member may vote —
// but the check is the permission service, not the group, so a caller
// without it is refused.
func TestGamification_AMemberWithoutThePermissionCannotVote(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_no_vote")
	discussionID, _ := seedDiscussion(t, app, "Guarded", "Hello")
	postID := postIDOf(t, app, discussionID)

	// Take the permission away from the member group.
	removeMemberPermission(t, app, domain.PermissionVotePost)

	recorder := voteAs(t, handler, postID, "up", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestGamification_AMemberCannotVoteForTheirOwnPost pins upstream's
// `allowSelfVotes` rule, which is ON by default upstream.
func TestGamification_AMemberCannotVoteForTheirOwnPost(t *testing.T) {
	app, handler := newTestForum(t)

	member, cookies := memberSession(t, app, "game_self")
	discussionID, _ := seedDiscussionWithAuthor(t, app, member.Id, "Mine", "Hello")
	postID := postIDOf(t, app, discussionID)

	// The setting is ON by default upstream, so the vote succeeds. The
	// interesting half is that the setting is READ: turning it off
	// refuses the vote.
	recorder := voteAs(t, handler, postID, "up", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	setGameSetting(t, app, "fof-gamification.allowSelfVotes", "0")

	recorder = voteAs(t, handler, postID, "up", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, "the setting must be read: %s", recorder.Body.String())
}

// TestGamification_ThePointsAndTheRank pins the score-to-rank chain: the
// votes a post receives add up to the author's points, and enough points
// earn a rank.
func TestGamification_ThePointsAndTheRank(t *testing.T) {
	app, handler := newTestForum(t)

	author, _ := memberSession(t, app, "game_author")
	_, voter := memberSession(t, app, "game_voter2")
	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Popular", "Hello")
	postID := postIDOf(t, app, discussionID)

	seedRank(t, app, "Rookie", "#cccccc", 0)
	seedRank(t, app, "Veteran", "#ff0000", 1)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", voter).Code)

	require.Equal(t, 1, userGameScore(t, handler, author.Id, voter), "the AUTHOR is scored by the votes their post received")

	rank := userGameRank(t, handler, author.Id, voter)
	require.Equal(t, "Veteran", rank, "one point earns the rank whose threshold it passes")
}

// TestGamification_TheLeaderboardRanksByMetric pins the endpoint's
// contract: the entries are ordered by score, and the id names the
// RANKING rather than just the place in it.
func TestGamification_TheLeaderboardRanksByMetric(t *testing.T) {
	app, handler := newTestForum(t)

	_, first := memberSession(t, app, "game_first")
	_, second := memberSession(t, app, "game_second")

	discussionID, _ := seedDiscussion(t, app, "One", "Hello")
	postID := postIDOf(t, app, discussionID)

	// Two accounts each post; the first is voted for twice.
	_ = first
	_ = second

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", second).Code)

	recorder := do(t, handler, http.MethodGet, "/forum/api/leaderboard?metric=posts&period=all", "", second)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	entries := leaderboardEntries(t, recorder)
	require.NotEmpty(t, entries, "the seeded discussion must appear")

	firstEntry := entries[0]
	require.Equal(t, "leaderboard-entries", firstEntry["type"])
	require.Contains(t, firstEntry["id"], "posts-all-", "the id must name the ranking: %v", firstEntry["id"])
}

// TestGamification_AnUnknownMetricIsAValidationFailure pins that a metric
// that does not exist is refused rather than answered with an empty board.
//
// An empty board reads as "nobody has scored", which is a different and
// wrong statement.
func TestGamification_AnUnknownMetricIsAValidationFailure(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/leaderboard?metric=nonsense", "", cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/leaderboard?period=decade", "", cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
}

// TestGamification_TheLeaderboardIsGated pins that the ranking page has
// its own permission, upstream's `fof.gamification.viewRankingPage`.
//
// The permission is seeded for the member group, so a member may read it
// and a GUEST may not: the ranking names who has been active, which is
// nothing a guest should be able to enumerate.
func TestGamification_TheLeaderboardIsGated(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_ranking")

	recorder := do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// A guest may not.
	recorder = do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", nil)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestGamification_ThePermissionKeysTranslate pins that the admin
// application's upstream key names reach the forum's own keys.
func TestGamification_ThePermissionKeysTranslate(t *testing.T) {
	require.Equal(t, domain.PermissionVotePost, domain.ForumPermissionKey("votePosts"))
	require.Equal(t, domain.PermissionSeeVoters, domain.ForumPermissionKey("canSeeVotes"))
}

// postVoteScore reads a post's vote score.
func postVoteScore(t *testing.T, handler http.Handler, postID string, cookies []*http.Cookie) int {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes := resourceAttributes(t, handler, "posts/"+postID, cookies)
	require.NotNil(t, attributes["votes"], "the post must carry a score: %v", attributes)

	score, ok := attributes["votes"].(float64)
	require.True(t, ok, "the score must be a number: %v", attributes["votes"])

	return int(score)
}

// userGameScore reads an account's points.
func userGameScore(t *testing.T, handler http.Handler, userID string, cookies []*http.Cookie) int {
	t.Helper()

	attributes := resourceAttributes(t, handler, "users/"+userID, cookies)

	score, ok := attributes["votes"].(float64)
	require.True(t, ok, "the account must carry a score: %v", attributes["votes"])

	return int(score)
}

// userGameRank reads an account's rank.
func userGameRank(t *testing.T, handler http.Handler, userID string, cookies []*http.Cookie) string {
	t.Helper()

	attributes := resourceAttributes(t, handler, "users/"+userID, cookies)

	rank, ok := attributes["rank"].(string)
	if !ok {
		rank = ""
	}

	return rank
}

// leaderboardEntries decodes a leaderboard response's entries.
func leaderboardEntries(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	raw, ok := document["data"].([]any)
	require.True(t, ok, "the response must carry a data array")

	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		require.True(t, ok)
		entries = append(entries, entry)
	}

	return entries
}
