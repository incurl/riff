package flarum

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/plugin"
	"github.com/incurl/riff/pb/flarum/plugins/polls"
	"github.com/incurl/riff/pb/flarum/repository"
)

// The polls extension, aligned with upstream `fof/polls` 2.x.
//
// The tests here pin the things a poll gets wrong QUIETLY: a guest that
// crashes the server, a poll that is in the database but never renders,
// a count that disagrees with the rows behind it, and a vote that leaks
// who voted.

// TestPolls_AnAnonymousFetchDoesNotPanic pins that a GUEST may read a
// poll.
//
// `actorIDOf` read `actor.User.ID`, and the session's actor carries a
// nil `User` for an anonymous caller — so every guest who fetched a poll
// dereferenced nil. A nil dereference in a request handler takes the
// whole PROCESS down, not just the request: one unauthenticated visitor
// killed the server.
//
// The error check only caught a missing SERVER, not a missing USER, so
// the guard read as if it covered the case.
func TestPolls_AnAnonymousFetchDoesNotPanic(t *testing.T) {
	app, handler := newTestForum(t)

	discussionID, _ := seedDiscussion(t, app, "Pollable", "Hello")
	postID := postIDOf(t, app, discussionID)

	pollID := seedPoll(t, app, postID, "First", "Second")

	// No cookies: the caller is a guest.
	recorder := do(t, handler, http.MethodGet, "/forum/api/polls/"+pollID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := data["attributes"].(map[string]any)
	require.True(t, ok, "the resource must carry attributes")

	require.Equal(t, "Which one?", attributes["question"])
}

// TestPolls_TheIncludedSetCarriesThePolls pins that a post's poll
// arrives WITH the response.
//
// The frontend resolves a relationship through the STORE, not through
// the identifier: `Model.hasMany` is
// `this.store.getById(relationshipData.type, relationshipData.id)`, and
// the store is filled from `payload.included`. So the RELATED RESOURCE
// has to travel with the response, or the relation reads as absent.
//
// The symptom is the quietest one in this plugin: the poll is in the
// database, the post carries the relationship, and the UI shows
// nothing — with no error anywhere. The frontend's own guard
// (`if (!poll) continue;`) skips the `undefined` the store returns.
func TestPolls_TheIncludedSetCarriesThePolls(t *testing.T) {
	app, handler := newTestForum(t)

	_, postID := seedDiscussion(t, app, "Pollable", "Hello")
	pollID := seedPoll(t, app, postID, "First", "Second")

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "polls",
		"the post's poll must travel with the response: %s", recorder.Body.String())

	polls := includedResourcesOf(t, recorder.Body.String(), "polls")
	require.Len(t, polls, 1, "the post carries its one poll")
	require.Equal(t, pollID, polls[0]["id"])
}

// TestPolls_TheIncludedSetCarriesTheOptionsAndTheVotes pins the deeper
// chains.
//
// Upstream's post endpoint declares:
//
//	->addDefaultInclude([
//	    'polls', 'polls.options', 'polls.myVotes', 'polls.myVotes.option',
//	])
//
// and the frontend reads the voter's own selection out of
// `poll.myVotes()` → `vote.option()`. So the VOTE resource and its
// `option` relation have to be in the response for the checkbox to
// reflect what the reader picked.
//
// A chain that stops one hop short renders the poll with no options and
// no ticked box — and the reader cannot tell that their vote is
// missing.
func TestPolls_TheIncludedSetCarriesTheOptionsAndTheVotes(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_chain_voter")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")
	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	included := includedTypesOf(t, recorder.Body.String())
	require.Contains(t, included, "poll_options",
		"the poll's options must travel with the response: %s", recorder.Body.String())
	require.Contains(t, included, "poll_votes",
		"the voter's own vote must travel with the response: %s", recorder.Body.String())

	// And the vote names its option, so the frontend can read the
	// selection.
	votes := includedResourcesOf(t, recorder.Body.String(), "poll_votes")
	require.Len(t, votes, 1, "the voter holds one vote")

	attributes, ok := votes[0]["attributes"].(map[string]any)
	require.True(t, ok, "a vote carries attributes")
	require.Equal(t, options[0], attributes["optionId"], "the vote names its option")
	require.Equal(t, pollID, attributes["pollId"], "the vote names its poll")
}

