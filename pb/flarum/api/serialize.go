package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/incurl/riff/pb/flarum/domain"
)

// Resource type names.
//
// These are the JSON:API types the reused frontend expects; the models
// in the frontend's `common/models` directory are keyed by them.
const (
	// TypeForum is the forum resource.
	TypeForum = "forums"
	// TypeUser is the user resource.
	TypeUser = "users"
	// TypeGroup is the group resource.
	TypeGroup = "groups"
	// TypeDiscussion is the discussion resource.
	TypeDiscussion = "discussions"
	// TypePost is the post resource.
	TypePost = "posts"
	// TypeNotification is the notification resource.
	TypeNotification = "notifications"
	// TypeTag is the tag resource.
	TypeTag = "tags"
)

// timestamp renders a time as the RFC 3339 string JSON:API expects.
//
// The frontend's Model.transformDate parses it with new Date(), so the
// string must be a valid RFC 3339 timestamp. A zero time renders as an
// empty string, which the frontend reads as "no date".
func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// UserResource renders a user as a JSON:API resource.
//
// The attribute names match the frontend's User model exactly: the
// frontend reads `username`, `displayName`, `avatarUrl`, `groups` and the
// `can*` flags by name.
//
// It is exported so the module can put the caller into the boot
// payload: the frontend reads the user from the store, and a
// relationship's identifier alone carries no attributes.
func (s *Server) UserResource(user *domain.User) Resource {
	return s.userResourceWithGroups("", user, user.Groups)
}

// avatarURL is the public URL of an account's picture, or empty.
//
// The bytes come from PocketBase's `users.avatar` file, streamed through
// this module's own `{basePath}/avatars/{id}` route (see the module's
// avatar handler) rather than linked at PocketBase's native
// `/api/files/users/...` URL: that one lives at the root of the
// listener, which the forum does not own. The route answers 404 when
// there is no picture, which is what makes the frontend render initials.
func (s *Server) avatarURL(user *domain.User) string {
	if user == nil || user.AvatarURL == "" {
		return ""
	}

	return s.basePath + "/avatars/" + user.ID
}

// applyPluginAttributes fills in the attributes the plugins contributed
// for a resource type.
//
// It is the one place a plugin's contribution reaches the wire, so every
// resource type plugins extend goes through it. A type that did not would
// carry its plugin attributes nowhere, and the frontend would read them as
// undefined — a missing `canLock` silently hides the lock control.
func (s *Server) applyPluginAttributes(ctx context.Context, resource *Resource, actorID, recordID string) error {
	for _, source := range s.attributes[resource.Type] {
		value, err := source.Value(ctx, AttributeRequest{
			ActorID:  actorID,
			RecordID: recordID,
			Type:     resource.Type,
		})
		if err != nil {
			return err
		}
		if resource.Attributes == nil {
			resource.Attributes = map[string]any{}
		}
		resource.Attributes[source.Name] = value
	}
	return nil
}

// userResourceWithGroups renders a user as a JSON:API resource with an
// explicit group list.
//
// `actorID` is the CALLER. The suspend extension's `canSuspend` attribute
// is a question about the pair, not about the account alone, so the caller
// has to travel with the resource.
func (s *Server) userResourceWithGroups(actorID string, user *domain.User, groups []string) Resource {
	if groups == nil {
		groups = user.Groups
	}

	attributes := map[string]any{
		"username":                user.Username,
		"displayName":             user.DisplayName,
		"slug":                    user.Username,
		"avatarUrl":               s.avatarURL(user),
		"hasUploadedAvatar":       user.AvatarURL != "",
		"isAdmin":                 user.IsAdmin,
		"joinTime":                timestamp(user.JoinedAt),
		"lastSeenAt":              timestamp(user.LastSeenAt),
		"discussionCount":         user.DiscussionCount,
		"commentCount":            user.CommentCount,
		"unreadNotificationCount": user.UnreadNotificationCount,
		"newNotificationCount":    user.UnreadNotificationCount,
		"preferences":             map[string]any{},
		"canEdit":                 false,
		"canEditAvatar":           false,
		"canEditGroups":           false,
		"canDelete":               false,
	}

	relationships := map[string]Relationship{
		"groups": {Data: identifiers(TypeGroup, groups)},
	}

	resource := Resource{
		Type:          TypeUser,
		ID:            user.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}

	// The suspension attributes are the only plugin-contributed ones on a
	// user today. A failure is logged rather than fatal: a user resource is
	// rendered inside list responses, and losing a whole feed because one
	// attribute could not be computed is the worse outcome.
	if err := s.applyPluginAttributes(context.Background(), &resource, actorID, user.ID); err != nil {
		s.logger.Debug("forum: a user attribute could not be computed", slog.String("error", err.Error()))
	}

	return resource
}

