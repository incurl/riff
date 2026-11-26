package api

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
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

	// `filter[tag]` arrives as a SLUG from the frontend (the tag page's URL is
	// `/t/<slug>`, and upstream resolves slugs) and as a record ID from an API
	// client. Both are accepted; an unknown value filters to nothing, which is
	// what a bogus id has always done.
	tagFilters, err := s.resolveTagFilters(query)
	if err != nil {
		return fail(e, errInternal("The discussions could not be loaded.", err))
	}

	filter := repository.DiscussionFilter{
		UserID:     query.Get("filter[user]"),
		TagFilters: tagFilters,
		State:      query.Get("filter[state]"),
		Search:     strings.TrimSpace(query.Get("filter[q]")),
		// The sort is upstream's: a bare name sorts ascending, a leading
		// "-" sorts descending (`-lastPostedAt`, `-commentCount`, ...).
		Sort: strings.TrimSpace(query.Get("sort")),
	}

	// A hidden tag hides its discussions from the ALL-DISCUSSIONS page only —
	// upstream's `HideHiddenTagsFromAllDiscussionsPage`, which does nothing
	// once any filter (or a full-text search) is in play.
	//
	// The tag filter is what makes this load-bearing: a tag page must keep
	// showing a hidden tag's discussions, or the page an operator is looking
	// at goes blank the moment they hide the tag.
	filter.ExcludeHiddenTags = filter.UserID == "" && len(filter.TagFilters) == 0 && filter.State == "" && filter.Search == ""

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

	meta, links := pagination(e, total, limit, offset)
	return s.writeDocumentWithIncluded(e, resources, meta, links)
}

// resolveTagFilters turns `filter[tag]` (and its negated spelling
// `filter[-tag]`) into the repository's OR-groups.
//
// The value is upstream's: a list of groups, each a comma-separated OR-set,
// and the groups are AND-ed. A member is a slug (what the frontend and an
// upstream client send), a record id (what this port's own tests and API
// clients send), or the pseudo-tag `untagged`.
//
// `untagged` is resolved BEFORE any slug lookup, so a real tag whose slug is
// `untagged` cannot shadow it — upstream compares the literal string first
// for the same reason. Empty members (`a,,b`) are dropped rather than
// resolved: upstream would turn one into a null id, which matches nothing.
func (s *Server) resolveTagFilters(query url.Values) ([]repository.TagFilter, error) {
	filters := make([]repository.TagFilter, 0, 2)

	for _, source := range []struct {
		key     string
		negated bool
	}{
		{key: "filter[tag]"},
		{key: "filter[-tag]", negated: true},
	} {
		for _, raw := range tagFilterValues(query, source.key) {
			members := make([]string, 0, 2)

			for _, part := range strings.Split(raw, ",") {
				member := strings.TrimSpace(part)

				switch member {
				case "":
					// A stray comma is not a member: upstream would resolve
					// it to a null id, which matches nothing.
				case repository.TagUntagged:
					members = append(members, repository.TagUntagged)
				default:
					id, err := s.repo.TagIDBySlug(member)
					if err != nil {
						return nil, err
					}
					if id == "" {
						// Not a slug: it may already be the record id.
						id = member
					}
					members = append(members, id)
				}
			}

			if len(members) == 0 {
				continue
			}

			filters = append(filters, repository.TagFilter{Members: members, Negated: source.negated})
		}
	}

	return filters, nil
}

// tagFilterValues collects one filter key's values, whatever shape it
// arrived in.
//
// A Flarum client can spell the same filter three ways, and all three are
// accepted because the client chooses:
//
//	filter[tag]=a        a single value (the tag page's route param)
//	filter[tag][]=a      an unindexed array
//	filter[tag][0]=a     an indexed array — what mithril's query builder
//	                     emits for a JS array, and what the gambit produces
//
// A repeated `filter[tag]` is the AND form and keeps its order. The bracketed
// forms are sorted by their index so the client's group order survives.
func tagFilterValues(query url.Values, key string) []string {
	values := append([]string{}, query[key]...)

	type indexedValue struct {
		index int
		order int
		value string
	}

	prefix := key + "["
	indexed := []indexedValue{}
	order := 0

	for name, entries := range query {
		if name == key || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, "]") {
			continue
		}

		inner := strings.TrimSuffix(strings.TrimPrefix(name, prefix), "]")

		// An unindexed `[]` keeps its encounter order; an indexed one sorts
		// by the number, which is the order the client wrote.
		index := order
		if parsed, err := strconv.Atoi(inner); err == nil {
			index = parsed
		}

		for _, entry := range entries {
			indexed = append(indexed, indexedValue{index: index, order: order, value: entry})
			order++
		}
	}

	slices.SortStableFunc(indexed, func(a, b indexedValue) int {
		if a.index != b.index {
			return cmp.Compare(a.index, b.index)
		}

		return cmp.Compare(a.order, b.order)
	})

	for _, entry := range indexed {
		values = append(values, entry.value)
	}

	return values
}

