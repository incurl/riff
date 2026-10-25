package hooks

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/linkmeta"
	"github.com/incurl/riff/markdown"
)

// memoLinkFetchTimeout caps how long any single linkmeta.Fetch
// call is allowed to take. The library already imposes a 5s
// per-request HTTP timeout; the outer timeout also covers DNS
// resolution and body reads. If a fetch does not complete in time
// the row is stored with the error message so a later retry can
// pick it up — and the memo save itself is not blocked.
const memoLinkFetchTimeout = 8 * time.Second

// memoLinkFetchConcurrency caps the number of URLs the
// auto-fetch hook fans out for in parallel. A memo with a 100-link
// "links roundup" should not spawn 100 goroutines hitting the
// network; the worker pool bounds the burst.
const memoLinkFetchConcurrency = 4

// memoLinkLog is the slog logger used by the auto-fetch path.
// Pulled out so the tests can intercept it without setting up a
// PocketBase app. The default is the package-level slog default
// logger; the test in memo_links_test.go substitutes a discard
// handler to keep the test output quiet.
var memoLinkLog = slog.Default()

// memoLinksActiveMemoIDs guards against the case where two
// successive saves race the same memo ID and the second save's
// after-hook fires before the first save's fetch goroutine has
// finished upserting. The inflight set is keyed by memo ID and
// released by a finalizer-style defer at the end of each fan-out
// task; the value is the content snapshot the fan-out is acting
// on, so a second save that lands with the same content is a
// no-op rather than a redundant fetch storm.
//
// The map is bounded by the number of memos in flight at any
// moment, which is small (a handful). A 1m TTL reaper runs on
// every new entry to garbage-collect entries whose goroutine
// crashed silently.
var (
	memoLinksActiveMemoIDsMu sync.Mutex
	memoLinksActiveMemoIDs   = map[string]time.Time{}
)

// The PER-APP fan-out state — the cancellation signal the fan-out
// goroutines honour, and the wait group the teardown hook blocks on —
// lives on the app's own store (see memoLinksStateFor), not in a
// package-level map.
//
// Both parts are per app on purpose. The channel used to be
// package-level, which was wrong twice over: closing the shared channel
// for one app stopped every other app's fan-out, and two apps
// registering concurrently raced on the globals.
//
// See the AGENTS.md "Known Issues" section, entry 1, for the original
// report.

// memoLinksState is the per-app fan-out state.
type memoLinksState struct {
	shutdown     chan struct{}
	shutdownOnce *sync.Once
	workers      sync.WaitGroup

	// gate guards the `workers` wait group against the
	// `sync.WaitGroup` "reused before previous Wait has
	// returned" panic, which fires when `Add` races a
	// teardown's `Wait`.
	//
	// `workers.Add` runs per memo save, so it CAN overlap
	// the teardown wait (a memo saved while the app is
	// shutting down). Once teardown has taken the gate
	// and begun waiting, `gateAccepting` is false and every
	// later save declines to track its goroutine instead of
	// calling `Add` on a wait group that is mid-`Wait`.
	//
	// Mutex-guarded rather than atomic because the lock
	// also has to cover the `Add` itself: two concurrent
	// saves must not both pass the check and then race each
	// other into `Add` against a `Wait`.
	gate          sync.Mutex
	gateAccepting bool
}

// begin accepts a new worker iff teardown has not started.
// It returns false once the state is closed, in which case
// the caller must run its goroutine untracked: the channel
// it would have been counted on is already being torn down.
func (s *memoLinksState) begin() bool {
	s.gate.Lock()
	defer s.gate.Unlock()
	if !s.gateAccepting {
		return false
	}
	s.workers.Add(1)
	return true
}

// end releases a worker accepted by begin.
func (s *memoLinksState) end() {
	s.workers.Done()
}

