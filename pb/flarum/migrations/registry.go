// Package migrations creates and versions the forum's PocketBase
// collections.
//
// Every migration is forward-only and idempotent: it must work on a
// fresh install and on an existing instance, and it must never destroy
// user data. The applied versions live in the forum's plugin_migrations
// collection.
//
// # The pre-flight collision check
//
// The forum shares a PocketBase instance with the rest of the host
// application. Before any migration runs, the module enumerates the
// existing collections and refuses to continue when a target physical
// name is already taken by a collection the forum does not own. The
// shared `users` collection is the documented exception.
//
// The check is a hard failure, not a warning: silently attaching to
// another module's `tags` collection would corrupt that module's data.
package migrations

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/repository"
)

// CollisionError reports a physical collection name that is already
// taken by a collection the forum does not own.
type CollisionError struct {
	// Name is the collided physical collection name.
	Name string
	// Prefix is the configured forum prefix.
	Prefix string
	// Existing is the collection that already holds the name.
	Existing string
}

// Error implements the error interface.
func (e *CollisionError) Error() string {
	return "forum: the collection " + e.Name + " already exists and is not owned by the forum module " +
		"(configured prefix " + e.Prefix + "); rename the forum's collection or free the name before migrating"
}

// CheckCollisions verifies that every physical collection name the forum
// intends to create is either free or already owned by the forum.
//
// The shared `users` collection is allowed: it is the host's account
// store and the forum reads it deliberately.
//
// It returns a [*CollisionError] naming the first collision it finds.
// The message names the collided collection and the configured prefix,
// so an operator can act on it without reading the source.
func CheckCollisions(app core.App, registry *repository.Registry) error {
	existing := map[string]*core.Collection{}
	collections, err := app.FindAllCollections()
	if err != nil {
		return errors.Wrap(err, "forum: could not enumerate the existing collections")
	}
	for _, collection := range collections {
		existing[collection.Name] = collection
	}

	// The forum's own physical names must not collide with a foreign
	// collection. A name the forum already owns is fine (the migration
	// is idempotent).
	for _, name := range registry.PhysicalNames() {
		other, taken := existing[name]
		if !taken {
			continue
		}
		// The forum may reuse a collection it created itself: an earlier
		// run left it there, and the migrations are idempotent. The
		// ownership MARKER decides, not the name: the host may own a
		// collection called `forum_tags` without the forum having made
		// it.
		if repository.IsOwned(other) {
			continue
		}
		return &CollisionError{Name: name, Prefix: registry.Prefix(), Existing: other.Name}
	}

	// The shared users collection must exist: without it there is no
	// account source, and every later query would fail in a confusing
	// way.
	if _, ok := existing[repository.SharedUsersCollection]; !ok {
		return errors.New("forum: the shared PocketBase users collection is missing; the forum reuses it as its account source")
	}

	return nil
}
