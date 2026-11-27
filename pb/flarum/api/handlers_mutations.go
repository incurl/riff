package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
	"github.com/incurl/riff/pb/flarum/session"
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
func decodeRequestBody(e *core.RequestEvent) (*requestBody, error) {
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

	// The author columns are relations to the shared `users` collection, so an
	// actor that is not a record there cannot be stamped: the save used to fail
	// the relation validation and answer 500 instead of saying why.
	if !s.actorHasForumProfile(e) {
		return fail(e, errNoForumProfile())
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	body, err := decodeRequestBody(e)
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

	// A plugin may own a writable relationship on the discussion (the
	// tags extension owns `tags`), so the relationships the request
	// carries are applied after the discussion exists.
	if err := s.writeRelationships(e, TypeDiscussion, discussion.ID, true, body.Data.Relationships); err != nil {
		return fail(e, errValidation(err.Error()))
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

	// A reply is stamped with its author, and the author column is a relation to
	// the shared `users` collection (see `actorHasForumProfile`).
	if !s.actorHasForumProfile(e) {
		return fail(e, errNoForumProfile())
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	body, err := decodeRequestBody(e)
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

	// The same rule the resource's `canReply` reports: a locked discussion
	// takes no new replies, but a caller who could unlock it may still
	// post. The gate and the flag have to agree, or the UI offers a
	// composer whose submit is refused.
	if discussion.IsLocked && !s.can(e, domain.PermissionLockDiscussion, TypeDiscussion, discussion.ID) {
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

// discussionWriteHandler serves the POST twin of the discussion write
// routes.
//
// The frontend does not send a PATCH or a DELETE: it turns every method
// other than GET and POST into a POST with an `X-HTTP-Method-Override`
// header (see the core's `Application.request`), and PocketBase's router
// matches on the REAL method before any handler runs. So the POST route
// is the only one such a request reaches, and the effective method has
// to be resolved here.
//
// The update and the delete share ONE path, so the override header — not
// the URL — is what tells them apart.
func (s *Server) discussionWriteHandler(e *core.RequestEvent) error {
	switch EffectiveMethod(e) {
	case http.MethodDelete:
		return s.deleteDiscussionHandler(e)
	case http.MethodPatch:
		return s.updateDiscussionHandler(e)
	default:
		return fail(e, errMethodNotAllowed())
	}
}

// postWriteHandler serves the POST twin of the post write routes.
//
// It is the twin of [Server.discussionWriteHandler], and the reasoning is
// the same: the frontend rewrites a PATCH or a DELETE into a POST with
// an `X-HTTP-Method-Override` header, and the router matches on the
// real method.
func (s *Server) postWriteHandler(e *core.RequestEvent) error {
	switch EffectiveMethod(e) {
	case http.MethodDelete:
		return s.deletePostHandler(e)
	case http.MethodPatch:
		return s.updatePostHandler(e)
	default:
		return fail(e, errMethodNotAllowed())
	}
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

	body, err := decodeRequestBody(e)
	if err != nil {
		return fail(e, err)
	}

	title := strings.TrimSpace(stringAttr(body.Data.Attributes, "title"))
	if title != "" && title != discussion.Title {
		// Upstream's `DiscussionPolicy::rename()`: the author, while the
		// `allow_renaming` setting permits it, or a moderator.
		allowed, err := s.canRenameDiscussion(e, discussion.ID)
		if err != nil {
			return fail(e, errInternal("The discussion could not be updated.", err))
		}
		if !allowed {
			return fail(e, errForbidden("You are not allowed to rename that discussion."))
		}
		if err := s.repo.UpdateDiscussionTitle(discussion.ID, title, actor.User.ID); err != nil {
			return fail(e, errInternal("The discussion could not be updated.", err))
		}
	}

	// A plugin may own a writable relationship on the discussion (the
	// tags extension owns `tags`).
	if err := s.writeRelationships(e, TypeDiscussion, discussion.ID, false, body.Data.Relationships); err != nil {
		return fail(e, errValidation(err.Error()))
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

	// The author may always delete their own discussion; anyone else
	// needs a moderator grant. See [Server.canAsModerator].
	if !s.canDeleteDiscussion(e, discussion) {
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

	// Upstream's `PostPolicy::edit()`: the author, while the
	// `allow_post_editing` setting permits it, or a moderator.
	allowed, err := s.canEditPostAsAuthor(e, post.ID)
	if err != nil {
		return fail(e, errInternal("The post could not be updated.", err))
	}
	if !allowed {
		return fail(e, errForbidden("You are not allowed to edit that post."))
	}

	body, err := decodeRequestBody(e)
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

	// The edit is announced, so a listener can react to it.
	//
	// Without this an edit reaches nobody: `EventPostEdited` was
	// declared and never dispatched, and a plugin that keeps a derived
	// set in step with a post's content (the Mentions plugin's mention
	// set, for instance) would keep the set the edit REMOVED.
	s.dispatch(e.Request.Context(), "forum.post.edited", actor.User.ID, updated.ID,
		map[string]any{"discussion": updated.DiscussionID})

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

	// The same author rule as the edit: `deletePost` is held by every
	// member, so the permission alone would let any member delete any
	// post.
	allowed, err := s.canDeletePost(e, post.ID)
	if err != nil {
		return fail(e, errInternal("The post could not be deleted.", err))
	}
	if !allowed {
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
//
// It is also registered for POST, because the frontend does not send a
// PATCH: `User.savePreferences` calls `Model.save()`, which sends a
// PATCH, and the core's `Application.request` rewrites every method
// other than GET and POST into a POST carrying an
// `X-HTTP-Method-Override` header. PocketBase's router matches on the
// REAL method before any handler runs, so without the POST twin the
// request matched no route at all and answered 404 — which the frontend
// reported as "That resource does not exist" for a settings save that
// was perfectly ordinary.
//
// The override is what makes the POST a PATCH, so a POST that names no
// method is refused.
func (s *Server) updateUserHandler(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		if err := RequireMethod(e, http.MethodPatch); err != nil {
			return fail(e, err)
		}
	}

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

	body, err := decodeRequestBody(e)
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

	// The group membership is an ADMIN's business: it is what makes an
	// account a moderator, and upstream manages it through the same
	// user resource (`Schema\\Relationship\\ToMany::make('groups')` is
	// writable there).
	//
	// Only an administrator may change it, and the IMPLICIT groups
	// (guest, member) are not assignable: they are not memberships but
	// the caller's state, and a row for them would let a caller claim
	// a group they are not in.
	if groups, ok := body.Data.Relationships["groups"]; ok {
		if !actor.User.IsAdmin {
			return fail(e, errForbidden("You are not allowed to change that account's groups."))
		}
		if err := s.setUserGroups(actor, id, groups); err != nil {
			return fail(e, err)
		}
	}

	if err := s.repo.SaveUserProfile(profile); err != nil {
		return fail(e, errInternal("The profile could not be saved.", err))
	}

	user, err := s.repo.UserByID(id)
	if err != nil {
		return fail(e, errInternal("The profile could not be saved.", err))
	}
	return s.writeDocument(e, Document{Data: s.userResourceWithGroups(s.actor(e).ID(), user, user.Groups)})
}

// readNotificationHandler serves PATCH /api/notifications/{id}.
//
// It is also registered for POST, for the same `X-HTTP-Method-Override`
// reason as the user update below.
func (s *Server) readNotificationHandler(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		if err := RequireMethod(e, http.MethodPatch); err != nil {
			return fail(e, err)
		}
	}

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

// deleteAllNotificationsHandler serves DELETE /api/notifications.
//
// It is upstream's `DeleteAllNotificationsController`, and the frontend
// calls it from the notification page's "delete all" button
// (framework/core/js/src/forum/states/NotificationListState.ts:60).
//
// The route is registered for POST as well, because the frontend does
// not send a DELETE: it turns every method other than GET and POST
// into a POST with an `X-HTTP-Method-Override` header (see the core's
// `Application.request`). The handler resolves the effective method
// itself.
func (s *Server) deleteAllNotificationsHandler(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		if err := RequireMethod(e, http.MethodDelete); err != nil {
			return fail(e, err)
		}
	}

	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	// Only the caller's OWN notifications: the repository filters by the
	// user id, so one account can never delete another's.
	if err := s.repo.DeleteAllNotifications(actor.User.ID); err != nil {
		return fail(e, errInternal("The notifications could not be deleted.", err))
	}
	return s.writeDocumentStatus(e, http.StatusNoContent, Document{})
}

// showGroupHandler serves GET /api/groups/{id}.
func (s *Server) showGroupHandler(e *core.RequestEvent) error {
	group, err := s.repo.FindGroup(e.Request.PathValue("id"))
	if err != nil {
		return fail(e, notFoundError("That group does not exist."))
	}
	return s.writeDocument(e, Document{Data: groupResource(group)})
}

// showMailSettingsHandler serves GET /api/mail/settings.
//
// The admin application's mail page fetches this on mount and reads
// `response.data.attributes`. The route has to answer with that shape:
// a 404 (or a body without `data`) leaves `response.data` undefined and
// the page throws on it.
//
// riff sends its mail through PocketBase's own SMTP configuration
// rather than through a forum-level mail driver, so the driver fields
// are empty and the page renders without the upstream driver settings.
// The bounce-reporting flags are false because there is no forum-level
// mail driver to report bounces for.
func (s *Server) showMailSettingsHandler(e *core.RequestEvent) error {
	return s.writeDocument(e, Document{Data: map[string]any{
		"type": "mailSettings",
		"id":   "mailSettings",
		"attributes": map[string]any{
			"fields":               map[string]any{},
			"sending":              false,
			"errors":               []any{},
			"canReceiveWebhooks":   false,
			"canProvisionWebhooks": false,
			"webhookUrl":           nil,
		},
	}})
}

// resetNotificationPreferencesHandler serves
// DELETE /api/users/{id}/notification-preferences.
//
// It resets the user's notification preferences to the defaults in one
// save, so the registered defaults apply again.
//
// The route has to exist: the frontend's settings page calls it, and an
// unrouted path falls through to the SPA catch-all, which answers with
// the SPA's index.html. The frontend's request helper throws on a
// non-2xx response, so the page reports an error — and because the
// body is HTML rather than JSON, the error carries the whole document
// instead of a readable message.
//
// Only the user themselves may reset their own preferences, the same
// rule upstream applies to the `preferences` field.
func (s *Server) resetNotificationPreferencesHandler(e *core.RequestEvent) error {
	// The frontend sends this as a POST with an `X-HTTP-Method-Override`
	// header, because it rewrites every method other than GET and POST.
	// The route is registered as `Any` for that reason, so the effective
	// method is resolved here.
	if err := RequireMethod(e, http.MethodDelete); err != nil {
		return fail(e, err)
	}

	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	if id != actor.User.ID {
		return fail(e, errForbidden("You are not allowed to change that account."))
	}

	profile, err := s.repo.FindUserProfile(id)
	if err != nil {
		return fail(e, errInternal("The profile could not be loaded.", err))
	}
	if profile == nil {
		// A user without a profile row has no preferences to reset,
		// and the answer is still the user: the frontend pushes the
		// response into its store.
		profile = &repository.UserProfile{UserID: id, IsActive: true}
	}

	if profile.Preferences == nil {
		profile.Preferences = map[string]any{}
	}
	// Upstream drops the `notify_{subject}_{driver}` keys so the
	// registered defaults apply again. The forum has no notification
	// drivers of its own, so there is normally nothing to drop — but
	// the keys are removed anyway, so a preference carried over from
	// another instance does not linger.
	for key := range profile.Preferences {
		if strings.HasPrefix(key, "notify_") {
			delete(profile.Preferences, key)
		}
	}

	if err := s.repo.SaveUserProfile(profile); err != nil {
		return fail(e, errInternal("The profile could not be saved.", err))
	}

	user, err := s.repo.UserByID(id)
	if err != nil {
		return fail(e, errInternal("The profile could not be loaded.", err))
	}
	return s.writeDocument(e, Document{Data: s.userResourceWithGroups(s.actor(e).ID(), user, user.Groups)})
}

// setUserGroups replaces an account's group membership.
//
// The membership is what makes an account a MODERATOR: the moderation
// powers live in the moderator group, and an explicit group row is what
// names the accounts that hold them.
//
// It is also what makes an account an ADMINISTRATOR, because the
// administrator flag is derived from the administrator group (see
// `pb/flarum/session/session.go::userFromRecord`).
//
// The caller is passed in rather than read off the request event: the
// forum resolves its callers from the session cookie or a bearer token,
// not from PocketBase's `e.Auth`, so `e.Auth` is nil for most of the
// requests that reach here.
//
// Only the EXPLICIT groups are assignable. The implicit groups (guest,
// member) are the caller's state rather than a membership, so a row
// for them would let an account claim a group it is not in — and the
// guest group would grant it the guest's read-only powers twice over.
func (s *Server) setUserGroups(actor session.Actor, userID string, relationship any) error {
	assignable := map[string]bool{
		domain.GroupModeratorID: true,
		domain.GroupAdminID:     true,
	}

	// The request names the groups the account should have, so the
	// current membership is replaced rather than added to.
	wanted := map[string]bool{}
	for _, entry := range relationshipIdentifiers(relationship) {
		if !assignable[entry] {
			return errForbidden("That group cannot be assigned.")
		}
		wanted[entry] = true
	}

	current, err := s.repo.UserGroupIDs(userID)
	if err != nil {
		return errInternal("The groups could not be loaded.", err)
	}

	// AN ADMINISTRATOR MAY NOT REMOVE THEIR OWN ADMINISTRATOR GROUP.
	//
	// The administrator group is what makes the account an administrator
	// (upstream derives `isAdmin()` from it), so an administrator who
	// drops it locks THEMSELVES out of the admin panel — and, if they
	// were the last administrator, locks EVERYONE out, because the
	// forum's only route to the admin panel is the admin group.
	//
	// Upstream's `User\SelfDemotionGuard` draws exactly this line, and
	// it draws it the same way: demoting ANOTHER administrator is
	// allowed, because the actor is still an administrator afterwards.
	// Only the self-demotion is refused.
	if actor.User != nil && userID == actor.User.ID && !wanted[domain.GroupAdminID] {
		for _, groupID := range current {
			if groupID == domain.GroupAdminID {
				return errForbidden("You cannot remove your own administrator group.")
			}
		}
	}

	for _, groupID := range current {
		if assignable[groupID] && !wanted[groupID] {
			if err := s.repo.RemoveUserFromGroup(userID, groupID); err != nil {
				return errInternal("The groups could not be saved.", err)
			}
		}
	}

	for groupID := range wanted {
		if err := s.repo.AddUserToGroup(userID, groupID); err != nil {
			return errInternal("The groups could not be saved.", err)
		}
	}

	return nil
}

// relationshipIdentifiers reads the group ids out of a relationship.
//
// The relationship is the raw decoded JSON, so it is read defensively:
// a `groups` relationship is an object with a `data` member, and the
// `data` member is either a list of identifiers or a single one.
func relationshipIdentifiers(relationship any) []string {
	ids := make([]string, 0)

	object, ok := relationship.(map[string]any)
	if !ok {
		return ids
	}

	switch data := object["data"].(type) {
	case []any:
		for _, item := range data {
			if identifier, ok := item.(map[string]any); ok {
				if id, ok := identifier["id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	case map[string]any:
		if id, ok := data["id"].(string); ok {
			ids = append(ids, id)
		}
	default:
		// A relationship the frontend does not recognise carries no
		// identifiers, which leaves the membership unchanged.
	}

	return ids
}
