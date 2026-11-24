package polls

import (
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// voteHandler serves PATCH /api/polls/{id}/votes.
//
// The body carries the voter's SELECTION: the set of option ids they
// have chosen. It is the whole selection rather than a delta, so the
// client and the server cannot disagree about what "my vote" is — which
// is upstream's `MultipleVotesPoll`.
//
// The route is registered for PATCH and for POST, because the frontend
// rewrites a PATCH into a POST with the method in a header and
// PocketBase's router matches on the REAL method before any handler
// runs.
func (p *Plugin) voteHandler(e *core.RequestEvent) error {
	// The route answers one verb. A POST that names no override is
	// refused, because a plain POST is not a vote.
	if err := api.RequireMethod(e, http.MethodPatch); err != nil {
		return api.WriteMethodError(e, err)
	}

	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	pollID := e.Request.PathValue("id")
	if pollID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A poll is required.")
	}

	poll, err := findPoll(e.App, p.physical[pollsCollection], pollID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That poll does not exist.")
	}

	// The vote is gated by the plugin's own permission, which upstream
	// scopes to the discussion the poll hangs off.
	allowed, err := api.Can(e, domain.PermissionVotePoll, api.TypePoll, pollID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to vote in that poll.")
	}

	// An ended poll takes no votes.
	if poll.hasEnded(nowString()) {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "That poll has ended.")
	}

	optionIDs, err := voteOptionIDs(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The vote could not be read.")
	}

	options, err := findOptions(e.App, p.physical[optionsCollection], pollID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	// The options must belong to the poll. A vote for an option of
	// another poll would otherwise count towards this one.
	known := make(map[string]struct{}, len(options))
	for _, option := range options {
		known[option.ID] = struct{}{}
	}
	for _, optionID := range optionIDs {
		if _, ok := known[optionID]; !ok {
			return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "Invalid option ID.")
		}
	}

	// The selection may not exceed what the poll allows.
	maxVotes := poll.Settings.maxVotes(len(options))
	if len(optionIDs) > maxVotes {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "You may not pick that many options.")
	}

	// CHANGING a vote is a separate power. Upstream asks for
	// `changeVote` only when the new selection differs from the old
	// one, so a voter who submits the same selection twice is not
	// refused for a vote they did not change.
	if err := p.checkChangeVote(e, poll, optionIDs, actor.User.ID); err != nil {
		return err
	}

	if _, _, err := applyVotes(e.App, p.physical[votesCollection], pollID, actor.User.ID, optionIDs); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	// The counts are RECOMPUTED from the vote rows rather than
	// incremented, so they cannot drift.
	if err := recountOptions(e.App, p.physical[optionsCollection], p.physical[votesCollection], pollID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}
	if err := recountPoll(e.App, p.physical[pollsCollection], p.physical[votesCollection], pollID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	return p.writePoll(e, http.StatusOK, pollID)
}

// checkChangeVote refuses a vote that CHANGES an existing one when the
// caller may not change it.
//
// Upstream's rule: the `changeVote` permission, or the poll's own
// `allow_change_vote` setting. The check only fires when the selection
// actually differs, so re-submitting the same vote is not a change.
func (p *Plugin) checkChangeVote(e *core.RequestEvent, poll *pollRecord, optionIDs []string, userID string) error {
	existing, err := votesByUser(e.App, p.physical[votesCollection], poll.ID, userID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}
	if len(existing) == 0 {
		// A first vote is not a change.
		return nil
	}

	current := make(map[string]struct{}, len(existing))
	for _, vote := range existing {
		current[vote.GetString("option")] = struct{}{}
	}

	wanted := make(map[string]struct{}, len(optionIDs))
	for _, optionID := range optionIDs {
		wanted[optionID] = struct{}{}
	}

	if len(current) == len(wanted) {
		same := true
		for optionID := range current {
			if _, ok := wanted[optionID]; !ok {
				same = false
				break
			}
		}
		if same {
			// The same selection: not a change.
			return nil
		}
	}

	allowed, err := api.Can(e, domain.PermissionChangePollVote, api.TypePoll, poll.ID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}
	if allowed {
		return nil
	}

	if poll.Settings.AllowChangeVote {
		return nil
	}

	return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to change your vote.")
}