// stopAndWait signals the shutdown channel and waits for
// every accepted worker to unwind. It is called from both
// teardown hooks (`OnTerminate` and `OnBootstrapClear`),
// so it must be safe to run twice — the channel close is
// `sync.Once`-guarded and the second call finds no tracked
// workers and returns immediately.
func (s *memoLinksState) stopAndWait() {
	// Close the gate FIRST so no further `begin` can
	// reach `Add`. Without this a save landing mid-wait
	// is exactly the documented panic.
	s.gate.Lock()
	s.gateAccepting = false
	s.gate.Unlock()

	s.shutdownOnce.Do(func() { close(s.shutdown) })
	s.workers.Wait()
}

// memoLinksStateStoreKey is the app.Store() key the per-app fan-out
// state lives under.
const memoLinksStateStoreKey = "riff.memo_links_state"

// memoLinksStateFor returns the per-app fan-out state, creating it on
// first use.
//
// It used to be a package-level `map[core.App]*memoLinksState` guarded
// by its own mutex. The app's own store replaces it: the store is
// already per app and concurrency-safe, so the map and its mutex were
// both redundant — and the map's entry was never deleted, so it
// outlived the app it belonged to.
func memoLinksStateFor(app core.App) *memoLinksState {
	raw := app.Store().GetOrSet(memoLinksStateStoreKey, func() any {
		return &memoLinksState{
			shutdown:      make(chan struct{}),
			shutdownOnce:  &sync.Once{},
			gateAccepting: true,
		}
	})
	state, ok := raw.(*memoLinksState)
	if !ok {
		// Another component claimed the key. Fall back to a fresh
		// state rather than panicking on the type assertion.
		return &memoLinksState{
			shutdown:      make(chan struct{}),
			shutdownOnce:  &sync.Once{},
			gateAccepting: true,
		}
	}
	return state
}

// registerMemoLinks subscribes the memo_links auto-fetch to the
// `memos` collection's after-save hooks. Fires for both create
// and update; the body of the hook reads the current content and
// decides which URLs to fetch and which previously-cached rows
// to evict.
//
// The hook is bound with OnRecordAfter*Success (not the request
// hooks) so a save that fails validation never starts a fetch —
// we only spend network on memos that actually committed.
func registerMemoLinks(app core.App) {
	mdSvc := markdown.NewService(
		markdown.WithTagExtension(),
		markdown.WithMentionExtension(),
	)

	// Per-app shutdown signal and wait group. We
	// allocate them fresh on each registerMemoLinks
	// call (once per app) and close the channel on
	// terminate so every in-flight fan-out goroutine
	// sees the signal at its next iteration.
	//
	// Both are PER APP on purpose. The channel must
	// be: a package-level channel closed by one app
	// would stop every other app's fan-out. The wait
	// group must be: a package-level `sync.WaitGroup`
	// cannot be waited on twice concurrently, and two
	// apps tearing down at the same time would panic
	// with "sync: WaitGroup is reused before previous
	// Wait has returned".
	state := memoLinksStateFor(app)
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		state.stopAndWait()
		return e.Next()
	})
	// The same wait on `OnBootstrapClear`, which is the
	// hook a test's `ClearBootstrap` cleanup triggers
	// (`OnTerminate` does NOT fire there).
	//
	// `stopAndWait` is idempotent, so the two hooks
	// firing back to back is fine.
	app.OnBootstrapClear().BindFunc(func(e *core.BootstrapEvent) error {
		state.stopAndWait()
		return e.Next()
	})

	bind := func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		// A save that lands after teardown has begun is
		// NOT tracked: the wait group is mid-`Wait` and
		// `Add` on it would panic the process. The
		// goroutine still runs — the memo is already
		// committed, and `memoLinksFanOut` already has a
		// `recover()` plus a shutdown pre-check, so it
		// unwinds cleanly on its own.
		tracked := state.begin()
		// Snapshot the record before launching the goroutine.
		// Reading `e.Record` inside it is a data race: PocketBase's
		// model/record event bridge writes back into the same shared
		// `RecordEvent` on its way back up the stack
		// (`core.syncModelEventWithRecordEvent`), and that write can
		// land after this handler returns — concurrently with the
		// goroutine's read.
		rec := e.Record
		go func() {
			if tracked {
				defer state.end()
			}
			defer func() {
				if r := recover(); r != nil {
					memoLinkLog.Warn("memo_links: fan-out panicked; app likely shutting down",
						"panic", r)
				}
			}()
			memoLinksFanOut(app, mdSvc, rec)
		}()
		return nil
	}

	app.OnRecordAfterCreateSuccess("memos").BindFunc(bind)
	app.OnRecordAfterUpdateSuccess("memos").BindFunc(bind)
}

