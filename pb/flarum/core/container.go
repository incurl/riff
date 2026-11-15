// Package core holds the forum's cross-cutting infrastructure: the
// dependency-injection container, the event bus, and the module's
// configuration.
//
// Go has no runtime method monkey-patching, so every point where Flarum
// would let an extension patch a class is modelled here as either a
// container binding (an interface that can be swapped) or an event (a
// declared interception point). See the plugin package for the extender
// vocabulary that plugins use to reach these.
package core

import (
	"sync"

	"github.com/pkg/errors"
)

// Priority orders competing bindings for the same service.
//
// The documented override order is core < plugin default < plugin
// high-priority override. Multiple overrides at the same priority are
// resolved in registration order, so the last one registered wins.
type Priority int

const (
	// PriorityCore is the binding a core service registers with.
	PriorityCore Priority = 0
	// PriorityPlugin is the default binding a plugin registers with.
	PriorityPlugin Priority = 100
	// PriorityPluginHigh is the binding a plugin registers with when it
	// must win over other plugins' defaults.
	PriorityPluginHigh Priority = 200
)

// binding is one registered service.
type binding struct {
	// value is the resolved service instance.
	value any
	// priority orders competing bindings for the same key.
	priority Priority
	// order is the registration sequence number, used to break ties
	// between bindings of equal priority (last registration wins).
	order uint64
}

// Container resolves services by an interface type.
//
// A key is the interface type itself, so a plugin binds "the permission
// checker" by its interface rather than by a string name — which is what
// keeps the extension points type-safe.
type Container struct {
	mu       sync.RWMutex
	bindings map[string]*binding
	order    uint64
	// factories holds lazy bindings, resolved on first use.
	factories map[string]func() (any, error)
}

// NewContainer returns an empty container.
func NewContainer() *Container {
	return &Container{
		bindings:  map[string]*binding{},
		factories: map[string]func() (any, error){},
	}
}

// Bind registers a service instance under the interface type T.
//
// The instance is stored under T's type key, so a lookup by a concrete
// type does not find an interface binding. Use [Container.BindValue] to
// register under a specific key.
func Bind[T any](c *Container, service T) {
	c.BindValue(typeKey[T](), service, PriorityCore)
}

// BindPriority registers a service instance under T with a priority.
func BindPriority[T any](c *Container, service T, priority Priority) {
	c.BindValue(typeKey[T](), service, priority)
}

// BindValue registers a service instance under an explicit key.
//
// A later binding with a higher priority replaces an earlier one. A later
// binding with the SAME priority also replaces it (registration order
// breaks the tie), which is what makes "the last plugin to override wins"
// true within one priority band.
func (c *Container) BindValue(key string, value any, priority Priority) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order++
	current, exists := c.bindings[key]
	if exists && current.priority > priority {
		// A higher-priority binding is already in place; keep it.
		return
	}
	c.bindings[key] = &binding{value: value, priority: priority, order: c.order}
}

// BindFactory registers a lazily-constructed service under T.
//
// The factory runs on the first lookup, and the result is cached. A
// factory that returns an error leaves the binding unregistered, so the
// next lookup retries it.
func BindFactory[T any](c *Container, factory func() (T, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order++
	key := typeKey[T]()
	c.factories[key] = func() (any, error) {
		value, err := factory()
		if err != nil {
			return nil, err
		}
		return value, nil
	}
	// A factory registration drops any existing eager binding: the
	// factory is the newer registration.
	delete(c.bindings, key)
}

// Resolve returns the service registered under T.
//
// It fails when nothing is registered, so a missing binding is a loud
// error at startup rather than a nil dereference at request time.
func Resolve[T any](c *Container) (T, error) {
	var zero T
	key := typeKey[T]()

	c.mu.RLock()
	factory, hasFactory := c.factories[key]
	current, hasBinding := c.bindings[key]
	c.mu.RUnlock()

	if hasBinding {
		value, ok := current.value.(T)
		if !ok {
			return zero, errors.Errorf("core: container binding %q holds a %T, not a %T", key, current.value, zero)
		}
		return value, nil
	}

	if hasFactory {
		value, err := factory()
		if err != nil {
			return zero, errors.Wrapf(err, "core: container factory for %q failed", key)
		}
		typed, ok := value.(T)
		if !ok {
			return zero, errors.Errorf("core: container factory for %q returned a %T, not a %T", key, value, zero)
		}
		c.mu.Lock()
		c.bindings[key] = &binding{value: typed, priority: PriorityCore, order: c.order}
		c.mu.Unlock()
		return typed, nil
	}

	return zero, errors.Errorf("core: no container binding for %q", key)
}

// Has reports whether a binding for T exists.
func Has[T any](c *Container) bool {
	key := typeKey[T]()
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, hasBinding := c.bindings[key]
	_, hasFactory := c.factories[key]
	return hasBinding || hasFactory
}
