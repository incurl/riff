package leafwiki

import (
	"slices"
	"strings"
)

// linkRow is one derived backlink row.
type linkRow struct {
	FromPageID string
	ToPageID   string
	ToPath     string
	ToTitle    string
	State      string
}

// addTargetsLocked registers a page as the owner of its path and title, so
// other pages' links can resolve to it.
func (st *indexState) addTargetsLocked(entry indexedPage) {
	if path := normaliseWikiPath(entry.Path); path != "" {
		st.pathToID[path] = entry.PageID
	}
	key := strings.ToLower(strings.TrimSpace(entry.Title))
	if key == "" {
		return
	}
	st.titleToIDs[key] = append(st.titleToIDs[key], entry.PageID)
}

// removeTargetsLocked drops a page from the resolution maps.
func (st *indexState) removeTargetsLocked(entry indexedPage) {
	path := normaliseWikiPath(entry.Path)
	// Only clear the path when this page still owns it: two pages cannot
	// share a path, but a rename leaves the old entry behind long enough to
	// matter here.
	if path != "" && st.pathToID[path] == entry.PageID {
		delete(st.pathToID, path)
	}
	key := strings.ToLower(strings.TrimSpace(entry.Title))
	if key == "" {
		return
	}
	ids := st.titleToIDs[key]
	kept := ids[:0]
	for _, id := range ids {
		if id != entry.PageID {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		delete(st.titleToIDs, key)
		return
	}
	st.titleToIDs[key] = kept
}

// rowsForLocked derives one page's link rows from its raw targets.
//
// Resolution is a map lookup against the CURRENT tree, so a page that was just
// created heals every link pointing at it — the same property the previous
// "drop the whole link table and re-insert it" approach had, without the SQL.
func (st *indexState) rowsForLocked(fromPageID string) []linkRow {
	targets := st.links[fromPageID]
	if len(targets) == 0 {
		return nil
	}

	rows := make([]linkRow, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	// One row per (source, target) pair: a page that links to the same target
	// twice — once as a Markdown link and once as a wiki link, say — must show
	// up once in the backlink panel.
	appendRow := func(row linkRow) {
		key := "id:" + row.ToPageID
		if row.ToPageID == "" {
			key = "path:" + row.ToPath
		}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		rows = append(rows, row)
	}

	for _, target := range targets {
		switch {
		case target.Path != "":
			toPath := normaliseWikiPath(target.Path)
			row := linkRow{FromPageID: fromPageID, ToPath: toPath}
			if id, ok := st.pathToID[toPath]; ok {
				row.ToPageID = id
				row.State = linkStateResolved
			} else {
				row.State = linkStateBroken
			}
			appendRow(row)

		case target.Title != "":
			key := strings.ToLower(strings.TrimSpace(target.Title))
			matches := st.titleToIDs[key]
			if len(matches) == 0 {
				appendRow(linkRow{
					FromPageID: fromPageID,
					ToPath:     wikilinkSentinelPrefix + target.Title,
					ToTitle:    target.Title,
					State:      linkStateBroken,
				})
				continue
			}
			for _, toID := range matches {
				appendRow(linkRow{
					FromPageID: fromPageID,
					ToPageID:   toID,
					ToTitle:    target.Title,
					State:      linkStateResolved,
				})
			}

		default:
			// An extracted link always has a path or a title; anything else
			// is not indexable.
		}
	}
	return rows
}

// deriveAllRowsLocked re-derives every page's rows. Used when the path or
// title universe changed, because that alters the resolution of links in pages
// that were not themselves edited.
func (st *indexState) deriveAllRowsLocked() {
	rows := make(map[string][]linkRow, len(st.links))
	for pageID := range st.links {
		if derived := st.rowsForLocked(pageID); len(derived) > 0 {
			rows[pageID] = derived
		}
	}
	st.rows = rows
}

// linksFrom returns the outgoing links of a page.
func (i *index) linksFrom(pageID string) []linkRow {
	if !i.available() {
		return nil
	}
	state := i.state()
	state.mu.RLock()
	defer state.mu.RUnlock()
	return slices.Clone(state.rows[pageID])
}

// linksTo returns the links pointing at a page.
func (i *index) linksTo(pageID string) []linkRow {
	return i.filterRows(func(row linkRow) bool { return row.ToPageID == pageID })
}

// linksToPath returns the links pointing at a route path, resolved or not.
func (i *index) linksToPath(path string) []linkRow {
	normalised := normaliseWikiPath(path)
	return i.filterRows(func(row linkRow) bool { return row.ToPath == normalised })
}

// brokenLinks returns every unresolved link.
func (i *index) brokenLinks() []linkRow {
	return i.filterRows(func(row linkRow) bool { return row.State == linkStateBroken })
}

// filterRows returns the derived rows matching keep.
//
// The reverse lookups scan every page's rows rather than maintaining a second
// index. A backlink panel is a per-page-view request and the row count is
// bounded by the wiki's link count, so this stays cheap while keeping one
// source of truth for the rows themselves.
func (i *index) filterRows(keep func(linkRow) bool) []linkRow {
	if !i.available() {
		return nil
	}
	state := i.state()
	state.mu.RLock()
	defer state.mu.RUnlock()

	out := make([]linkRow, 0, len(state.rows))
	for _, rows := range state.rows {
		for _, row := range rows {
			if keep(row) {
				out = append(out, row)
			}
		}
	}
	return out
}
