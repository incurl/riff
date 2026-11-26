package tags

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// listTagsHandler serves GET /api/tags.
//
// The response is a JSON:API collection of tag resources.
//
// `filter[q]` is upstream's `TagSearcher`: a prefix match on a tag's name or
// slug. It is what the header's search dropdown asks when the reader types, so
// a tag with no discussions yet is still findable by name.
func (p *Plugin) listTagsHandler(e *core.RequestEvent) error {
	physical := physicalName(e)
	tags, err := listTags(e.App, physical, e.Request.URL.Query().Get("filter[q]"))
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be loaded.")
	}

	resources := make([]api.Resource, 0, len(tags))
	for _, tag := range tags {
		resource, err := p.tagResourceWithRelationships(e, tag)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be loaded.")
		}
		resources = append(resources, resource)
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resources})
}

// tagResourceWithRelationships renders a tag with the plugin's
// relationships.
//
// The relationships are contributed through the extender mechanism, so
// the plugin resolves them itself: the CORE does not know about a
// plugin's resource types, and the tag resource is the plugin's own.
func (p *Plugin) tagResourceWithRelationships(e *core.RequestEvent, tag *domain.Tag) (api.Resource, error) {
	resource := p.tagResourceFor(e, tag)

	children, err := childTagIDs(e.App, physicalName(e), tag.ID)
	if err != nil {
		return api.Resource{}, err
	}

	childIdentifiers := make([]api.Identifier, 0, len(children))
	for _, id := range children {
		childIdentifiers = append(childIdentifiers, api.Identifier{Type: api.TypeTag, ID: id})
	}

	resource.Relationships["children"] = api.Relationship{Data: childIdentifiers}

	if tag.LastPostedDiscussion != "" {
		resource.Relationships["lastPostedDiscussion"] = api.Relationship{
			Data: api.Identifier{Type: api.TypeDiscussion, ID: tag.LastPostedDiscussion},
		}
	}

	return resource, nil
}

// showTagHandler serves GET /api/tags/{id}.
func (p *Plugin) showTagHandler(e *core.RequestEvent) error {
	physical := physicalName(e)
	tag, err := findTag(e.App, physical, e.Request.PathValue("id"))
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That tag does not exist.")
	}
	resource, err := p.tagResourceWithRelationships(e, tag)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be loaded.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resource})
}

