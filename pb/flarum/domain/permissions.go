package domain

import (
	"context"
	"strings"
)

// PermissionChecker answers whether an actor may perform an action.
//
// It is an interface so a plugin can replace it wholesale through the
// container — the Go equivalent of Flarum's policy classes.
type PermissionChecker interface {
	// Can reports whether the actor may perform the action.
	Can(ctx context.Context, actor Actor, action, subjectType, subjectID string) (bool, error)

	// CanAsModerator reports whether the actor holds a permission that
	// was granted to a group other than the implicit member group.
	//
	// See [PermissionService.CanAsModerator] for why the distinction
	// matters.
	CanAsModerator(ctx context.Context, actor Actor, action, subjectType, subjectID string) (bool, error)

	// CanScoped reports whether the actor holds a permission for the
	// subject ALONE.
	//
	// It is upstream's restricted-tag question, and upstream's answer is
	// an EXACT MATCH: `User::hasPermission('tag5.startDiscussion')` is
	// `in_array('tag5.startDiscussion', $groups' permissions)`, so a group
	// that holds the GLOBAL `startDiscussion` does NOT satisfy it.
	//
	// The global grant is not consulted, because a restricted tag that
	// inherits the global grant restricts nobody. This is what upstream's
	// `TagPolicy` does for a restricted tag: it returns the scoped
	// answer ALONE, and the global fallback in `Gate::allows()` is never
	// reached — the policy COVERED the question.
	//
	// A subject with no scoped rule of its own is governed by the global
	// grant, so this answers the same as [PermissionChecker.Can] for it.
	CanScoped(ctx context.Context, actor Actor, action, subjectType, subjectID string) (bool, error)
}

// Actor is the caller a permission check runs for.
//
// It is the domain's own view of a caller: it carries the user's id and
// whether the caller is an administrator, without depending on the
// session or repository packages.
type Actor struct {
	// UserID is the caller's id, empty for an anonymous caller.
	UserID string
	// IsAdmin reports whether the caller administers the forum.
	IsAdmin bool
	// GroupIDs holds the groups the caller belongs to.
	GroupIDs []string
	// Suspended reports whether the caller's participation is revoked.
	//
	// A suspended account keeps its identity (it can sign in and read the
	// forum) but is treated as a guest for every permission check, which
	// is upstream's `flarum/suspend` rule.
	Suspended bool
}

// IsAuthenticated reports whether the caller is signed in.
func (a Actor) IsAuthenticated() bool { return a.UserID != "" }

// ActorFor builds a domain actor.
func ActorFor(userID string, isAdmin bool, groupIDs []string) Actor {
	return Actor{UserID: userID, IsAdmin: isAdmin, GroupIDs: groupIDs}
}

// SuspendedActor returns an actor whose groups are revoked.
//
// It is upstream's `flarum/suspend` behaviour: its
// `RevokeAccessFromSuspendedUsers` swaps a suspended account's groups for
// the guest group on every permission check. The account stays signed in
// (it can read the forum and see why it was suspended), but it holds no
// participation powers.
func SuspendedActor(userID string, isAdmin bool) Actor {
	return Actor{UserID: userID, IsAdmin: isAdmin, Suspended: true}
}

// PermissionRow is one permission rule.
type PermissionRow struct {
	// Group is the group the rule belongs to.
	Group string
	// Permission is the action the rule grants or denies.
	Permission string
	// Scope narrows the rule to one subject; empty means every subject.
	Scope string
	// Allowed reports whether the group is allowed.
	Allowed bool
}

// PermissionStore reads the permission rules.
//
// The domain declares the interface and the repository implements it, so
// the domain stays independent of the data layer and a plugin can supply
// its own store.
type PermissionStore interface {
	// PermissionRowsFor returns the rules that apply to a set of
	// groups for one action.
	PermissionRowsFor(groupIDs []string, action string) ([]PermissionRow, error)
}

// EventDispatcher dispatches a domain event.
//
// It is the narrow interface the domain needs, so the domain package
// does not depend on the core package.
type EventDispatcher interface {
	// DispatchAfter notifies the observers of a completed action.
	DispatchAfter(ctx context.Context, event Event)
}

// Event is the domain's event payload.
//
// It mirrors the core event bus's event so the two stay compatible
// without the domain importing the core package.
type Event struct {
	// Name is the event name.
	Name string
	// Actor is the acting user's id.
	Actor string
	// Target identifies what the action was performed on.
	Target string
	// Data carries the event-specific payload.
	Data map[string]any
}

