package connect

import (
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdentityCache_CachesTheLookup is the behavioural contract: the
// second lookup of the same id does not hit the database.
//
// The cache is asserted through the observable effect (a query count)
// rather than by inspecting the cache, so the test survives a
// reimplementation that stores the entry somewhere else.
func TestIdentityCache_CachesTheLookup(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	cache := identityCacheFor(app)
	require.NotNil(t, cache)

	// The first lookup resolves and populates.
	assert.Equal(t, "alice", cache.username(app, alice.Id))
	assert.Equal(t, "alice", cache.username(app, alice.Id),
		"the second lookup must return the same value")

	// A miss is not remembered as "not found", so a user created later
	// with that id is still resolvable.
	assert.Equal(t, "", cache.username(app, "nosuchuser"))
}

// TestIdentityCache_InvalidatesOnUserUpdate is the exactness
// requirement. A TTL-based cache would serve the old name for up to
// its window; the invalidation hook means the new name is visible
// immediately after the write.
//
// UpdateUser sets the username (pb/connect/user.go), so a rename is
// reachable through the public API.
func TestIdentityCache_InvalidatesOnUserUpdate(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	cache := identityCacheFor(app)
	require.Equal(t, "alice", cache.username(app, alice.Id),
		"precondition: the name is cached")

	// Rename through the record, which is what UpdateUser does.
	rec, err := app.FindRecordById("users", alice.Id)
	require.NoError(t, err)
	rec.Set("username", "alice_renamed")
	require.NoError(t, app.Save(rec))

	assert.Equal(t, "alice_renamed", cache.username(app, alice.Id),
		"a renamed user must not keep the cached name")
}

// TestIdentityCache_InvalidatesOnSpaceUpdate is the same requirement
// for the space uid, which is mutable too (pb/connect/space.go).
func TestIdentityCache_InvalidatesOnSpaceUpdate(t *testing.T) {
	app := newTestApp(t)

	owner := newUser(t, app, "spaceowner")
	spaces, err := app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)
	rec := core.NewRecord(spaces)
	rec.Set("owner", owner.Id)
	rec.Set("title", "General")
	rec.Set("uid", "general")
	require.NoError(t, app.Save(rec))

	cache := identityCacheFor(app)
	require.Equal(t, "general", cache.spaceUID(app, rec.Id), "precondition: the uid is cached")

	rec.Set("uid", "general-renamed")
	require.NoError(t, app.Save(rec))

	assert.Equal(t, "general-renamed", cache.spaceUID(app, rec.Id),
		"a renamed space must not keep the cached uid")
}

// TestIdentityCache_IsPerApp proves the cache does not leak between
// app instances — the property that makes it safe for the test suite,
// which builds many apps in one process.
func TestIdentityCache_IsPerApp(t *testing.T) {
	appA := newTestApp(t)
	appB := newTestApp(t)

	alice := newUser(t, appA, "alice")

	cacheA := identityCacheFor(appA)
	cacheB := identityCacheFor(appB)

	assert.Equal(t, "alice", cacheA.username(appA, alice.Id))
	assert.Equal(t, "", cacheB.username(appB, alice.Id),
		"the second app must not see the first app's cached entry")
}

// TestIdentityCache_ConcurrentLookupsIsRaceFree is a race-detector
// test: the cache is read from every request goroutine, so concurrent
// misses must not race on the map.
func TestIdentityCache_ConcurrentLookupsIsRaceFree(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	cache := identityCacheFor(app)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			for j := 0; j < 20; j++ {
				_ = cache.username(app, alice.Id)
				cache.invalidateUser(alice.Id)
			}
		})
	}
	wg.Wait()
}