// addTagHandler serves POST /api/discussions/{id}/tags.
//
// The body names the tag to attach. Attaching a tag the discussion
// already carries succeeds without creating a second link.
func (p *Plugin) addTagHandler(e *core.RequestEvent) error {
	_, err := api.CurrentActor(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	discussionID := e.Request.PathValue("id")
	if discussionID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A discussion is required.")
	}

	// Attaching a tag CHANGES the discussion, so the caller must be
	// allowed to change its tags. Upstream's authority for this is
	// the tags extension's `Access\\DiscussionPolicy::tag()`: the
	// AUTHOR of the discussion, and only while the
	// `allow_tag_change` setting permits it.
	//
	// Without this check every signed-in member could tag any
	// discussion in the forum.
	allowed, err := api.CanTagDiscussion(e, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be attached.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to tag that discussion.")
	}

	var body struct {
		Data struct {
			Attributes struct {
				Tag string `json:"tag"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The request body could not be read.")
	}

	tagID := strings.TrimSpace(body.Data.Attributes.Tag)
	if tagID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A tag is required.")
	}

	physical := physicalName(e)
	tag, err := findTag(e.App, physical, tagID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That tag does not exist.")
	}

	// A restricted tag has its own permission scope: only the groups
	// that may see it may attach it.
	if tag.IsRestricted {
		allowed, err := canInTag(e, domain.PermissionEditTag, tag)
		if err != nil || !allowed {
			return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to use that tag.")
		}
	}

	// The count rules are checked AFTER the permission and BEFORE the
	// write: attaching a tag that breaks them would leave the
	// discussion in a state no editor could produce.
	if err := p.checkCountsAfterAttaching(e, discussionID, tag); err != nil {
		return err
	}

	if err := attachTag(e.App, discussionID, tag.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be attached.")
	}

	return api.WriteDocument(e, http.StatusCreated, api.Document{Data: tagResource(tag)})
}

// removeTagHandler serves DELETE /api/discussions/{id}/tags/{tagId}.
func (p *Plugin) removeTagHandler(e *core.RequestEvent) error {
	if _, err := api.CurrentActor(e); err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	discussionID := e.Request.PathValue("id")
	tagID := e.Request.PathValue("tagId")

	// Detaching a tag changes the discussion too, so it carries the
	// same policy check as attaching one.
	allowed, err := api.CanTagDiscussion(e, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be removed.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to untag that discussion.")
	}

	// A removal may break the MINIMUM counts, and upstream refuses it:
	// the rules are checked against the set that would result.
	if err := p.checkCountsAfterDetaching(e, discussionID, tagID); err != nil {
		return err
	}

	if err := detachTag(e.App, discussionID, tagID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be removed.")
	}

	return api.WriteDocument(e, http.StatusNoContent, api.Document{})
}

// tagResource renders a tag as a JSON:API resource.
//
// The attribute names match upstream Flarum's `TagResource`, which is
// what the frontend's tag components read. The set is upstream's, in
// upstream's order:
//
//	name, description, slug, storedSlug, color, icon, isHidden,
//	isPrimary, isRestricted, discussionCount, position, defaultSort,
//	isChild, lastPostedAt, canStartDiscussion, canAddToDiscussion
//
// The `can*` attributes carry the CALLER's capabilities, so they are
// computed here rather than read off the tag.
func tagResource(tag *domain.Tag) api.Resource {
	// `position` is NULLABLE upstream, and the null is LOAD-BEARING: it is
	// how the frontend tells a placed tag from an unplaced one. The admin's
	// tag page splits its two lists on exactly that — `position() !== null &&
	// !isChild()` for the primary list and `position() === null` for the
	// "Secondary tags" list — so serving the column's stored 0 for an
	// unplaced tag put EVERY tag in the primary list and left the secondary
	// one permanently empty.
	//
	// `isPrimary` is the stored flag that says which of the two a tag is
	// (`applyTag` keeps it and the column in step), so it is what decides
	// the shape here.
	position := any(nil)
	if tag.IsPrimary {
		position = tag.Position
	}

	return api.Resource{
		Type: api.TypeTag,
		ID:   tag.ID,
		Attributes: map[string]any{
			"name":            tag.Name,
			"slug":            tag.Slug,
			"description":     tag.Description,
			"color":           tag.Color,
			"icon":            tag.Icon,
			"position":        position,
			"isHidden":        tag.IsHidden,
			"isPrimary":       tag.IsPrimary,
			"isRestricted":    tag.IsRestricted,
			"discussionCount": tag.DiscussionCount,
			"defaultSort":     tag.DefaultSort,
			// A tag with a parent is a child, and upstream reports that
			// as an attribute rather than a relationship alone: the
			// frontend sorts on it.
			"isChild": tag.ParentID != "",
			// The stored slug is the RAW column value, and it is visible
			// only to a caller who may edit the tag: it is what the
			// admin page edits, and the public `slug` is the
			// slug-driver's rendering of it.
			"storedSlug": tag.Slug,
			// The last time a discussion with this tag was posted. It
			// is NULL until one is, and the attribute is always
			// present: upstream's is a nullable DateTime, and the
			// frontend reads the ABSENCE as "never posted" — which
			// would be the same answer, but the shape would differ
			// from upstream's.
			"lastPostedAt": nil,
		},
		Relationships: map[string]api.Relationship{
			"parent": {Data: identifierOrNil(api.TypeTag, tag.ParentID)},
		},
	}
}

// canInTag reports whether the caller may perform the action in the tag.
//
// A RESTRICTED tag has its own permission scope, and the scope answers
// the question ALONE: the global grant is not consulted.
//
// This is upstream's `TagPolicy`: for a restricted tag it returns
// `$actor->hasPermission("tag{$id}.{$ability}")` — an EXACT match
// against the group's permission rows — and the global fallback in
// `Gate::allows()` is never reached, because the policy COVERED the
// question. Upstream's `User::hasPermission` is an exact string match,
// so a group holding the global `startDiscussion` does NOT satisfy a
// check for `tag5.startDiscussion`.
//
// The port's fallback would have opened every restricted tag to every
// member, because the member group holds `startDiscussion` globally —
// and a restricted tag that restricts nobody is worse than no restricted
// tag at all, because the operator believes it is closed.
//
// An unrestricted tag has no scope of its own, so the ordinary check
// answers it.
func canInTag(e *core.RequestEvent, action string, tag *domain.Tag) (bool, error) {
	if tag == nil {
		return false, nil
	}

	if tag.IsRestricted {
		return api.CanScoped(e, action, api.TypeTag, tag.ID)
	}

	return api.Can(e, action, api.TypeTag, tag.ID)
}

// tagResourceFor renders a tag for a specific caller.
//
// It is the form the endpoints use, because the `can*` attributes and
// the `storedSlug` visibility depend on WHO is asking.
func (*Plugin) tagResourceFor(e *core.RequestEvent, tag *domain.Tag) api.Resource {
	resource := tagResource(tag)

	canStart, err := canInTag(e, domain.PermissionStartDiscussion, tag)
	if err != nil {
		canStart = false
	}

	// A tag may be attached to a discussion by a caller who may start
	// a discussion in it — upstream's `addToDiscussion`.
	canAdd, err := canInTag(e, domain.PermissionStartDiscussion, tag)
	if err != nil {
		canAdd = false
	}

	resource.Attributes["canStartDiscussion"] = canStart
	resource.Attributes["canAddToDiscussion"] = canAdd

	if !tag.LastPostedAt.IsZero() {
		resource.Attributes["lastPostedAt"] = tag.LastPostedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	// The `storedSlug` is only visible to a caller who may edit the
	// tag, which is upstream's rule.
	canEdit, err := canInTag(e, domain.PermissionEditTag, tag)
	if err != nil {
		canEdit = false
	}
	if !canEdit {
		delete(resource.Attributes, "storedSlug")
	}

	if tag.LastPostedDiscussion != "" {
		resource.Relationships["lastPostedDiscussion"] = api.Relationship{
			Data: api.Identifier{Type: api.TypeDiscussion, ID: tag.LastPostedDiscussion},
		}
	}

	return resource
}

// identifierOrNil builds a resource identifier, or nil.
func identifierOrNil(resourceType, id string) any {
	if id == "" {
		return nil
	}
	return api.Identifier{Type: resourceType, ID: id}
}

// physicalName resolves the plugin's collection name.
//
// The forum's registry owns the prefix, so the plugin does not name a
// physical collection itself.
func physicalName(_ *core.RequestEvent) string {
	return physicalPrefix + "tags"
}

// physicalPrefix is the forum's reserved collection prefix.
//
// It is a constant rather than a lookup because the plugin's
// collection is created by its own migration, which runs before the
// registry is reachable from a request.
const physicalPrefix = "forum_"