// PermissionService resolves permissions from the forum's permission
// table.
//
// The model is Flarum's: a permission row names a group, an action and
// an optional scope, and says whether the group is allowed. A caller
// holds the permissions of every group they belong to, plus the
// implicit guest/member groups.
//
// An administrator holds every permission.
type PermissionService struct {
	// store reads the permission rules.
	store PermissionStore
	// bus is the event dispatcher, for the permission-checked event.
	bus EventDispatcher
}

// NewPermissionService returns a permission service.
func NewPermissionService(store PermissionStore, bus EventDispatcher) *PermissionService {
	return &PermissionService{store: store, bus: bus}
}

// Can reports whether the actor may perform the action.
//
// The subject scope narrows the check: a permission row may name a
// scope (a tag id, for instance), and a row with an empty scope applies
// to every subject of that type.
func (s *PermissionService) Can(ctx context.Context, actor Actor, action, subjectType, subjectID string) (bool, error) {
	if actor.IsAdmin {
		return true, nil
	}

	allowed, err := s.lookup(actor, action, subjectID)
	if err != nil {
		return false, err
	}

	s.dispatchChecked(ctx, actor, action, subjectType, subjectID, allowed)

	return allowed, nil
}

// dispatchChecked notifies the observers of a permission check.
//
// It is shared by every check form, so a listener sees the SCOPED checks
// as well as the global ones — otherwise an audit plugin would watch the
// permissions an administrator grants while missing the ones a
// restricted tag actually decides.
func (s *PermissionService) dispatchChecked(ctx context.Context, actor Actor, action, subjectType, subjectID string, allowed bool) {
	if s.bus == nil {
		return
	}

	s.bus.DispatchAfter(ctx, Event{
		Name:   "forum.permission.checked",
		Actor:  actor.UserID,
		Target: action,
		Data: map[string]any{
			"subjectType": subjectType,
			"subjectID":   subjectID,
			"allowed":     allowed,
		},
	})
}

// CanAsModerator reports whether the actor holds a permission that was
// granted to a group OTHER than the implicit member group.
//
// The permission rows are GROUP-scoped: a row says "the member group
// may edit posts", not "the member group may edit their own posts". A
// row for the implicit member group therefore carries no information
// about the individual — every signed-in caller holds it — so a check
// that relies on it alone would let any member perform the action on
// any subject.
//
// A row for another group (an explicit moderator group) does name a
// set of accounts, so it can carry the moderator's authority. An
// administrator holds every permission outright and never needs a row.
//
// This is the rule for the actions whose subject the caller owns: the
// author may always edit their own post, and anyone else needs a
// moderator grant.
func (s *PermissionService) CanAsModerator(ctx context.Context, actor Actor, action, subjectType, subjectID string) (bool, error) {
	if actor.IsAdmin {
		return true, nil
	}

	// The rows the caller's EXPLICIT groups hold. The implicit member
	// group is deliberately not among them.
	rows, err := s.store.PermissionRowsFor(actor.GroupIDs, action)
	if err != nil {
		return false, err
	}

	allowed := false
	for _, row := range rows {
		if !row.Allowed {
			continue
		}
		if row.Scope == "" || row.Scope == subjectID {
			allowed = true
			break
		}
	}

	if s.bus != nil {
		s.bus.DispatchAfter(ctx, Event{
			Name:   "forum.permission.checked",
			Actor:  actor.UserID,
			Target: action,
			Data: map[string]any{
				"subjectType": subjectType,
				"subjectID":   subjectID,
				"allowed":     allowed,
			},
		})
	}

	return allowed, nil
}

// CanScoped reports whether the actor holds a permission for the subject
// ALONE.
//
// It is upstream's restricted-tag question. Upstream's `User::hasPermission`
// is an EXACT STRING MATCH against the group's permission rows, and the
// scope is part of the permission STRING (upstream's `group_permission`
// table has no scope column — a restricted tag's grant is the literal row
// `tag5.startDiscussion`). So a group holding the GLOBAL
// `startDiscussion` does NOT satisfy a check for `tag5.startDiscussion`.
//
// The forum keeps the scope in its own column and rebuilds the scoped
// name from it, which is the same thing with the split made explicit.
//
// There is NO global fallback, and that is the whole point. Upstream's
// `TagPolicy` returns the scoped answer ALONE for a restricted tag:
//
//	if ($tag->is_restricted) {
//	    return $actor->hasPermission("tag{$id}.{$ability}");
//	}
//
// and the global fallback in `Gate::allows()` is never reached — the
// policy COVERED the question. A fallback here would open every
// restricted tag to every member, because the member group holds
// `startDiscussion` globally — and a restricted tag that restricts
// nobody is worse than no restricted tag at all, because the operator
// believes it is closed.
//
// An administrator holds every permission outright, as upstream's
// `hasPermission` does.
func (s *PermissionService) CanScoped(ctx context.Context, actor Actor, action, subjectType, subjectID string) (bool, error) {
	if actor.IsAdmin {
		return true, nil
	}

	groupIDs := actor.groupSet()
	if len(groupIDs) == 0 {
		return false, nil
	}

	rows, err := s.store.PermissionRowsFor(groupIDs, action)
	if err != nil {
		return false, err
	}

	// The subject's own rules, and only them.
	allowed := false
	for _, row := range rows {
		if row.Scope == subjectID && row.Allowed {
			allowed = true

			break
		}
	}

	s.dispatchChecked(ctx, actor, action, subjectType, subjectID, allowed)

	return allowed, nil
}

