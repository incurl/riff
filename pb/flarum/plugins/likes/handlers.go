package likes

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// likeHandler serves POST /api/posts/{id}/like.
//
// The permission check is the plugin's own: upstream Flarum's
// LikePostPolicy refuses a user who likes their own post unless the
// `like_own_post` setting allows it. The forum's v1 scope allows it,
// and the setting is read from the forum's settings table.
func likeHandler(e *core.RequestEvent) error {
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

	if err := addLike(e.App, postID, actor.User.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be recorded.")
	}

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
func unlikeHandler(e *core.RequestEvent) error {
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

	if err := removeLike(e.App, postID, actor.User.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be removed.")
	}

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