// writeDocumentWithIncluded writes a document and the resources its
// primary resources relate to.
//
// The `included` array is what makes a relation resolvable on the
// client: the frontend reads a relationship out of the STORE, and the
// store is filled from `payload.included`. Without it the identifier
// in the relationship names a resource that never arrives.
func (s *Server) writeDocumentWithIncluded(e *core.RequestEvent, primaries []Resource, meta *Meta, links *Links) error {
	included, err := s.includedResources(e, primaries)
	if err != nil {
		return fail(e, errInternal("The forum could not be loaded.", err))
	}

	return s.writeDocument(e, Document{
		Data:     primaries,
		Included: included,
		Meta:     meta,
		Links:    links,
	})
}

// writeSingleDocumentWithIncluded writes a document for ONE resource.
//
// A single resource is an OBJECT in JSON:API, not an array: the
// frontend's `Model.hasOne` reads `payload.data.type` and
// `payload.data.id` directly, so an array would break every relation
// on the page.
func (s *Server) writeSingleDocumentWithIncluded(e *core.RequestEvent, primary Resource) error {
	included, err := s.includedResources(e, []Resource{primary})
	if err != nil {
		return fail(e, errInternal("The forum could not be loaded.", err))
	}

	return s.writeDocument(e, Document{
		Data:     primary,
		Included: included,
	})
}

