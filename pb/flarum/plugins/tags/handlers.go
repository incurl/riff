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
func listTagsHandler(e *core.RequestEvent) error {
	physical := physicalName(e)
	tags, err := listTags(e.App, physical)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be loaded.")
	}

	resources := make([]api.Resource, 0, len(tags))
	for _, tag := range tags {
		resources = append(resources, tagResource(tag))
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resources})
}

// showTagHandler serves GET /api/tags/{id}.
func showTagHandler(e *core.RequestEvent) error {
	physical := physicalName(e)
	tag, err := findTag(e.App, physical, e.Request.PathValue("id"))
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That tag does not exist.")
	}
	return api.WriteDocument(e, http.StatusOK, api.Document{Data: tagResource(tag)})
}

// addTagHandler serves POST /api/discussions/{id}/tags.
//
// The body names the tag to attach. Attaching a tag the discussion
// already carries succeeds without creating a second link.
func addTagHandler(e *core.RequestEvent) error {
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
		allowed, err := api.Can(e, domain.PermissionEditTag, api.TypeTag, tag.ID)
		if err != nil || !allowed {
			return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to use that tag.")
		}
	}

	if err := attachTag(e.App, discussionID, tag.ID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be attached.")
	}

	return api.WriteDocument(e, http.StatusCreated, api.Document{Data: tagResource(tag)})
}

// removeTagHandler serves DELETE /api/discussions/{id}/tags/{tagId}.
func removeTagHandler(e *core.RequestEvent) error {
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

	if err := detachTag(e.App, discussionID, tagID); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tag could not be removed.")
	}

	return api.WriteDocument(e, http.StatusNoContent, api.Document{})
}

// tagResource renders a tag as a JSON:API resource.
//
// The attribute names match upstream Flarum's TagResource, which is
// what the frontend's tag components read.
func tagResource(tag *domain.Tag) api.Resource {
	return api.Resource{
		Type: api.TypeTag,
		ID:   tag.ID,
		Attributes: map[string]any{
			"name":            tag.Name,
			"slug":            tag.Slug,
			"description":     tag.Description,
			"color":           tag.Color,
			"icon":            tag.Icon,
			"position":        tag.Position,
			"isHidden":        tag.IsHidden,
			"isPrimary":       tag.IsPrimary,
			"isRestricted":    tag.IsRestricted,
			"discussionCount": tag.DiscussionCount,
		},
		Relationships: map[string]api.Relationship{
			"parent": {Data: identifierOrNil(api.TypeTag, tag.ParentID)},
		},
	}
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
