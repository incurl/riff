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

// The Polls plugin. The things worth pinning are the ones a poll gets
// wrong quietly:
//
//   - the vote and the reported count disagree (the UI shows a number
//     the server does not agree with);
//   - a vote is not idempotent (a double-clicked option inflates the
//     count), which is what the unique index on (poll, option, user)
//     prevents;
//   - the count is visible to a reader who may not see it.
//
// seedPoll creates a poll with the given options and returns its id.
//
// The poll's author is the account that wrote the post, which is what
// upstream does: a poll is authored by whoever attached it.
func seedPoll(t *testing.T, app core.App, postID string, options ...string) string {
	t.Helper()

	polls := mustCollection(t, app, "forum_polls")
	optionCollection := mustCollection(t, app, "forum_poll_options")

	post, err := app.FindRecordById(mustCollection(t, app, "forum_posts"), postID)
	require.NoError(t, err)

	poll := core.NewRecord(polls)
	poll.Set("question", "Which one?")
	poll.Set("post", postID)
	poll.Set("user", post.GetString("user"))
	require.NoError(t, app.Save(poll))

	for _, answer := range options {
		option := core.NewRecord(optionCollection)
		option.Set("answer", answer)
		option.Set("poll", poll.Id)
		require.NoError(t, app.Save(option))
	}

	return poll.Id
}

// pollOptionIDs returns the ids of a poll's options, in order.
func pollOptionIDs(t *testing.T, app core.App, pollID string) []string {
	t.Helper()

	records, err := app.FindAllRecords(mustCollection(t, app, "forum_poll_options"), nil)
	require.NoError(t, err)

	ids := make([]string, 0)
	for _, record := range records {
		if record.GetString("poll") == pollID {
			ids = append(ids, record.Id)
		}
	}

	return ids
}

// voteInPoll casts a vote for the given option ids.
func voteInPoll(t *testing.T, handler http.Handler, pollID string, optionIDs []string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	encoded, err := json.Marshal(optionIDs)
	require.NoError(t, err)

	body := `{"data":{"optionIds":` + string(encoded) + `}}`

	return doWithHeaders(t, handler, http.MethodPatch, "/forum/api/polls/"+pollID+"/votes", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
}

// pollDocument fetches a poll and returns its document.
func pollDocument(t *testing.T, handler http.Handler, pollID string, cookies []*http.Cookie) map[string]any {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/api/polls/"+pollID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	return document
}

// pollAttributes fetches a poll and returns its attributes.
func pollAttributes(t *testing.T, handler http.Handler, pollID string, cookies []*http.Cookie) map[string]any {
	t.Helper()

	document := pollDocument(t, handler, pollID, cookies)

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := data["attributes"].(map[string]any)
	require.True(t, ok, "the resource must carry attributes")

	return attributes
}

// optionCounts returns the vote count of every option of a poll.
func optionCounts(t *testing.T, handler http.Handler, pollID string, cookies []*http.Cookie) map[string]int {
	t.Helper()

	document := pollDocument(t, handler, pollID, cookies)

	included, ok := document["included"].([]any)
	if !ok {
		included = nil
	}
	counts := make(map[string]int)

	for _, item := range included {
		option, isObject := item.(map[string]any)
		if !isObject {
			continue
		}
		attributes, isMap := option["attributes"].(map[string]any)
		if !isMap {
			continue
		}
		count, isNumber := attributes["voteCount"].(float64)
		if !isNumber {
			continue
		}
		id, isString := option["id"].(string)
		if !isString {
			continue
		}
		counts[id] = int(count)
	}

	return counts
}

// voteRowCount returns the number of vote rows a poll holds.
func voteRowCount(t *testing.T, app core.App, pollID string) int {
	t.Helper()

	records, err := app.FindAllRecords(mustCollection(t, app, "forum_poll_votes"), nil)
	require.NoError(t, err)

	count := 0
	for _, record := range records {
		if record.GetString("poll") == pollID {
			count++
		}
	}

	return count
}

// TestPolls_TheVoteIsRecordedAndTheCountFollows pins the round trip: the
// vote is recorded, the option's count follows it, and the poll's own
// count is the number of votes cast.
func TestPolls_TheVoteIsRecordedAndTheCountFollows(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_voter")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)
	require.Len(t, options, 2)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 1, voteRowCount(t, app, pollID), "the vote is recorded once")

	counts := optionCounts(t, handler, pollID, cookies)
	require.Equal(t, 1, counts[options[0]], "the chosen option carries the vote")
	require.Equal(t, 0, counts[options[1]], "the other option does not")

	attributes := pollAttributes(t, handler, pollID, cookies)
	require.Equal(t, float64(1), attributes["voteCount"], "the poll counts the votes cast")
}

