package api

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The `included` array of a response.
//
// The frontend resolves a relationship through the STORE, not through
// the identifier: `Model.hasOne` is
// `this.store.getById(relationshipData.type, relationshipData.id)`, and
// the store is filled from `payload.included`:
//
//	pushPayload(payload) {
//	  if (payload.included) payload.included.map(this.pushObject.bind(this));
//	  ...
//	}
//
// So a relationship identifier alone is NOT enough. The related
// RESOURCE has to travel with the response, or the relation reads as
// absent: `getById` returns undefined and the frontend renders a
// blank author, a missing avatar, an empty excerpt.
//
// Upstream sends those resources, and it sends them by default:
//
//	DiscussionResource.php:101  defaultInclude(['user', 'user.groups',
//	                                            'lastPostedUser', ...])
//	DiscussionResource.php:87   defaultInclude(['user', 'user.groups',
//	                                            'firstPost', ...])
//	PostResource.php:147        defaultInclude(['user', 'discussion',
//	                                            'discussion.lastPostedUser'])
//
// and the frontend asks for the same thing explicitly — the discussion
// list sends `include: ['user', 'lastPostedUser']`
// (framework/core/js/src/forum/states/DiscussionListState.ts:32).
//
// The default set is what this port sends. It is upstream's set minus
// the relations this port has no equivalent for (`mostRelevantPost` is
// upstream's search-relevance relation, and `user.groups` is carried by
// the user resource's own `groups` relationship).

// defaultIncludesFor returns the relations a resource type carries by
// default.
//
// The sets are upstream's `defaultInclude` lists, minus the relations
// this port does not have:
//
//   - `mostRelevantPost` is upstream's search-relevance relation: it
//     names the post that best matches the search term. This port has
//     no equivalent, and the frontend falls back to `firstPost()`.
//   - `user.groups` is not a separate resource here: the user resource
//     carries its `groups` relationship itself, and the group
//     RESOURCES are what the frontend needs to render a badge.
func defaultIncludesFor(resourceType string) []string {
	switch resourceType {
	case TypeDiscussion:
		return []string{"user", "lastPostedUser", "firstPost", "lastPost"}
	case TypePost:
		return []string{"user", "discussion"}
	case TypeUser:
		return []string{"groups"}
	default:
		return nil
	}
}

// includedResources builds the `included` array for a set of primary
// resources.
//
// The relations are the DEFAULT set for the resource type, plus
// whatever the request asked for with `include`.
//
// The resources are DEDUPLICATED: a page of discussions by the same
// author carries that author once, and the JSON:API contract says the
// `included` array holds each resource once.
func (s *Server) includedResources(e *core.RequestEvent, primaries []Resource) ([]Resource, error) {
	requested := requestedIncludes(e)

	seen := map[string]struct{}{}
	out := []Resource{}

	// The users first: a page of posts by the same author needs that
	// author once, and the users are the relation EVERY resource type
	// carries.
	users := map[string]*domain.User{}

	for _, primary := range primaries {
		for _, relation := range relationNames(primary.Type, requested) {
			identifier := relatedIdentifier(primary, relation)
			if identifier == "" {
				continue
			}
			if _, done := seen[identifier]; done {
				continue
			}

			resources, ok, err := s.relatedResources(e, primary.Type, relation, identifier, users)
			if err != nil {
				return nil, err
			}
			if !ok {
				// A relation that names a row which is gone is simply
				// absent: the response still stands.
				continue
			}

			for _, resource := range resources {
				key := resource.Type + ":" + resource.ID
				if _, done := seen[key]; done {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, resource)
			}
		}
	}

	return out, nil
}

// relatedUser loads a user, reusing the ones already loaded.
func (s *Server) relatedUser(id string, cache map[string]*domain.User) (*domain.User, error) {
	if user, ok := cache[id]; ok {
		return user, nil
	}

	user, err := s.repo.UserByID(id)
	if err != nil {
		// A relation that names an account which is gone is simply
		// absent: the response still stands. The error is not returned
		// because a MISSING relation is not a failed request — the
		// resource is still there, it just has no related account to
		// point at.
		return nil, nil //nolint:nilerr // a missing relation is not a failed request
	}
	cache[id] = user
	return user, nil
}

