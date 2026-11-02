package leafwiki

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Collection names. They live in their own migration
// (pb/migrations/1940000000_create_leafwiki_tables.go) and are deliberately
// prefixed so they do not collide with riff's own collections.
const (
	// pagesCollection is LeafWiki's page/search/tag/property index.
	pagesCollection = "leafwiki_pages"

	// linksCollection is LeafWiki's backlink index.
	linksCollection = "leafwiki_links"
)

// Link states, mirroring LeafWiki's LinkState.
const (
	linkStateResolved = "resolved"
	linkStateBroken   = "broken"
)

// wikilinkSentinelPrefix marks a link target that has to be resolved by page
// title (a [[WikiLink]]) instead of by route path.
const wikilinkSentinelPrefix = "wikilink:"

// indexedPage is one row of the page index.
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

// Index is the PocketBase-backed replacement for LeafWiki's SQLite indexes
// (pages/search/tags/properties plus links).
//
// The index is a cache: [Index.Sync] reconciles it with the Markdown tree, and
// everything it holds can be dropped and rebuilt. That is why a failed write
// is logged rather than returned — a stale search row must never stop a page
// from loading.
type index struct {
	app core.App
	log *slog.Logger
}

// newIndex returns an index bound to an app.
func newIndex(app core.App) *index {
	return &index{app: app, log: slog.Default().With("component", "leafwiki-index")}
}

// available reports whether the collections exist. A PocketBase project that
// imports this package without its migration still runs; it simply has no
// index.
func (i *index) available() bool {
	if i == nil || i.app == nil {
		return false
	}
	if _, err := i.app.FindCollectionByNameOrId(pagesCollection); err != nil {
		return false
	}
	return true
}

// Sync reconciles the index with the given pages.
//
// Rows that are unchanged are left alone, new pages are inserted, vanished
// pages are removed, and the link table is rebuilt from scratch: a link's
// state depends on the whole tree (a page that was just created heals every
// link pointing at it), so recomputing is both simpler and more correct than
// patching individual rows.
func (i *index) sync(pages []*Page) {
	if !i.available() {
		return
	}

	existing, err := i.allRecords()
	if err != nil {
		i.log.Warn("could not read the page index", "error", err)
		return
	}

	seen := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		entry := entryFromPage(page)
		seen[entry.PageID] = struct{}{}

		record, ok := existing[entry.PageID]
		if ok && recordMatches(record, entry) {
			continue
		}
		if !ok {
			record = core.NewRecord(i.mustCollection(pagesCollection))
		}
		if err := i.saveEntry(record, entry); err != nil {
			i.log.Warn("could not index page", "pageId", entry.PageID, "error", err)
		}
	}

	for pageID, record := range existing {
		if _, ok := seen[pageID]; ok {
			continue
		}
		if err := i.app.Delete(record); err != nil {
			i.log.Warn("could not remove a stale index row", "pageId", pageID, "error", err)
		}
	}

	i.rebuildLinks(pages)
}

// mustCollection returns a collection that availability already checked.
func (i *index) mustCollection(name string) *core.Collection {
	collection, err := i.app.FindCollectionByNameOrId(name)
	if err != nil {
		panic("leafwiki: collection " + name + " is missing: " + err.Error())
	}
	return collection
}

// normaliseWikiPath trims slashes so a stored path and a requested path
// compare equal.
func normaliseWikiPath(path string) string {
	return strings.Trim(strings.TrimSpace(path), "/")
}

// entryFromPage projects a page onto an index row.
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

// recordMatches reports whether an index row already reflects an entry.
func recordMatches(record *core.Record, entry indexedPage) bool {
	return record.GetString("version") == entry.Version &&
		record.GetString("path") == entry.Path &&
		record.GetString("title") == entry.Title &&
		record.GetString("kind") == string(entry.Kind)
}

// saveEntry writes an entry into a record.
func (i *index) saveEntry(record *core.Record, entry indexedPage) error {
	record.Set("page_id", entry.PageID)
	record.Set("path", entry.Path)
	record.Set("slug", entry.Slug)
	record.Set("title", entry.Title)
	record.Set("kind", string(entry.Kind))
	record.Set("content", entry.Content)
	record.Set("excerpt", entry.Excerpt)
	record.Set("tags", entry.Tags)
	record.Set("properties", entry.Properties)
	record.Set("pinned", entry.Pinned)
	record.Set("position", entry.Position)
	record.Set("created_at", entry.CreatedAt)
	record.Set("updated_at", entry.UpdatedAt)
	record.Set("version", entry.Version)
	record.Set("creator_id", entry.CreatorID)
	record.Set("last_author_id", entry.LastAuthorID)
	return i.app.Save(record)
}

// allRecords returns the current page index keyed by page id.
func (i *index) allRecords() (map[string]*core.Record, error) {
	records, err := i.app.FindAllRecords(pagesCollection)
	if err != nil {
		return nil, errors.Wrap(err, "leafwiki: could not load the page index")
	}
	byPageID := make(map[string]*core.Record, len(records))
	for _, record := range records {
		byPageID[record.GetString("page_id")] = record
	}
	return byPageID, nil
}

// AllPages returns every indexed page.
func (i *index) allPages() []indexedPage {
	if !i.available() {
		return []indexedPage{}
	}
	records, err := i.app.FindAllRecords(pagesCollection)
	if err != nil {
		i.log.Warn("could not load the page index", "error", err)
		return []indexedPage{}
	}
	pages := make([]indexedPage, 0, len(records))
	for _, record := range records {
		pages = append(pages, entryFromRecord(record))
	}
	return pages
}

// PageByID returns one indexed page.
func (i *index) pageByID(pageID string) (indexedPage, bool) {
	if !i.available() {
		return indexedPage{}, false
	}
	record, err := i.app.FindFirstRecordByFilter(pagesCollection, "page_id = {:pageID}", map[string]any{"pageID": pageID})
	if err != nil {
		return indexedPage{}, false
	}
	return entryFromRecord(record), true
}

// entryFromRecord reads an index row.
func entryFromRecord(record *core.Record) indexedPage {
	return indexedPage{
		PageID:       record.GetString("page_id"),
		Path:         record.GetString("path"),
		Slug:         record.GetString("slug"),
		Title:        record.GetString("title"),
		Kind:         NodeKind(record.GetString("kind")),
		Content:      record.GetString("content"),
		Excerpt:      record.GetString("excerpt"),
		Tags:         jsonStrings(record, "tags"),
		Properties:   jsonStringMap(record, "properties"),
		Pinned:       record.GetBool("pinned"),
		Position:     record.GetInt("position"),
		CreatedAt:    record.GetString("created_at"),
		UpdatedAt:    record.GetString("updated_at"),
		Version:      record.GetString("version"),
		CreatorID:    record.GetString("creator_id"),
		LastAuthorID: record.GetString("last_author_id"),
	}
}

// jsonStrings reads a JSON array field as a string slice.
func jsonStrings(record *core.Record, field string) []string {
	raw, ok := record.GetRaw(field).(types.JSONRaw)
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return []string{}
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return []string{}
	}
	return values
}

// jsonStringMap reads a JSON object field as a string map.
func jsonStringMap(record *core.Record, field string) map[string]string {
	raw, ok := record.GetRaw(field).(types.JSONRaw)
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return map[string]string{}
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return map[string]string{}
	}
	return values
}
