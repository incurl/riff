package gamification

import (
	"strconv"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// The attributes the extension publishes.
//
// They are upstream's, name for name, because the frontend reads them
// through model methods it declares itself (`Extend.Model(Post)
// .attribute('hasUpvoted')`): an attribute with a different name is an
// attribute nothing reads.
//
// # Why the permission is asked on the DISCUSSION
//
// Upstream's `Access\PostPolicy` asks `votePosts`, `canSeeVotes` and
// `canSeeVoters` against the POST'S DISCUSSION, not against the post:
//
//	if (!$this->tags->allowsPost($post)) { return $this->deny(); }
//	return $actor->can('votePosts', $post->discussion);
//
// The discussion is what a permission can be SCOPED to — a restricted tag
// carries its own grant (`tag{id}.discussion.canSeeVotes`), which the tags
// extension resolves through `CanScoped`. Asking on the post loses that
// scope: the grant for a restricted tag would apply to no post at all.

// canSeeVotes reports whether the caller may see a post's vote tally.
func (p *Plugin) canSeeVotes(req *plugin.AttributeRequest) bool {
	return p.canOnDiscussion(req, domain.PermissionSeeVoteCount)
}

// canSeeVoters reports whether the caller may see WHO voted.
func (p *Plugin) canSeeVoters(req *plugin.AttributeRequest) bool {
	return p.canOnDiscussion(req, domain.PermissionSeeVoters)
}

// canVote reports whether the caller may vote on a post.
//
// The permission is only half the question: upstream's
// `Access\PostPolicy::vote()` ALSO denies a vote on one's own post unless
// `fof-gamification.allowSelfVotes` is on, and the attribute has to ask the
// same two questions the route does. A `canVote` that ignores the setting
// draws a live button whose click answers 403 — the flag and the endpoint
// disagreeing, which is the failure this port keeps finding.
func (p *Plugin) canVote(req *plugin.AttributeRequest) bool {
	if !p.canOnDiscussion(req, domain.PermissionVotePost) {
		return false
	}

	return p.mayVoteOnPost(req.App, req.RecordID, req.ActorID)
}

// mayVoteOnPost applies the self-vote rule to a post.
//
// A guest (no actor id) is not the author of anything, so the rule passes:
// the control is offered and the click opens the login modal, which is
// upstream's behaviour for an anonymous reader.
func (p *Plugin) mayVoteOnPost(app core.App, postID, actorID string) bool {
	if actorID == "" {
		return true
	}

	author, err := postAuthor(app, p.postsPhysical, postID)
	if err != nil {
		return false
	}

	if author != actorID {
		return true
	}

	return settingBoolAt(app, SettingAllowSelfVotes, true)
}

// canOnDiscussion asks a permission against the discussion a POST belongs
// to.
//
// A post whose discussion cannot be resolved answers false: an attribute
// that cannot be computed must not claim a permission the write path would
// refuse.
func (p *Plugin) canOnDiscussion(req *plugin.AttributeRequest, permission string) bool {
	if req.Can == nil {
		return false
	}

	discussionID, err := discussionOfPost(req.App, p.postsPhysical, req.RecordID)
	if err != nil || discussionID == "" {
		return false
	}

	return req.Can(permission, api.TypeDiscussion, discussionID)
}

// discussionVoteAttributes computes the vote state of a DISCUSSION.
//
// Upstream serves five fields on the discussion resource, all of them the
// state of the FIRST POST — the discussion list shows the original post's
// score, so a reader sees a number before opening anything:
//
//	votes, seeVotes, canVote, hasUpvoted, hasDownvoted
//
// The first post is read from the discussion's own `first_post` column.
func (p *Plugin) discussionVoteAttributes(app core.App, discussionID, actorID string) (score int, up, down bool, err error) {
	postID, err := firstPostOfDiscussion(app, p.discussionsPhysical, discussionID)
	if err != nil || postID == "" {
		return 0, false, false, nil //nolint:nilerr // a discussion with no first post has no tally
	}

	score, err = postScore(app, p.physical[votesCollection], postID)
	if err != nil {
		return 0, false, false, err
	}

	up, err = hasVote(app, p.physical[votesCollection], postID, actorID, VoteUp)
	if err != nil {
		return 0, false, false, err
	}

	down, err = hasVote(app, p.physical[votesCollection], postID, actorID, VoteDown)
	if err != nil {
		return 0, false, false, err
	}

	return score, up, down, nil
}

// canOnSelf asks a permission against a DISCUSSION named by an id that is
// already a discussion.
func canOnSelf(req *plugin.AttributeRequest, permission string) bool {
	if req.Can == nil {
		return false
	}
	return req.Can(permission, api.TypeDiscussion, req.RecordID)
}

// leaderboardMetricOptions is the metric list the rankings page renders.
//
// Upstream publishes it on the FORUM document as
// `fof-gamification.leaderboardMetrics`, and its `RankingsPage` reads it
// before it loads anything:
//
//	this.metric = m.route.param('metric')
//	  || app.forum.attribute('fof-gamification.defaultLeaderboardMetric')
//	  || this.metrics()[0]?.key || 'posts';
//
// so an empty list is a page whose selector shows nothing at all — the
// default is a fallback for a list that EXISTS.
//
// `supportsPeriods` is upstream's, and it is what greys the period
// selector out for a metric that counts everything: the four metrics this
// port has all drill into a window, so all four are true.
func leaderboardMetricOptions() []map[string]any {
	labels := metricLabels()

	return []map[string]any{
		{"key": MetricPosts, "label": labels[MetricPosts], "supportsPeriods": true},
		{"key": MetricDiscussions, "label": labels[MetricDiscussions], "supportsPeriods": true},
		{"key": MetricUpvotes, "label": labels[MetricUpvotes], "supportsPeriods": true},
		{"key": MetricVotes, "label": labels[MetricVotes], "supportsPeriods": true},
	}
}

// metricLabels are the metric names the selector shows.
//
// They are untranslated English: this port ships no locale bundle for the
// extension, so a translation key would render as the key itself.
func metricLabels() map[string]string {
	return map[string]string{
		MetricPosts:       "Posts",
		MetricDiscussions: "Discussions",
		MetricUpvotes:     "Upvotes received",
		MetricVotes:       "Score",
	}
}

// leaderboardPeriodOptions is the period list the rankings page renders.
func leaderboardPeriodOptions() []string {
	return []string{PeriodDay, PeriodWeek, PeriodMonth, PeriodYear, PeriodAllTime}
}

// settingBoolAt reads a boolean setting without a request event.
//
// The attribute path has no `*core.RequestEvent`, and the settings column
// is a JSON field: `api.SettingTruthy` is what decodes the raw JSON a stored
// value arrives as.
func settingBoolAt(app core.App, key string, fallback bool) bool {
	value, err := api.SettingValueAt(app, key, fallback)
	if err != nil {
		return fallback
	}

	return api.SettingTruthy(value, fallback)
}

// settingStringAt reads a string setting without a request event.
//
// The forum document's attributes have no request to read a setting
// through, so they use the core's app-level reader and the same
// `api.SettingString` decoding a stored value needs.
func settingStringAt(app core.App, key, fallback string) string {
	value, err := api.SettingValueAt(app, key, fallback)
	if err != nil {
		return fallback
	}

	text, ok := api.SettingString(value)
	if !ok || text == "" {
		return fallback
	}

	return text
}

// settingIntAt reads an integer setting without a request event.
func settingIntAt(app core.App, key string, fallback int) int {
	value, err := api.SettingValueAt(app, key, fallback)
	if err != nil {
		return fallback
	}

	text, ok := api.SettingString(value)
	if !ok {
		return fallback
	}

	parsed, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}

	return parsed
}
