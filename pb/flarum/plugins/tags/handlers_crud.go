package tags

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// createTagHandler serves POST /api/tags.
//
// It is upstream's `Endpoint\Create`: the body carries the tag's
// attributes, and the `createTag` permission governs it.
func (p *Plugin) createTagHandler(e *core.RequestEvent) error {
	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	allowed, err := api.Can(e, domain.PermissionCreateTag, api.TypeTag, "")
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be created.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to create a tag.")
	}

	attributes, _, err := tagAttributes(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The tag could not be read.")
	}

	if strings.TrimSpace(attributes.Name) == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A name is required.")
	}
	if strings.TrimSpace(attributes.Slug) == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A slug is required.")
	}

	// A slug must not carry a slash or a space: it is a URL segment.
	if strings.ContainsAny(attributes.Slug, "/\\ ") {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The slug may not contain a slash or a space.")
	}

	// The parent must exist, when there is one.
	if attributes.ParentID != "" {
		if _, err := findTag(e.App, physicalName(e), attributes.ParentID); err != nil {
			return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The parent tag does not exist.")
		}
	}

	// A primary tag is placed after the last primary tag of its
	// parent, which is upstream's rule.
	if attributes.IsPrimary {
		position, err := nextPrimaryPosition(e.App, physicalName(e), attributes.ParentID)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be created.")
		}
		attributes.Position = position
	}

	tag, err := createTag(e.App, physicalName(e), attributes)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be created.")
	}

	return api.WriteDocument(e, http.StatusCreated, api.Document{Data: p.tagResourceFor(e, tag)})
}

// updateTagHandler serves PATCH /api/tags/{id}.
//
// It is registered for PATCH and for POST, because the frontend
// rewrites a PATCH into a POST with the method in a header and
// PocketBase's router matches on the REAL method before any handler
// runs.
func (p *Plugin) updateTagHandler(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		if err := api.RequireMethod(e, http.MethodPatch); err != nil {
			return api.WriteMethodError(e, err)
		}
	}

	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	tagID := e.Request.PathValue("id")
	if tagID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A tag is required.")
	}

	// The tag has to EXIST before the permission is asked: the
	// permission of a restricted tag is scoped to the tag, so a missing
	// tag would otherwise be answered as a denial rather than a 404.
	existing, err := findTag(e.App, physicalName(e), tagID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That tag does not exist.")
	}

	allowed, err := canInTag(e, domain.PermissionEditTag, existing)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be saved.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to edit that tag.")
	}

	attributes, present, err := tagAttributes(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The tag could not be read.")
	}

	// An update only writes the fields the body carries: a partial
	// update must not clear the fields it says nothing about.
	merged := mergeTag(existing, attributes, present)

	if merged.Slug != existing.Slug {
		if strings.ContainsAny(merged.Slug, "/\\ ") {
			return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The slug may not contain a slash or a space.")
		}
	}

	if merged.ParentID != "" && merged.ParentID != existing.ParentID {
		if _, err := findTag(e.App, physicalName(e), merged.ParentID); err != nil {
			return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The parent tag does not exist.")
		}
	}

	tag, unrestricted, err := updateTag(e.App, physicalName(e), existing.ID, merged)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be saved.")
	}

	// A tag that stops being restricted gives up its scoped permission
	// rows: they grant a scope that no longer exists.
	if unrestricted {
		if err := deleteTagScopedPermissions(e.App, existing.ID); err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be saved.")
		}
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: p.tagResourceFor(e, tag)})
}

// deleteTagHandler serves DELETE /api/tags/{id}.
func (*Plugin) deleteTagHandler(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		if err := api.RequireMethod(e, http.MethodDelete); err != nil {
			return api.WriteMethodError(e, err)
		}
	}

	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	tagID := e.Request.PathValue("id")
	if tagID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A tag is required.")
	}

	existing, err := findTag(e.App, physicalName(e), tagID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That tag does not exist.")
	}

	allowed, err := canInTag(e, domain.PermissionDeleteTag, existing)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be deleted.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to delete that tag.")
	}

	if err := deleteTag(e.App, physicalName(e), existing.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be deleted.")
	}

	// The tag's scoped permission rows go with it.
	if err := deleteTagScopedPermissions(e.App, existing.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be deleted.")
	}

	return api.WriteDocument(e, http.StatusNoContent, api.Document{})
}

// orderTagsHandler serves POST /api/tags/order.
//
// It is upstream's `OrderTagsController`: the body carries the ids of
// the tags in their new order, and the positions are written in one
// go.
func (*Plugin) orderTagsHandler(e *core.RequestEvent) error {
	if _, err := api.RequireAuth(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	allowed, err := api.Can(e, domain.PermissionEditTag, api.TypeTag, "")
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be reordered.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to reorder the tags.")
	}

	order, err := tagOrder(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The order could not be read.")
	}

	if err := setTagPositions(e.App, physicalName(e), order); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be reordered.")
	}

	return api.WriteDocument(e, http.StatusNoContent, api.Document{})
}

// tagWriteHandler dispatches a POST on a tag to the verb the override
// header names.
//
// The frontend rewrites every method other than GET and POST into a
// POST with the intended method in `X-HTTP-Method-Override`, and
// PocketBase's router matches on the REAL method before any handler
// runs — so a route registered only for PATCH is UNREACHABLE from the
// frontend, and a route registered only for DELETE is too.
//
// The two verbs share ONE POST path, because two POST routes on the
// same path CONFLICT in Go's `http.ServeMux` and the panic takes the
// process down. The override header is what tells them apart.
func (p *Plugin) tagWriteHandler(e *core.RequestEvent) error {
	switch api.EffectiveMethod(e) {
	case http.MethodPatch:
		return p.updateTagHandler(e)
	case http.MethodDelete:
		return p.deleteTagHandler(e)
	default:
		// A POST that names no override is neither a patch nor a
		// delete, so the route refuses it with a 405.
		return api.WriteMethodError(e, api.RequireMethod(e, http.MethodPatch))
	}
}
