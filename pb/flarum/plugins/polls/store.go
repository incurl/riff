package polls

import (
	"encoding/json"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// The poll settings, as upstream keeps them in the poll's JSON column.
//
// Upstream moved them from columns into one JSON document, and the
// frontend reads the derived attributes (`publicPoll`,
// `allowMultipleVotes`, `maxVotes`, `hideVotes`, `allowChangeVote`)
// rather than the raw keys. These are the raw keys.
const (
	// settingPublicPoll makes the voters visible.
	settingPublicPoll = "public_poll"
	// settingAllowMultipleVotes allows more than one option.
	settingAllowMultipleVotes = "allow_multiple_votes"
	// settingMaxVotes caps how many options may be picked.
	settingMaxVotes = "max_votes"
	// settingHideVotes hides the counts until the poll ends.
	settingHideVotes = "hide_votes"
	// settingAllowChangeVote allows a vote to be changed.
	settingAllowChangeVote = "allow_change_vote"
)

// pollSettings is the decoded settings document.
type pollSettings struct {
	// PublicPoll makes the voters visible.
	PublicPoll bool `json:"public_poll"`
	// AllowMultipleVotes allows more than one option.
	AllowMultipleVotes bool `json:"allow_multiple_votes"`
	// MaxVotes caps how many options may be picked. Zero means "as many
	// as there are options", which is upstream's reading.
	MaxVotes int `json:"max_votes"`
	// HideVotes hides the counts until the poll ends.
	HideVotes bool `json:"hide_votes"`
	// AllowChangeVote allows a vote to be changed.
	AllowChangeVote bool `json:"allow_change_vote"`
}

// decodeSettings reads a poll's settings document.
//
// The column is a JSON field, so it arrives as RAW JSON BYTES
// (`types.JSONRaw`) rather than as a map. A reader that only handles the
// map reads EVERY poll as having the default settings — which is how
// "the setting is not read" happens silently.
func decodeSettings(raw any) pollSettings {
	settings := pollSettings{AllowChangeVote: true}

	switch typed := raw.(type) {
	case nil:
		return settings
	case []byte:
		return decodeSettingsBytes(typed, settings)
	case types.JSONRaw:
		return decodeSettingsBytes(typed, settings)
	case json.RawMessage:
		return decodeSettingsBytes(typed, settings)
	case string:
		return decodeSettingsBytes([]byte(typed), settings)
	case map[string]any:
		return decodeSettingsMap(typed, settings)
	default:
		return settings
	}
}

// decodeSettingsBytes decodes a settings document held in bytes.
func decodeSettingsBytes(raw []byte, settings pollSettings) pollSettings {
	if len(raw) == 0 {
		return settings
	}

	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return settings
	}

	return decodeSettingsMap(decoded, settings)
}

// decodeSettingsMap decodes a settings document held in a map.
func decodeSettingsMap(decoded map[string]any, settings pollSettings) pollSettings {
	if value, ok := decoded[settingPublicPoll].(bool); ok {
		settings.PublicPoll = value
	}
	if value, ok := decoded[settingAllowMultipleVotes].(bool); ok {
		settings.AllowMultipleVotes = value
	}
	if value, ok := decoded[settingMaxVotes].(float64); ok {
		settings.MaxVotes = int(value)
	}
	if value, ok := decoded[settingHideVotes].(bool); ok {
		settings.HideVotes = value
	}
	if value, ok := decoded[settingAllowChangeVote].(bool); ok {
		settings.AllowChangeVote = value
	}

	return settings
}

// maxVotes returns how many options a poll allows.
//
// A poll that allows multiple votes but caps them at ZERO allows as many
// as it has options, which is upstream's reading of `max_votes = 0`.
func (s pollSettings) maxVotes(optionCount int) int {
	if !s.AllowMultipleVotes {
		return 1
	}
	if s.MaxVotes <= 0 {
		return optionCount
	}

	return s.MaxVotes
}

// pollRecord is a poll row, decoded.
type pollRecord struct {
	// ID is the poll's id.
	ID string
	// Question is the poll's question.
	Question string
	// Subtitle is the poll's optional subtitle.
	Subtitle string
	// PostID is the post the poll hangs off.
	PostID string
	// UserID is the account that created the poll.
	UserID string
	// EndDate is when the poll ends, empty when it does not.
	EndDate string
	// Published says whether a global poll is published.
	Published bool
	// VoteCount is the number of votes cast.
	VoteCount int
	// CreatedAt is when the poll was created.
	CreatedAt string
	// UpdatedAt is when the poll was last changed.
	UpdatedAt string
	// Settings are the poll's settings.
	Settings pollSettings
}