// relatedResources builds the resources a relation names.
//
// The second answer is false when the relation names something that is
// gone: a missing relation is not a failed request, the response still
// stands without it.
//
// A relation can name MORE THAN ONE resource — a user's groups — so
// this returns a list.
func (s *Server) relatedResources(e *core.RequestEvent, resourceType, relation, identifier string, users map[string]*domain.User) ([]Resource, bool, error) {
	switch resourceType {
	case TypeDiscussion:
		switch relation {
		case "user", "lastPostedUser":
			user, err := s.relatedUser(identifier, users)
			if err != nil || user == nil {
				return nil, false, err
			}
			return []Resource{s.userResourceWithGroups(s.actor(e).ID(), user, user.Groups)}, true, nil
		case "firstPost", "lastPost":
			post, err := s.repo.FindPost(identifier)
			if err != nil {
				// A post that is gone is simply absent.
				return nil, false, nil //nolint:nilerr // a missing relation is not a failed request
			}
			resource, err := s.postResource(e, post)
			if err != nil {
				return nil, false, err
			}
			return []Resource{resource}, true, nil
		default:
			// A relation this resource type does not carry.
		}
	case TypePost:
		switch relation {
		case "user":
			user, err := s.relatedUser(identifier, users)
			if err != nil || user == nil {
				return nil, false, err
			}
			return []Resource{s.userResourceWithGroups(s.actor(e).ID(), user, user.Groups)}, true, nil
		case "discussion":
			discussion, err := s.repo.FindDiscussion(identifier)
			if err != nil {
				// A discussion that is gone is simply absent.
				return nil, false, nil //nolint:nilerr // a missing relation is not a failed request
			}
			return []Resource{discussionResource(discussion, false, false, false, false, false, nil)}, true, nil
		default:
			// A relation this resource type does not carry.
		}
	case TypeUser:
		if relation == "groups" {
			groupIDs, err := s.repo.UserGroupIDs(identifier)
			if err != nil {
				// A user whose groups cannot be read carries none.
				return nil, false, nil //nolint:nilerr // a missing relation is not a failed request
			}
			// A user with several groups carries each of them, so this
			// is the one relation that can name more than one resource.
			resources, err := s.groupResources(groupIDs)
			if err != nil {
				return nil, false, err
			}
			if len(resources) == 0 {
				return nil, false, nil
			}
			return resources, true, nil
		}
	default:
		// A resource type this port does not relate.
	}

	// A relation this resource type does not carry.
	return nil, false, nil
}

// groupResources builds the resources for a set of group ids.
func (s *Server) groupResources(groupIDs []string) ([]Resource, error) {
	out := make([]Resource, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		group, err := s.repo.FindGroup(groupID)
		if err != nil {
			continue
		}
		out = append(out, groupResource(group))
	}
	return out, nil
}

// requestedIncludes reads the `include` query parameter.
//
// The frontend sends a comma-separated list (`include=user,discussion`)
// and Mithril's query serialiser turns an array into the same thing
// (`include[]=user&include[]=discussion`), so both are read.
func requestedIncludes(e *core.RequestEvent) []string {
	if e == nil {
		return nil
	}

	query := e.Request.URL.Query()

	var names []string
	for _, raw := range query["include"] {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// relationNames returns the relations to resolve for a resource.
//
// It is the DEFAULT set for the type, plus whatever the request asked
// for. A relation the resource does not carry is dropped, so an
// `include` of a relation this port does not have is ignored rather
// than answered with nothing.
func relationNames(resourceType string, requested []string) []string {
	known := map[string]bool{}
	for _, name := range defaultIncludesFor(resourceType) {
		known[name] = true
	}

	out := make([]string, 0, len(known)+len(requested))
	out = append(out, defaultIncludesFor(resourceType)...)

	for _, name := range requested {
		if known[name] {
			continue
		}
		known[name] = true
		out = append(out, name)
	}

	return out
}

// relatedIdentifier reads the identifier a relation names.
//
// The relation's `data` is an identifier object, and a relation with no
// data (an empty one) names nothing.
//
// The identifier is a [Identifier] when the resource was built by this
// package and a plain map when it came from somewhere else (a plugin's
// own relationship), so both are read.
func relatedIdentifier(primary Resource, relation string) string {
	related, ok := primary.Relationships[relation]
	if !ok {
		return ""
	}

	switch data := related.Data.(type) {
	case Identifier:
		return data.ID
	case *Identifier:
		if data == nil {
			return ""
		}
		return data.ID
	case map[string]any:
		if id, ok := data["id"].(string); ok {
			return id
		}
	case string:
		return data
	default:
		// A relation whose data is something else names nothing.
	}

	return ""
}
