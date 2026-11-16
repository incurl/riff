package api

import (
	"context"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// listDiscussionsHandler serves GET /api/discussions.
//
// The listing supports the filters the frontend's index page uses: the
// tag filter, the "started by me" filter, and a search term.
func (s *Server) listDiscussionsHandler(e *core.RequestEvent) error {
	limit, offset := pageParams(e)
	query := e.Request.URL.Query()

	filter := repository.DiscussionFilter{
		UserID: query.Get("filter[user]"),
		TagID:  query.Get("filter[tag]"),
		State:  query.Get("filter[state]"),
		Search: strings.TrimSpace(query.Get("filter[q]")),
	}

	discussions, total, err := s.repo.ListDiscussions(filter, limit, offset)
	if err != nil {
		return fail(e, errInternal("The discussions could not be loaded.", err))
	}

	resources := make([]Resource, 0, len(discussions))
	for _, discussion := range discussions {
		resource, err := s.discussionResource(e, discussion)
		if err != nil {
			return fail(e, errInternal("The discussions could not be loaded.", err))
		}
		resources = append(resources, resource)
	}

	return s.writeDocument(e, Document{
		Data: resources,
		Meta: &Meta{Page: &PageMeta{Total: total}},
	})
}

// showDiscussionHandler serves GET /api/discussions/{id}.
func (s *Server) showDiscussionHandler(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if id == "" {
		return fail(e, errNotFound("That discussion does not exist."))
	}

	discussion, err := s.repo.FindDiscussion(id)
	if err != nil {
		return fail(e, notFoundError("That discussion does not exist."))
	}

	// A hidden discussion is invisible to everyone but a moderator, and
	// it answers 404 rather than 403 so the response never confirms
	// that it exists.
	if discussion.IsHidden && !s.can(e, domain.PermissionHideDiscussion, TypeDiscussion, discussion.ID) {
		return fail(e, notFoundError("That discussion does not exist."))
	}

	resource, err := s.discussionResource(e, discussion)
	if err != nil {
		return fail(e, errInternal("The discussion could not be loaded.", err))
	}

	return s.writeDocument(e, Document{Data: resource})
}

// discussionResource renders a discussion with its relationships filled
// in.
func (s *Server) discussionResource(e *core.RequestEvent, discussion *domain.Discussion) (Resource, error) {
	postIDs, err := s.repo.DiscussionPostIDs(discussion.ID)
	if err != nil {
		return Resource{}, err
	}
	tagIDs, err := s.repo.DiscussionTagIDs(discussion.ID)
	if err != nil {
		return Resource{}, err
	}

	discussion.PostIDs = postIDs
	discussion.TagIDs = tagIDs

	canReply := s.can(e, domain.PermissionReply, TypeDiscussion, discussion.ID)
	if discussion.State == domain.DiscussionLocked {
		canReply = false
	}

	return discussionResource(
		discussion,
		canReply,
		s.can(e, domain.PermissionEditDiscussion, TypeDiscussion, discussion.ID),
		s.can(e, domain.PermissionHideDiscussion, TypeDiscussion, discussion.ID),
		s.can(e, domain.PermissionDeleteDiscussion, TypeDiscussion, discussion.ID),
		tagIDs,
	), nil
}

// listPostsHandler serves GET /api/posts.
//
// The frontend lists a discussion's posts through the discussion's
// `posts` relationship, but it also queries the posts endpoint directly
// when a post is hidden or when the discussion page paginates.
func (s *Server) listPostsHandler(e *core.RequestEvent) error {
	limit, offset := pageParams(e)
	query := e.Request.URL.Query()

	discussionID := query.Get("filter[discussion]")
	authorSlug := query.Get("filter[author]")

	var (
		posts []*domain.Post
		err   error
	)

	switch {
	case discussionID != "":
		posts, err = s.repo.ListPosts(discussionID, limit, offset)
	case authorSlug != "":
		// The profile page lists a user's posts: `filter[author]` is the
		// user's slug (their username), and `filter[type]` narrows to the
		// post types the page shows.
		posts, err = s.repo.ListPostsByAuthor(authorSlug, query.Get("filter[type]"), limit, offset)
	default:
		return fail(e, errValidation("A discussion or author filter is required."))
	}

	if err != nil {
		return fail(e, errInternal("The posts could not be loaded.", err))
	}

	resources := make([]Resource, 0, len(posts))
	for _, post := range posts {
		resource, err := s.postResource(e, post)
		if err != nil {
			return fail(e, errInternal("The posts could not be loaded.", err))
		}
		resources = append(resources, resource)
	}

	return s.writeDocument(e, Document{Data: resources})
}

// showPostHandler serves GET /api/posts/{id}.
func (s *Server) showPostHandler(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	post, err := s.repo.FindPost(id)
	if err != nil {
		return fail(e, notFoundError("That post does not exist."))
	}

	if post.IsHidden && !s.can(e, domain.PermissionHidePost, TypePost, post.ID) {
		return fail(e, notFoundError("That post does not exist."))
	}

	resource, err := s.postResource(e, post)
	if err != nil {
		return fail(e, errInternal("The post could not be loaded.", err))
	}
	return s.writeDocument(e, Document{Data: resource})
}

// postResource renders a post with its plugin-contributed attributes.
func (s *Server) postResource(e *core.RequestEvent, post *domain.Post) (Resource, error) {
	extra := map[string]any{}
	ctx := e.Request.Context()
	for _, source := range s.attributes[TypePost] {
		value, err := source.Value(ctx, AttributeRequest{
			ActorID:  s.actor(e).ID(),
			RecordID: post.ID,
			Type:     TypePost,
		})
		if err != nil {
			return Resource{}, err
		}
		extra[source.Name] = value
	}

	resource := postResource(
		post,
		s.can(e, domain.PermissionEditPost, TypePost, post.ID),
		s.can(e, domain.PermissionDeletePost, TypePost, post.ID),
		s.can(e, domain.PermissionHidePost, TypePost, post.ID),
		extra,
	)

	// A plugin may contribute a relationship too.
	for _, source := range s.relationships[TypePost] {
		identifiers, err := source.Resolve(ctx, RelationshipRequest{
			ActorID:  s.actor(e).ID(),
			RecordID: post.ID,
			Type:     TypePost,
		})
		if err != nil {
			return Resource{}, err
		}
		if resource.Relationships == nil {
			resource.Relationships = map[string]Relationship{}
		}
		if source.Many {
			resource.Relationships[source.Name] = Relationship{Data: identifiers}
		} else if len(identifiers) > 0 {
			resource.Relationships[source.Name] = Relationship{Data: identifiers[0]}
		} else {
			resource.Relationships[source.Name] = Relationship{Data: nil}
		}
	}

	return resource, nil
}

// listUsersHandler serves GET /api/users.
//
// Only accounts the caller may see are listed. The forum has no separate
// user store: the listing reads the shared `users` collection.
func (s *Server) listUsersHandler(e *core.RequestEvent) error {
	limit, offset := pageParams(e)

	users, err := s.repo.ListUsers(limit, offset)
	if err != nil {
		return fail(e, errInternal("The users could not be loaded.", err))
	}

	resources := make([]Resource, 0, len(users))
	for _, user := range users {
		resources = append(resources, s.userResourceWithGroups(user, user.Groups))
	}

	return s.writeDocument(e, Document{Data: resources})
}

// showUserHandler serves GET /api/users/{id}.
func (s *Server) showUserHandler(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")

	// `?bySlug=true` means the path segment is the user's SLUG, not
	// their record id. The profile route is keyed by the username, so
	// that is how the frontend asks for it: a user page cannot know the
	// record id without loading the user first.
	var (
		user *domain.User
		err  error
	)
	if wantsSlugLookup(e) {
		user, err = s.repo.UserBySlug(id)
	} else {
		user, err = s.repo.UserByID(id)
	}
	if err != nil {
		return fail(e, notFoundError("That user does not exist."))
	}
	return s.writeDocument(e, Document{Data: s.userResourceWithGroups(user, user.Groups)})
}

// wantsSlugLookup reports whether the request asked for a slug lookup.
//
// The frontend sends `bySlug=true`; a bare flag (`?bySlug`) is accepted
// too, and a `false` value means a plain id lookup.
func wantsSlugLookup(e *core.RequestEvent) bool {
	raw := strings.ToLower(strings.TrimSpace(e.Request.URL.Query().Get("bySlug")))
	return raw == "true" || raw == "1"
}

// listGroupsHandler serves GET /api/groups.
func (s *Server) listGroupsHandler(e *core.RequestEvent) error {
	groups, err := s.repo.ListGroups()
	if err != nil {
		return fail(e, errInternal("The groups could not be loaded.", err))
	}

	resources := make([]Resource, 0, len(groups))
	for _, group := range groups {
		resources = append(resources, groupResource(group))
	}

	return s.writeDocument(e, Document{Data: resources})
}

// listNotificationsHandler serves GET /api/notifications.
func (s *Server) listNotificationsHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	limit, offset := pageParams(e)
	notifications, total, err := s.repo.ListNotifications(actor.User.ID, limit, offset)
	if err != nil {
		return fail(e, errInternal("The notifications could not be loaded.", err))
	}

	resources := make([]Resource, 0, len(notifications))
	for _, notification := range notifications {
		resources = append(resources, notificationResource(notification))
	}

	return s.writeDocument(e, Document{
		Data: resources,
		Meta: &Meta{Page: &PageMeta{Total: total}},
	})
}