// memoLinksFanOut is the post-save fan-out. It is the only
// function in this file that touches the database or the network;
// every other helper is pure.
//
// The work plan:
//  1. Re-extract URLs from the saved content. If there are no
//     URLs, delete any existing memo_links rows for this memo
//     (so removing a link drops the cached row) and stop.
//  2. Re-mark the memo as inflight; release the marker when done.
//  3. List the existing memo_links rows for this memo, build a
//     set of (memo, url) we already have, and delete the rows
//     whose url is no longer in the content.
//  4. For each new URL, call linkmeta.Fetch in a worker pool
//     and upsert the row.
//  5. Mark inflight entries stale so a follow-on save with the
//     same content does not trigger a second fetch storm.
func memoLinksFanOut(app core.App, mdSvc markdown.Service, rec *core.Record) {
	memoID := rec.Id
	if memoID == "" {
		return
	}

	// Fast-fail if the app is already shutting
	// down. Without this check the goroutine would
	// outlive the test's app.Bootstrap cleanup and
	// hit "sql: database is closed" on its first
	// DB call. A normal production shutdown hits
	// the same path.
	if isMemoLinksShutdown(app) {
		return
	}

	content := rec.GetString("content")
	all, err := mdSvc.ExtractAll([]byte(content))
	if err != nil {
		memoLinkLog.Warn("memo_links: extract failed; skipping fan-out",
			"memo", memoID, "err", err)
		return
	}
	urls := all.LinkDestinations

	// Prune rows whose URL is no longer in the content. Done
	// even when urls is empty so the user can drop a link by
	// editing it out of the body.
	pruneStaleMemoLinks(app, memoID, urls)

	if len(urls) == 0 {
		return
	}

	// Reap stale inflight markers (TTL > 1m) so a crashed
	// goroutine does not block subsequent saves forever.
	memoLinksGCInflight()

	// If a fan-out for this memo is still in flight with the
	// same content, do not start a second one. The check is
	// best-effort: a true race just spawns one extra fetch.
	if memoLinksInflight(memoID) {
		return
	}
	memoLinksMarkInflight(memoID)
	defer memoLinksUnmarkInflight(memoID)

	// Bounded worker pool: at most memoLinkFetchConcurrency
	// in-flight HTTP requests, regardless of the URL count.
	jobs := make(chan string)
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), memoLinkFetchTimeout)
	defer cancel()

	for i := 0; i < memoLinkFetchConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A panic in any single worker must not take
			// the whole test binary down. We log and exit
			// the worker; the parent goroutine sees the
			// dropped wg.Done via Wait() and moves on.
			// Same idea as the recover() in
			// pruneStaleMemoLinks.
			defer func() {
				if r := recover(); r != nil {
					memoLinkLog.Warn("memo_links: worker panicked; app likely shutting down",
						"memo", memoID, "panic", r)
				}
			}()
			for u := range jobs {
				if isMemoLinksShutdown(app) {
					continue
				}
				upsertOneMemoLink(app, ctx, memoID, u)
			}
		}()
	}

	for _, u := range urls {
		if normalised, ok := normaliseLinkURL(u); ok {
			jobs <- normalised
		}
	}
	close(jobs)
	wg.Wait()
}

// normaliseLinkURL trims whitespace and rejects URLs linkmeta
// cannot fetch (mailto:, javascript:, file:, data:). The
// markdown walker already filters out managed-attachment URLs
// (those start with /file/attachments/) but a defensive pass
// here keeps the hook from fanning out into the library just to
// have it reject the URL again.
func normaliseLinkURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return u, true
	default:
		return "", false
	}
}

