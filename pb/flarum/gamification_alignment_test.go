package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugins/gamification"
)

// The alignment with upstream `fof/gamification` 2.x. Four things are
// pinned here, each of which used to be a silent disagreement between the
// plugin and the client that reads it:
//
//   - the leaderboard's QUERY SHAPE (`filter[metric]`, `filter[period]`,
//     `page[limit]`, `links.next`, and the `included` users);
//   - the forum-document attributes the rankings page reads BEFORE it
//     loads anything;
//   - the DISCUSSION's own vote attributes (the list's score);
//   - the two powers upstream keeps apart (`canSeeVotes` and
//     `canSeeVoters`), which this port had collapsed into one.

// TestGamification_TheLeaderboardTakesUpstreamsQuery pins the request
// shape `app.store.find('leaderboard-entries', …)` sends.
//
// The metric and the period live under `filter`, where JSON:API reserves a
// resource's own parameters, and the page is `page[offset]`/`page[limit]`.
// A server that reads only the flat spelling answers the DEFAULT board to
// every request, which looks like a leaderboard that ignores its own
// controls.
func TestGamification_TheLeaderboardTakesUpstreamsQuery(t *testing.T) {
	app, handler := newTestForum(t)

	author, _ := memberSession(t, app, "game_query_author")
	_, voter := memberSession(t, app, "game_query_voter")

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Query", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", voter).Code)

	recorder := do(t, handler, http.MethodGet,
		"/forum/api/leaderboard-entries?filter[metric]=votes&filter[period]=all&page[offset]=0&page[limit]=1", "", voter)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	entries, ok := document["data"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1, "page[limit] bounds the page")

	entry, ok := entries[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "votes:all:1", entry["id"], "the metric and period come from the FILTER")

	// The named accounts travel with the page: the frontend resolves
	// `entry.user()` out of the store, and the store is filled from
	// `included`.
	included, ok := document["included"].([]any)
	require.True(t, ok, "the page must include its users: %s", recorder.Body.String())
	require.NotEmpty(t, included)

	foundUser := false
	for _, item := range included {
		resource, ok := item.(map[string]any)
		if ok && resource["type"] == "users" && resource["id"] == author.Id {
			foundUser = true
		}
	}
	require.True(t, foundUser, "the entry's author must be included: %v", included)

	// The page size is reported: the client sizes its own paging by it.
	meta, ok := document["meta"].(map[string]any)
	require.True(t, ok)

	perPage, ok := meta["perPage"].(float64)
	require.True(t, ok)
	require.InDelta(t, 1, perPage, 0.001)
}

// TestGamification_TheLeaderboardLinksToItsNextPage pins `links.next`.
//
// It IS the "load more" control: the frontend renders one only when the
// link is present, so a page that ends without one stops the list there
// however many entries remain.
func TestGamification_TheLeaderboardLinksToItsNextPage(t *testing.T) {
	app, handler := newTestForum(t)

	first, _ := memberSession(t, app, "game_link_first")
	secondUser, second := memberSession(t, app, "game_link_second")

	// Both accounts post, so the `posts` board has two entries and a
	// one-entry page has a successor.
	discussionID, _ := seedDiscussionWithAuthor(t, app, first.Id, "Linked", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", second).Code)

	secondDiscussion, _ := seedDiscussionWithAuthor(t, app, secondUser.Id, "Also linked", "Hello")
	require.NotEmpty(t, postIDOf(t, app, secondDiscussion))

	// The page holds one entry and the board holds more.
	recorder := do(t, handler, http.MethodGet,
		"/forum/api/leaderboard-entries?filter[metric]=posts&filter[period]=all&page[limit]=1", "", second)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	links, ok := document["links"].(map[string]any)
	require.True(t, ok, "a page with successors must carry links: %s", recorder.Body.String())

	next, ok := links["next"].(string)
	require.True(t, ok, "the next link is what the load-more control reads")
	require.Contains(t, next, "filter[metric]=posts", "the link must keep the ranking it pages through")
	require.Contains(t, next, "page[offset]=1")
}

// TestGamification_TheForumDocumentCarriesTheRankingSettings pins the
// attributes `RankingsPage` reads before it loads anything.
//
// The page's metric selector is built from `leaderboardMetrics` and its
// default comes from `defaultLeaderboardMetric`; an empty list is a
// selector with no options, and a missing default makes the operator's
// choice a control that changes nothing.
func TestGamification_TheForumDocumentCarriesTheRankingSettings(t *testing.T) {
	app, handler := newTestForum(t)

	setForumSetting(t, app, gamification.SettingDefaultMetric, gamification.MetricUpvotes)
	setForumSetting(t, app, gamification.SettingDefaultPeriod, gamification.PeriodMonth)
	setForumSetting(t, app, gamification.SettingRankAmt, "3")

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	attributes, ok := document["data"].(map[string]any)["attributes"].(map[string]any)
	require.True(t, ok)

	require.Equal(t, true, attributes["canViewRankingPage"], "a guest may read the ranking")

	metrics, ok := attributes["fof-gamification.leaderboardMetrics"].([]any)
	require.True(t, ok, "the metric list must be an array: %v", attributes["fof-gamification.leaderboardMetrics"])
	require.Len(t, metrics, 4)

	first, ok := metrics[0].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, first["key"])
	require.NotEmpty(t, first["label"], "a metric with no label renders as a blank option")
	require.Equal(t, true, first["supportsPeriods"])

	periods, ok := attributes["fof-gamification.leaderboardPeriods"].([]any)
	require.True(t, ok)
	require.Len(t, periods, 5)

	require.Equal(t, gamification.MetricUpvotes, attributes["fof-gamification.defaultLeaderboardMetric"])
	require.Equal(t, gamification.PeriodMonth, attributes["fof-gamification.defaultLeaderboardPeriod"])
	require.InDelta(t, 3, attributes["fof-gamification.rankAmt"], 0.001)
}

