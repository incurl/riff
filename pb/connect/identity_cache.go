package connect

import (
	"sync"

	"github.com/pocketbase/pocketbase/core"
)

// identityCache memoises the two record-id → display-name resolutions
// the feed path performs for every row: a user id to a username, and a
// space id to its uid slug.
//
// Why it exists. `recordToMemo` resolves the author and the space of
// *every* memo it renders, and PocketBase has no record cache — each
// resolution is a real point query. At the SPA's page size of 16 that
// is ~32 extra queries per feed page, plus one more for every reaction
// on the page (memos_translate.go). The same two lookups are made by
// every other path that renders a name: ListReactions,
// ListMemoRelations, ListAttachments, ListUserWebhooks,
// ListPersonalAccessTokens, the space translators and the linked
// identities. A cache helps all of them; batching the query would only
// help ListMemos.
//
// Where it lives. On `app.Store()`, PocketBase's own per-app
// key-value store. That gives three things for free: the cache is
// scoped to the app instance (so two apps in one process — the test
// suite runs many — cannot see each other's entries), the store is
// concurrency-safe, and there is no package-level mutable state to
// guard. PocketBase uses `app.Store()` the same way for its own rate
// limiter (apis/middlewares_rate_limit.go).
//
// Expiry. By *invalidation*, not by TTL. A username is mutable
// (UpdateUser sets it, pb/connect/user.go) and so is a space uid
// (pb/connect/space.go), so a TTL would serve a stale name for up to
// its window. Instead the entry is dropped when the underlying record
// is written, which makes the cache exact: the only window in which a
// stale name can be served is between the write and the after-success
// hook, which is sub-millisecond.
//
// The maps are bounded by the size of the `users` and `spaces`
// collections, so no eviction is needed. (The linkmeta cache is a
// different animal: its key space is URLs, which is unbounded, and it
// keeps its own TTL and eviction for that reason.)
type identityCache struct {
	mu sync.RWMutex
	// usernames maps a user record id to its username.
	usernames map[string]string
	// spaceUIDs maps a space record id to its uid slug.
	spaceUIDs map[string]string
}

// identityCacheStoreKey is the app.Store() key the cache lives under.
const identityCacheStoreKey = "riff.identity_cache"

// identityCacheFor returns the cache for app, creating it on first
// use. The store's GetOrSet is atomic — it re-checks under the write
// lock — so two concurrent callers share one cache.
//
// The invalidation hooks are installed here rather than in Register, so
// the cache is correct for every app that looks a name up — including
// the test apps, which never reach Register. The hooks are bound once
// per app: the store records that it has been done, so a second
// lookup does not bind them again.
func identityCacheFor(app core.App) *identityCache {
	raw := app.Store().GetOrSet(identityCacheStoreKey, func() any {
		return &identityCache{
			usernames: map[string]string{},
			spaceUIDs: map[string]string{},
		}
	})
	cache, ok := raw.(*identityCache)
	if !ok {
		// Another component claimed the key. Rather than panic, fall
		// back to an uncached lookup so the request still succeeds.
		return &identityCache{
			usernames: map[string]string{},
			spaceUIDs: map[string]string{},
		}
	}

	app.Store().GetOrSet(identityCacheInstalledKey, func() any {
		installIdentityCacheInvalidation(app)
		return true
	})
	return cache
}

// identityCacheInstalledKey records that the invalidation hooks have
// been bound for this app.
const identityCacheInstalledKey = "riff.identity_cache_installed"

// username returns the cached username for userID, resolving it on a
// miss. It returns "" when the user does not exist.
func (c *identityCache) username(app core.App, userID string) string {
	if userID == "" {
		return ""
	}
	c.mu.RLock()
	name, ok := c.usernames[userID]
	c.mu.RUnlock()
	if ok {
		return name
	}

	rec, err := app.FindRecordById("users", userID)
	if err != nil {
		// Not cached: a miss is re-resolved every time, which is the
		// point — a deleted or unknown id must not be remembered as
		// "not found", or a user created later with that id would stay
		// invisible until the process restarted.
		return ""
	}
	name = rec.GetString("username")

	c.mu.Lock()
	c.usernames[userID] = name
	c.mu.Unlock()
	return name
}

// spaceUID returns the cached uid slug for spaceID, resolving it on a
// miss. It returns "" when the space does not exist.
func (c *identityCache) spaceUID(app core.App, spaceID string) string {
	if spaceID == "" {
		return ""
	}
	c.mu.RLock()
	uid, ok := c.spaceUIDs[spaceID]
	c.mu.RUnlock()
	if ok {
		return uid
	}

	rec, err := app.FindRecordById("spaces", spaceID)
	if err != nil {
		return ""
	}
	uid = rec.GetString("uid")

	c.mu.Lock()
	c.spaceUIDs[spaceID] = uid
	c.mu.Unlock()
	return uid
}

// invalidateUser drops the cached username for userID. It is called
// when a `users` row is written.
func (c *identityCache) invalidateUser(userID string) {
	if userID == "" {
		return
	}
	c.mu.Lock()
	delete(c.usernames, userID)
	c.mu.Unlock()
}

// invalidateSpace drops the cached uid slug for spaceID. It is called
// when a `spaces` row is written.
func (c *identityCache) invalidateSpace(spaceID string) {
	if spaceID == "" {
		return
	}
	c.mu.Lock()
	delete(c.spaceUIDs, spaceID)
	c.mu.Unlock()
}

// installIdentityCacheInvalidation wires the cache's invalidation to
// the record hooks.
//
// It is registered per app, and the store it writes into is per app, so
// there is no cross-app state and nothing to tear down — the cache dies
// with the app instance, which is what the per-app maps this replaced
// could not guarantee (four of the six never deleted their entry).
func installIdentityCacheInvalidation(app core.App) {
	app.OnRecordAfterCreateSuccess("users").BindFunc(func(e *core.RecordEvent) error {
		identityCacheFor(app).invalidateUser(e.Record.Id)
		return nil
	})
	app.OnRecordAfterUpdateSuccess("users").BindFunc(func(e *core.RecordEvent) error {
		identityCacheFor(app).invalidateUser(e.Record.Id)
		return nil
	})
	app.OnRecordAfterDeleteSuccess("users").BindFunc(func(e *core.RecordEvent) error {
		identityCacheFor(app).invalidateUser(e.Record.Id)
		return nil
	})

	app.OnRecordAfterCreateSuccess("spaces").BindFunc(func(e *core.RecordEvent) error {
		identityCacheFor(app).invalidateSpace(e.Record.Id)
		return nil
	})
	app.OnRecordAfterUpdateSuccess("spaces").BindFunc(func(e *core.RecordEvent) error {
		identityCacheFor(app).invalidateSpace(e.Record.Id)
		return nil
	})
	app.OnRecordAfterDeleteSuccess("spaces").BindFunc(func(e *core.RecordEvent) error {
		identityCacheFor(app).invalidateSpace(e.Record.Id)
		return nil
	})
}