// hasEnded reports whether the poll's end date has passed.
func (p pollRecord) hasEnded(now string) bool {
	if p.EndDate == "" {
		return false
	}

	return p.EndDate <= now
}

// findPoll reads a poll by id.
func findPoll(app core.App, physical, pollID string) (*pollRecord, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, pollID)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the poll could not be read")
	}

	return decodePoll(record), nil
}

// decodePoll decodes a poll row.
func decodePoll(record *core.Record) *pollRecord {
	poll := &pollRecord{
		ID:        record.Id,
		Question:  record.GetString("question"),
		Subtitle:  record.GetString("subtitle"),
		PostID:    record.GetString("post"),
		UserID:    record.GetString("user"),
		EndDate:   record.GetString("end_date"),
		Published: record.GetBool("published"),
		VoteCount: record.GetInt("vote_count"),
		CreatedAt: record.GetString("created_at"),
		UpdatedAt: record.GetString("updated_at"),
		Settings:  decodeSettings(record.Get("settings")),
	}

	return poll
}

// pollOptionRecord is an option row, decoded.
type pollOptionRecord struct {
	// ID is the option's id.
	ID string
	// PollID is the poll the option belongs to.
	PollID string
	// Answer is the option's text.
	Answer string
	// VoteCount is the number of votes for the option.
	VoteCount int
	// CreatedAt is when the option was created.
	CreatedAt string
	// UpdatedAt is when the option was last changed.
	UpdatedAt string
}

// pollVoteRecord is a vote row, decoded.
type pollVoteRecord struct {
	// ID is the vote's id.
	ID string
	// PollID is the poll the vote belongs to.
	PollID string
	// OptionID is the option the vote is for.
	OptionID string
	// UserID is the account that cast the vote.
	UserID string
	// CreatedAt is when the vote was cast.
	CreatedAt string
	// UpdatedAt is when the vote was last changed.
	UpdatedAt string
}

// findOptions returns a poll's options, in creation order.
func findOptions(app core.App, physical, pollID string) ([]pollOptionRecord, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the poll options could not be read")
	}

	options := make([]pollOptionRecord, 0)
	for _, record := range records {
		if record.GetString("poll") != pollID {
			continue
		}
		options = append(options, pollOptionRecord{
			ID:        record.Id,
			PollID:    record.GetString("poll"),
			Answer:    record.GetString("answer"),
			VoteCount: record.GetInt("vote_count"),
			CreatedAt: record.GetString("created_at"),
			UpdatedAt: record.GetString("updated_at"),
		})
	}

	return options, nil
}

// findOption reads a poll option by id.
func findOption(app core.App, physical, optionID string) (*pollOptionRecord, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, optionID)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the poll option could not be read")
	}

	return &pollOptionRecord{
		ID:        record.Id,
		PollID:    record.GetString("poll"),
		Answer:    record.GetString("answer"),
		VoteCount: record.GetInt("vote_count"),
		CreatedAt: record.GetString("created_at"),
		UpdatedAt: record.GetString("updated_at"),
	}, nil
}

// findVote reads a poll vote by id.
func findVote(app core.App, physical, voteID string) (*pollVoteRecord, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, voteID)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the vote could not be read")
	}

	return &pollVoteRecord{
		ID:        record.Id,
		PollID:    record.GetString("poll"),
		OptionID:  record.GetString("option"),
		UserID:    record.GetString("user"),
		CreatedAt: record.GetString("created_at"),
		UpdatedAt: record.GetString("updated_at"),
	}, nil
}

// votesByUser returns the votes an account holds on a poll.
func votesByUser(app core.App, physical, pollID, userID string) ([]*core.Record, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the votes could not be read")
	}

	votes := make([]*core.Record, 0)
	for _, record := range records {
		if record.GetString("poll") != pollID || record.GetString("user") != userID {
			continue
		}
		votes = append(votes, record)
	}

	return votes, nil
}