// TestPolls_AVoteIsIdempotent pins that voting twice for the same option
// does not inflate the count.
//
// The unique index on (poll, option, user) is what makes this hold
// against two concurrent requests; the application-level check alone
// would not.
func TestPolls_AVoteIsIdempotent(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_twice")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 1, voteRowCount(t, app, pollID), "the vote is still one row")

	counts := optionCounts(t, handler, pollID, cookies)
	require.Equal(t, 1, counts[options[0]], "the count is not inflated")
}

// TestPolls_ChangingAVoteReplacesIt pins that a voter's second choice
// REPLACES the first: the old option loses the vote and the new one
// gains it.
func TestPolls_ChangingAVoteReplacesIt(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_changer")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = voteInPoll(t, handler, pollID, []string{options[1]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 1, voteRowCount(t, app, pollID), "the vote is still one row")

	counts := optionCounts(t, handler, pollID, cookies)
	require.Equal(t, 0, counts[options[0]], "the old option lost the vote")
	require.Equal(t, 1, counts[options[1]], "the new option carries it")
}

// TestPolls_WithdrawingTheVoteIsAnEmptySelection pins that an empty
// selection withdraws the vote.
func TestPolls_WithdrawingTheVoteIsAnEmptySelection(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_withdrawer")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 1, voteRowCount(t, app, pollID))

	recorder = voteInPoll(t, handler, pollID, []string{}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 0, voteRowCount(t, app, pollID), "the vote is withdrawn")
}

// TestPolls_AMemberWithoutThePermissionCannotVote pins the permission
// gate: the vote is the plugin's own route, so removing the permission
// removes the vote.
func TestPolls_AMemberWithoutThePermissionCannotVote(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_no_permission")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	removeMemberPermission(t, app, domain.PermissionVotePoll)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	require.Equal(t, 0, voteRowCount(t, app, pollID), "no vote was recorded")
}

// TestPolls_AGuestCannotVote pins that a vote needs an account.
func TestPolls_AGuestCannotVote(t *testing.T) {
	app, handler := newTestForum(t)

	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, nil)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())

	require.Equal(t, 0, voteRowCount(t, app, pollID), "no vote was recorded")
}

// TestPolls_APlainPostIsRefused pins that the route answers one verb: a
// POST that names no override is not a vote.
//
// The frontend rewrites a PATCH into a POST with the method in a header,
// and PocketBase's router matches on the REAL method before any handler
// runs, so the route carries a POST twin — but the twin resolves the
// effective method itself and refuses a POST that names none.
func TestPolls_APlainPostIsRefused(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_plain_post")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	body := `{"data":{"optionIds":["` + options[0] + `"]}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/polls/"+pollID+"/votes", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code, recorder.Body.String())

	require.Equal(t, 0, voteRowCount(t, app, pollID), "no vote was recorded")
}