// TestPolls_AMultiVotePollHoldsOneRowPerOption pins that a poll which
// allows several votes actually works.
//
// **THE UNIQUE INDEX WAS ON (poll, user), NOT (poll, option, user).** A
// voter may therefore hold only ONE vote row per poll, which makes
// `allow_multiple_votes` impossible: the second row violates the
// constraint and the vote route answers 500.
//
// The code contradicted both the comment above it and the decision log,
// which both said the index was on (poll, option, user).
func TestPolls_AMultiVotePollHoldsOneRowPerOption(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_multi_voter")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second", "Third")
	options := pollOptionIDs(t, app, pollID)
	require.Len(t, options, 3)

	// The poll allows two votes.
	polls := mustCollection(t, app, "forum_polls")
	poll, err := app.FindRecordById(polls, pollID)
	require.NoError(t, err)
	poll.Set("settings", map[string]any{
		"allow_multiple_votes": true,
		"max_votes":            2,
	})
	require.NoError(t, app.Save(poll))

	recorder := voteInPoll(t, handler, pollID, []string{options[0], options[1]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 2, voteRowCount(t, app, pollID),
		"a multi-vote poll holds one row per (option, voter)")

	counts := optionCounts(t, handler, pollID, cookies)
	require.Equal(t, 1, counts[options[0]], "the first option carries the vote")
	require.Equal(t, 1, counts[options[1]], "the second option carries the vote")
	require.Equal(t, 0, counts[options[2]], "the third option does not")
}

// TestPolls_AMultiVotePollRefusesMoreThanTheMaximum pins the cap.
//
// Upstream's `validateInput` requires `max:$maxVotes` items, and a
// poll whose `max_votes` is zero allows as many as it has options.
func TestPolls_AMultiVotePollRefusesMoreThanTheMaximum(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_too_many")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second", "Third")
	options := pollOptionIDs(t, app, pollID)

	polls := mustCollection(t, app, "forum_polls")
	poll, err := app.FindRecordById(polls, pollID)
	require.NoError(t, err)
	poll.Set("settings", map[string]any{
		"allow_multiple_votes": true,
		"max_votes":            2,
	})
	require.NoError(t, app.Save(poll))

	recorder := voteInPoll(t, handler, pollID, options, cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code,
		"a selection beyond the cap is refused: %s", recorder.Body.String())
}

// TestPolls_AGuestsMyVotesIsEmpty pins that a guest sees no votes.
//
// **THE EMPTY ACTOR ID WAS TREATED AS "NO FILTER".** `voteIDs` read an
// empty user id as "every vote in the poll", and an anonymous caller
// has no id — so the guest's `myVotes` named EVERY voter. The poll's
// voters were handed to an unauthenticated reader.
//
// Upstream's `myVotes` matches nothing for a guest.
func TestPolls_AGuestsMyVotesIsEmpty(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_secret_voter")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The guest fetches the poll.
	document := pollDocument(t, handler, pollID, nil)

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	relationships, ok := data["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	myVotes, ok := relationships["myVotes"].(map[string]any)
	require.True(t, ok, "the poll must carry a myVotes relationship")

	identifiers, ok := myVotes["data"].([]any)
	require.True(t, ok, "the relationship must carry its identifiers")
	require.Len(t, identifiers, 0,
		"a guest's myVotes is empty — it is not every vote in the poll")
}

// TestPolls_TheVoteIndexIsOnPollOptionUser pins the index itself.
//
// The index is the only place the guarantee holds against two
// concurrent requests: an application-level check alone would let a
// double-clicked option add a second row.
func TestPolls_TheVoteIndexIsOnPollOptionUser(t *testing.T) {
	app, _ := newTestForum(t)

	collection := mustCollection(t, app, "forum_poll_votes")

	found := false
	for _, index := range collection.Indexes {
		if strings.Contains(index, "poll") && strings.Contains(index, "option") && strings.Contains(index, "user") {
			found = true
			require.Contains(t, strings.ToUpper(index), "UNIQUE",
				"the index must be unique: %s", index)
		}
	}

	require.True(t, found,
		"the votes collection must carry a unique index on (poll, option, user): %v", collection.Indexes)
}

// TestPolls_TheVoteIndexRepairReplacesTheWrongIndex pins the repair
// step.
//
// The collection is created by a PLUGIN migration, and a plugin
// migration that has already been recorded does NOT run again — so
// changing the creation step fixes a fresh install and leaves every
// existing instance with the wrong index. The repair is therefore a
// separate, idempotent step.
func TestPolls_TheVoteIndexRepairReplacesTheWrongIndex(t *testing.T) {
	app, _ := newTestForum(t)

	collection := mustCollection(t, app, "forum_poll_votes")

	// Put the wrong index back, as an instance that ran the old code
	// would have it.
	collection.RemoveIndex("idx_forum_poll_votes_poll_option_user")
	collection.AddIndex("idx_forum_poll_votes_poll_user", true, "poll, user", "")
	require.NoError(t, app.Save(collection))

	// The repair replaces it.
	require.NoError(t, repairPollVoteIndex(t, app))

	repaired := mustCollection(t, app, "forum_poll_votes")

	legacy := false
	for _, index := range repaired.Indexes {
		if strings.Contains(index, "_poll_user") {
			legacy = true
		}
	}
	require.False(t, legacy,
		"the wrong index must be dropped: %v", repaired.Indexes)

	// And the repair is idempotent.
	require.NoError(t, repairPollVoteIndex(t, app))
}

// TestPolls_TheEndDateIsAnRFC3339Timestamp pins the timestamp's shape.
//
// The column is PocketBase's date layout (`2006-01-02 15:04:05.000Z`),
// and the port's other timestamps are RFC 3339. Emitting the raw
// column value works by accident — JavaScript's `new Date()` happens to
// parse it — but it is not the format `Model.transformDate` documents.
func TestPolls_TheEndDateIsAnRFC3339Timestamp(t *testing.T) {
	app, handler := newTestForum(t)

	_, postID := seedDiscussion(t, app, "Pollable", "Hello")
	pollID := seedPoll(t, app, postID, "First", "Second")

	polls := mustCollection(t, app, "forum_polls")
	poll, err := app.FindRecordById(polls, pollID)
	require.NoError(t, err)
	poll.Set("end_date", "2026-01-02 15:04:05.000Z")
	require.NoError(t, app.Save(poll))

	attributes := pollAttributes(t, handler, pollID, nil)
	require.Equal(t, "2026-01-02T15:04:05Z", attributes["endDate"],
		"the end date is an RFC 3339 timestamp")
}

// TestPolls_TheVoteRouteRefusesAMalformedBody pins the body rules.
//
// Upstream's `validateInput` requires the `options` key to be PRESENT
// and an ARRAY, with every id one of the poll's options — otherwise a
// validation exception.
func TestPolls_TheVoteRouteRefusesAMalformedBody(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_bad_body")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	// A body that is not an array.
	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/polls/"+pollID+"/votes",
		`{"data":{"optionIds":"nope"}}`, cookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code,
		"a non-array selection is refused: %s", recorder.Body.String())

	// An option of another poll.
	recorder = voteInPoll(t, handler, pollID, []string{"not_an_option"}, cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code,
		"a foreign option is refused: %s", recorder.Body.String())

	// An option that is not one of the poll's.
	otherPoll := seedPoll(t, app, postID, "Other")
	otherOptions := pollOptionIDs(t, app, otherPoll)
	require.NotEqual(t, options[0], otherOptions[0])

	recorder = voteInPoll(t, handler, pollID, []string{otherOptions[0]}, cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code,
		"an option of another poll is refused: %s", recorder.Body.String())
}

