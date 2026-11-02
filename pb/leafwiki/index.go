package leafwiki

import (
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/pocketbase/pocketbase/core"
)

// Link states, mirroring LeafWiki's LinkState.
const (
	linkStateResolved = "resolved"
	linkStateBroken   = "broken"
)

// wikilinkSentinelPrefix marks a link target that has to be resolved by page
// title (a [[WikiLink]]) instead of by route path.
const wikilinkSentinelPrefix = "wikilink:"

// indexStoreKey is the app.Store() key the index lives under. The prefix
// matches the rest of riff's per-app state (see pb/connect/identity_cache.go).
const indexStoreKey = "riff.leafwiki_index"

// indexedPage is one entry of the page index.
type indexedPage struct {
	PageID       string
	Path         string
	Slug         string
	Title        string
	Kind         NodeKind
	Content      string
	Excerpt      string
	Tags         []string
	Properties   map[string]string
	Pinned       bool
	Position     int
	CreatedAt    string
	UpdatedAt    string
	Version      string
	CreatorID    string
	LastAuthorID string
}

// nodeRef is the body-free projection of a tree node: everything the index can
// learn from the reconstructed tree WITHOUT opening the page file.
//
// This is what makes the sync incremental. reconstruct() already parses every
// page's frontmatter to build the tree, so the version, path, title, kind,
// pinned flag and position of every node are in hand before the index runs. A
// node whose projection matches its cached entry cannot have changed, so its
// body is never read — an edit to one page touches one page, not the wiki.
type nodeRef struct {
	ID       string
	Path     string
	Slug     string
	Title    string
	Kind     NodeKind
	Version  string
	Pinned   bool
	Position int
}

// indexState is the per-app, in-memory index.
//
// Everything here is derived from the Markdown tree, which is the source of
// truth, so the whole thing can be dropped and rebuilt at any time. It is
// deliberately NOT persisted: the tree is re-read from disk on every load and
// the index is rebuilt from it, so a database would only ever be written and
// never read across a restart.
//
// Locking: the tree's own mutex serialises every caller (index methods are
// only reached through *Tree), but the state is also reachable through
// app.Store(), so it keeps its own lock rather than relying on that.
type indexState struct {
	mu sync.RWMutex

	// pages is the page index, keyed by page id.
	pages map[string]indexedPage

	// pathToID and titleToIDs resolve a link target to the page that owns
	// it. They are maintained alongside pages so a single page's rows can be
	// re-derived without walking the whole index.
	pathToID   map[string]string
	titleToIDs map[string][]string

	// links holds each page's raw outgoing targets, parsed from its body.
	// Keeping them means a path or title change only has to re-resolve, not
	// re-parse every page.
	links map[string][]LinkTarget

	// rows holds each page's derived backlink rows (state already resolved).
	rows map[string][]linkRow
}

// newIndexState returns an empty index.
func newIndexState() *indexState {
	return &indexState{
		pages:      map[string]indexedPage{},
		pathToID:   map[string]string{},
		titleToIDs: map[string][]string{},
		links:      map[string][]LinkTarget{},
		rows:       map[string][]linkRow{},
	}
}

// Index is the in-memory replacement for LeafWiki's SQLite indexes
// (pages/search/tags/properties plus links).
//
// The index is a cache: [index.syncTree] reconciles it with the Markdown tree,
// and everything it holds can be dropped and rebuilt. That is why a page that
// cannot be read is logged and skipped rather than failing the request — a
// stale search entry must never stop a page from loading.
type index struct {
	app core.App
	log *slog.Logger
}

// newIndex returns an index bound to an app.
func newIndex(app core.App) *index {
	return &index{app: app, log: slog.Default().With("component", "leafwiki-index")}
}

// available reports whether the index can be used. It is always true for a
// bound index: the index lives in the app's own store, so there is no
// collection to be missing and no migration to have been forgotten.
func (i *index) available() bool {
	return i != nil && i.app != nil
}

// state returns this app's index state, creating it on first use.
//
// GetOrSet is atomic (it re-checks under the write lock), so two concurrent
// callers share one state. The key is per app, so the many apps a test binary
// boots cannot see each other's entries.
func (i *index) state() *indexState {
	raw := i.app.Store().GetOrSet(indexStoreKey, func() any { return newIndexState() })
	state, ok := raw.(*indexState)
	if !ok {
		// Another component claimed the key. Rather than panic, hand back a
		// throwaway state: the caller still gets correct results, it just
		// does not share them.
		return newIndexState()
	}
	return state
}

