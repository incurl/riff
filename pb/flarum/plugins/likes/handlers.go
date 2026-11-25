package likes

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// toggleHandler dispatches the like route on the EFFECTIVE method.
//
// The route is mounted for POST and for DELETE, and this is the one place
// that decides what the request means:
//
//	POST                                 like
//	POST + X-HTTP-Method-Override:DELETE unlike
//	DELETE                               unlike
//
// The middle row is the SPA's: it never sends a DELETE, because the frame's
// `Application.request` rewrites every method other than GET and POST. Without
// this dispatch the rewritten request lands on the POST route and LIKES the
// post — so the button can like and can never unlike, silently, because a
// repeat like is idempotent and answers 200.
func (p *Plugin) toggleHandler(e *core.RequestEvent) error {
	switch api.EffectiveMethod(e) {
	case http.MethodPost:
		return p.likeHandler(e)
	case http.MethodDelete:
		return p.unlikeHandler(e)
	default:
		return api.WriteMethodError(e, api.RequireMethod(e, http.MethodPost))
	}
}

// likeHandler serves POST /api/posts/{id}/like.
//
// The permission check is the plugin's own: upstream Flarum's
// LikePostPolicy refuses a user who likes their own post unless the
// `like_own_post` setting allows it. The forum's v1 scope allows it,
// and the setting is read from the forum's settings table.
func (p *Plugin) likeHandler(e *core.RequestEvent) error {
	actor, err := api.CurrentActor(e)
	if err != nil || !actor.IsAuthenticated() {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	postID := e.Request.PathValue("id")
	if postID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A post is required.")
	}

	allowed, err := api.Can(e, domain.PermissionLikePost, api.TypePost, postID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be recorded.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to like that post.")
	}

	// Upstream's `LikePostPolicy::like()`: the author of a post may
	// not like it unless the `like_own_post` setting permits it.
	if err := refuseOwnLike(e, postID, actor.User.ID); err != nil {
		return err
	}

	if err := addLike(e.App, postID, actor.User.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be recorded.")
	}

	// Upstream raises `PostWasLiked` and notifies the post's author.
	// The event is dispatched FIRST, so a listener sees the like
	// before the notification exists.
	p.dispatchLiked(e, postID, actor.User.ID)

	count, err := likeCount(e.App, postID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be recorded.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data: map[string]any{
			"type":       api.TypePost,
			"id":         postID,
			"attributes": map[string]any{"isLiked": true, "likesCount": count},
		},
	})
}

// unlikeHandler serves DELETE /api/posts/{id}/like.
func (p *Plugin) unlikeHandler(e *core.RequestEvent) error {
	actor, err := api.CurrentActor(e)
	if err != nil || !actor.IsAuthenticated() {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	postID := e.Request.PathValue("id")
	if postID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A post is required.")
	}

	// Removing a like is never gated by the self-like rule: the like
	// exists, and taking it back is not a new like.
	if err := removeLike(e.App, postID, actor.User.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be removed.")
	}

	// Upstream raises `PostWasUnliked`.
	p.dispatchUnliked(e, postID, actor.User.ID)

	count, err := likeCount(e.App, postID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be removed.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data: map[string]any{
			"type":       api.TypePost,
			"id":         postID,
			"attributes": map[string]any{"isLiked": false, "likesCount": count},
		},
	})
}