// groupResource renders a group as a JSON:API resource.
func groupResource(group *domain.Group) Resource {
	return Resource{
		Type: TypeGroup,
		ID:   group.ID,
		Attributes: map[string]any{
			"nameSingular": group.NameSingular,
			"namePlural":   group.NamePlural,
			"color":        group.Color,
			"icon":         group.Icon,
			"isHidden":     group.IsHidden,
			"position":     group.Position,
			"memberCount":  group.MemberCount,
		},
	}
}

// discussionResource renders a discussion as a JSON:API resource.
//
// The `can*` attributes carry the caller's own capabilities, which is
// how the frontend decides whether to show the edit/hide/delete
// controls.
func discussionResource(discussion *domain.Discussion, canReply, canRename, canHide, canDelete, canTag bool, tagIDs []string) Resource {
	attributes := map[string]any{
		"title":            discussion.Title,
		"slug":             DiscussionSlug(discussion.ID, discussion.Slug),
		"createdAt":        timestamp(discussion.CreatedAt),
		"lastPostedAt":     timestamp(discussion.LastPostedAt),
		"lastPostNumber":   discussion.LastPostNumber,
		"commentCount":     discussion.CommentCount,
		"participantCount": discussion.ParticipantCount,
		"viewCount":        discussion.ViewCount,
		"isHidden":         discussion.IsHidden,
		"hiddenAt":         timestamp(discussion.HiddenAt),
		"canReply":         canReply,
		"canRename":        canRename,
		"canHide":          canHide,
		"canDelete":        canDelete,
		// Upstream's tags extension exposes the same flag
		// (`Schema\Boolean::make('canTag')`), computed with the same
		// policy the tag endpoint checks, so the control and the
		// endpoint always agree.
		"canTag":      canTag,
		"unreadCount": 0,
	}

	relationships := map[string]Relationship{
		"user":           {Data: identifier(TypeUser, discussion.UserID)},
		"lastPostedUser": {Data: identifier(TypeUser, discussion.LastPostedUserID)},
		"firstPost":      {Data: identifier(TypePost, discussion.FirstPostID)},
		"lastPost":       {Data: identifier(TypePost, discussion.LastPostID)},
		"hiddenUser":     {Data: identifier(TypeUser, discussion.HiddenUserID)},
		"posts":          {Data: identifiers(TypePost, discussion.PostIDs)},
		"tags":           {Data: identifiers(TypeTag, tagIDs)},
	}

	return Resource{
		Type:          TypeDiscussion,
		ID:            discussion.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}
}

// postResource renders a post as a JSON:API resource.
//
// The `contentHtml` attribute is what the frontend renders; `content`
// carries the Markdown source for the edit form.
func postResource(post *domain.Post, canEdit, canDelete, canHide bool, extra map[string]any) Resource {
	attributes := map[string]any{
		"content":      post.Content,
		"contentHtml":  post.ContentHTML,
		"contentType":  post.ContentType,
		"createdAt":    timestamp(post.CreatedAt),
		"editedAt":     timestamp(post.EditedAt),
		"hiddenAt":     timestamp(post.HiddenAt),
		"isHidden":     post.IsHidden,
		"number":       post.Number,
		"type":         post.Type,
		"canEdit":      canEdit,
		"canDelete":    canDelete,
		"canHide":      canHide,
		"renderFailed": false,
	}

	// A plugin's contributed attributes ride alongside the core ones.
	for key, value := range extra {
		attributes[key] = value
	}

	relationships := map[string]Relationship{
		"user":       {Data: identifier(TypeUser, post.UserID)},
		"discussion": {Data: identifier(TypeDiscussion, post.DiscussionID)},
		"editedUser": {Data: identifier(TypeUser, post.EditedUserID)},
		"hiddenUser": {Data: identifier(TypeUser, post.HiddenUserID)},
	}

	return Resource{
		Type:          TypePost,
		ID:            post.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}
}

// notificationResource renders a notification as a JSON:API resource.
func notificationResource(notification *domain.Notification) Resource {
	attributes := map[string]any{
		"type":      notification.Type,
		"subject":   notification.Subject,
		"excerpt":   notification.Excerpt,
		"readAt":    timestamp(notification.ReadAt),
		"createdAt": timestamp(notification.CreatedAt),
		"isRead":    notification.IsRead,
	}

	relationships := map[string]Relationship{
		"user":       {Data: identifier(TypeUser, notification.UserID)},
		"fromUser":   {Data: identifier(TypeUser, notification.FromUserID)},
		"discussion": {Data: identifier(TypeDiscussion, notification.DiscussionID)},
		"post":       {Data: identifier(TypePost, notification.PostID)},
	}

	return Resource{
		Type:          TypeNotification,
		ID:            notification.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}
}

// identifier builds a resource identifier.
func identifier(resourceType, id string) any {
	if id == "" {
		return nil
	}
	return Identifier{Type: resourceType, ID: id}
}

// identifiers builds a list of resource identifiers.
func identifiers(resourceType string, ids []string) []Identifier {
	out := make([]Identifier, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, Identifier{Type: resourceType, ID: id})
	}
	return out
}
