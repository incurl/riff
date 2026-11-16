package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// requestBody is the JSON:API request document.
//
// The frontend posts `{"data": {"type": ..., "attributes": {...}}}`.
type requestBody struct {
	// Data is the resource the request acts on.
	Data requestResource `json:"data"`
}

// requestResource is the resource object of a request document.
type requestResource struct {
	// Type is the JSON:API resource type.
	Type string `json:"type"`
	// ID is the resource's id, empty on a create.
	ID string `json:"id"`
	// Attributes are the fields to write.
	Attributes map[string]any `json:"attributes"`
	// Relationships are the relations to write.
	Relationships map[string]any `json:"relationships"`
}

// decodeBody reads the JSON:API request document.
func decodeBody(e *core.RequestEvent) (*requestBody, error) {
	var body requestBody
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return nil, errValidation("The request body could not be read.")
	}
	return &body, nil
}

// stringAttr reads a string attribute.
func stringAttr(attributes map[string]any, key string) string {
	value, ok := attributes[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strings.TrimSuffix(strings.TrimRight(formatFloat(typed), "0"), ".")
	default:
		return ""
	}
}

// formatFloat renders a float without a trailing ".0".
func formatFloat(value float64) string {
	raw := strings.TrimRight(strings.TrimRight(jsonNumber(value), "0"), ".")
	return raw
}

// jsonNumber renders a float as JSON does.
func jsonNumber(value float64) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "0"
	}
	return string(encoded)
}

// createDiscussionHandler serves POST /api/discussions.
//
// The request body carries the title and the first post's content.
func (s *Server) createDiscussionHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if !s.can(e, domain.PermissionStartDiscussion, TypeDiscussion, "") {
		return fail(e, errForbidden("You are not allowed to start a discussion."))
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	body, err := decodeBody(e)
	if err != nil {
		return fail(e, err)
	}

	title := strings.TrimSpace(stringAttr(body.Data.Attributes, "title"))
	if title == "" {
		return fail(e, errValidation("A discussion needs a title."))
	}

	content := stringAttr(body.Data.Attributes, "content")
	if strings.TrimSpace(content) == "" {
		return fail(e, errValidation("A discussion needs a message."))
	}

	discussion, post, err := s.createDiscussion(actor.User.ID, title, content)
	if err != nil {
		return fail(e, err)
	}

	resource, err := s.discussionResource(e, discussion)
	if err != nil {
		return fail(e, errInternal("The discussion could not be created.", err))
	}
	postResource, err := s.postResource(e, post)
	if err != nil {
		return fail(e, errInternal("The discussion could not be created.", err))
	}

	return s.writeDocumentStatus(e, http.StatusCreated, Document{
		Data:     resource,
		Included: []Resource{postResource},
	})
}

// createPostHandler serves POST /api/posts.
//
// The request body names the discussion to reply in.
func (s *Server) createPostHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	body, err := decodeBody(e)
	if err != nil {
		return fail(e, err)
	}

	discussionID := relationshipID(body.Data.Relationships, "discussion")
	if discussionID == "" {
		discussionID = stringAttr(body.Data.Attributes, "discussion")
	}
	if discussionID == "" {
		return fail(e, errValidation("A reply needs a discussion."))
	}

	discussion, err := s.repo.FindDiscussion(discussionID)
	if err != nil {
		return fail(e, notFoundError("That discussion does not exist."))
	}

	if discussion.State == domain.DiscussionLocked {
		return fail(e, errForbidden("That discussion is locked."))
	}
	if !s.can(e, domain.PermissionReply, TypeDiscussion, discussion.ID) {
		return fail(e, errForbidden("You are not allowed to reply here."))
	}

	content := stringAttr(body.Data.Attributes, "content")
	if strings.TrimSpace(content) == "" {
		return fail(e, errValidation("A reply needs a message."))
	}

	post, err := s.createPost(actor.User.ID, discussion.ID, content)
	if err != nil {
		return fail(e, err)
	}

	resource, err := s.postResource(e, post)
	if err != nil {
		return fail(e, errInternal("The reply could not be created.", err))
	}
	return s.writeDocumentStatus(e, http.StatusCreated, Document{Data: resource})
}

