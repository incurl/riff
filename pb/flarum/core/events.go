package core

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/pkg/errors"
)

// Event names dispatched by the forum core.
//
// They mirror Flarum's event vocabulary, restricted to the v1 scope.
const (
	// EventUserRegistered fires after a user account is created.
	EventUserRegistered = "forum.user.registered"
	// EventUserUpdated fires after a user account is changed.
	EventUserUpdated = "forum.user.updated"
	// EventDiscussionCreated fires after a discussion is created.
	EventDiscussionCreated = "forum.discussion.created"
	// EventDiscussionLocked fires after a discussion is locked or unlocked.
	EventDiscussionLocked = "forum.discussion.locked"
	// EventDiscussionHidden fires after a discussion is hidden or restored.
	EventDiscussionHidden = "forum.discussion.hidden"
	// EventPostCreated fires after a post is created.
	EventPostCreated = "forum.post.created"
	// EventPostEdited fires after a post is edited.
	EventPostEdited = "forum.post.edited"
	// EventPostDeleted fires after a post is hidden or deleted.
	EventPostDeleted = "forum.post.deleted"
	// EventDiscussionTagged fires after a tag is attached to a discussion.
	EventDiscussionTagged = "forum.discussion.tagged"
	// EventDiscussionUntagged fires after a tag is removed from a discussion.
	EventDiscussionUntagged = "forum.discussion.untagged"
	// EventPostLiked fires after a post is liked.
	EventPostLiked = "forum.post.liked"
	// EventPostUnliked fires after a post is unliked.
	EventPostUnliked = "forum.post.unliked"
	// EventPermissionChecked fires after a permission check resolves.
	EventPermissionChecked = "forum.permission.checked"
	// EventSettingsSaved fires after settings are written.
	EventSettingsSaved = "forum.settings.saved"
	// EventPluginBooted fires after a plugin's Boot phase completes.
	EventPluginBooted = "forum.plugin.booted"
)

// Event is the payload delivered to listeners.
//
// It is intentionally a small, stable struct: plugins must not depend on
// core internals beyond this contract.
type Event struct {
	// Name is the event name, one of the Event* constants.
	Name string
	// Actor is the user the action was performed by, if any. Empty for
	// system actions.
	Actor string
	// Target identifies what the action was performed on, e.g. the id
	// of a discussion.
	Target string
	// Data carries the event-specific payload. The keys depend on the
	// event name and are documented with each dispatch site.
	Data map[string]any
}

// BeforeHandler runs before an action and may alter it.
//
// The handler receives a mutable copy of the event context. Returning an
// error interrupts the flow: the action is aborted and the error is
// returned to the caller.
type BeforeHandler func(ctx context.Context, event *Event) error

// AfterHandler observes a completed action.
//
// It is read-only on the committed result: mutating the event has no
// effect on the action that already happened.
type AfterHandler func(ctx context.Context, event Event)

// listener is one registered handler.
type listener struct {
	// name is the event name the listener is registered for.
	name string
	// owner is the plugin ID that registered the listener, or "" for a
	// core listener. It is what makes a failure attributable.
	owner string
	// priority orders listeners of the same event; lower runs first.
	priority int
	// before is the interceptor, if any.
	before BeforeHandler
	// after is the notification handler, if any.
	after AfterHandler
	// order is the registration sequence number.
	order uint64
}

// EventBus dispatches events to registered listeners.
//
// It is safe for concurrent use.
type EventBus struct {
	mu        sync.RWMutex
	listeners []*listener
	order     uint64
	logger    *slog.Logger
}

// NewEventBus returns a bus that logs through the given logger.
//
// A nil logger falls back to the default slog logger.
func NewEventBus(logger *slog.Logger) *EventBus {
	if logger == nil {
		logger = slog.Default()
	}
	return &EventBus{logger: logger}
}

// SubscribeBefore registers an interceptor for an event.
//
// The interceptor runs before the action and may abort it by returning an
// error. owner is the plugin ID (empty for core).
func (b *EventBus) SubscribeBefore(name, owner string, priority int, handler BeforeHandler) {
	if handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order++
	b.listeners = append(b.listeners, &listener{
		name: name, owner: owner, priority: priority,
		before: handler, order: b.order,
	})
}

// SubscribeAfter registers a notification handler for an event.
//
// owner is the plugin ID (empty for core).
func (b *EventBus) SubscribeAfter(name, owner string, priority int, handler AfterHandler) {
	if handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order++
	b.listeners = append(b.listeners, &listener{
		name: name, owner: owner, priority: priority,
		after: handler, order: b.order,
	})
}

// Unsubscribe removes every listener registered by a plugin.
//
// It is called when a plugin is disabled or shut down, so a disabled
// plugin leaves no active listener behind.
func (b *EventBus) Unsubscribe(owner string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.listeners[:0]
	for _, l := range b.listeners {
		if l.owner == owner {
			continue
		}
		kept = append(kept, l)
	}
	b.listeners = kept
}

// DispatchBefore runs the interceptors for an event.
//
// The event is mutated in place, so a handler's changes are visible to
// the next handler and to the action that follows. The first handler that
// returns an error aborts the dispatch and the error is returned.
//
// A handler that panics is recovered and logged: a plugin's bug must not
// take the forum down.
func (b *EventBus) DispatchBefore(ctx context.Context, event *Event) error {
	if event == nil {
		return nil
	}
	for _, l := range b.snapshot(event.Name) {
		if l.before == nil {
			continue
		}
		if err := b.runBefore(ctx, l, event); err != nil {
			return err
		}
	}
	return nil
}

// runBefore runs one interceptor with panic isolation.
func (b *EventBus) runBefore(ctx context.Context, l *listener, event *Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("forum: event interceptor panicked",
				slog.String("event", event.Name),
				slog.String("owner", l.owner),
				slog.Any("panic", r),
			)
			err = errors.Errorf("forum: plugin %q panicked in a %q interceptor", l.owner, event.Name)
		}
	}()
	return l.before(ctx, event)
}

// DispatchAfter notifies the observers of a completed action.
//
// The event is passed by value: a handler cannot mutate the committed
// result. A handler that panics is recovered and logged.
func (b *EventBus) DispatchAfter(ctx context.Context, event Event) {
	if event.Name == "" {
		return
	}
	for _, l := range b.snapshot(event.Name) {
		if l.after == nil {
			continue
		}
		b.runAfter(ctx, l, event)
	}
}

// runAfter runs one observer with panic isolation.
func (b *EventBus) runAfter(ctx context.Context, l *listener, event Event) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("forum: event observer panicked",
				slog.String("event", event.Name),
				slog.String("owner", l.owner),
				slog.Any("panic", r),
			)
		}
	}()
	l.after(ctx, event)
}

// snapshot returns the listeners for an event, ordered by priority and then
// by registration order.
func (b *EventBus) snapshot(name string) []*listener {
	b.mu.RLock()
	matched := make([]*listener, 0, len(b.listeners))
	for _, l := range b.listeners {
		if l.name == name {
			matched = append(matched, l)
		}
	}
	b.mu.RUnlock()

	sortListeners(matched)
	return matched
}

// sortListeners orders listeners by priority, then by registration order.
func sortListeners(listeners []*listener) {
	slices.SortStableFunc(listeners, func(a, b *listener) int {
		if a.priority != b.priority {
			return a.priority - b.priority
		}
		return int(a.order - b.order)
	})
}
