package polls

import (
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
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

	// The vote is gated by the plugin's own permission, scoped to the
	// DISCUSSION the poll hangs off.
	//
	// **UPSTREAM SCOPES IT TO THE DISCUSSION, NOT TO THE POLL.** Its
	// policy is `$actor->can('polls.vote', $discussion)`, and this
	// port's key table maps upstream's `discussion.polls.vote` onto
	// `votePoll`. A poll-scoped check can never match a grant the
	// operator made for a discussion — and it is the same scope the
	// `canVote` attribute asks, so the flag and the write path agree.
	subjectType, subjectID, err := p.voteSubject(e.App, poll)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	allowed, err := api.Can(e, domain.PermissionVotePoll, subjectType, subjectID)
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

	subjectType, subjectID, err := p.voteSubject(e.App, poll)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	allowed, err := api.Can(e, domain.PermissionChangePollVote, subjectType, subjectID)
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
//
// **THE INCLUDED SET IS THE POLL'S OPTIONS AND THE CALLER'S OWN VOTES**,
// which is upstream's `['options', 'myVotes', 'myVotes.option']` on the
// poll's own show endpoint.
//
// The frontend reads the reader's current selection out of
// `poll.myVotes()` → `vote.option()`, so a response without the vote
// RESOURCES renders a poll whose ticked box does not match what the
// reader picked.
func (p *Plugin) writePoll(e *core.RequestEvent, status int, pollID string) error {
	poll, err := findPoll(e.App, p.physical[pollsCollection], pollID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That poll does not exist.")
	}

	resource, err := p.pollResource(e, poll)
	if err != nil {
		if errors.Is(err, errPollGone) {
			return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That poll does not exist.")
		}
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The poll could not be loaded.")
	}

	included, err := p.pollIncluded(e, poll)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The poll could not be loaded.")
	}

	return api.WriteDocument(e, status, api.Document{
		Data:     resource,
		Included: included,
	})
}

// pollIncluded builds the `included` array for a poll: its options, the
// caller's own votes, and the option each of those votes names.
//
// It is the same set the forum's `included` walk builds for a poll, so
// the poll fetched by id and the poll that arrived with a post carry
// the same resources.
func (p *Plugin) pollIncluded(e *core.RequestEvent, poll *pollRecord) ([]api.Resource, error) {
	req := p.resourceRequest(e)

	included := make([]api.Resource, 0)

	options, err := findOptions(e.App, p.physical[optionsCollection], poll.ID)
	if err != nil {
		return nil, err
	}

	for _, option := range options {
		rendered, err := p.renderPollOption(e.Request.Context(), &plugin.ResourceRequest{
			App:       req.App,
			ActorID:   req.ActorID,
			ID:        option.ID,
			IsAdmin:   req.IsAdmin,
			Can:       req.Can,
			CanScoped: req.CanScoped,
		})
		if err != nil {
			return nil, err
		}
		if rendered == nil {
			continue
		}
		included = append(included, pluginResourceToAPI(*rendered))
	}

	// The caller's own votes, and the option each names.
	myVotes, err := p.myVoteIdentifiers(e.App, poll.ID, req.ActorID)
	if err != nil {
		return nil, err
	}

	for _, vote := range myVotes {
		rendered, err := p.renderPollVote(e.Request.Context(), &plugin.ResourceRequest{
			App:       req.App,
			ActorID:   req.ActorID,
			ID:        vote.ID,
			IsAdmin:   req.IsAdmin,
			Can:       req.Can,
			CanScoped: req.CanScoped,
		})
		if err != nil {
			return nil, err
		}
		if rendered == nil {
			continue
		}
		included = append(included, pluginResourceToAPI(*rendered))
	}

	return included, nil
}

// pollResource renders a poll as a JSON:API resource.
//
// **IT IS THE SAME BUILDER THE `included` WALK USES.** The plugin's own
// route and the forum's `included` walk must answer with the SAME
// shape, or the frontend sees two different polls: the one it fetched
// by id and the one that arrived with the post.
//
// The resource serializer is the single implementation, and this is the
// plugin-facing form of it.
func (p *Plugin) pollResource(e *core.RequestEvent, poll *pollRecord) (api.Resource, error) {
	req := p.resourceRequest(e)
	req.ID = poll.ID

	rendered, err := p.renderPoll(e.Request.Context(), req)
	if err != nil {
		return api.Resource{}, err
	}
	if rendered == nil {
		return api.Resource{}, errPollGone
	}

	return pluginResourceToAPI(*rendered), nil
}

// errPollGone reports that a poll is no longer there.
var errPollGone = errors.New("polls: the poll does not exist")

// pluginResourceToAPI converts a plugin resource into the API
// package's own.
//
// The two packages are siblings, and the API package does not import
// the plugin package — so the conversion happens here.
func pluginResourceToAPI(rendered plugin.Resource) api.Resource {
	relationships := map[string]api.Relationship{}
	for name, relationship := range rendered.Relationships {
		relationships[name] = api.Relationship{Data: relationship.Data}
	}

	return api.Resource{
		Type:          rendered.Type,
		ID:            rendered.ID,
		Attributes:    rendered.Attributes,
		Relationships: relationships,
	}
}

// nowString returns the current time in the format the poll's end date
// is stored in.
func nowString() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05.000Z")
}

// errBadOptionIDs reports an unreadable selection.
var errBadOptionIDs = errors.New("polls: the option ids could not be read")
