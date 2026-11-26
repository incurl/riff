package polls

import (
	"context"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// The polls extension's JSON:API RESOURCES.
//
// Upstream registers three: `polls`, `poll_options` and `poll_votes`.
// The port declares the same three type names, which is what the
// frontend's store is keyed by.
//
// They are rendered through the framework's resource-serializer
// extension point, because the forum's `included` walk has no other way
// to reach a plugin's OWN resource type. Without it the post's `polls`
// relationship carries the IDENTIFIER and no RESOURCE, and the
// frontend — which resolves a relation out of the STORE, and fills the
// store from `payload.included` — reads the poll as absent: the poll
// is in the database, the post carries the relationship, and the UI
// shows nothing, with no error anywhere.

// resourceRequest builds a resource request from a request event.
//
// The plugin's OWN routes and the forum's `included` walk ask the same
// questions, and they must get the same answers: the `canVote` the
// frontend renders and the `canVote` the vote route enforces are one
// question, asked once.
func (p *Plugin) resourceRequest(e *core.RequestEvent) *plugin.ResourceRequest {
	actorID := ""

	actor, err := api.CurrentActor(e)
	if err == nil {
		actorID = actor.ID()
	}

	return &plugin.ResourceRequest{
		App:       e.App,
		ActorID:   actorID,
		IsAdmin:   err == nil && actor.IsAdmin(),
		Can:       p.canChecker(e),
		CanScoped: p.canCheckerScoped(e),
	}
}

// canChecker returns a permission checker bound to a request.
//
// It is the forum's own permission service, so the flag a plugin's
// resource carries and the flag its route enforces cannot disagree.
func (*Plugin) canChecker(e *core.RequestEvent) func(action, subjectType, subjectID string) bool {
	return func(action, subjectType, subjectID string) bool {
		allowed, err := api.Can(e, action, subjectType, subjectID)
		return err == nil && allowed
	}
}

// canCheckerScoped is the scoped form of [Plugin.canChecker]: the
// subject's own rules answer the question, and the global grant is not
// consulted.
//
// It is what a RESTRICTED TAG asks.
func (*Plugin) canCheckerScoped(e *core.RequestEvent) func(action, subjectType, subjectID string) bool {
	return func(action, subjectType, subjectID string) bool {
		allowed, err := api.CanScoped(e, action, subjectType, subjectID)
		return err == nil && allowed
	}
}

// renderPoll renders a poll as a JSON:API resource.
//
// It is upstream's `PollResource`, attribute for attribute, and the
// attribute names are what the frontend's poll components read.
func (p *Plugin) renderPoll(_ context.Context, req *plugin.ResourceRequest) (*plugin.Resource, error) {
	poll, err := findPoll(req.App, p.physical[pollsCollection], req.ID)
	if err != nil {
		// A poll that is gone is simply absent from the `included`
		// array: a missing relation is not a failed request.
		return nil, nil //nolint:nilerr // a missing resource is not a failed request
	}

	options, err := findOptions(req.App, p.physical[optionsCollection], poll.ID)
	if err != nil {
		return nil, err
	}

	canSeeCount, err := p.canSeeVoteCountAs(req, poll)
	if err != nil {
		return nil, err
	}

	canSeeVoters, err := p.canSeeVotersAs(req, poll, canSeeCount)
	if err != nil {
		return nil, err
	}

	optionIdentifiers := make([]plugin.Identifier, 0, len(options))
	for _, option := range options {
		optionIdentifiers = append(optionIdentifiers, plugin.Identifier{Type: TypePollOption, ID: option.ID})
	}

	// The VOTERS are the poll's own votes, and they travel only when the
	// caller may see them. Upstream's `votes` relationship is
	// `visible: can('seeVoters', $poll)` — and upstream OMITS the
	// relationship rather than sending an empty one, so the frontend
	// reads "no voters" as "you may not see the voters".
	var voteIdentifiers []plugin.Identifier
	if canSeeVoters {
		voteIdentifiers, err = p.allVoteIdentifiers(req.App, poll.ID)
		if err != nil {
			return nil, err
		}
	}

	// The caller's OWN votes. A guest has none: an empty actor id is
	// "this caller has no votes", NOT "every vote in the poll".
	myVoteIdentifiers, err := p.myVoteIdentifiers(req.App, poll.ID, req.ActorID)
	if err != nil {
		return nil, err
	}

	canVote, err := p.canVoteAs(req, poll)
	if err != nil {
		return nil, err
	}

	canChangeVote, err := p.canChangeVoteAs(req, poll)
	if err != nil {
		return nil, err
	}

	hasEnded := poll.hasEnded(nowString())

	attributes := map[string]any{
		"question":           poll.Question,
		"subtitle":           poll.Subtitle,
		"hasEnded":           hasEnded,
		"allowMultipleVotes": poll.Settings.AllowMultipleVotes,
		"maxVotes":           poll.Settings.maxVotes(len(options)),
		"endDate":            pollEndDate(poll),
		"createdAt":          pollTimestamp(poll.CreatedAt),
		"updatedAt":          pollTimestamp(poll.UpdatedAt),
		"canVote":            canVote && !hasEnded,
		"canEdit":            false,
		"canDelete":          false,
		"canSeeVoters":       canSeeVoters,
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

	relationships := map[string]plugin.Relationship{
		"options": {Data: optionIdentifiers},
		"myVotes": {Data: myVoteIdentifiers},
	}

	if canSeeVoters {
		relationships["votes"] = plugin.Relationship{Data: voteIdentifiers}
	}

	if poll.PostID != "" {
		relationships["post"] = plugin.Relationship{Data: plugin.Identifier{Type: api.TypePost, ID: poll.PostID}}
	}
	if poll.UserID != "" {
		relationships["user"] = plugin.Relationship{Data: plugin.Identifier{Type: api.TypeUser, ID: poll.UserID}}
	}

	return &plugin.Resource{
		Type:          TypePoll,
		ID:            poll.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}, nil
}

// renderPollOption renders a poll option as a JSON:API resource.
//
// It is upstream's `PollOptionResource`, which declares NO
// relationships — the port carries a `poll` relation too, which is
// harmless and lets the frontend walk back the other way.
func (p *Plugin) renderPollOption(_ context.Context, req *plugin.ResourceRequest) (*plugin.Resource, error) {
	option, err := findOption(req.App, p.physical[optionsCollection], req.ID)
	if err != nil {
		return nil, nil //nolint:nilerr // a missing resource is not a failed request
	}

	poll, err := findPoll(req.App, p.physical[pollsCollection], option.PollID)
	if err != nil {
		return nil, nil //nolint:nilerr // a missing resource is not a failed request
	}

	canSeeCount, err := p.canSeeVoteCountAs(req, poll)
	if err != nil {
		return nil, err
	}

	attributes := map[string]any{
		"answer":    option.Answer,
		"createdAt": pollTimestamp(option.CreatedAt),
		"updatedAt": pollTimestamp(option.UpdatedAt),
	}

	// The count is omitted when it may not be seen, for the same reason
	// the poll's own count is.
	if canSeeCount {
		attributes["voteCount"] = option.VoteCount
	}

	return &plugin.Resource{
		Type:       TypePollOption,
		ID:         option.ID,
		Attributes: attributes,
		Relationships: map[string]plugin.Relationship{
			"poll": {Data: plugin.Identifier{Type: TypePoll, ID: poll.ID}},
		},
	}, nil
}

// renderPollVote renders a poll vote as a JSON:API resource.
//
// **THE VOTE RESOURCE DID NOT EXIST AT ALL.** The type name was
// declared and used for identifiers, but nothing anywhere built one —
// so even once the `included` walk could reach a plugin's resource, the
// vote had nothing to render.
//
// The frontend needs it: the caller's own selection is read out of
// `poll.myVotes()`, and every vote names its option. Without the vote
// RESOURCE and its `option` RELATION in the response, the checkbox
// cannot reflect what the reader picked.
func (p *Plugin) renderPollVote(_ context.Context, req *plugin.ResourceRequest) (*plugin.Resource, error) {
	vote, err := findVote(req.App, p.physical[votesCollection], req.ID)
	if err != nil {
		return nil, nil //nolint:nilerr // a missing resource is not a failed request
	}

	attributes := map[string]any{
		"pollId":    vote.PollID,
		"optionId":  vote.OptionID,
		"createdAt": pollTimestamp(vote.CreatedAt),
		"updatedAt": pollTimestamp(vote.UpdatedAt),
	}

	relationships := map[string]plugin.Relationship{
		"poll":   {Data: plugin.Identifier{Type: TypePoll, ID: vote.PollID}},
		"option": {Data: plugin.Identifier{Type: TypePollOption, ID: vote.OptionID}},
	}

	if vote.UserID != "" {
		relationships["user"] = plugin.Relationship{Data: plugin.Identifier{Type: api.TypeUser, ID: vote.UserID}}
	}

	return &plugin.Resource{
		Type:          TypePollVote,
		ID:            vote.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}, nil
}

// canSeeVoteCountAs reports whether the caller may see the counts.
//
// It is upstream's `seeVoteCount` policy: the voter may always see the
// count of a poll they have voted in, and so may the poll's author, and
// so may anybody who may see the results without voting. A poll that
// hides its votes keeps hiding them until it ends.
//
// The variant that takes a request event is the one the plugin's own
// ROUTES use; this one is the resource serializer's, which has no
// request event of its own.
func (p *Plugin) canSeeVoteCountAs(req *plugin.ResourceRequest, poll *pollRecord) (bool, error) {
	actorID := req.ActorID

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
		votes, err := votesByUser(req.App, p.physical[votesCollection], poll.ID, actorID)
		if err != nil {
			return false, err
		}
		if len(votes) > 0 {
			return true, nil
		}
	}

	if req.Can == nil {
		return false, nil
	}

	return req.Can(domain.PermissionSeePollVoteCount, TypePoll, poll.ID), nil
}

// canSeeVotersAs reports whether the caller may see WHO voted.
//
// **UPSTREAM'S `seeVoters` REQUIRES `seeVoteCount`.** Its policy is:
//
//	if (!$actor->can('seeVoteCount', $poll)) deny;
//	if ($poll->public_poll) allow;
//
// so a poll whose counts are hidden does not expose its voters either —
// and `public_poll` alone is not enough when the count is hidden.
//
// The port's own `canSeeVoters` did not ask, and it was gated on
// `canSeeVoters && canSeeCount` — close, but not the same question:
// upstream denies a poll whose counts are hidden even to somebody who
// may otherwise see the voters.
func (*Plugin) canSeeVotersAs(req *plugin.ResourceRequest, poll *pollRecord, canSeeCount bool) (bool, error) {
	if !canSeeCount {
		return false, nil
	}

	if poll.Settings.PublicPoll {
		return true, nil
	}

	if req.Can == nil {
		return false, nil
	}

	return req.Can(domain.PermissionSeePollVoters, TypePoll, poll.ID), nil
}

// canVoteAs reports whether the caller may vote in the poll.
//
// Upstream's `vote` policy is SCOPED TO THE DISCUSSION: the poll hangs
// off a post, and the post hangs off a discussion, and the permission
// is `polls.vote` against that discussion. Scoping it to the poll
// instead makes a restricted tag's grant meaningless — see
// [Plugin.voteDiscussionID].
func (p *Plugin) canVoteAs(req *plugin.ResourceRequest, poll *pollRecord) (bool, error) {
	if req.Can == nil {
		return false, nil
	}

	subjectType, subjectID, err := p.voteSubject(req.App, poll)
	if err != nil {
		return false, err
	}

	return req.Can(domain.PermissionVotePoll, subjectType, subjectID), nil
}

// canChangeVoteAs reports whether the caller may change a vote they
// already cast.
//
// Upstream's `changeVote` policy is the permission OR the poll's own
// `allow_change_vote` setting.
func (p *Plugin) canChangeVoteAs(req *plugin.ResourceRequest, poll *pollRecord) (bool, error) {
	if req.Can == nil {
		return false, nil
	}

	subjectType, subjectID, err := p.voteSubject(req.App, poll)
	if err != nil {
		return false, err
	}

	return req.Can(domain.PermissionChangePollVote, subjectType, subjectID), nil
}

// voteSubject returns the subject the vote permissions are asked
// against.
//
// Upstream's `vote` policy asks for `polls.vote` against the
// DISCUSSION the poll hangs off:
//
//	$can = $discussion ? $actor->can('polls.vote', $discussion) : false;
//
// and `domain/permission_keys.go` maps upstream's `discussion.polls.vote`
// onto this port's `votePoll`. The scope has to be the discussion, so a
// RESTRICTED TAG's grant can restrict voting — the same trap the tags
// plugin already solved for `startDiscussion`.
//
// A poll that hangs off no post is a GLOBAL poll, and upstream's global
// branch asks for `discussion.polls.vote` with no subject at all.
func (p *Plugin) voteSubject(app core.App, poll *pollRecord) (string, string, error) {
	discussionID, err := p.voteDiscussionID(app, poll)
	if err != nil {
		return "", "", err
	}
	if discussionID == "" {
		// A global poll has no discussion, and upstream asks for the
		// permission with no subject at all.
		return TypePoll, poll.ID, nil
	}

	return api.TypeDiscussion, discussionID, nil
}

// pollEndDate renders a poll's end date as an RFC 3339 timestamp.
//
// The column is PocketBase's date layout (`2006-01-02 15:04:05.000Z`),
// and the port's other timestamps are rendered through the API
// package's `timestamp()`. Emitting the raw column value works by
// accident — JavaScript's `new Date()` happens to parse it — but it is
// not the format `Model.transformDate` documents, and a client that
// formats it as a date gets a different answer.
func pollEndDate(poll *pollRecord) string {
	if poll.EndDate == "" {
		return ""
	}

	parsed, err := time.Parse(pocketbaseDateLayout, poll.EndDate)
	if err != nil {
		return ""
	}

	return parsed.UTC().Format(time.RFC3339)
}

// pollTimestamp renders a stored date as an RFC 3339 timestamp.
//
// **EVERY TIMESTAMP ON THE WIRE IS RFC 3339.** The columns hold
// PocketBase's own date layout (`2006-01-02 15:04:05.000Z`), and the
// port's other resources render through the API package's
// `timestamp()`. Emitting the raw column value works by accident —
// JavaScript's `new Date()` happens to parse it — but it is not the
// format `Model.transformDate` documents, and a client that formats it
// gets a different answer.
//
// An absent or unparsable value renders as the empty string, which the
// frontend reads as "no date".
func pollTimestamp(value string) string {
	if value == "" {
		return ""
	}

	parsed, err := time.Parse(pocketbaseDateLayout, value)
	if err != nil {
		return ""
	}

	return parsed.UTC().Format(time.RFC3339)
}

// pocketbaseDateLayout is the layout PocketBase stores a date field in.
const pocketbaseDateLayout = "2006-01-02 15:04:05.000Z"
