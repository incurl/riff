package api

import (
	"slices"
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
//
// A PLUGIN's own resource types contribute their own default set
// through its resource serializer: the polls extension's poll carries
// `options`, `myVotes` and `myVotes.option`, which is upstream's
// `['options', 'myVotes', 'myVotes.option']`.
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

// includePath is one relation to resolve, as a chain of names.
//
// Upstream's include paths are DOTTED: `firstPost.user.groups` names
// the discussion's first post, that post's author, and that author's
// groups. The chain is resolved hop by hop, and every resource it
// touches travels in the `included` array.
//
// The first element is the relation on the primary resource; the rest
// are relations on the resources that follow.
type includePath []string

// first returns the relation the path starts with.
func (p includePath) first() string { return p[0] }

// rest returns the path's remaining hops.
func (p includePath) rest() includePath { return p[1:] }

// includedResources builds the `included` array for a set of primary
// resources.
//
// The relations are the DEFAULT set for the resource type, plus
// whatever the request asked for with `include`.
//
// The resources are DEDUPLICATED: a page of discussions by the same
// author carries that author once, and the JSON:API contract says the
// `included` array holds each resource once.
//
// The walk is RECURSIVE, because a relation may name a relation: the
// discussion's `firstPost` is a post, and the post's `user` is an
// account. Upstream's `firstPost.user.groups` is a three-deep chain, and
// the frontend needs every link of it in the response.
func (s *Server) includedResources(e *core.RequestEvent, primaries []Resource) ([]Resource, error) {
	requested := requestedIncludes(e)

	// The caller and its permission checkers are resolved ONCE.
	//
	// `session.Resolver.Resolve` verifies a JWT and reads the account and
	// its groups, and `permissionChecker` closes over the domain actor —
	// so resolving them per RESOURCE is an N+1 on a page of posts.
	walk := &includeWalk{
		server:    s,
		event:     e,
		seen:      map[string]struct{}{},
		users:     map[string]*domain.User{},
		actorID:   s.actor(e).ID(),
		isAdmin:   s.actor(e).IsAdmin(),
		can:       s.permissionChecker(e),
		canScoped: s.permissionCheckerScoped(e),
	}

	for _, primary := range primaries {
		for _, path := range s.relationPaths(primary.Type, requested) {
			if err := walk.includePath(primary, path, 0); err != nil {
				return nil, err
			}
		}
	}

	return walk.out, nil
}

// includeWalk is the per-request state of an `included` walk.
//
// The caller is resolved once, and the resources already emitted are
// remembered, because every hop of every relation chain shares them.
type includeWalk struct {
	// server is the API server.
	server *Server
	// event is the request.
	event *core.RequestEvent
	// seen holds the `type:id` of every resource already emitted.
	seen map[string]struct{}
	// users caches the accounts already loaded, so a page of posts by
	// one author reads that author once.
	users map[string]*domain.User
	// actorID is the caller's id, empty for a guest.
	actorID string
	// isAdmin reports whether the caller administers the forum.
	isAdmin bool
	// can answers the ordinary permission question for the caller.
	can func(action, subjectType, subjectID string) bool
	// canScoped answers the scoped one.
	canScoped func(action, subjectType, subjectID string) bool
	// out is the accumulated `included` array.
	out []Resource
}

// maxIncludeDepth bounds the recursion.
//
// A relation may name a relation, and nothing stops a plugin from
// declaring a cycle (`a` → `b` → `a`). The `seen` set stops the walk
// from REPEATING a resource, but a cycle would still recurse once per
// hop before the set caught it — and a cycle among relations that name
// each other but are never the same resource would not be caught at
// all. The depth bound makes the walk total.
//
// It is generous enough for upstream's own chains: `firstPost.user.groups`
// is three hops deep.
const maxIncludeDepth = 8

// includePath resolves one include chain and recurses into it.
//
// The first hop's relation is read off the primary resource, and every
// following hop is read off the resource the previous hop named.
//
// The resources already emitted and the accounts already loaded ride on
// the walk, because every hop of every chain shares them.
func (w *includeWalk) includePath(primary Resource, path includePath, depth int) error {
	if depth > maxIncludeDepth || len(path) == 0 {
		return nil
	}

	relation := path.first()
	rest := path.rest()

	for _, identifier := range relatedIdentifiers(primary, relation) {
		if identifier.ID == "" {
			continue
		}

		// The RELATED type is what decides how the identifier is
		// resolved, not the type of the resource that carries the
		// relation: the post's `polls` relation names a `polls`
		// resource, and the poll's `options` relation names a
		// `poll_options` one.
		//
		// The identifier carries its own type, and it is authoritative:
		// the relation's `data` says what it points at. It falls back
		// to the source type only when it names none — which is the
		// case for a relation this port builds by hand.
		relatedType := identifier.Type
		if relatedType == "" {
			relatedType = primary.Type
		}

		resources, ok, err := w.relatedResources(relatedType, identifier.ID)
		if err != nil {
			return err
		}
		if !ok {
			// A relation that names a row which is gone is simply
			// absent: the response still stands.
			continue
		}

		for _, resource := range resources {
			key := resource.Type + ":" + resource.ID
			if _, done := w.seen[key]; !done {
				w.seen[key] = struct{}{}
				w.out = append(w.out, resource)
			}

			// The rest of the chain is resolved against the resource
			// this hop named, so `firstPost.user.groups` reaches the
			// author's groups.
			if len(rest) == 0 {
				continue
			}
			if err := w.includePath(resource, rest, depth+1); err != nil {
				return err
			}
		}
	}

	return nil
}

// relatedResources builds the resource an identifier names.
//
// **THE DISPATCH IS ON THE RELATED TYPE, NOT ON THE SOURCE TYPE.** A
// relation's `data` says what it points at, and the identifier carries
// that type. Keying the switch on the SOURCE type instead meant only
// the relations the switch happened to enumerate could ever resolve —
// the post's `polls` relation (a plugin's) and a user's `groups` (a
// to-many relation the old reader never reached) both fell through to
// nothing.
//
// The second answer is false when the relation names something that is
// gone: a missing relation is not a failed request, the response still
// stands without it.
func (w *includeWalk) relatedResources(relatedType, identifier string) ([]Resource, bool, error) {
	s := w.server

	switch relatedType {
	case TypeUser:
		user, err := s.relatedUser(identifier, w.users)
		if err != nil || user == nil {
			return nil, false, err
		}
		return []Resource{s.userResourceWithGroups(w.actorID, user, user.Groups)}, true, nil
	case TypePost:
		post, err := s.repo.FindPost(identifier)
		if err != nil {
			// A post that is gone is simply absent.
			return nil, false, nil //nolint:nilerr // a missing relation is not a failed request
		}
		resource, err := s.postResource(w.event, post)
		if err != nil {
			return nil, false, err
		}
		return []Resource{resource}, true, nil
	case TypeDiscussion:
		discussion, err := s.repo.FindDiscussion(identifier)
		if err != nil {
			// A discussion that is gone is simply absent.
			return nil, false, nil //nolint:nilerr // a missing relation is not a failed request
		}
		return []Resource{discussionResource(discussion, false, false, false, false, false, nil)}, true, nil
	case TypeGroup:
		group, err := s.repo.FindGroup(identifier)
		if err != nil {
			// A group that is gone is simply absent.
			return nil, false, nil //nolint:nilerr // a missing relation is not a failed request
		}
		return []Resource{groupResource(group)}, true, nil
	}

	// A PLUGIN's own resource type: the walk asks the plugin to render
	// it. This is what makes the polls extension's `polls`,
	// `poll_options` and `poll_votes` travel in the `included` array.
	if serializer, ok := s.resourceSerializers[relatedType]; ok {
		resource, err := serializer.Render(w.event.Request.Context(), ResourceRequest{
			ActorID:   w.actorID,
			ID:        identifier,
			IsAdmin:   w.isAdmin,
			Can:       w.can,
			CanScoped: w.canScoped,
		})
		if err != nil {
			return nil, false, err
		}
		if resource == nil {
			// The plugin says the resource is not there, or that the
			// caller may not see it: it is simply absent.
			return nil, false, nil
		}
		return []Resource{*resource}, true, nil
	}

	// A resource type this port does not render.
	return nil, false, nil
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

// relationPaths returns the include chains to resolve for a resource.
//
// It is the DEFAULT set for the type, plus whatever the request asked
// for. A relation the resource does not carry is dropped, so an
// `include` of a relation this port does not have is ignored rather
// than answered with nothing.
//
// A NESTED name (`firstPost.user.groups`) is one chain, and its first
// hop is the relation on the resource itself.
func (s *Server) relationPaths(resourceType string, requested []string) []includePath {
	defaults := defaultIncludesFor(resourceType)
	defaults = append(defaults, s.defaultIncludes[resourceType]...)

	out := make([]includePath, 0, len(defaults)+len(requested))

	for _, name := range defaults {
		// A default include is a PATH like a requested one: the polls
		// extension declares `polls.options` and `polls.myVotes.option`,
		// and a path taken as one hop names a relation that does not
		// exist.
		hops := strings.Split(strings.TrimSpace(name), ".")
		if len(hops) == 0 || hops[0] == "" {
			continue
		}
		out = append(out, includePath(hops))
	}

	for _, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		hops := strings.Split(name, ".")
		if len(hops) == 0 {
			continue
		}

		// The chain's FIRST hop must be a relation the resource
		// carries; a chain that starts with an unknown relation names
		// nothing.
		if !s.relationKnown(resourceType, hops[0]) {
			continue
		}

		out = append(out, includePath(hops))
	}

	return out
}

// relationKnown reports whether a resource type carries a relation.
//
// It is the set the walk can follow: the core's own relations, and a
// plugin's own — a plugin contributes a relation through the same
// extender the resource serializer uses.
func (s *Server) relationKnown(resourceType, relation string) bool {
	switch resourceType {
	case TypeDiscussion:
		switch relation {
		case "user", "lastPostedUser", "firstPost", "lastPost":
			return true
		default:
			// A relation the discussion does not carry.
		}
	case TypePost:
		switch relation {
		case "user", "discussion":
			return true
		default:
			// A relation the post does not carry.
		}
	case TypeUser:
		if relation == "groups" {
			return true
		}
	default:
		// A resource type with no core relations of its own.
	}

	// A plugin's own relation, on a core resource or on a plugin's own
	// resource.
	return slices.Contains(s.pluginRelationships[resourceType], relation)
}

// relatedIdentifiers reads the identifiers a relation names.
//
// A relation's `data` is an identifier object for a to-one relation and
// an ARRAY of them for a to-many one, and a relation with no data names
// nothing.
//
// **A TO-MANY RELATION MUST BE READ AS A LIST.** Reading a single
// identifier out of a to-many relation's data never matches — the data
// is an array — so the relation is never followed and the resources it
// names never travel in the `included` array. A user's `groups` is
// exactly that shape, and it was silently absent: the frontend reads
// the absence as "no groups", so the account renders without a badge.
func relatedIdentifiers(primary Resource, relation string) []Identifier {
	related, ok := primary.Relationships[relation]
	if !ok {
		return nil
	}

	switch data := related.Data.(type) {
	case Identifier:
		return []Identifier{data}
	case *Identifier:
		if data == nil {
			return nil
		}
		return []Identifier{*data}
	case []Identifier:
		return data
	case []any:
		out := make([]Identifier, 0, len(data))
		for _, item := range data {
			switch typed := item.(type) {
			case Identifier:
				out = append(out, typed)
			case map[string]any:
				out = append(out, identifierFromMap(typed))
			case string:
				out = append(out, Identifier{ID: typed})
			default:
				// An item that is not an identifier names nothing.
			}
		}
		return out
	case map[string]any:
		identifier := identifierFromMap(data)
		if identifier.ID == "" {
			return nil
		}
		return []Identifier{identifier}
	case string:
		if data == "" {
			return nil
		}
		return []Identifier{{ID: data}}
	default:
		// A relation whose data is something else names nothing.
	}

	return nil
}

// identifierFromMap reads an identifier out of a decoded JSON object.
//
// The object arrives as `map[string]any` when it came off the wire
// rather than out of this package's own resource builder, and a field
// that is missing or is not a string names nothing.
func identifierFromMap(decoded map[string]any) Identifier {
	id, ok := decoded["id"].(string)
	if !ok {
		return Identifier{}
	}

	// A missing or non-string type names nothing, so the zero value is
	// the right answer: the walk falls back to the source type.
	resourceType, isString := decoded["type"].(string)
	if !isString {
		return Identifier{ID: id}
	}

	return Identifier{Type: resourceType, ID: id}
}
