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

// lookup resolves the permission rows for an actor.
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
	if unscopedAllow {
		return true, nil
	}
	_ = unscopedDeny
	return false, nil
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