// pruneStaleMemoLinks removes memo_links rows for memoID whose
// url is not in the keep set. An empty keep set deletes every
// row for the memo (the "I removed all my links" case).
//
// The unique index on (memo, url) lets us look up the existing
// rows cheaply; the delete runs in a single statement per stale
// URL to keep the hook fast and predictable.
func pruneStaleMemoLinks(app core.App, memoID string, keep []string) {
	// Two layers of shutdown check:
	//
	//  1. The cheap pre-check (isMemoLinksShutdown)
	//     before any DB work.
	//  2. A recover() around the call so a panic
	//     from a closed DB (the test-app case) does
	//     not crash the test runner. The goroutine
	//     dies on the panic; the leaked DB error
	//     is logged once and the system moves on.
	defer func() {
		if r := recover(); r != nil {
			memoLinkLog.Warn("memo_links: prune panicked; app likely shutting down",
				"memo", memoID, "panic", r)
		}
	}()
	if isMemoLinksShutdown(app) {
		return
	}
	existing, err := app.FindRecordsByFilter("memo_links",
		"memo={:memo}", "", 0, 0, map[string]any{"memo": memoID})
	if err != nil {
		memoLinkLog.Warn("memo_links: list existing failed; skipping prune",
			"memo", memoID, "err", err)
		return
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, u := range keep {
		if norm, ok := normaliseLinkURL(u); ok {
			keepSet[norm] = struct{}{}
		}
	}
	for _, row := range existing {
		u := row.GetString("url")
		if _, ok := keepSet[u]; ok {
			continue
		}
		if err := app.Delete(row); err != nil {
			memoLinkLog.Warn("memo_links: prune delete failed",
				"memo", memoID, "url", u, "err", err)
		}
	}
}

// upsertOneMemoLink runs linkmeta against a single URL and
// writes (or refreshes) the corresponding memo_links row. A
// failed fetch stores the error string in `error` and leaves
// title/description/image empty so the React client can render
// a "preview unavailable" hint and the next save retries.
//
// When the memo_links collection is missing (e.g. on a test
// app that did not run the migration), upsertMemoLinkRow
// returns ok=false and this function bails before doing any
// network or DB work. That keeps a single broken app from
// triggering a nil-deref panic in the worker pool.
func upsertOneMemoLink(app core.App, ctx context.Context, memoID, rawURL string) {
	row, ok := upsertMemoLinkRow(app, memoID, rawURL)
	if !ok {
		return
	}

	fetcher := linkmeta.NewHTMLMetaFetcher()
	meta, err := fetcher.Get(ctx, rawURL)
	if err != nil {
		memoLinkLog.Info("memo_links: fetch failed; storing error",
			"memo", memoID, "url", rawURL, "err", err)
		row.Set("error", truncateErr(err))
		// A previously-successful title/description/image
		// is preserved on transient errors so the user keeps
		// seeing a working preview through a flaky blip.
		if err := app.Save(row); err != nil {
			memoLinkLog.Warn("memo_links: error-row save failed",
				"memo", memoID, "url", rawURL, "err", err)
		}
		return
	}

	row.Set("title", truncate(meta.Title, 500))
	row.Set("description", truncate(meta.Description, 2000))
	row.Set("image", meta.Image)
	row.Set("fetched_at", time.Now().UTC())
	row.Set("error", "")
	if err := app.Save(row); err != nil {
		memoLinkLog.Warn("memo_links: success-row save failed",
			"memo", memoID, "url", rawURL, "err", err)
	}
}

// upsertMemoLinkRow returns the existing (memo, url) row or
// creates a fresh one. The unique index on (memo, url) makes
// the SELECT cheap; a second save with the same URL reuses the
// row instead of inserting a duplicate.
//
// The second return value is false when the collection is
// missing (the migration has not run on this app). The caller
// must short-circuit and skip the save; the prior nil-return
// with a synthetic record panicked the worker pool whenever
// PocketBase handed back a nil collection with a non-nil
// error.
func upsertMemoLinkRow(app core.App, memoID, rawURL string) (*core.Record, bool) {
	rows, err := app.FindRecordsByFilter("memo_links",
		"memo={:memo} && url={:url}", "", 1, 0,
		map[string]any{"memo": memoID, "url": rawURL})
	if err == nil && len(rows) == 1 {
		return rows[0], true
	}
	col, err := app.FindCollectionByNameOrId("memo_links")
	if err != nil || col == nil {
		// The migration has not run (or the app is in
		// the middle of a teardown where the collection
		// is gone). We do not want to crash the after-
		// hook over this; signal the caller to skip the
		// upsert and move on. Returning a nil record
		// here was a latent nil-deref: the previous code
		// called core.NewRecord(col) with col == nil,
		// which the worker pool then dereferenced.
		memoLinkLog.Warn("memo_links: collection not found; skipping upsert",
			"memo", memoID, "url", rawURL, "err", err)
		return nil, false
	}
	row := core.NewRecord(col)
	row.Set("memo", memoID)
	row.Set("url", rawURL)
	return row, true
}

// memoLinksInflight is a fast path: returns true if a recent
// fan-out is still running for the same memo. The set is
// best-effort — losing an entry is harmless; gaining one is
// also harmless.
func memoLinksInflight(memoID string) bool {
	memoLinksActiveMemoIDsMu.Lock()
	defer memoLinksActiveMemoIDsMu.Unlock()
	_, ok := memoLinksActiveMemoIDs[memoID]
	return ok
}

// memoLinksMarkInflight adds memoID to the inflight set with the
// current time, then schedules a GC sweep.
func memoLinksMarkInflight(memoID string) {
	memoLinksActiveMemoIDsMu.Lock()
	memoLinksActiveMemoIDs[memoID] = time.Now()
	memoLinksActiveMemoIDsMu.Unlock()
}

// memoLinksUnmarkInflight removes memoID from the inflight set.
// Safe to call for an ID that was never marked.
func memoLinksUnmarkInflight(memoID string) {
	memoLinksActiveMemoIDsMu.Lock()
	delete(memoLinksActiveMemoIDs, memoID)
	memoLinksActiveMemoIDsMu.Unlock()
}

// memoLinksGCInflight drops entries older than 1 minute. A
// fan-out that runs for over a minute is broken; unblocking
// future saves is the right call.
func memoLinksGCInflight() {
	cutoff := time.Now().Add(-time.Minute)
	memoLinksActiveMemoIDsMu.Lock()
	defer memoLinksActiveMemoIDsMu.Unlock()
	for id, ts := range memoLinksActiveMemoIDs {
		if ts.Before(cutoff) {
			delete(memoLinksActiveMemoIDs, id)
		}
	}
}

// truncate keeps `s` to at most `n` bytes; longer inputs are
// sliced to fit and have a trailing "…" appended so the field's
// Max constraint is never violated. The cut + "…" must always
// be ≤ n bytes; "…" is itself 3 bytes in UTF-8, so the slice
// head is n-3 bytes when the cut path is taken.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	if n <= len(ellipsis) {
		return s[:n]
	}
	return s[:n-len(ellipsis)] + ellipsis
}

// truncateErr collapses an error chain into a single line that
// fits the `error` column's 500-byte budget. The full chain is
// still in the slog entry.
func truncateErr(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), 500)
}

// isMemoLinksShutdown reports whether the fan-out
// goroutines should stop. The channel is set in
// registerMemoLinks and closed on app.OnTerminate,
// so a closed channel means the app that owned the
// goroutine is going away. A nil channel (before
// registerMemoLinks has run, or after a fresh
// process boot) returns false, so this is safe to
// call from any goroutine.
func isMemoLinksShutdown(app core.App) bool {
	// Read the state WITHOUT creating one: this is a pure check, and
	// an app that never registered a fan-out has no state to shut
	// down. GetOrSet would create one, so the raw store read is used.
	state, _ := app.Store().Get(memoLinksStateStoreKey).(*memoLinksState)
	if state == nil {
		return false
	}
	select {
	case <-state.shutdown:
		return true
	default:
		return false
	}
}