// voteOptionIDs reads the selected option ids from the body.
//
// The body is the JSON:API shape (`{"data":{"optionIds":[...]}}`) and the
// flat form (`{"optionIds":[...]}`), because the upstream frontend posts
// the first and an API consumer the second.
func voteOptionIDs(e *core.RequestEvent) ([]string, error) {
	body := map[string]any{}

	if err := api.DecodeBody(e, &body); err != nil {
		return nil, err
	}

	raw, ok := body["optionIds"]
	if !ok {
		if data, isData := body["data"].(map[string]any); isData {
			raw, ok = data["optionIds"]
		}
	}
	if !ok {
		return nil, nil
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, errBadOptionIDs
	}

	ids := make([]string, 0, len(list))
	for _, item := range list {
		id, ok := item.(string)
		if !ok {
			return nil, errBadOptionIDs
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}

	return ids, nil
}

// showPollHandler serves GET /api/polls/{id}.
func (p *Plugin) showPollHandler(e *core.RequestEvent) error {
	pollID := e.Request.PathValue("id")
	if pollID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A poll is required.")
	}

	return p.writePoll(e, http.StatusOK, pollID)
}

// listPollsHandler serves GET /api/polls.
//
// The list is the polls of the post the caller names, or every poll
// when no post is named.
func (p *Plugin) listPollsHandler(e *core.RequestEvent) error {
	postID := strings.TrimSpace(e.Request.URL.Query().Get("post"))

	polls, err := p.listPolls(e.App, postID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The polls could not be loaded.")
	}

	resources := make([]api.Resource, 0, len(polls))
	for _, poll := range polls {
		resource, err := p.pollResource(e, poll)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The polls could not be loaded.")
		}
		resources = append(resources, resource)
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resources})
}

// listPolls returns the polls of a post, or every poll.
func (p *Plugin) listPolls(app core.App, postID string) ([]*pollRecord, error) {
	collection, err := app.FindCollectionByNameOrId(p.physical[pollsCollection])
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", p.physical[pollsCollection])
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, err
	}

	polls := make([]*pollRecord, 0)
	for _, record := range records {
		if postID != "" && record.GetString("post") != postID {
			continue
		}
		polls = append(polls, decodePoll(record))
	}

	return polls, nil
}

// writePoll answers with the poll's resource and its included options.
func (p *Plugin) writePoll(e *core.RequestEvent, status int, pollID string) error {
	poll, err := findPoll(e.App, p.physical[pollsCollection], pollID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That poll does not exist.")
	}

	resource, err := p.pollResource(e, poll)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The poll could not be loaded.")
	}

	options, err := findOptions(e.App, p.physical[optionsCollection], pollID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The poll could not be loaded.")
	}

	included := make([]api.Resource, 0, len(options))
	for _, option := range options {
		included = append(included, p.optionResource(e, poll, option))
	}

	return api.WriteDocument(e, status, api.Document{
		Data:     resource,
		Included: included,
	})
}