// TestGamification_TheDiscussionCarriesItsFirstPostsVotes pins the
// discussion resource's vote fields.
//
// Upstream serves them on the DISCUSSION, not only on the post, because
// the discussion list shows the original post's score before the reader
// opens anything.
func TestGamification_TheDiscussionCarriesItsFirstPostsVotes(t *testing.T) {
	app, handler := newTestForum(t)

	author, _ := memberSession(t, app, "game_disc_author")
	_, voter := memberSession(t, app, "game_disc_voter")

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Scored", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", voter).Code)

	attributes := resourceAttributes(t, handler, "discussions/"+discussionID, voter)

	require.InDelta(t, 1, attributes["votes"], 0.001, "the discussion carries its first post's score: %v", attributes)
	require.Equal(t, true, attributes["seeVotes"])
	require.Equal(t, true, attributes["canVote"])
	require.Equal(t, true, attributes["hasUpvoted"], "the voter's own vote is on the discussion too")
}

// TestGamification_TheTallyAndTheVotersAreSeparatePowers pins upstream's
// two permissions.
//
// `discussion.canSeeVotes` shows the NUMBER and `discussion.canSeeVoters`
// shows the NAMES, and upstream's policy asks them separately. Mapping
// both onto one key made the admin grid unable to grant one without the
// other — and handed the list of who voted to everybody who could read a
// score.
func TestGamification_TheTallyAndTheVotersAreSeparatePowers(t *testing.T) {
	app, handler := newTestForum(t)

	author, _ := memberSession(t, app, "game_power_author")
	_, voter := memberSession(t, app, "game_power_voter")

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Powers", "Hello")
	postID := postIDOf(t, app, discussionID)

	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", voter).Code)

	// The MEMBER holds the tally and not the names.
	attributes := resourceAttributes(t, handler, "posts/"+postID, voter)
	require.Equal(t, true, attributes["canSeeVotes"], "a voter may see the tally")
	require.InDelta(t, 1, attributes["votes"], 0.001)
	require.Equal(t, false, attributes["seeVoters"], "a member may not see WHO voted: %v", attributes)

	// A MODERATOR holds both.
	_, moderator := moderatorSession(t, app, "game_power_mod")
	moderatorAttributes := resourceAttributes(t, handler, "posts/"+postID, moderator)
	require.Equal(t, true, moderatorAttributes["seeVoters"])

	// Take the tally away from the member group and the tally is ABSENT
	// rather than zero: an invisible field is what upstream sends.
	removeMemberPermission(t, app, domain.PermissionSeeVoteCount)

	memberAttributes := resourceAttributes(t, handler, "posts/"+postID, voter)
	require.Nil(t, memberAttributes["votes"], "a reader without the tally is not told the post scored nothing")
	require.Equal(t, false, memberAttributes["canSeeVotes"])
}

// TestGamification_TheCanVoteFlagAgreesWithTheRoute pins that the flag and
// the endpoint ask the SAME two questions.
//
// Upstream's `Access\PostPolicy::vote()` denies a vote on one's own post
// unless `fof-gamification.allowSelfVotes` is on, and the post's `canVote`
// attribute is upstream's `$actor->can('vote', $post)` — the same policy. A
// flag that ignored the setting would draw a live button whose click answers
// 403, which is the failure mode this port keeps finding: the control is not
// a failed write, it is a control that should never have been offered.
func TestGamification_TheCanVoteFlagAgreesWithTheRoute(t *testing.T) {
	app, handler := newTestForum(t)

	author, cookies := memberSession(t, app, "game_selfvote_author")

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Mine", "Hello")
	postID := postIDOf(t, app, discussionID)

	// The setting is ON by default (upstream's default), so the author may
	// vote on their own post and both sides agree.
	attributes := resourceAttributes(t, handler, "posts/"+postID, cookies)
	require.Equal(t, true, attributes["canVote"])
	require.Equal(t, http.StatusOK, voteAs(t, handler, postID, "up", cookies).Code)

	// Turn it OFF: the flag goes false, and the route refuses.
	setForumSetting(t, app, gamification.SettingAllowSelfVotes, "0")

	attributes = resourceAttributes(t, handler, "posts/"+postID, cookies)
	require.Equal(t, false, attributes["canVote"], "the flag must follow the setting")

	recorder := voteAs(t, handler, postID, "down", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The DISCUSSION's flag is the same question about its first post.
	discussionAttributes := resourceAttributes(t, handler, "discussions/"+discussionID, cookies)
	require.Equal(t, false, discussionAttributes["canVote"])
}
