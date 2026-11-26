package gamification

import (
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// voteHandler serves POST /api/posts/{id}/vote.
//
// The body carries `vote`: "up", "down", or empty to withdraw the vote.
//
// Upstream's `PostResourceFields` writes the vote through a post UPDATE
// (`vote` is a writable field), but the permission that governs it is the
// extension's own and the post update handler does not consult it. So the
// vote is its own route, exactly as the Likes plugin's like is: the check
// and the write belong together.
func (p *Plugin) voteHandler(e *core.RequestEvent) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	postID := e.Request.PathValue("id")
	if postID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A post is required.")
	}

	// The permission is asked against the POST'S DISCUSSION, which is what
	// upstream's `Access\PostPolicy::vote()` does
	// (`$actor->can('votePosts', $post->discussion)`).
	//
	// The discussion is what a permission can be SCOPED to: a restricted
	// tag carries its own grant, and asking on the post would ignore it —
	// the grant for one tag would apply to every post, or to none.
	discussionID, err := discussionOfPost(e.App, p.postsPhysical, postID)
	if err != nil || discussionID == "" {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That post does not exist.")
	}

	allowed, err := api.Can(e, domain.PermissionVotePost, api.TypeDiscussion, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to vote on that post.")
	}

	value, err := voteValue(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The vote could not be read.")
	}

	// A vote for one's own post is refused unless the setting allows it.
	// Upstream's policy checks this before the permission, and the setting
	// is read from the forum's settings table.
	if value != 0 {
		self, err := p.isOwnPost(e, postID, actor.User.ID)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
		}
		if self && !settingBool(e, SettingAllowSelfVotes, true) {
			return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You cannot vote for your own post.")
		}
	}

	if _, _, err := setVote(e.App, p.physical[votesCollection], postID, actor.User.ID, value); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	score, err := postScore(e.App, p.physical[votesCollection], postID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The vote could not be recorded.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data: map[string]any{
			"type": api.TypePost,
			"id":   postID,
			"attributes": map[string]any{
				"votes":        score,
				"hasUpvoted":   value == VoteUp,
				"hasDownvoted": value == VoteDown,
			},
		},
	})
}

// voteValue reads the requested vote.
//
// The body is the JSON:API shape (`{"data":{"attributes":{"vote":"up"}}}`)
// and the flat form, because the forum's own front end posts one and an
// upstream client the other.
func voteValue(e *core.RequestEvent) (int, error) {
	body := map[string]any{}

	if err := api.DecodeBody(e, &body); err != nil {
		return 0, err
	}

	attributes := body
	if data, ok := body["data"].(map[string]any); ok {
		if nested, ok := data["attributes"].(map[string]any); ok {
			attributes = nested
		}
	}

	raw, ok := attributes["vote"].(string)
	if !ok {
		raw = ""
	}

	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "up", "+1", "1":
		return VoteUp, nil
	case "down", "-1":
		return VoteDown, nil
	case "":
		return 0, nil
	default:
		return 0, errBadVote
	}
}

// isOwnPost reports whether the account wrote the post.
func (p *Plugin) isOwnPost(e *core.RequestEvent, postID, userID string) (bool, error) {
	collection, err := e.App.FindCollectionByNameOrId(p.postsPhysical)
	if err != nil {
		return false, err
	}

	record, err := e.App.FindRecordById(collection, postID)
	if err != nil {
		return false, err
	}

	return record.GetString("user") == userID, nil
}

// settingBool reads a boolean forum setting.
//
// An unset setting falls back to the default upstream seeds, so a fresh
// instance behaves like an upstream one.
func settingBool(e *core.RequestEvent, key string, fallback bool) bool {
	value, err := api.SettingValue(e, key, fallback)
	if err != nil {
		return fallback
	}

	return api.SettingTruthy(value, fallback)
}

// errBadVote reports an unreadable vote value.
var errBadVote = errors.New("gamification: the vote value is not understood")