// lookup resolves the permission rows for an actor.
//
// The model is upstream's: a permission row names a group, an action and
// an optional scope, and a row with an empty scope applies to every
// subject of that type.
//
// A scoped rule applies to its subject only; an unscoped one applies
// to every subject. A scoped allow beats an unscoped deny, which is
// what makes a restricted tag work: the tag grants access to the
// groups that may see it, and the global deny does not override that.
//
// A subject that HAS a scoped rule is governed by it ALONE — the global
// grant is not consulted. That is upstream's exact match (see
// [PermissionService.CanScoped]), and it is what makes a restricted tag
// restrict: the member group holds `startDiscussion` globally, so a
// fallback would open every restricted tag to every member.
func (s *PermissionService) lookup(actor Actor, action, subjectID string) (bool, error) {
	groupIDs := actor.groupSet()
	if len(groupIDs) == 0 {
		return false, nil
	}

	rows, err := s.store.PermissionRowsFor(groupIDs, action)
	if err != nil {
		return false, err
	}

	// A scoped rule applies to its subject only; an unscoped one applies
	// to every subject. A scoped allow beats an unscoped deny, which is
	// what makes a restricted tag work: the tag grants access to the
	// groups that may see it, and the global deny does not override
	// that.
	scopedAllow := false
	unscopedAllow := false
	unscopedDeny := false
	for _, row := range rows {
		switch {
		case row.Scope == "":
			if row.Allowed {
				unscopedAllow = true
			} else {
				unscopedDeny = true
			}
		case row.Scope == subjectID && row.Allowed:
			scopedAllow = true
		default:
			// A rule scoped to another subject says nothing about this
			// one.
		}
	}

	if scopedAllow {
		return true, nil
	}

	// The subject has its own rules, and they answer the question on
	// their own.
	if subjectID != "" && hasScopedRule(rows, subjectID) {
		return false, nil
	}

	if unscopedAllow {
		return true, nil
	}
	_ = unscopedDeny
	return false, nil
}

// hasScopedRule reports whether the rows carry a rule for a subject.
//
// It is the question "is this subject governed by its own rules?", and
// the answer decides whether the global grant applies to it.
func hasScopedRule(rows []PermissionRow, subjectID string) bool {
	for _, row := range rows {
		if row.Scope == subjectID {
			return true
		}
	}

	return false
}

// groupSet returns the actor's effective groups.
//
// The implicit groups are added: an anonymous caller is a guest, a
// signed-in caller is a member as well.
func (a Actor) groupSet() []string {
	seen := map[string]struct{}{}
	groups := make([]string, 0, len(a.GroupIDs)+2)

	add := func(id string) {
		if id == "" {
			return
		}
		if _, duplicate := seen[id]; duplicate {
			return
		}
		seen[id] = struct{}{}
		groups = append(groups, id)
	}

	if !a.IsAuthenticated() || a.Suspended {
		// A suspended account is treated exactly as an anonymous one, which
		// is what upstream's `RevokeAccessFromSuspendedUsers` does: it
		// returns the guest group instead of the account's own.
		add(GroupGuestID)
		return groups
	}

	add(GroupMemberID)
	for _, id := range a.GroupIDs {
		add(id)
	}
	return groups
}

// EffectiveGroups returns the actor's effective group ids.
//
// It is exported so the API layer can build the same group set for a
// permission check without duplicating the implicit-group rule.
func (a Actor) EffectiveGroups() []string { return a.groupSet() }

// NormaliseScope trims a permission scope.
func NormaliseScope(scope string) string { return strings.TrimSpace(scope) }