// applyVotes records a set of votes, replacing the voter's previous ones.
//
// The body is the SET of options the voter has chosen: an option that is
// not in the set loses its vote, and an option that is in it gains one.
// This is upstream's `MultipleVotesPoll`: the endpoint takes the whole
// selection rather than a delta, so the client and the server cannot
// disagree about what "my vote" is.
//
// It returns the votes that were added and the ones that were removed,
// so the caller can recount the affected options.
func applyVotes(app core.App, physical, pollID, userID string, optionIDs []string) (added []string, removed []string, err error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	existing, err := votesByUser(app, physical, pollID, userID)
	if err != nil {
		return nil, nil, err
	}

	wanted := make(map[string]struct{}, len(optionIDs))
	for _, id := range optionIDs {
		wanted[id] = struct{}{}
	}

	// The votes that lose their option are removed: a voter who changed
	// their mind does not keep the old vote.
	kept := make(map[string]struct{})
	for _, vote := range existing {
		optionID := vote.GetString("option")
		if _, still := wanted[optionID]; still {
			kept[optionID] = struct{}{}
			continue
		}
		if err := app.Delete(vote); err != nil {
			return nil, nil, errors.Wrap(err, "polls: the vote could not be removed")
		}
		removed = append(removed, optionID)
	}

	// The votes that gain an option are created.
	for _, optionID := range optionIDs {
		if _, already := kept[optionID]; already {
			continue
		}
		record := core.NewRecord(collection)
		record.Set("poll", pollID)
		record.Set("option", optionID)
		record.Set("user", userID)
		if err := app.Save(record); err != nil {
			return nil, nil, errors.Wrap(err, "polls: the vote could not be saved")
		}
		added = append(added, optionID)
	}

	return added, removed, nil
}

// recountOptions writes the current vote counts onto a poll's options.
//
// The count is RECOMPUTED from the vote rows rather than incremented,
// because an increment drifts: a vote that is removed has to decrement
// the count it incremented, and a row that was never counted (a vote
// cast before the option existed) breaks the arithmetic. A recount is
// always right, and it is cheap enough at a poll's size.
func recountOptions(app core.App, physical, votesPhysical, pollID string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return errors.Wrap(err, "polls: the poll options could not be read")
	}

	for _, record := range records {
		if record.GetString("poll") != pollID {
			continue
		}

		count, err := countVotes(app, votesPhysical, record.Id)
		if err != nil {
			return err
		}

		if record.GetInt("vote_count") == count {
			continue
		}

		record.Set("vote_count", count)
		if err := app.Save(record); err != nil {
			return errors.Wrap(err, "polls: the vote count could not be saved")
		}
	}

	return nil
}

// countVotes returns the number of votes an option holds.
func countVotes(app core.App, physical, optionID string) (int, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return 0, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return 0, errors.Wrap(err, "polls: the votes could not be read")
	}

	count := 0
	for _, record := range records {
		if record.GetString("option") == optionID {
			count++
		}
	}

	return count, nil
}

// recountPoll writes the poll's own vote count.
//
// The poll's count is the number of VOTES, not the number of voters: a
// voter who picked three options has cast three votes, and upstream's
// `refreshVoteCount` counts the vote rows.
func recountPoll(app core.App, physical, votesPhysical, pollID string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, pollID)
	if err != nil {
		return errors.Wrap(err, "polls: the poll could not be read")
	}

	votes, err := allVotes(app, votesPhysical, pollID)
	if err != nil {
		return err
	}

	count := len(votes)

	if record.GetInt("vote_count") == count {
		return nil
	}

	record.Set("vote_count", count)
	if err := app.Save(record); err != nil {
		return errors.Wrap(err, "polls: the vote count could not be saved")
	}

	return nil
}

// allVotes returns every vote a poll holds.
//
// It is the poll's own count, so it is NOT filtered by voter: a voter
// who picked three options has cast three votes.
func allVotes(app core.App, physical, pollID string) ([]*core.Record, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the votes could not be read")
	}

	votes := make([]*core.Record, 0)
	for _, record := range records {
		if record.GetString("poll") == pollID {
			votes = append(votes, record)
		}
	}

	return votes, nil
}

// allVoteIdentifiers returns every vote a poll holds, as identifiers.
//
// It is what the poll's `votes` relationship carries, and it is the
// poll's own count — which is NOT filtered by voter: a voter who
// picked three options has cast three votes, and upstream's
// `refreshVoteCount` counts the vote rows.
//
// The caller may not see them: upstream's `votes` relationship is
// `visible: can('seeVoters', $poll)`.
func (p *Plugin) allVoteIdentifiers(app core.App, pollID string) ([]plugin.Identifier, error) {
	collection, err := app.FindCollectionByNameOrId(p.physical[votesCollection])
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", p.physical[votesCollection])
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the votes could not be read")
	}

	ids := make([]plugin.Identifier, 0, len(records))
	for _, record := range records {
		if record.GetString("poll") != pollID {
			continue
		}
		ids = append(ids, plugin.Identifier{Type: TypePollVote, ID: record.Id})
	}

	return ids, nil
}

