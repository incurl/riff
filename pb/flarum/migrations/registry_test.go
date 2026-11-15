package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	// The blank import registers PocketBase's own init migration, which
	// creates the base tables (including the `users` auth collection).
	_ "github.com/pocketbase/pocketbase/migrations"

	"github.com/incurl/riff/pb/flarum/repository"
)

// newTestApp boots a PocketBase app for a migration test.
func newTestApp(t *testing.T) core.App {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_forum_migrations_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")

	return app
}

// TestCheckCollisions_SharedUsersIsAllowed pins the documented
// exception: the shared `users` collection is the host's, and the forum
// reads it deliberately.
func TestCheckCollisions_SharedUsersIsAllowed(t *testing.T) {
	app := newTestApp(t)
	registry := repository.NewRegistry(repository.DefaultPrefix)

	require.NoError(t, CheckCollisions(app, registry),
		"the shared users collection must not be treated as a collision")
}

// TestCheckCollisions_ForeignCollectionIsRejected pins the pre-flight
// check: a name the forum wants to create but does NOT own must abort
// the migration rather than silently attaching to another module's
// collection.
//
// The dangerous case is a host collection whose name happens to match
// what the forum would create — for instance when the host already had
// a `forum_groups` collection, or when an operator changes the forum's
// prefix and the old collections are still in the database.
func TestCheckCollisions_ForeignCollectionIsRejected(t *testing.T) {
	app := newTestApp(t)

	// The host owns a collection under the name the forum would use.
	// It carries NO ownership marker, because the host made it.
	foreign := core.NewBaseCollection("forum_groups")
	require.NoError(t, app.Save(foreign))

	registry := repository.NewRegistry(repository.DefaultPrefix)

	err := CheckCollisions(app, registry)
	require.Error(t, err, "a host-owned collection must abort the migration")

	collision, ok := err.(*CollisionError)
	require.True(t, ok, "the error must be a CollisionError, got %T", err)
	require.Equal(t, "forum_groups", collision.Name)
	require.Equal(t, repository.DefaultPrefix, collision.Prefix)
}

// TestCheckCollisions_ForumOwnedNameIsAllowed pins that re-running the
// check against collections the forum already created succeeds.
func TestCheckCollisions_ForumOwnedNameIsAllowed(t *testing.T) {
	app := newTestApp(t)
	registry := repository.NewRegistry(repository.DefaultPrefix)

	require.NoError(t, Core(app, registry), "the core migrations should succeed")
	require.NoError(t, CheckCollisions(app, registry),
		"the forum's own collections must not be treated as collisions")
}

// TestCheckCollisions_CollisionErrorNamesTheCollection pins the error
// message: it must name the collided collection and the prefix, so an
// operator can act on it.
func TestCheckCollisions_CollisionErrorNamesTheCollection(t *testing.T) {
	app := newTestApp(t)

	host := core.NewBaseCollection("forum_settings")
	require.NoError(t, app.Save(host))

	registry := repository.NewRegistry(repository.DefaultPrefix)
	err := CheckCollisions(app, registry)
	require.Error(t, err, "a host-owned collection must abort the migration")

	collision, ok := err.(*CollisionError)
	require.True(t, ok, "the error must be a CollisionError, got %T", err)
	require.Equal(t, "forum_settings", collision.Name)
	require.Contains(t, collision.Error(), "forum_settings")
	require.Contains(t, collision.Error(), "prefix")
}

// TestCore_IsIdempotent pins that the core migrations can be applied
// twice without failing.
func TestCore_IsIdempotent(t *testing.T) {
	app := newTestApp(t)
	registry := repository.NewRegistry(repository.DefaultPrefix)

	require.NoError(t, Core(app, registry), "the first run should succeed")
	require.NoError(t, Core(app, registry), "the second run should succeed")
}

// TestCore_CreatesTheForumCollections pins that every core collection
// exists after the migrations.
func TestCore_CreatesTheForumCollections(t *testing.T) {
	app := newTestApp(t)
	registry := repository.NewRegistry(repository.DefaultPrefix)

	require.NoError(t, Core(app, registry))

	for _, name := range registry.PhysicalNames() {
		_, err := app.FindCollectionByNameOrId(name)
		require.NoErrorf(t, err, "the %q collection should exist", name)
	}
}

// TestCore_DoesNotCreateAUsersCollection pins the shared-users boundary:
// the forum never creates its own account store.
func TestCore_DoesNotCreateAUsersCollection(t *testing.T) {
	app := newTestApp(t)
	registry := repository.NewRegistry(repository.DefaultPrefix)

	require.NoError(t, Core(app, registry))

	// The shared collection is the host's, and it is untouched.
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	require.Equal(t, core.CollectionTypeAuth, users.Type,
		"the shared users collection must stay an auth collection")

	// The forum must not have created a prefixed copy of it.
	_, err = app.FindCollectionByNameOrId("forum_users")
	require.Error(t, err, "the forum must not create its own account store")
}
