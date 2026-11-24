package lock

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// lockHandler serves POST /api/discussions/{id}/lock.
func (p *Plugin) lockHandler(e *core.RequestEvent) error {
	return p.setLockHandler(e, true)
}

// unlockHandler serves DELETE /api/discussions/{id}/lock.
func (p *Plugin) unlockHandler(e *core.RequestEvent) error {
	return p.setLockHandler(e, false)
}

// setLockHandler locks or unlocks a discussion.
//
// The order of the checks is the core's: authentication, then CSRF, then
// the permission, then the write. An anonymous caller therefore learns
// nothing about whether the discussion exists.
func (p *Plugin) setLockHandler(e *core.RequestEvent, locked bool) error {
	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	discussionID := e.Request.PathValue("id")
	if discussionID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A discussion is required.")
	}

	allowed, err := api.Can(e, domain.PermissionLockDiscussion, api.TypeDiscussion, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The discussion could not be locked.")
	}
	if !allowed {
		// Locking is not an author power: upstream's `Lock\Access\
		// DiscussionPolicy` has no ownership rule for it, so a
		// discussion's author without the `lock` permission is refused
		// exactly like anyone else.
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to lock that discussion.")
	}

	if err := setLocked(e.App, p.physical, discussionID, locked); err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That discussion does not exist.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data: map[string]any{
			"type": api.TypeDiscussion,
			"id":   discussionID,
			"attributes": map[string]any{
				"isLocked": locked,
				"canLock":  true,
			},
		},
	})
}