// TestPolls_TheMethodOverrideIsHonoured pins that the POST twin with
// the override header answers the vote, which is how the frontend's
// PATCH actually reaches the server.
func TestPolls_TheMethodOverrideIsHonoured(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_override")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	body := `{"data":{"optionIds":["` + options[0] + `"]}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/polls/"+pollID+"/votes", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token", "X-HTTP-Method-Override": "PATCH"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 1, voteRowCount(t, app, pollID), "the vote was recorded")
}

// TestPolls_AVoteForAnOptionOfAnotherPollIsRefused pins that the option
// must belong to the poll.
//
// A vote for a foreign option would otherwise count towards this poll.
func TestPolls_AVoteForAnOptionOfAnotherPollIsRefused(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_foreign")
	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	otherPollID := seedPoll(t, app, postID, "Other")
	foreign := pollOptionIDs(t, app, otherPollID)

	recorder := voteInPoll(t, handler, pollID, []string{foreign[0]}, cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	require.Equal(t, 0, voteRowCount(t, app, pollID), "no vote was recorded")
}

// TestPolls_TheCountIsHiddenFromAReaderWhoMayNotSeeIt pins that the
// count is ABSENT rather than zero when the reader may not see it.
//
// The frontend reads the absence of the attribute as "hidden", so a zero
// would read as "nobody has voted".
func TestPolls_TheCountIsHiddenFromAReaderWhoMayNotSeeIt(t *testing.T) {
	app, handler := newTestForum(t)

	_, voterCookies := memberSession(t, app, "poll_reader_votes")
	_, otherCookies := memberSession(t, app, "poll_reader_waits")

	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	// A reader who has NOT voted may not see the count.
	attributes := pollAttributes(t, handler, pollID, otherCookies)
	_, present := attributes["voteCount"]
	require.False(t, present, "the count is hidden from a reader who may not see it")

	// The voter may see it.
	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, voterCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes = pollAttributes(t, handler, pollID, voterCookies)
	require.Equal(t, float64(1), attributes["voteCount"], "the voter sees the count")
}

// TestPolls_ThePermissionKeysTranslate pins that upstream's permission
// names map onto the forum's own.
//
// Without the translation the admin's permission page is inert: every
// key it writes is a key the forum never reads, so granting a
// permission changes nothing.
func TestPolls_ThePermissionKeysTranslate(t *testing.T) {
	require.Equal(t, domain.PermissionVotePoll, domain.ForumPermissionKey("polls.vote"))
	require.Equal(t, domain.PermissionChangePollVote, domain.ForumPermissionKey("polls.changeVote"))
	require.Equal(t, domain.PermissionSeePollVoteCount, domain.ForumPermissionKey("polls.viewResultsWithoutVoting"))

	// And back again.
	require.Equal(t, "polls.vote", domain.UpstreamPermissionKey(domain.PermissionVotePoll))
}

// TestPolls_ThePostCarriesItsPolls pins the wiring the frontend depends
// on: the POST resource carries the `polls` relationship.
//
// The frontend renders the poll by reading `post.polls()`, so a poll
// that exists but is not on the post's relationship is INVISIBLE — the
// poll is in the database and the UI shows nothing, with no error
// anywhere.
func TestPolls_ThePostCarriesItsPolls(t *testing.T) {
	app, handler := newTestForum(t)

	_, postID := seedDiscussion(t, app, "Pollable", "Hello")
	pollID := seedPoll(t, app, postID, "First", "Second")

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	relationships, ok := data["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	polls, ok := relationships["polls"].(map[string]any)
	require.True(t, ok, "the post must carry a polls relationship")

	identifiers, ok := polls["data"].([]any)
	require.True(t, ok, "the relationship must carry its identifiers")
	require.Len(t, identifiers, 1, "the post carries its one poll")

	identifier, ok := identifiers[0].(map[string]any)
	require.True(t, ok, "an identifier must be an object")
	require.Equal(t, "polls", identifier["type"], "the identifier names the poll type")
	require.Equal(t, pollID, identifier["id"], "the identifier names the poll")
}