// pollResource renders a poll as a JSON:API resource.
//
// The attribute names match upstream's `PollResource`, which is what the
// frontend's poll components read.
func (p *Plugin) pollResource(e *core.RequestEvent, poll *pollRecord) (api.Resource, error) {
	actorID := actorIDOf(e)

	canVote, err := api.Can(e, domain.PermissionVotePoll, api.TypePoll, poll.ID)
	if err != nil {
		return api.Resource{}, err
	}

	canChangeVote, err := api.Can(e, domain.PermissionChangePollVote, api.TypePoll, poll.ID)
	if err != nil {
		return api.Resource{}, err
	}

	canSeeVoters, err := api.Can(e, domain.PermissionSeePollVoters, api.TypePoll, poll.ID)
	if err != nil {
		return api.Resource{}, err
	}

	// The vote COUNT is hidden until the voter has voted or the poll has
	// ended, unless the poll is public or the caller may see the count
	// without voting. Upstream's `seeVoteCount` policy.
	canSeeCount, err := p.canSeeVoteCount(e, poll, actorID)
	if err != nil {
		return api.Resource{}, err
	}

	options, err := findOptions(e.App, p.physical[optionsCollection], poll.ID)
	if err != nil {
		return api.Resource{}, err
	}

	optionIDs := make([]api.Identifier, 0, len(options))
	for _, option := range options {
		optionIDs = append(optionIDs, api.Identifier{Type: TypePollOption, ID: option.ID})
	}

	voteIDs, err := p.voteIDs(e.App, poll.ID, "")
	if err != nil {
		return api.Resource{}, err
	}

	voteIdentifiers := make([]api.Identifier, 0, len(voteIDs))
	for _, vote := range voteIDs {
		voteIdentifiers = append(voteIdentifiers, api.Identifier{Type: TypePollVote, ID: vote.ID})
	}

	myVoteIDs, err := p.voteIDs(e.App, poll.ID, actorID)
	if err != nil {
		return api.Resource{}, err
	}

	myVoteIdentifiers := make([]api.Identifier, 0, len(myVoteIDs))
	for _, vote := range myVoteIDs {
		myVoteIdentifiers = append(myVoteIdentifiers, api.Identifier{Type: TypePollVote, ID: vote.ID})
	}

	attributes := map[string]any{
		"question":           poll.Question,
		"subtitle":           poll.Subtitle,
		"hasEnded":           poll.hasEnded(nowString()),
		"allowMultipleVotes": poll.Settings.AllowMultipleVotes,
		"maxVotes":           poll.Settings.maxVotes(len(options)),
		"endDate":            poll.EndDate,
		"canVote":            canVote && !poll.hasEnded(nowString()),
		"canEdit":            false,
		"canDelete":          false,
		"canSeeVoters":       canSeeVoters && canSeeCount,
		"canChangeVote":      canChangeVote || poll.Settings.AllowChangeVote,
		"isGlobal":           poll.PostID == "",
		"publicPoll":         poll.Settings.PublicPoll,
		"hideVotes":          poll.Settings.HideVotes,
		"allowChangeVote":    poll.Settings.AllowChangeVote,
		"voteCount":          poll.VoteCount,
	}

	// The count is omitted rather than sent as zero when it may not be
	// seen: the frontend reads the ABSENCE of the attribute as "hidden",
	// so a zero would read as "nobody has voted".
	if !canSeeCount {
		delete(attributes, "voteCount")
	}

	relationships := map[string]api.Relationship{
		"options": {Data: optionIDs},
		"votes":   {Data: voteIdentifiers},
		"myVotes": {Data: myVoteIdentifiers},
	}

	if poll.PostID != "" {
		relationships["post"] = api.Relationship{Data: api.Identifier{Type: api.TypePost, ID: poll.PostID}}
	}
	if poll.UserID != "" {
		relationships["user"] = api.Relationship{Data: api.Identifier{Type: api.TypeUser, ID: poll.UserID}}
	}

	return api.Resource{
		Type:          TypePoll,
		ID:            poll.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}, nil
}

// optionResource renders an option as a JSON:API resource.
func (p *Plugin) optionResource(e *core.RequestEvent, poll *pollRecord, option pollOptionRecord) api.Resource {
	canSeeCount, err := p.canSeeVoteCount(e, poll, actorIDOf(e))
	if err != nil {
		canSeeCount = false
	}

	attributes := map[string]any{
		"answer":    option.Answer,
		"createdAt": "",
		"updatedAt": "",
	}

	// The count is omitted when it may not be seen, for the same reason
	// the poll's own count is.
	if canSeeCount {
		attributes["voteCount"] = option.VoteCount
	}

	return api.Resource{
		Type:       TypePollOption,
		ID:         option.ID,
		Attributes: attributes,
		Relationships: map[string]api.Relationship{
			"poll": {Data: api.Identifier{Type: TypePoll, ID: poll.ID}},
		},
	}
}

// canSeeVoteCount reports whether the caller may see the counts.
//
// Upstream's `seeVoteCount` policy: the voter may always see the count
// of a poll they have voted in, and so may the poll's author, and so
// may anybody who may see the results without voting. A poll that hides
// its votes keeps hiding them until it ends.
func (p *Plugin) canSeeVoteCount(e *core.RequestEvent, poll *pollRecord, actorID string) (bool, error) {
	// A poll that hides its votes keeps hiding them until it ends.
	if poll.Settings.HideVotes && poll.EndDate != "" && !poll.hasEnded(nowString()) && poll.UserID != actorID {
		return false, nil
	}

	if poll.Settings.PublicPoll {
		return true, nil
	}

	if poll.UserID == actorID {
		return true, nil
	}

	// The voter may see the count of a poll they have voted in.
	if actorID != "" {
		votes, err := votesByUser(e.App, p.physical[votesCollection], poll.ID, actorID)
		if err != nil {
			return false, err
		}
		if len(votes) > 0 {
			return true, nil
		}
	}

	allowed, err := api.Can(e, domain.PermissionSeePollVoteCount, api.TypePoll, poll.ID)
	if err != nil {
		return false, err
	}

	return allowed, nil
}

// actorIDOf returns the caller's id, or an empty string.
func actorIDOf(e *core.RequestEvent) string {
	actor, err := api.CurrentActor(e)
	if err != nil {
		return ""
	}

	return actor.User.ID
}

// nowString returns the current time in the format the poll's end date
// is stored in.
func nowString() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05.000Z")
}

// errBadOptionIDs reports an unreadable selection.
var errBadOptionIDs = errors.New("polls: the option ids could not be read")
