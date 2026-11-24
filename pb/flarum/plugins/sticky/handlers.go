package sticky

import (
	"net/http"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// stickyHandler serves POST /api/discussions/{id}/sticky.
func (p *Plugin) stickyHandler(e *core.RequestEvent) error {
	return p.setStickyHandler(e, true)
}

// unstickyHandler serves DELETE /api/discussions/{id}/sticky.
func (p *Plugin) unstickyHandler(e *core.RequestEvent) error {
	return p.setStickyHandler(e, false)
}

// setStickyHandler pins or unpins a discussion.
func (p *Plugin) setStickyHandler(e *core.RequestEvent, sticky bool) error {
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

	allowed, err := api.Can(e, domain.PermissionStickyDiscussion, api.TypeDiscussion, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The discussion could not be pinned.")
	}
	if !allowed {
		// Pinning is not an author power: upstream's policy has no
		// ownership rule for it.
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to pin that discussion.")
	}

	if err := setSticky(e.App, p.physical, discussionID, sticky); err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That discussion does not exist.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data: map[string]any{
			"type": api.TypeDiscussion,
			"id":   discussionID,
			"attributes": map[string]any{
				"isSticky":  sticky,
				"canSticky": true,
			},
		},
	})
}

// isSticky reports whether a discussion is pinned.
func isSticky(app core.App, physical, discussionID string) (bool, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return false, errors.Wrapf(err, "sticky: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, discussionID)
	if err != nil {
		return false, nil //nolint:nilerr // a gone discussion is simply not pinned
	}

	return record.GetBool(stickyField), nil
}

// setSticky writes the flag. A repeat is a no-op.
func setSticky(app core.App, physical, discussionID string, sticky bool) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "sticky: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, discussionID)
	if err != nil {
		return errors.Wrap(err, "sticky: the discussion does not exist")
	}

	if record.GetBool(stickyField) == sticky {
		return nil
	}

	record.Set(stickyField, sticky)

	if err := app.Save(record); err != nil {
		return errors.Wrap(err, "sticky: the discussion could not be saved")
	}

	return nil
}
