package tags

import (
	"net/http"
	"strings"

	"github.com/pkg/errors"

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

	attributes, present, err := tagAttributes(e)
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

	// The parent must exist, when there is one, and it decides what kind of
	// tag this is: a tag with a parent is a CHILD of it, and a child is not
	// primary — see [Plugin.resolveTagParent].
	if err := p.resolveTagParent(e, attributes, "", present["parent"], present["isPrimary"]); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}

	// A primary tag is placed after the last primary tag, which is
	// upstream's rule.
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

	// The tree is resolved AFTER the merge: a partial body may name only
	// the parent, and the self-check needs the tag's own id. A PATCH that
	// only renames a child carries `isPrimary: false` through the merge
	// without having NAMED it, which is why the handler passes what the
	// body said rather than what the merged tag holds.
	wasPrimary := existing.IsPrimary
	if err := p.resolveTagParent(e, merged, existing.ID, present["parent"], present["isPrimary"]); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}

	// A tag that BECOMES primary takes a place after the last primary
	// tag: its old position is a child's (0 for a tag that was never
	// dragged), so keeping it would put the promoted tag first and
	// collide with the tag that is already there.
	if merged.IsPrimary && !wasPrimary && !present["position"] {
		position, err := nextPrimaryPosition(e.App, physicalName(e), "")
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be saved.")
		}
		merged.Position = position
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

// resolveTagParent validates a tag's place in the tree and applies the
// invariant.
//
// The tree is TWO LEVELS — a root tag (a "primary" one when it is placed) and
// its children — and the three columns that describe it (`parent`, `position`,
// `is_primary`) must agree. Upstream's model is the same: `Tag::isChild()` is
// the parent relation, and `OrderTagsController` writes all three together.
//
// The rules, and why each is refused rather than papered over:
//
//   - the parent must exist (a dangling relation is a tag that renders
//     nowhere);
//   - the parent must be a ROOT tag: a child of a child is a third level the
//     admin tree does not render and the count rules were not written for;
//   - a tag cannot be its own parent (a cycle renders nowhere and never
//     stops being one);
//   - a body that asks for `isPrimary: true` AND names a parent asks for two
//     contradictory things, and guessing which it meant is how a tag
//     disappears from the page.
//
// What is NORMALISED instead of refused is the consequence of a single
// unambiguous statement: naming a parent makes the tag a child (so it is not
// primary), and asking for primary makes it a root (so it loses its parent).
//
// `tagID` is the tag being written, empty on create. `parentNamed` and
// `primaryNamed` say which of the two the BODY named, because a merged tag
// carries values the body never mentioned — and a PATCH that only renames a
// child must not be read as "make this a root".
func (*Plugin) resolveTagParent(e *core.RequestEvent, tag *domain.Tag, tagID string, parentNamed, primaryNamed bool) error {
	if parentNamed && tag.ParentID != "" && primaryNamed && tag.IsPrimary {
		return errPrimaryWithParent
	}

	if tag.ParentID != "" {
		if tagID != "" && tag.ParentID == tagID {
			return errSelfParent
		}

		parent, err := findTag(e.App, physicalName(e), tag.ParentID)
		if err != nil {
			return errParentMissing
		}
		if parent.ParentID != "" {
			return errNestedParent
		}
	}

	// Asking for primary makes the tag a ROOT: a primary tag has no parent.
	if primaryNamed && tag.IsPrimary {
		tag.ParentID = ""
		return nil
	}

	// A tag with a parent is a CHILD, and a child is not primary.
	if tag.ParentID != "" {
		tag.IsPrimary = false
	}

	return nil
}

// The refusals [Plugin.resolveTagParent] answers with.
var (
	// errPrimaryWithParent reports a body that asks for both.
	errPrimaryWithParent = errors.New("a tag with a parent cannot be a primary tag")
	// errSelfParent reports a tag that names itself as its parent.
	errSelfParent = errors.New("a tag cannot be its own parent")
	// errParentMissing reports a parent that does not exist.
	errParentMissing = errors.New("the parent tag does not exist")
	// errNestedParent reports a parent that is itself a child.
	errNestedParent = errors.New("a secondary tag cannot be the parent of another tag")
)

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

	if err := setTagOrder(e.App, physicalName(e), order); err != nil {
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
