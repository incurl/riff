// Package gamification is the forum's Gamification plugin.
//
// It mirrors upstream FoF's `fof/gamification` extension: members vote on
// posts, the votes add up to points, points earn ranks, and a leaderboard
// ranks the members.
//
// The extension upstream is a large one — votes, ranks, auto-assigned
// groups, a trending score, a leaderboard with several metrics and periods,
// a rankings page, a votes page, an admin settings surface, and image
// uploads for the podium. This port implements the CORE of it, which is the
// part the rest of the forum can build on:
//
//   - the votes themselves (`post_votes`), with the same unique index and
//     the same +/-1 values;
//   - the points they add up to, and the ranks they earn;
//   - the post and user attributes the frontend reads (`votes`,
//     `hasUpvoted`, `hasDownvoted`, `canVote`, `canSeeVotes`, …);
//   - the leaderboard endpoint, with its metrics and periods.
//
// What is NOT ported is recorded in DECISIONS.md: the trending score (a
// Reddit "hot" formula over a column this port does not need), the
// auto-assigned groups, the rankings page's charts, the votes page and the
// podium images. Each is a surface of its own, and half of one is worse
// than none.
package gamification

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
//
// It is upstream's extension id, so the documentation, the catalogue
// namespace and the permission keys line up with `fof/gamification`.
const PluginID = "fof/gamification"

// Version is the plugin's version.
const Version = "1.0.0"

// The logical collection names the plugin owns.
const (
	// votesCollection holds the votes.
	votesCollection = "votes"
	// ranksCollection holds the ranks.
	ranksCollection = "ranks"
)

// The setting keys the plugin reads.
//
// They are upstream's own keys, so an operator's saved settings carry
// over and the admin page (upstream's, vendored) reads what it expects.
//
// They are the settings this port READS. Upstream's page carries
// about twenty more, but a setting nobody reads is a control that
// changes nothing — and one the admin portal would show as if it did.
const (
	// SettingAllowSelfVotes allows voting for one's own post.
	SettingAllowSelfVotes = "fof-gamification.allowSelfVotes"
	// SettingDefaultMetric is the leaderboard's default metric.
	SettingDefaultMetric = "fof-gamification.defaultMetric"
	// SettingDefaultPeriod is the leaderboard's default period.
	SettingDefaultPeriod = "fof-gamification.defaultPeriod"
	// SettingRankAmt is how many rank badges a member's name shows.
	//
	// It is upstream's `fof-gamification.rankAmt` (default 2), and its
	// `addUserInfo` reads it off the forum document before it renders the
	// stack of badges: a member who has passed five ranks wears the two
	// highest, so a name is not buried under a wall of labels.
	SettingRankAmt = "fof-gamification.rankAmt"
)

// DefaultRankAmt is upstream's default for [SettingRankAmt].
const DefaultRankAmt = 2

// Plugin is the Gamification plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// physical maps a logical collection name onto the real one.
	physical map[string]string
	// postsPhysical is the posts collection's real name.
	postsPhysical string
	// discussionsPhysical is the discussions collection's real name.
	discussionsPhysical string
}