// relationshipID reads an identifier out of a relationship map.
func relationshipID(relationships map[string]any, name string) string {
	if relationships == nil {
		return ""
	}
	raw, ok := relationships[name]
	if !ok || raw == nil {
		return ""
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	data, ok := entry["data"]
	if !ok || data == nil {
		return ""
	}
	switch typed := data.(type) {
	case map[string]any:
		if id, ok := typed["id"].(string); ok {
			return id
		}
	case string:
		return typed
	default:
		return ""
	}
	return ""
}

// updateDiscussionHandler serves PATCH /api/discussions/{id}.
func (s *Server) updateDiscussionHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	discussion, err := s.repo.FindDiscussion(id)
	if err != nil {
		return fail(e, notFoundError("That discussion does not exist."))
	}

	body, err := decodeBody(e)
	if err != nil {
		return fail(e, err)
	}

	title := strings.TrimSpace(stringAttr(body.Data.Attributes, "title"))
	if title != "" && title != discussion.Title {
		if !s.can(e, domain.PermissionEditDiscussion, TypeDiscussion, discussion.ID) {
			return fail(e, errForbidden("You are not allowed to rename that discussion."))
		}
		if err := s.repo.UpdateDiscussionTitle(discussion.ID, title, actor.User.ID); err != nil {
			return fail(e, errInternal("The discussion could not be updated.", err))
		}
	}

	resource, err := s.discussionResource(e, discussion)
	if err != nil {
		return fail(e, errInternal("The discussion could not be updated.", err))
	}
	return s.writeDocument(e, Document{Data: resource})
}

// deleteDiscussionHandler serves DELETE /api/discussions/{id}.
func (s *Server) deleteDiscussionHandler(e *core.RequestEvent) error {
	if _, err := s.requireAuth(e); err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	discussion, err := s.repo.FindDiscussion(id)
	if err != nil {
		return fail(e, notFoundError("That discussion does not exist."))
	}

	if !s.can(e, domain.PermissionDeleteDiscussion, TypeDiscussion, discussion.ID) {
		return fail(e, errForbidden("You are not allowed to delete that discussion."))
	}

	if err := s.repo.DeleteDiscussion(discussion.ID); err != nil {
		return fail(e, errInternal("The discussion could not be deleted.", err))
	}

	return s.writeDocumentStatus(e, http.StatusNoContent, Document{})
}

// updatePostHandler serves PATCH /api/posts/{id}.
func (s *Server) updatePostHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	post, err := s.repo.FindPost(id)
	if err != nil {
		return fail(e, notFoundError("That post does not exist."))
	}

	if !s.can(e, domain.PermissionEditPost, TypePost, post.ID) {
		return fail(e, errForbidden("You are not allowed to edit that post."))
	}

	body, err := decodeBody(e)
	if err != nil {
		return fail(e, err)
	}

	content := stringAttr(body.Data.Attributes, "content")
	if strings.TrimSpace(content) == "" {
		return fail(e, errValidation("A post needs a message."))
	}

	contentHTML := s.renderContent(content)

	updated, err := s.repo.UpdatePostContent(post.ID, content, contentHTML, actor.User.ID)
	if err != nil {
		return fail(e, errInternal("The post could not be updated.", err))
	}

	resource, err := s.postResource(e, updated)
	if err != nil {
		return fail(e, errInternal("The post could not be updated.", err))
	}
	return s.writeDocument(e, Document{Data: resource})
}