// TestPolls_AnEndedPollTakesNoVotes pins that an ended poll refuses a
// vote.
func TestPolls_AnEndedPollTakesNoVotes(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_late_voter")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	polls := mustCollection(t, app, "forum_polls")
	poll, err := app.FindRecordById(polls, pollID)
	require.NoError(t, err)
	poll.Set("end_date", "2000-01-02 15:04:05.000Z")
	require.NoError(t, app.Save(poll))

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code,
		"an ended poll takes no votes: %s", recorder.Body.String())

	require.Equal(t, 0, voteRowCount(t, app, pollID), "no vote was recorded")
}

// TestPolls_TheCountAgreesWithTheRows pins that the reported count is
// the number of vote rows.
//
// The count is RECOMPUTED from the rows rather than incremented,
// because an increment drifts: a vote that is removed has to decrement
// the count it incremented, and a row that was never counted breaks the
// arithmetic.
func TestPolls_TheCountAgreesWithTheRows(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "poll_count_voter")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second")
	options := pollOptionIDs(t, app, pollID)

	recorder := voteInPoll(t, handler, pollID, []string{options[0]}, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Equal(t, 1, voteRowCount(t, app, pollID))
	require.Equal(t, float64(1), pollAttributes(t, handler, pollID, cookies)["voteCount"],
		"the poll's count is the number of vote rows")
}

// TestPolls_TheCountIsAbsentForAReaderWhoMayNotSeeIt pins that the
// count is OMITTED rather than sent as zero.
//
// The frontend reads the absence of the attribute as "hidden", so a
// zero would read as "nobody has voted" — a different and wrong
// statement.
func TestPolls_TheCountIsAbsentForAReaderWhoMayNotSeeIt(t *testing.T) {
	app, handler := newTestForum(t)

	_, otherCookies := memberSession(t, app, "poll_not_voted")
	_, postID := seedDiscussion(t, app, "Pollable", "Hello")

	pollID := seedPoll(t, app, postID, "First", "Second")

	attributes := pollAttributes(t, handler, pollID, otherCookies)
	_, present := attributes["voteCount"]
	require.False(t, present,
		"the count is absent for a reader who may not see it")
}

// repairPollVoteIndex runs the polls plugin's index repair.
//
// The repair is the plugin migration step's body, and the forum's tests
// drive it through the plugin itself so the test exercises the same
// path an instance takes on its next boot.
func repairPollVoteIndex(t *testing.T, app core.App) error {
	t.Helper()

	instance := polls.New()

	extenders := plugin.NewExtenders()
	extenders.Registry = repository.NewRegistry(repository.DefaultPrefix)
	require.NoError(t, instance.Register(context.Background(), extenders))

	return instance.RepairVoteIndex(app)
}
