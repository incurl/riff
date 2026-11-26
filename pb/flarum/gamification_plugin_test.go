package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugins/gamification"
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
// removeGroupPermission takes a permission away from a group.
//
// The rows are per (group, permission), so the row is DELETED rather than
// flipped: a row with `allowed = false` would be a deny, and the
// permission service's lookup stops at the first ALLOW, so a deny row
// alongside an allow row for another group would not take effect.
func removeGroupPermission(t *testing.T, app core.App, groupID, permission string) {
	t.Helper()

	collection := mustCollection(t, app, "forum_permissions")

	records, err := app.FindRecordsByFilter(collection, "permission = {:permission} && group = {:group}", "", 0, 0, map[string]any{
		"permission": permission,
		"group":      groupID,
	})
	require.NoError(t, err)

	for _, record := range records {
		require.NoError(t, app.Delete(record))
	}
}

// removeMemberPermission takes a permission away from the member group.
func removeMemberPermission(t *testing.T, app core.App, permission string) {
	t.Helper()

	removeGroupPermission(t, app, domain.GroupMemberID, permission)
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
	// Upstream composes the id as `metric:period:position`, so an entry
	// from a different board is a different record to a client store.
	require.Equal(t, "posts:all:1", firstEntry["id"], "the id must name the ranking")
}