// deletePostHandler serves DELETE /api/posts/{id}.
func (s *Server) deletePostHandler(e *core.RequestEvent) error {
	if _, err := s.requireAuth(e); err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	post, err := s.repo.FindPost(id)
	if err != nil {
		return fail(e, notFoundError("That post does not exist."))
	}

	if !s.can(e, domain.PermissionDeletePost, TypePost, post.ID) {
		return fail(e, errForbidden("You are not allowed to delete that post."))
	}

	if err := s.repo.DeletePost(post.ID); err != nil {
		return fail(e, errInternal("The post could not be deleted.", err))
	}

	return s.writeDocumentStatus(e, http.StatusNoContent, Document{})
}

// updateUserHandler serves PATCH /api/users/{id}.
//
// The forum writes only its own profile extras; the shared account's
// reserved fields are never touched.
func (s *Server) updateUserHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	if id != actor.User.ID && !actor.User.IsAdmin {
		return fail(e, errForbidden("You are not allowed to change that account."))
	}

	body, err := decodeBody(e)
	if err != nil {
		return fail(e, err)
	}

	profile, err := s.repo.FindUserProfile(id)
	if err != nil {
		return fail(e, errInternal("The profile could not be loaded.", err))
	}
	if profile == nil {
		profile = &repository.UserProfile{UserID: id, IsActive: true}
	}

	if name := strings.TrimSpace(stringAttr(body.Data.Attributes, "displayName")); name != "" {
		profile.DisplayName = name
	}
	if preferences, ok := body.Data.Attributes["preferences"].(map[string]any); ok {
		profile.Preferences = preferences
	}

	if err := s.repo.SaveUserProfile(profile); err != nil {
		return fail(e, errInternal("The profile could not be saved.", err))
	}

	user, err := s.repo.UserByID(id)
	if err != nil {
		return fail(e, errInternal("The profile could not be saved.", err))
	}
	return s.writeDocument(e, Document{Data: userResourceWithGroups(user, user.Groups)})
}

// readNotificationHandler serves PATCH /api/notifications/{id}.
func (s *Server) readNotificationHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	if err := s.repo.MarkNotificationRead(actor.User.ID, e.Request.PathValue("id")); err != nil {
		return fail(e, notFoundError("That notification does not exist."))
	}
	return s.writeDocumentStatus(e, http.StatusNoContent, Document{})
}

// readAllNotificationsHandler serves POST /api/notifications/read.
func (s *Server) readAllNotificationsHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	if err := s.repo.MarkAllNotificationsRead(actor.User.ID); err != nil {
		return fail(e, errInternal("The notifications could not be updated.", err))
	}
	return s.writeDocumentStatus(e, http.StatusNoContent, Document{})
}

// listSettingsHandler serves GET /api/settings.
func (s *Server) listSettingsHandler(e *core.RequestEvent) error {
	settings, err := s.repo.AllSettings()
	if err != nil {
		return fail(e, errInternal("The settings could not be loaded.", err))
	}
	return s.writeDocument(e, Document{Data: map[string]any{"settings": settings}})
}

// saveSettingsHandler serves POST /api/settings.
func (s *Server) saveSettingsHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	if !actor.User.IsAdmin {
		return fail(e, errForbidden("You are not allowed to change the settings."))
	}

	body, err := decodeBody(e)
	if err != nil {
		return fail(e, err)
	}

	settings, ok := body.Data.Attributes["settings"].(map[string]any)
	if !ok {
		return fail(e, errValidation("The settings could not be read."))
	}

	for key, value := range settings {
		if err := s.repo.SaveSetting(key, value, actor.User.ID); err != nil {
			return fail(e, errInternal("The settings could not be saved.", err))
		}
	}

	return s.writeDocument(e, Document{Data: map[string]any{"settings": settings}})
}

// showGroupHandler serves GET /api/groups/{id}.
func (s *Server) showGroupHandler(e *core.RequestEvent) error {
	group, err := s.repo.FindGroup(e.Request.PathValue("id"))
	if err != nil {
		return fail(e, notFoundError("That group does not exist."))
	}
	return s.writeDocument(e, Document{Data: groupResource(group)})
}