// New returns the Gamification plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Gamification",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#e74c3c",
				Glyph:           "fas fa-thumbs-up",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	// The collections carry the forum's own prefix, like every other
	// plugin's (see `ext.Registry.Physical`). NOT `ExtensionPhysical`:
	// that form embeds the plugin slug, and the slug of `fof/gamification`
	// contains a HYPHEN, which PocketBase's collection-name rule
	// (`^\w+$`) rejects — the migration would fail with "name: must be in a
	// valid format" and abort the bootstrap.
	p.physical = map[string]string{
		votesCollection: ext.Registry.Physical(votesCollection),
		ranksCollection: ext.Registry.Physical(ranksCollection),
	}
	p.postsPhysical = ext.Registry.Physical("posts")
	p.discussionsPhysical = ext.Registry.Physical("discussions")

	// --- layer 1: the migrations ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_votes",
		Up: func(_ context.Context, app core.App) error {
			return createVotesCollection(app, p.physical[votesCollection], p.postsPhysical)
		},
	})
	ext.AddMigration(plugin.Migration{
		Name: "create_ranks",
		Up: func(_ context.Context, app core.App) error {
			return createRanksCollection(app, p.physical[ranksCollection])
		},
	})

	// --- layer 2: the API -----------------------------------------------
	// The post's vote state and the score.
	//
	// The tally and the caller's own vote are VISIBLE only where the
	// caller may see the tally (upstream's `Field::visible()`): an
	// invisible field is ABSENT rather than zero, so a reader without the
	// permission is not told a post scored nothing.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "votes",
		Visible:  p.canSeeVotes,
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return postScore(req.App, p.physical[votesCollection], req.RecordID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "hasUpvoted",
		Visible:  p.canSeeVotes,
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return hasVote(req.App, p.physical[votesCollection], req.RecordID, req.ActorID, VoteUp)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "hasDownvoted",
		Visible:  p.canSeeVotes,
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return hasVote(req.App, p.physical[votesCollection], req.RecordID, req.ActorID, VoteDown)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "canSeeVotes",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return p.canSeeVotes(req), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "seeVoters",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return p.canSeeVoters(req), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypePost,
		Name:     "canVote",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return p.canVote(req), nil
		},
	})

	// The DISCUSSION carries the same state for its first post.
	//
	// Upstream serves `votes`, `seeVotes`, `canVote`, `hasUpvoted` and
	// `hasDownvoted` on the discussion resource, which is what the
	// discussion LIST renders: a reader sees the original post's score
	// without opening anything.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "votes",
		Visible:  func(req *plugin.AttributeRequest) bool { return canOnSelf(req, domain.PermissionSeeVoteCount) },
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			score, _, _, err := p.discussionVoteAttributes(req.App, req.RecordID, req.ActorID)
			return score, err
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "seeVotes",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return canOnSelf(req, domain.PermissionSeeVoteCount), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "canVote",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			postID, err := firstPostOfDiscussion(req.App, p.discussionsPhysical, req.RecordID)
			if err != nil || postID == "" {
				return false, nil //nolint:nilerr // a discussion with no first post takes no votes
			}

			// The SELF-VOTE rule is part of the question here too: the
			// discussion's tally is its first post's, so a reader who wrote
			// that post is asking about their own post.
			return p.mayVoteOnPost(req.App, postID, req.ActorID) &&
				req.Can(domain.PermissionVotePost, api.TypeDiscussion, req.RecordID), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "hasUpvoted",
		Visible: func(req *plugin.AttributeRequest) bool {
			return canOnSelf(req, domain.PermissionSeeVoteCount) && req.ActorID != ""
		},
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			_, up, _, err := p.discussionVoteAttributes(req.App, req.RecordID, req.ActorID)
			return up, err
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeDiscussion,
		Name:     "hasDownvoted",
		Visible: func(req *plugin.AttributeRequest) bool {
			return canOnSelf(req, domain.PermissionSeeVoteCount) && req.ActorID != ""
		},
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			_, _, down, err := p.discussionVoteAttributes(req.App, req.RecordID, req.ActorID)
			return down, err
		},
	})

	// The user's score and the ranks they have earned.
	//
	// `points` is upstream's name for it (`UserResourceFields`:
	// `Schema\Number::make('points')->property('votes')`), and `votes` is
	// kept because this port's older frontend and its clients read that
	// one. Both are the votes the account's posts RECEIVED.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "votes",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return userScore(req.App, p.physical[votesCollection], p.postsPhysical, req.RecordID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "points",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return userScore(req.App, p.physical[votesCollection], p.postsPhysical, req.RecordID)
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeUser,
		Name:     "rank",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return userRank(req.App, p.physical[ranksCollection], p.physical[votesCollection], p.postsPhysical, req.RecordID)
		},
	})

	// The voters, as a relationship.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "upvotes",
		Type:     api.TypeUser,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return voterIDs(req.App, p.physical[votesCollection], req.RecordID, 1)
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "downvotes",
		Type:     api.TypeUser,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return voterIDs(req.App, p.physical[votesCollection], req.RecordID, -1)
		},
	})

	// The ranks a user holds, and the ranks themselves on the forum
	// document.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypeUser,
		Name:     "ranks",
		Type:     TypeRank,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return rankIDs(req.App, p.physical[ranksCollection], p.physical[votesCollection], p.postsPhysical, req.RecordID)
		},
	})

	// The vote toggle is its own route: the permission that governs it
	// (`votePosts`) is the plugin's.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/posts/{id}/vote",
		Handler: p.voteHandler,
	})

	// The leaderboard.
	//
	// It is served at the resource's own URL — `/api/leaderboard-entries`,
	// which is what `app.store.find('leaderboard-entries', …)` requests,
	// since upstream's `LeaderboardResource` has no path of its own (the
	// framework prefixes `/api/{type}`) — and at the older `/api/leaderboard`
	// alias, so an existing client keeps working.
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/leaderboard",
		Handler: p.leaderboardHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/leaderboard-entries",
		Handler: p.leaderboardHandler,
	})

	// The ranks, at upstream's four endpoints.
	//
	// The index is PUBLIC (a rank is drawn next to a member's name on
	// every post), and the three writes are administrator-only. The
	// update and the delete share the `{id}` path and are told apart by
	// the EFFECTIVE METHOD, because the admin application sends a DELETE
	// as a POST carrying `X-HTTP-Method-Override`.
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/ranks",
		Handler: p.listRanksHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/ranks",
		Handler: p.createRankHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "PATCH",
		Path:    "/api/ranks/{id}",
		Handler: p.updateRanksHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/ranks/{id}",
		Handler: p.updateRanksHandler,
	})

	// --- the plugin's own resource type ---------------------------------
	// `ranks` is the type the `user.ranks` relationship points at, and the
	// type the frontend's `Rank` model is registered under. A relationship
	// identifier alone is not enough — the frontend resolves a relation
	// out of the STORE, and the store is filled from `included` — so the
	// resource has to be renderable or the badge draws nothing.
	ext.AddResourceSerializer(plugin.ResourceSerializer{
		Type:   TypeRank,
		Render: p.renderRankAt,
	})

	// --- the forum document ----------------------------------------------
	// The settings the RANKINGS PAGE reads before it loads anything.
	//
	// Upstream publishes them on the forum resource
	// (`Api\ForumResourceFields`) and its `RankingsPage` reads them from
	// `app.forum`:
	//
	//	canViewRankingPage        the gate the page branches on
	//	leaderboardMetrics        the metric selector's options
	//	leaderboardPeriods        the period selector's options
	//	defaultLeaderboardMetric  the metric it opens on
	//	defaultLeaderboardPeriod  the period it opens on
	//	rankAmt                   how many badges a member's name shows
	//
	// An empty list is not harmless: the page would render a selector with
	// no options and load whatever its hardcoded fallback is, which is how
	// an operator's "default metric" becomes a control that changes
	// nothing.
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "canViewRankingPage",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return req.Can(domain.PermissionViewRankingPage, api.TypeForum, req.RecordID), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "fof-gamification.leaderboardMetrics",
		Value: func(_ context.Context, _ *plugin.AttributeRequest) (any, error) {
			return leaderboardMetricOptions(), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "fof-gamification.leaderboardPeriods",
		Value: func(_ context.Context, _ *plugin.AttributeRequest) (any, error) {
			return leaderboardPeriodOptions(), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "fof-gamification.defaultLeaderboardMetric",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			// The REGISTRY's first metric is the fallback, which is what
			// the page's own `metrics()[0]?.key` would have used.
			return settingStringAt(req.App, SettingDefaultMetric, MetricPosts), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "fof-gamification.defaultLeaderboardPeriod",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return settingStringAt(req.App, SettingDefaultPeriod, PeriodYear), nil
		},
	})
	ext.AddAttribute(plugin.AttributeContributor{
		Resource: api.TypeForum,
		Name:     "fof-gamification.rankAmt",
		Value: func(_ context.Context, req *plugin.AttributeRequest) (any, error) {
			return settingIntAt(req.App, SettingRankAmt, DefaultRankAmt), nil
		},
	})

	// --- the permissions -------------------------------------------------
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionVotePost,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionSeeVoteCount,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionSeeVoters,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionViewRankingPage,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	// --- layer 3: the frontend ------------------------------------------
	// --- the settings ---------------------------------------------------
	// The settings the plugin READS, with upstream's keys.
	//
	// They are declared HERE because the server READS them: the
	// defaults are what the forum falls back to when a setting is
	// unset, and they must be the same values the admin portal shows.
	//
	// The four that upstream's page carries but this port does not
	// READ are NOT declared: a setting nobody reads is a control that
	// changes nothing, and the admin portal showing one would be a
	// lie about what the forum does.
	ext.AddSettings(plugin.SettingsSchema{
		Fields: []plugin.SettingField{
			{
				Key:     SettingAllowSelfVotes,
				Label:   "Allow voting for your own posts",
				Type:    "bool",
				Default: "1",
			},
			{
				Key:     SettingDefaultMetric,
				Label:   "Default leaderboard metric",
				Type:    "select",
				Default: MetricPosts,
				Options: []string{MetricPosts, MetricDiscussions, MetricUpvotes, MetricVotes},
			},
			{
				Key:     SettingDefaultPeriod,
				Label:   "Default leaderboard period",
				Type:    "select",
				Default: PeriodYear,
				Options: []string{PeriodDay, PeriodWeek, PeriodMonth, PeriodYear, PeriodAllTime},
			},
			{
				Key:     SettingRankAmt,
				Label:   "Rank badges shown next to a name",
				Type:    "number",
				Default: DefaultRankAmt,
			},
		},
	})

	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/gamification/forum.js",
		Admin:    "plugins/gamification/admin.js",
		CSS:      "plugins/gamification/style.css",
	})

	return nil
}

// Boot runs the plugin's startup work.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error { return nil }

// Shutdown releases the plugin's resources.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error { return nil }

// TypeRank is the JSON:API type of a rank resource.
//
// It is upstream's `ranks`.
const TypeRank = "ranks"

// TypeLeaderboardEntry is the JSON:API type of a leaderboard entry.
//
// It is upstream's `leaderboard-entries`.
const TypeLeaderboardEntry = "leaderboard-entries"
