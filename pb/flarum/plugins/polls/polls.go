// Package polls is the forum's Polls plugin.
//
// It mirrors upstream FoF's `fof/polls` extension: a member attaches a
// poll to a post, the reader picks one or more options, and the counts
// are shown to whoever is allowed to see them.
//
// The extension upstream is a large one — discussion polls, global
// (forum-wide) polls, poll groups, image uploads, scheduled publishing,
// a drafts workflow, a polls directory page and a ranking board. This
// port implements the CORE of it:
//
//   - the poll, its options and its votes (`forum_polls`,
//     `forum_poll_options`, `forum_poll_votes`);
//   - the vote route (`PATCH /api/polls/{id}/votes`), which is the only
//     way to record a vote;
//   - the attributes the frontend reads (`question`, `hasEnded`,
//     `canVote`, `canChangeVote`, `canSeeVoters`, `voteCount`, …);
//   - the poll relationship on the post, and the poll's own
//     relationships (options, votes, myVotes, post, user).
//
// What is NOT ported is recorded in DECISIONS.md: global polls, poll
// groups, image uploads, scheduled publishing, the drafts workflow and
// the polls directory page. Each is a surface of its own, and half of
// one is worse than none.
package polls

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
// namespace and the permission keys line up with `fof/polls`.
const PluginID = "fof/polls"

// Version is the plugin's version.
const Version = "1.0.0"

// The logical collection names the plugin owns.
const (
	// pollsCollection holds the polls.
	pollsCollection = "polls"
	// optionsCollection holds the poll options.
	optionsCollection = "poll_options"
	// votesCollection holds the votes.
	votesCollection = "poll_votes"
)

// The setting keys the plugin reads.
//
// The JSON:API types the plugin owns.
//
// They are upstream's own names, because the frontend's store is keyed
// by type.
const (
	// TypePoll is the poll resource type.
	TypePoll = "polls"
	// TypePollOption is the poll option resource type.
	TypePollOption = "poll_options"
	// TypePollVote is the poll vote resource type.
	TypePollVote = "poll_votes"
)

// Plugin is the Polls plugin.
type Plugin struct {
	// manifest is the plugin's manifest.
	manifest plugin.Manifest
	// physical maps a logical collection name onto the real one.
	physical map[string]string
	// postsPhysical is the posts collection's real name.
	postsPhysical string
}

// New returns the Polls plugin.
func New() *Plugin {
	return &Plugin{
		manifest: plugin.Manifest{
			ID:          PluginID,
			Name:        "Polls",
			Version:     Version,
			CoreRequire: ">=2.0.0",
			Deps:        []string{},
			BackendKind: plugin.BackendStaticGo,
			Icon: plugin.Icon{
				BackgroundColor: "#e74c3c",
				Glyph:           "fas fa-poll",
			},
		},
	}
}

// Manifest describes the plugin.
func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

// RepairVoteIndex replaces the votes collection's unique index with the
// one the multi-vote poll needs.
//
// It is the plugin migration step's body, exported so the forum's tests
// can drive it directly: the repair is what an instance that already
// ran the old creation step relies on, and a test that cannot call it
// cannot pin it.
//
// The step itself is idempotent, so calling it twice is harmless.
func (p *Plugin) RepairVoteIndex(app core.App) error {
	return repairVoteIndex(app, p.physical[votesCollection])
}