// myVoteIdentifiers returns the votes ONE ACCOUNT holds on a poll.
//
// It is what the poll's `myVotes` relationship carries, and it is the
// frontend's own selection: the checkbox reflects what the reader
// picked, and the frontend reads it out of `poll.myVotes()`.
//
// **A GUEST HAS NO VOTES.** Upstream's `myVotes` matches nothing for an
// anonymous caller, and an empty user id here means "this caller has
// cast no votes" — NOT "every vote in the poll". A reader that treats
// the empty id as "no filter" hands the guest every voter's vote
// resource, which names the poll's voters in the response.
func (p *Plugin) myVoteIdentifiers(app core.App, pollID, userID string) ([]plugin.Identifier, error) {
	if userID == "" {
		return []plugin.Identifier{}, nil
	}

	collection, err := app.FindCollectionByNameOrId(p.physical[votesCollection])
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", p.physical[votesCollection])
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the votes could not be read")
	}

	ids := make([]plugin.Identifier, 0, len(records))
	for _, record := range records {
		if record.GetString("poll") != pollID || record.GetString("user") != userID {
			continue
		}
		ids = append(ids, plugin.Identifier{Type: TypePollVote, ID: record.Id})
	}

	return ids, nil
}

// voteDiscussionID returns the id of the discussion a poll hangs off.
//
// The poll is attached to a POST, and the post hangs off a discussion,
// and upstream's vote permission is scoped to the DISCUSSION:
//
//	$can = $discussion ? $actor->can('polls.vote', $discussion) : false;
//
// The scope has to be the discussion, because upstream's key is
// `discussion.polls.vote` and a RESTRICTED TAG's grant is a grant on
// that discussion. A poll-scoped check makes the tag's grant
// meaningless — the check never asks about the tag.
//
// A poll that hangs off no post has no discussion, and upstream's global
// branch asks for the permission with no subject at all.
func (p *Plugin) voteDiscussionID(app core.App, poll *pollRecord) (string, error) {
	if poll.PostID == "" {
		return "", nil
	}

	collection, err := app.FindCollectionByNameOrId(p.postsPhysical)
	if err != nil {
		return "", errors.Wrapf(err, "polls: the %q collection is missing", p.postsPhysical)
	}

	record, err := app.FindRecordById(collection, poll.PostID)
	if err != nil {
		return "", errors.Wrap(err, "polls: the poll's post could not be read")
	}

	return record.GetString("discussion"), nil
}

// pollIDs returns the ids of the polls attached to a post.
func (p *Plugin) pollIDs(app core.App, postID string) ([]plugin.Identifier, error) {
	collection, err := app.FindCollectionByNameOrId(p.physical[pollsCollection])
	if err != nil {
		return nil, errors.Wrapf(err, "polls: the %q collection is missing", p.physical[pollsCollection])
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "polls: the polls could not be read")
	}

	ids := make([]plugin.Identifier, 0)
	for _, record := range records {
		if record.GetString("post") != postID {
			continue
		}
		ids = append(ids, plugin.Identifier{Type: TypePoll, ID: record.Id})
	}

	return ids, nil
}

// optionIDs returns the ids of a poll's options.
func (p *Plugin) optionIDs(app core.App, pollID string) ([]plugin.Identifier, error) {
	options, err := findOptions(app, p.physical[optionsCollection], pollID)
	if err != nil {
		return nil, err
	}

	ids := make([]plugin.Identifier, 0, len(options))
	for _, option := range options {
		ids = append(ids, plugin.Identifier{Type: TypePollOption, ID: option.ID})
	}

	return ids, nil
}

// pollPostID returns the id of the post a poll hangs off.
func (p *Plugin) pollPostID(app core.App, pollID string) ([]plugin.Identifier, error) {
	poll, err := findPoll(app, p.physical[pollsCollection], pollID)
	if err != nil {
		return nil, err
	}

	if poll.PostID == "" {
		return nil, nil
	}

	return []plugin.Identifier{{Type: api.TypePost, ID: poll.PostID}}, nil
}

// pollUserID returns the id of the account that created a poll.
func (p *Plugin) pollUserID(app core.App, pollID string) ([]plugin.Identifier, error) {
	poll, err := findPoll(app, p.physical[pollsCollection], pollID)
	if err != nil {
		return nil, err
	}

	if poll.UserID == "" {
		return nil, nil
	}

	return []plugin.Identifier{{Type: api.TypeUser, ID: poll.UserID}}, nil
}