// showDiscussionHandler serves GET /api/discussions/{id}.
func (s *Server) showDiscussionHandler(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if id == "" {
		return fail(e, errNotFound("That discussion does not exist."))
	}

	// `?bySlug=true` means the path segment is the discussion's SLUG,
	// not its record id. The frontend asks for it that way because the
	// discussion URL is built from the slug
	// (`app.routes.discussion` uses `discussion.slug()`), and the slug
	// is what the reader sees.
	//
	// Upstream resolves it the same way (`DiscussionResource::find`:
	// `fromSlug($id, $actor)` when `bySlug` is set, `findOrFail($id)`
	// otherwise), and the slug carries the id in its first segment —
	// which is what makes it unique, since two discussions may share a
	// title.
	if wantsSlugLookup(e) {
		id = DiscussionIDFromSlug(id)
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

	return s.writeSingleDocumentWithIncluded(e, resource)
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

	// A locked discussion takes no new replies — except from a caller who
	// could unlock it. That is upstream's `Lock\Access\DiscussionPolicy::
	// reply()`: `is_locked && $actor->cannot('lock')` denies, so a
	// moderator keeps replying in a locked thread, which is what makes a
	// lock-with-a-note workflow possible.
	if discussion.IsLocked && !s.can(e, domain.PermissionLockDiscussion, TypeDiscussion, discussion.ID) {
		canReply = false
	}

	// The UI flags have to come from the SAME check the write path
	// makes, or the two disagree: upstream computes `canRename` and
	// `canDelete` with the same `can()` call the write path uses.
	//
	// The owner-aware check is the one that matters, because the
	// group permission alone says "no" for the author (the author
	// holds no `editDiscussion` row) and "yes" for a moderator.
	canRename, err := s.canRenameDiscussion(e, discussion.ID)
	if err != nil {
		canRename = false
	}

	canTag, err := s.canTagDiscussion(e, discussion.ID)
	if err != nil {
		canTag = false
	}

	resource := discussionResource(
		discussion,
		canReply,
		canRename,
		s.can(e, domain.PermissionHideDiscussion, TypeDiscussion, discussion.ID),
		s.canDeleteDiscussion(e, discussion),
		canTag,
		tagIDs,
	)

	// The plugins' own attributes: `isLocked`/`canLock` from the Lock
	// plugin, `isSticky`/`canSticky` from Sticky. Without this the
	// frontend reads them as undefined and draws no control and no badge.
	actor := s.actor(e)
	if err := s.applyPluginAttributes(e.Request.Context(), &resource, actor.ID(), discussion.ID, actor.User != nil && actor.User.IsAdmin); err != nil {
		return Resource{}, err
	}

	return resource, nil
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
	ids := query.Get("filter[id]")

	var (
		posts []*domain.Post
		err   error
	)

	switch {
	case ids != "":
		// The post stream asks for a WINDOW of posts by id: the
		// discussion's `posts` relationship names them all but carries no
		// attributes, so the stream loads the ones it is about to show
		// (`app.store.find('posts', loadIds)` builds
		// `?filter[id]=a,b,c`).
		posts, err = s.repo.PostsByIDs(strings.Split(ids, ","))
	case discussionID != "":
		posts, err = s.repo.ListPosts(discussionID, limit, offset)
	case authorSlug != "":
		// The profile page lists a user's posts: `filter[author]` is the
		// user's slug (their username), and `filter[type]` narrows to the
		// post types the page shows.
		posts, err = s.repo.ListPostsByAuthor(authorSlug, query.Get("filter[type]"), limit, offset)
	default:
		return fail(e, errValidation("A discussion, an author or an id filter is required."))
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

	return s.writeDocumentWithIncluded(e, resources, nil, nil)
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
	return s.writeSingleDocumentWithIncluded(e, resource)
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

	// The same owner-aware check the write path uses, so the flags and
	// the endpoint always agree.
	canEdit, canDelete := false, false
	if allowed, err := s.canEditPostAsAuthor(e, post.ID); err == nil {
		canEdit = allowed
	}
	if allowed, err := s.canDeletePost(e, post.ID); err == nil {
		canDelete = allowed
	}

	resource := postResource(
		post,
		canEdit,
		canDelete,
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
		resources = append(resources, s.userResourceWithGroups(s.actor(e).ID(), user, user.Groups))
	}

	return s.writeDocument(e, Document{Data: resources})
}

// settingTextOr reads a setting as text, with a fallback.
//
// The settings column is a PocketBase JSONField, so the value comes
// back as `types.JSONRaw` (a []byte of JSON); a JSON string decodes to
// the string it holds. [settingString] does the decoding.
//
// A setting that cannot be read falls back to the default, so a
// missing or unreadable setting never blanks the forum document.
// languageSelectorEnabled reports whether the forum shows its language
// selector.
//
// Upstream serves the SETTING (`ForumResource`: `$this->settings->get(
// 'show_language_selector', true)`), and its default is ON. The port
// hardcoded `false`, so the selector never rendered — which made a second
// language unreachable from the UI even when its catalogue was imported.
//
// The frontend needs more than the flag: `HeaderSecondary` also requires
// more than one locale to list, so an instance with only `en` shows
// nothing either way.
func languageSelectorEnabled(settings map[string]any) bool {
	value, ok := settings["show_language_selector"]
	if !ok {
		return true
	}
	return SettingTruthy(value, true)
}

func settingTextOr(settings map[string]any, key, fallback string) string {
	value, ok := SettingString(settings[key])
	if !ok {
		return fallback
	}
	return value
}

// assetURL builds the public URL of an uploaded asset.
//
// The paths are the forum's own upload paths (`logo_path` and
// friends), written by the module's asset upload endpoint. An unset
// path yields an empty URL — which is what the frontend expects for
// "no logo".
//
// The URL is under `/branding/`, not `/assets/`: the latter is the
// BUILT BUNDLE tree, served from the embedded frontend, so a logo
// stored there would be shadowed by the bundle route (or shadow it).
func (s *Server) assetURL(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return s.basePath + "/branding" + "/" + strings.TrimPrefix(path, "/")
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

	primary := s.userResourceWithGroups(s.actor(e).ID(), user, user.Groups)

	// The user's own relations travel with it: upstream's user resource
	// declares `groups`, and the frontend renders a group badge from the
	// GROUP resources the response carries — not from the relationship's
	// identifiers, which name rows the store has never seen.
	included, err := s.includedResources(e, []Resource{primary})
	if err != nil {
		return fail(e, errInternal("The user could not be loaded.", err))
	}

	return s.writeDocument(e, Document{Data: primary, Included: included})
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
	document, err := s.ForumDocument(e)
	if err != nil {
		return fail(e, errInternal("The forum could not be loaded.", err))
	}

	return s.writeDocument(e, Document{Data: document})
}

// ForumDocument builds the forum resource.
//
// It is EXPORTED because the module needs it too: the boot payload
// carries the forum document, and the SPA reads the forum's attributes
// out of it (`app.forum.attribute('canStartDiscussion')`).
//
// The `can*` attributes are the CALLER's permissions, exactly as
// upstream computes them (`ForumResource.php`:
// `$context->getActor()->can('startDiscussion')`). They are what the
// frontend's own affordances are built on: the "New Discussion" button
// is disabled when `canStartDiscussion` is false
// (`IndexSidebar.tsx`).
//
// The document is therefore PER-CALLER, and a caller that resolves to
// nobody gets the guest's answer. It is one document, built in one
// place: the boot payload and the API's `GET /api/` used to build it
// separately, and the copies drifted — the boot payload's copy
// hardcoded the flags to false, so the button was disabled for every
// signed-in account even though the API reported the right answer.
func (s *Server) ForumDocument(e *core.RequestEvent) (Resource, error) {
	actor := s.actor(e)

	groups, err := s.repo.ListGroups()
	if err != nil {
		return Resource{}, err
	}

	actorResource := any(nil)
	if actor.User != nil {
		actorResource = s.userResourceWithGroups(actor.User.ID, actor.User, actor.User.Groups)
	}

	// The theme settings. Upstream carries them on the FORUM DOCUMENT
	// (framework/core/src/Api/Resource/ForumResource.php:104-112), and
	// the frontend reads them from there:
	//
	//	forumDefault ??= app.forum.attribute('colorScheme') ?? 'auto';
	//
	// They are SETTINGS, so they have to come from the settings table —
	// an administrator's choice is what the forum shows, and a hard
	// coded value would make the admin's Appearance page a no-op.
	// The settings that could not be read are simply absent: the forum
	// document then carries upstream's defaults, which is better than a
	// blank document.
	//
	// **THE TITLE IS A SETTING TOO.** The admin's Basics page writes
	// `forum_title`, and the frontend reads it back here as
	// `app.forum.attribute('title')` — it is the `document.title`, the
	// header's home-link text and the `{forumName}` parameter of every
	// translation that takes one. It used to be the literal "Forum", so
	// an administrator could save a name, watch the admin form keep it,
	// and the forum went on showing the old one: a control that changes
	// nothing, which is the failure this file's settings are most prone
	// to.
	settings, err := s.repo.AllSettings()
	if err != nil {
		settings = map[string]any{}
	}

	attributes := map[string]any{
		"title":                   settingTextOr(settings, "forum_title", DefaultForumTitle),
		"description":             "",
		"baseUrl":                 s.basePath,
		"basePath":                s.basePath,
		"baseOrigin":              "",
		"apiUrl":                  s.apiPath,
		"debug":                   false,
		"welcomeTitle":            settingTextOr(settings, "welcome_title", ""),
		"welcomeMessage":          settingTextOr(settings, "welcome_message", ""),
		"showLanguageSelector":    languageSelectorEnabled(settings),
		"showThemeSelector":       false,
		"showPostSource":          false,
		"allowSignUp":             false,
		"defaultRoute":            settingTextOr(settings, "default_route", "/"),
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

		// The theme. `colorScheme` is the one the frontend reads for
		// the default scheme, and the two colours are what the theme
		// CSS is built from.
		"colorScheme":         settingTextOr(settings, "color_scheme", "auto"),
		"themePrimaryColor":   settingTextOr(settings, "theme_primary_color", "#4D698E"),
		"themeSecondaryColor": settingTextOr(settings, "theme_secondary_color", "#4D698E"),

		// The logos. They are settings too (`logo_path` and friends),
		// and the Appearance page's upload buttons write them.
		"logoUrl":         s.assetURL(settingTextOr(settings, "logo_path", "")),
		"logoDarkModeUrl": s.assetURL(settingTextOr(settings, "logo_dark_mode_path", "")),
		"faviconUrl":      s.assetURL(settingTextOr(settings, "favicon_path", "")),
		"emailLogoUrl":    s.assetURL(settingTextOr(settings, "email_logo_path", "")),

		// The custom header and footer.
		"headerHtml": settingTextOr(settings, "custom_header", ""),
		"footerHtml": settingTextOr(settings, "custom_footer", ""),
	}

	relationships := map[string]Relationship{
		"groups": {Data: identifiers(TypeGroup, groupIDs(groups))},
		"actor":  {Data: actorResource},
	}

	resource := Resource{
		Type:          TypeForum,
		ID:            "1",
		Attributes:    attributes,
		Relationships: relationships,
	}

	// The plugins contribute to the FORUM DOCUMENT the same way they
	// contribute to a record. Upstream's
	// `Extend.ApiResource(ForumResource::class)->fields(...)` is what
	// publishes a field on this document, and the Statistics extension
	// uses it for `statisticsEntities` — the list a page lays its widgets
	// out from before any number arrives.
	//
	// This is the BOOT payload's document too (`routes.go` calls this from
	// the shell), which is the copy the SPA actually reads: the SPA reads
	// the forum document out of the boot payload, never out of `GET /api/`.
	actorID := ""
	if actor.User != nil {
		actorID = actor.User.ID
	}
	isAdmin := actor.User != nil && actor.User.IsAdmin
	if err := s.applyPluginAttributes(e.Request.Context(), &resource, actorID, resource.ID, isAdmin); err != nil {
		return Resource{}, err
	}

	return resource, nil
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
