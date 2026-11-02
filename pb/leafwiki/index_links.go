package leafwiki

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// linkRow is one row of the backlink index.
type linkRow struct {
	FromPageID string
	ToPageID   string
	ToPath     string
	ToTitle    string
	State      string
}

// rebuildLinks drops and rebuilds the link table for the given pages.
//
// Rebuilding rather than patching is deliberate: a link's state depends on the
// whole tree. Creating a page heals every link that points at its path, and
// deleting one breaks them; recomputing is the only way to get both right
// without a per-mutation bookkeeping graph.
func (i *index) rebuildLinks(pages []*Page) {
	if _, err := i.app.FindCollectionByNameOrId(linksCollection); err != nil {
		return
	}

	pathToID := make(map[string]string, len(pages))
	titleToIDs := make(map[string][]string, len(pages))
	for _, page := range pages {
		pathToID[normaliseWikiPath(page.Path)] = page.ID
		key := strings.ToLower(strings.TrimSpace(page.Title))
		if key == "" {
			continue
		}
		titleToIDs[key] = append(titleToIDs[key], page.ID)
	}

	rows := make([]linkRow, 0, len(pages)*2)
	seen := make(map[string]struct{}, len(pages)*2)

	appendRow := func(row linkRow) {
		// One row per (source, target) pair: a page that links to the same
		// target twice — once as a Markdown link and once as a wiki link,
		// say — must show up once in the backlink panel.
		key := row.FromPageID + "\x00"
		if row.ToPageID != "" {
			key += "id:" + row.ToPageID
		} else {
			key += "path:" + row.ToPath
		}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		rows = append(rows, row)
	}

	for _, page := range pages {
		for _, target := range ExtractLinks(page.Content) {
			switch {
			case target.Path != "":
				toPath := normaliseWikiPath(target.Path)
				row := linkRow{FromPageID: page.ID, ToPath: toPath}
				if id, ok := pathToID[toPath]; ok {
					row.ToPageID = id
					row.State = linkStateResolved
				} else {
					row.State = linkStateBroken
				}
				appendRow(row)

			case target.Title != "":
				key := strings.ToLower(strings.TrimSpace(target.Title))
				matches := titleToIDs[key]
				if len(matches) == 0 {
					appendRow(linkRow{
						FromPageID: page.ID,
						ToPath:     wikilinkSentinelPrefix + target.Title,
						ToTitle:    target.Title,
						State:      linkStateBroken,
					})
					continue
				}
				for _, toID := range matches {
					appendRow(linkRow{
						FromPageID: page.ID,
						ToPageID:   toID,
						ToTitle:    target.Title,
						State:      linkStateResolved,
					})
				}

			default:
				// An extracted link always has a path or a title; anything
				// else is not indexable.
			}
		}
	}

	err := i.app.RunInTransaction(func(txApp core.App) error {
		existing, findErr := txApp.FindAllRecords(linksCollection)
		if findErr != nil {
			return errors.Wrap(findErr, "leafwiki: could not load the link index")
		}
		for _, record := range existing {
			if deleteErr := txApp.Delete(record); deleteErr != nil {
				return errors.Wrap(deleteErr, "leafwiki: could not clear the link index")
			}
		}

		collection, collectionErr := txApp.FindCollectionByNameOrId(linksCollection)
		if collectionErr != nil {
			return collectionErr
		}
		for _, row := range rows {
			record := core.NewRecord(collection)
			record.Set("from_page_id", row.FromPageID)
			record.Set("to_page_id", row.ToPageID)
			record.Set("to_path", row.ToPath)
			record.Set("to_title", row.ToTitle)
			record.Set("state", row.State)
			if saveErr := txApp.Save(record); saveErr != nil {
				return errors.Wrap(saveErr, "leafwiki: could not write a link row")
			}
		}
		return nil
	})
	if err != nil {
		i.log.Warn("could not rebuild the link index", "error", err)
	}
}

// linksFrom returns the outgoing links of a page.
func (i *index) linksFrom(pageID string) []linkRow {
	return i.findLinks("from_page_id = {:id}", map[string]any{"id": pageID})
}

// linksTo returns the links pointing at a page.
func (i *index) linksTo(pageID string) []linkRow {
	return i.findLinks("to_page_id = {:id}", map[string]any{"id": pageID})
}

// linksToPath returns the links pointing at a route path, resolved or not.
func (i *index) linksToPath(path string) []linkRow {
	return i.findLinks("to_path = {:path}", map[string]any{"path": normaliseWikiPath(path)})
}

// brokenLinks returns every unresolved link.
func (i *index) brokenLinks() []linkRow {
	return i.findLinks("state = {:state}", map[string]any{"state": linkStateBroken})
}

// findLinks runs a link query.
func (i *index) findLinks(filter string, params map[string]any) []linkRow {
	if i == nil || i.app == nil {
		return nil
	}
	records, err := i.app.FindRecordsByFilter(linksCollection, filter, "", 0, 0, params)
	if err != nil {
		return nil
	}
	rows := make([]linkRow, 0, len(records))
	for _, record := range records {
		rows = append(rows, linkRow{
			FromPageID: record.GetString("from_page_id"),
			ToPageID:   record.GetString("to_page_id"),
			ToPath:     record.GetString("to_path"),
			ToTitle:    record.GetString("to_title"),
			State:      record.GetString("state"),
		})
	}
	return rows
}