// showForumHandler serves GET /api/.
//
// It is the frontend's boot document: the forum's attributes, the groups
// the caller may see, and the caller itself.
func (s *Server) showForumHandler(e *core.RequestEvent) error {
	actor := s.actor(e)

	groups, err := s.repo.ListGroups()
	if err != nil {
		return fail(e, errInternal("The forum could not be loaded.", err))
	}

	actorResource := any(nil)
	if actor.User != nil {
		actorResource = s.userResourceWithGroups(actor.User, actor.User.Groups)
	}

	attributes := map[string]any{
		"title":                   "Forum",
		"description":             "",
		"baseUrl":                 s.basePath,
		"basePath":                s.basePath,
		"baseOrigin":              "",
		"apiUrl":                  s.apiPath,
		"debug":                   false,
		"welcomeTitle":            "",
		"welcomeMessage":          "",
		"showLanguageSelector":    false,
		"showThemeSelector":       false,
		"showPostSource":          false,
		"allowSignUp":             false,
		"defaultRoute":            "/",
		"canViewForum":            s.can(e, domain.PermissionViewForum, TypeForum, "1"),
		"canStartDiscussion":      s.can(e, domain.PermissionStartDiscussion, TypeForum, "1"),
		"canSearchUsers":          s.can(e, domain.PermissionSearchUsers, TypeForum, "1"),
		"canCreateAccessToken":    false,
		"canModerateAccessTokens": false,
		"canEditUserCredentials":  false,
		"serviceWorker":           false,
		"assetsBaseUrl":           s.basePath + "/assets",
		"jsChunksBaseUrl":         s.basePath + "/assets/js",
		"adminUrl":                s.basePath + "/admin",
		"version":                 "2.0.0",
	}

	relationships := map[string]Relationship{
		"groups": {Data: identifiers(TypeGroup, groupIDs(groups))},
		"actor":  {Data: actorResource},
	}

	return s.writeDocument(e, Document{
		Data: Resource{
			Type:          TypeForum,
			ID:            "1",
			Attributes:    attributes,
			Relationships: relationships,
		},
	})
}

// groupIDs returns the ids of a group list.
func groupIDs(groups []*domain.Group) []string {
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

// unusedContext keeps the context import honest.
var _ = context.Background