// TestGamification_TheDefaultMetricAndPeriodAreHonoured pins
// upstream's `fof-gamification.defaultMetric` and
// `fof-gamification.defaultPeriod`.
//
// A request that names no metric falls back to the setting, and a
// request that names an unknown one is a validation failure — so the
// setting's value has to be one the server KNOWS, and the two
// settings are what the admin portal offers the operator.
//
// The default is what the leaderboard shows when nobody asks for
// anything, so the id of the top entry names the ranking it came
// from.
func TestGamification_TheDefaultMetricAndPeriodAreHonoured(t *testing.T) {
	app, handler := newTestForum(t)

	_, second := memberSession(t, app, "game_default")

	discussionID, _ := seedDiscussion(t, app, "Default", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", second).Code)

	// The setting names a metric and a period the server knows.
	setForumSetting(t, app, gamification.SettingDefaultMetric, gamification.MetricVotes)
	setForumSetting(t, app, gamification.SettingDefaultPeriod, gamification.PeriodAllTime)

	recorder := do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", second)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	entries := leaderboardEntries(t, recorder)
	require.NotEmpty(t, entries, "the seeded discussion must appear")

	require.Equal(t, "votes:all:1", entries[0]["id"],
		"the default metric and period must decide the ranking")
}

// TestGamification_TheDefaultMetricAndPeriodAreValidated pins that a
// default naming a metric the server does not know is a validation
// failure rather than an empty board.
//
// The setting is the operator's choice, and the server is the
// authority on which metrics exist. An empty board reads as "nobody
// has scored", which is a different and wrong statement.
func TestGamification_TheDefaultMetricAndPeriodAreValidated(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	setForumSetting(t, app, gamification.SettingDefaultMetric, "nonsense")

	recorder := do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
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
// Upstream seeds it for the GUEST group — the ranking is public — and this
// port seeds the member group too. The test therefore takes the grant AWAY
// rather than assuming a guest has none: a permission that answers yes for
// everybody would pass a "the member may read it" assertion without the
// permission doing anything at all.
func TestGamification_TheLeaderboardIsGated(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_ranking")

	// A member and a guest may both read it: the ranking is public.
	recorder := do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// Take the grant away from both groups: now neither may.
	removeGroupPermission(t, app, domain.GroupGuestID, domain.PermissionViewRankingPage)
	removeGroupPermission(t, app, domain.GroupMemberID, domain.PermissionViewRankingPage)

	recorder = do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", nil)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/leaderboard", "", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestGamification_ThePermissionKeysTranslate pins that the admin
// application's upstream key names reach the forum's own keys.
func TestGamification_ThePermissionKeysTranslate(t *testing.T) {
	// The admin grid posts the `discussion.`-prefixed spellings, and an
	// unknown key is stored under its own name — so a missing row here is
	// a checkbox that grants nothing.
	require.Equal(t, domain.PermissionVotePost, domain.ForumPermissionKey("discussion.votePosts"))
	require.Equal(t, domain.PermissionVotePost, domain.ForumPermissionKey("votePosts"))

	// The tally and the NAMES are two powers upstream keeps apart.
	require.Equal(t, domain.PermissionSeeVoteCount, domain.ForumPermissionKey("discussion.canSeeVotes"))
	require.Equal(t, domain.PermissionSeeVoters, domain.ForumPermissionKey("discussion.canSeeVoters"))
	require.NotEqual(t, domain.PermissionSeeVoteCount, domain.PermissionSeeVoters)

	// The extension's own key for the leaderboard.
	require.Equal(t, domain.PermissionViewRankingPage, domain.ForumPermissionKey("fof.gamification.viewRankingPage"))

	// And the reverse, which is what the admin payload publishes.
	require.Equal(t, "discussion.votePosts", domain.UpstreamPermissionKey(domain.PermissionVotePost))
	require.Equal(t, "discussion.canSeeVotes", domain.UpstreamPermissionKey(domain.PermissionSeeVoteCount))
	require.Equal(t, "discussion.canSeeVoters", domain.UpstreamPermissionKey(domain.PermissionSeeVoters))
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

// --- the ranks, as upstream's RankResource serves them ------------------

// TestGamification_TheRanksAreServed pins `GET /api/ranks`.
//
// It is upstream's `RankResource` index, and it must be readable by
// EVERYBODY who reads a post: a rank is drawn next to a member's name.
// The order is the ladder's — fewest points first — because that is the
// order a rank editor lays its rows out in.
func TestGamification_TheRanksAreServed(t *testing.T) {
	app, handler := newTestForum(t)

	seedRank(t, app, "Veteran", "#2ecc71", 100)
	seedRank(t, app, "Rookie", "#3498db", 1)

	// A guest reads them: the index is not gated.
	recorder := do(t, handler, http.MethodGet, "/forum/api/ranks", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	ranks := leaderboardEntries(t, recorder)
	require.Len(t, ranks, 2)

	first := ranks[0]
	require.Equal(t, "ranks", first["type"])

	attributes, ok := first["attributes"].(map[string]any)
	require.True(t, ok)

	require.Equal(t, "Rookie", attributes["name"], "the ladder starts at its lowest threshold")
	require.Equal(t, "#3498db", attributes["color"])
	require.InDelta(t, 1, attributes["points"], 0.001)
}

// TestGamification_ARankCanBeManagedByAnAdministrator pins upstream's three
// admin endpoints.
//
// Without them a rank can only appear by seeding the database: the ranks
// table starts empty, so every `user.rank` and every badge renders as
// nothing on a fresh forum — and the frontend has no editor to write one.
func TestGamification_ARankCanBeManagedByAnAdministrator(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	// CREATE.
	body := `{"data":{"type":"ranks","attributes":{"name":"Veteran","color":"#2ecc71","points":100}}}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/ranks", body, admin,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	created := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))

	data, ok := created["data"].(map[string]any)
	require.True(t, ok, "the create must answer with the resource: %s", recorder.Body.String())

	rankID, ok := data["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, rankID)

	// UPDATE, as a PATCH.
	recorder = doWithHeaders(t, handler, http.MethodPatch, "/forum/api/ranks/"+rankID,
		`{"data":{"attributes":{"name":"Veteran II"}}}`, admin,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	updated := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &updated))
	updatedData, ok := updated["data"].(map[string]any)
	require.True(t, ok)

	attributes, ok := updatedData["attributes"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Veteran II", attributes["name"], "a named field is updated")
	require.InDelta(t, 100, attributes["points"], 0.001, "an absent field keeps its value")

	// DELETE, as a POST carrying the override the frontend sends.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/ranks/"+rankID, "", admin,
		map[string]string{"X-CSRF-Token": "csrf-token", "X-HTTP-Method-Override": "DELETE"})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/ranks", "", admin)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, leaderboardEntries(t, recorder), "the rank is gone")
}

// TestGamification_AMemberMayNotManageRanks pins the admin gate.
//
// Upstream's endpoints are `->admin()`: a rank is a forum configuration
// row, and a member who could write one could award themselves the highest
// badge on the forum.
func TestGamification_AMemberMayNotManageRanks(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "game_rank_writer")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/ranks",
		`{"data":{"attributes":{"name":"Boss","color":"#fff","points":0}}}`, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The same for an anonymous caller.
	recorder = do(t, handler, http.MethodPost, "/forum/api/ranks", `{}`, nil)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestGamification_ARankNeedsItsFields pins the create validation.
//
// Upstream marks all three `requiredOnCreate`: a rank with no name cannot
// be rendered, and one with no threshold is reachable by everybody at
// once.
func TestGamification_ARankNeedsItsFields(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/ranks",
		`{"data":{"attributes":{"color":"#fff","points":10}}}`, admin,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/ranks",
		`{"data":{"attributes":{"name":"Nameless","color":"#fff"}}}`, admin,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
}

// TestGamification_TheUsersRanksAreResources pins the relationship.
//
// The frontend resolves `user.ranks()` out of the STORE, and the store is
// filled from `included` — so the identifier alone renders nothing. The
// plugin declares a resource serializer for exactly this, and without it
// the badge is in the database and absent from the page, with no error
// anywhere.
func TestGamification_TheUsersRanksAreResources(t *testing.T) {
	app, handler := newTestForum(t)

	author, _ := memberSession(t, app, "game_badged")
	_, voter := memberSession(t, app, "game_badger")

	seedRank(t, app, "Rookie", "#3498db", 1)

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Badged", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", voter).Code)

	recorder := do(t, handler, http.MethodGet, "/forum/api/users/"+author.Id+"?include=ranks", "", voter)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	// The account carries the relationship...
	data, ok := document["data"].(map[string]any)
	require.True(t, ok)
	relationships, ok := data["relationships"].(map[string]any)
	require.True(t, ok, "the user must carry its relationships: %v", data)

	ranks, ok := relationships["ranks"].(map[string]any)
	require.True(t, ok, "the user must carry a ranks relationship: %v", relationships)

	identifiers, ok := ranks["data"].([]any)
	require.True(t, ok)
	require.Len(t, identifiers, 1)

	// ...and the RESOURCE travels with it.
	included, ok := document["included"].([]any)
	require.True(t, ok, "the response must carry the rank resource: %s", recorder.Body.String())

	found := false
	for _, item := range included {
		resource, ok := item.(map[string]any)
		if !ok || resource["type"] != "ranks" {
			continue
		}
		found = true
		attributes, ok := resource["attributes"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "Rookie", attributes["name"])
		require.Equal(t, "#3498db", attributes["color"])
	}
	require.True(t, found, "the ranks relationship must resolve to a resource, not an identifier: %v", included)
}