// Register declares the plugin's extenders.
func (p *Plugin) Register(_ context.Context, ext *plugin.Extenders) error {
	p.physical = map[string]string{
		pollsCollection:   ext.Registry.Physical(pollsCollection),
		optionsCollection: ext.Registry.Physical(optionsCollection),
		votesCollection:   ext.Registry.Physical(votesCollection),
	}
	p.postsPhysical = ext.Registry.Physical("posts")

	// --- layer 1: the migration ---------------------------------------
	ext.AddMigration(plugin.Migration{
		Name: "create_polls",
		Up: func(_ context.Context, app core.App) error {
			if err := createPollsCollection(app, p.physical[pollsCollection], p.postsPhysical); err != nil {
				return err
			}
			if err := createPollOptionsCollection(app, p.physical[optionsCollection], p.physical[pollsCollection]); err != nil {
				return err
			}
			return createPollVotesCollection(app, p.physical[votesCollection], p.physical[pollsCollection], p.physical[optionsCollection])
		},
	})

	// The vote index repair is a SEPARATE step, and it is idempotent.
	//
	// The `create_polls` step has already been recorded on every
	// existing instance, so it will not run again — and the wrong index
	// (unique on (poll, user)) is what it created. A separate step runs
	// on the next boot and replaces the index with the one the
	// multi-vote poll needs: unique on (poll, option, user).
	ext.AddMigration(plugin.Migration{
		Name: "repair_vote_index",
		Up: func(_ context.Context, app core.App) error {
			return repairVoteIndex(app, p.physical[votesCollection])
		},
	})

	// --- layer 2: the API -----------------------------------------------
	// The poll hangs off the post, so the post carries the relationship
	// the frontend reads.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: api.TypePost,
		Name:     "polls",
		Type:     TypePoll,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return p.pollIDs(req.App, req.RecordID)
		},
	})

	// The post's poll arrives WITH its options and with the reader's own
	// votes. Upstream declares it on the post's endpoint:
	//
	//	->addDefaultInclude([
	//	    'polls', 'polls.options', 'polls.myVotes', 'polls.myVotes.option',
	//	])
	//
	// and the same set on the discussion's `firstPost`. The chains are
	// DOTTED, and the forum's `included` walk follows every hop.
	ext.AddDefaultInclude(plugin.DefaultIncludeContributor{
		Resource: api.TypePost,
		Paths: []string{
			"polls",
			"polls.options",
			"polls.myVotes",
			"polls.myVotes.option",
		},
	})

	// A poll carries its options and the reader's own votes, which is
	// upstream's `defaultInclude(['options', 'myVotes', 'myVotes.option'])`
	// on the poll's show endpoint. The plugin's own route builds that set
	// itself (`writePoll`), and this declaration is what makes a poll
	// reached as a PRIMARY through the forum's walk carry the same
	// resources — one contract, two call sites.
	ext.AddDefaultInclude(plugin.DefaultIncludeContributor{
		Resource: TypePoll,
		Paths: []string{
			"options",
			"myVotes",
			"myVotes.option",
		},
	})

	// The extension's OWN resource types. Without them the forum's
	// `included` walk has no way to render a poll, and the frontend —
	// which resolves a relation out of the STORE, and fills the store
	// from `payload.included` — reads the poll as absent.
	ext.AddResourceSerializer(plugin.ResourceSerializer{
		Type:   TypePoll,
		Render: p.renderPoll,
	})
	ext.AddResourceSerializer(plugin.ResourceSerializer{
		Type:   TypePollOption,
		Render: p.renderPollOption,
	})
	ext.AddResourceSerializer(plugin.ResourceSerializer{
		Type:   TypePollVote,
		Render: p.renderPollVote,
	})

	// The poll's own relationships.
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: TypePoll,
		Name:     "options",
		Type:     TypePollOption,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return p.optionIDs(req.App, req.RecordID)
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: TypePoll,
		Name:     "votes",
		Type:     TypePollVote,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return p.allVoteIdentifiers(req.App, req.RecordID)
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: TypePoll,
		Name:     "myVotes",
		Type:     TypePollVote,
		Many:     true,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			// A GUEST HAS NO VOTES. An empty actor id is "this caller has
			// cast none", never "no filter" — the latter would name every
			// voter in the poll to an unauthenticated reader.
			return p.myVoteIdentifiers(req.App, req.RecordID, req.ActorID)
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: TypePoll,
		Name:     "post",
		Type:     api.TypePost,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return p.pollPostID(req.App, req.RecordID)
		},
	})
	ext.AddRelationship(plugin.RelationshipContributor{
		Resource: TypePoll,
		Name:     "user",
		Type:     api.TypeUser,
		Resolve: func(_ context.Context, req *plugin.RelationshipRequest) ([]plugin.Identifier, error) {
			return p.pollUserID(req.App, req.RecordID)
		},
	})

	// The vote is its own route: the permission that governs it
	// (`polls.vote`) is the plugin's, and the poll update handler does
	// not consult it.
	ext.AddRoute(plugin.RouteDef{
		Method:  "PATCH",
		Path:    "/api/polls/{id}/votes",
		Handler: p.voteHandler,
	})
	// The frontend rewrites a PATCH into a POST with the method in a
	// header, and PocketBase's router matches on the REAL method before
	// any handler runs, so the route needs a POST twin.
	ext.AddRoute(plugin.RouteDef{
		Method:  "POST",
		Path:    "/api/polls/{id}/votes",
		Handler: p.voteHandler,
	})

	// The poll itself, so the frontend can load a poll by id.
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/polls/{id}",
		Handler: p.showPollHandler,
	})
	ext.AddRoute(plugin.RouteDef{
		Method:  "GET",
		Path:    "/api/polls",
		Handler: p.listPollsHandler,
	})

	// --- the permissions -------------------------------------------------
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionVotePoll,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionChangePollVote,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionSeePollVoters,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})
	ext.AddPermission(plugin.PermissionHandler{
		Action: domain.PermissionSeePollVoteCount,
		Check: func(_ context.Context, _ *plugin.PermissionRequest) error {
			return nil
		},
	})

	// --- layer 3: the frontend ------------------------------------------
	ext.AddAsset(plugin.AssetBundle{
		PluginID: PluginID,
		Version:  Version,
		Forum:    "plugins/polls/forum.js",
		Admin:    "plugins/polls/admin.js",
		CSS:      "plugins/polls/style.css",
		// The extension's own catalogue. Without it every `trans()` call
		// in the poll renders the raw key — the UI shows
		// "fof-polls.forum.poll_ended" instead of "This poll has
		// ended."
		Locale: "plugins/polls/forum-en.js",
	})

	return nil
}

// Boot runs the plugin's startup work.
func (*Plugin) Boot(_ context.Context, _ *plugin.Extenders) error { return nil }

// Shutdown releases the plugin's resources.
func (*Plugin) Shutdown(_ context.Context, _ *plugin.Extenders) error { return nil }