// syncTree reconciles the index with the tree.
//
// refs is the body-free projection of every node, and load is called ONLY for
// a node whose projection differs from its cached entry — so the cost of an
// edit is proportional to the pages that actually changed, not to the wiki.
// Pages that are no longer in refs are dropped.
//
// Link rows are re-derived for the pages that changed. A path or title change
// (a create, a rename, a delete) alters which pages every other page's links
// resolve to, so that case re-derives the whole row set — still entirely in
// memory, and cheaper than the per-row SQL the collections needed.
func (i *index) syncTree(refs []nodeRef, load func(pageID string) (indexedPage, bool)) {
	if !i.available() {
		return
	}
	state := i.state()
	state.mu.Lock()
	defer state.mu.Unlock()

	seen := make(map[string]struct{}, len(refs))
	changed := make([]string, 0, 4)
	structural := false

	for _, ref := range refs {
		seen[ref.ID] = struct{}{}

		cached, cachedOK := state.pages[ref.ID]
		if cachedOK && sameNode(cached, ref) {
			continue
		}

		entry, ok := load(ref.ID)
		if !ok {
			// The page file could not be read. Leave whatever is cached in
			// place rather than serving a half-built entry.
			continue
		}
		// The tree owns the node fields; the loader only contributes the
		// body-derived ones (content, excerpt, tags, properties).
		entry.Path = ref.Path
		entry.Slug = ref.Slug
		entry.Title = ref.Title
		entry.Kind = ref.Kind
		entry.Version = ref.Version
		entry.Pinned = ref.Pinned
		entry.Position = ref.Position

		if cachedOK {
			state.removeTargetsLocked(cached)
		}
		if !cachedOK || cached.Path != entry.Path || cached.Title != entry.Title {
			structural = true
		}

		state.pages[ref.ID] = entry
		state.addTargetsLocked(entry)
		state.links[ref.ID] = ExtractLinks(entry.Content)
		changed = append(changed, ref.ID)
	}

	for pageID, cached := range state.pages {
		if _, ok := seen[pageID]; ok {
			continue
		}
		state.removeTargetsLocked(cached)
		delete(state.pages, pageID)
		delete(state.links, pageID)
		delete(state.rows, pageID)
		structural = true
	}

	if structural {
		state.deriveAllRowsLocked()
		return
	}
	for _, pageID := range changed {
		state.rows[pageID] = state.rowsForLocked(pageID)
	}
}

// sameNode reports whether a cached entry already reflects a node projection.
//
// Version is derived from the page's updatedAt, which every mutation bumps, so
// it is the change signal. The rest guards against a page whose node fields
// were rewritten without the timestamp moving.
func sameNode(entry indexedPage, ref nodeRef) bool {
	return entry.Version == ref.Version &&
		entry.Path == ref.Path &&
		entry.Slug == ref.Slug &&
		entry.Title == ref.Title &&
		entry.Kind == ref.Kind &&
		entry.Pinned == ref.Pinned &&
		entry.Position == ref.Position
}

// allPages returns every indexed page, ordered by page id so callers that do
// not sort get a stable sequence.
func (i *index) allPages() []indexedPage {
	if !i.available() {
		return []indexedPage{}
	}
	state := i.state()
	state.mu.RLock()
	defer state.mu.RUnlock()

	pages := make([]indexedPage, 0, len(state.pages))
	for _, entry := range state.pages {
		pages = append(pages, entry)
	}
	slices.SortFunc(pages, func(a, b indexedPage) int { return strings.Compare(a.PageID, b.PageID) })
	return pages
}

// pageByID returns one indexed page.
func (i *index) pageByID(pageID string) (indexedPage, bool) {
	if !i.available() {
		return indexedPage{}, false
	}
	state := i.state()
	state.mu.RLock()
	defer state.mu.RUnlock()

	entry, ok := state.pages[pageID]
	return entry, ok
}

// normaliseWikiPath trims slashes so a stored path and a requested path
// compare equal.
func normaliseWikiPath(path string) string {
	return strings.Trim(strings.TrimSpace(path), "/")
}

// entryFromPage projects a page onto an index entry.
func entryFromPage(page *Page) indexedPage {
	tags := page.Tags
	if tags == nil {
		tags = []string{}
	}
	properties := page.Properties
	if properties == nil {
		properties = map[string]string{}
	}
	return indexedPage{
		PageID:       page.ID,
		Path:         page.Path,
		Slug:         page.Slug,
		Title:        page.Title,
		Kind:         page.Kind,
		Content:      page.Content,
		Excerpt:      Excerpt(page.Content),
		Tags:         tags,
		Properties:   properties,
		Pinned:       page.Pinned,
		Position:     page.Position,
		CreatedAt:    formatMetadataTime(page.Metadata.CreatedAt),
		UpdatedAt:    formatMetadataTime(page.Metadata.UpdatedAt),
		Version:      page.Version,
		CreatorID:    page.Metadata.CreatorID,
		LastAuthorID: page.Metadata.LastAuthorID,
	}
}
