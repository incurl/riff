package leafwiki

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// searchHandler serves GET /api/search.
func (s *Server) searchHandler(e *core.RequestEvent) error {
	query := e.Request.URL.Query()

	limit := parseIntDefault(query.Get("limit"), 10)
	if limit < 1 || limit > 100 {
		limit = 10
	}
	offset := parseIntDefault(query.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	return e.JSON(http.StatusOK, s.index.search(query.Get("q"), query["tags"], limit, offset))
}

// parseIntDefault parses an integer query parameter.
func parseIntDefault(raw string, fallback int) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return fallback
	}
	return parsed
}

// tagsHandler serves GET /api/tags.
func (s *Server) tagsHandler(e *core.RequestEvent) error {
	query := e.Request.URL.Query()
	limit := parseIntDefault(query.Get("limit"), 50)
	return e.JSON(http.StatusOK, s.index.tagFacets(query.Get("q"), limit, query["selected"]))
}

// taggedPage is one entry of GET /api/tags/pages.
type taggedPage struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Path       string     `json:"path"`
	Excerpt    string     `json:"excerpt,omitempty"`
	Tags       []string   `json:"tags"`
	UpdatedAt  string     `json:"updatedAt,omitempty"`
	LastAuthor *UserLabel `json:"lastAuthor,omitempty"`
}

// pagesByTagsHandler serves GET /api/tags/pages.
func (s *Server) pagesByTagsHandler(e *core.RequestEvent) error {
	matches := s.index.pagesByTags(e.Request.URL.Query()["tags"])

	ids := make([]string, 0, len(matches))
	for _, entry := range matches {
		ids = append(ids, entry.LastAuthorID)
	}
	labels := s.userLabels(ids)

	pages := make([]taggedPage, 0, len(matches))
	for _, entry := range matches {
		tags := entry.Tags
		if tags == nil {
			tags = []string{}
		}
		page := taggedPage{
			ID:        entry.PageID,
			Title:     entry.Title,
			Path:      entry.Path,
			Excerpt:   entry.Excerpt,
			Tags:      tags,
			UpdatedAt: entry.UpdatedAt,
		}
		if label, ok := labels[entry.LastAuthorID]; ok {
			page.LastAuthor = &UserLabel{ID: label.ID, Username: label.Username}
		}
		pages = append(pages, page)
	}
	return e.JSON(http.StatusOK, pages)
}

// backlink is an incoming link.
type backlink struct {
	FromPageID string `json:"from_page_id"`
	FromPath   string `json:"from_path"`
	ToPageID   string `json:"to_page_id"`
	FromTitle  string `json:"from_title"`
	Broken     bool   `json:"broken"`
}

// outgoingLink is an outgoing link.
type outgoingLink struct {
	FromPageID  string `json:"from_page_id"`
	ToPageID    string `json:"to_page_id"`
	ToPath      string `json:"to_path"`
	ToPageTitle string `json:"to_page_title"`
	Broken      bool   `json:"broken"`
}

// linkStatusCounts is the counter block of the link status.
type linkStatusCounts struct {
	Backlinks       int `json:"backlinks"`
	BrokenIncoming  int `json:"broken_incoming"`
	Outgoings       int `json:"outgoings"`
	BrokenOutgoings int `json:"broken_outgoings"`
}

// linkStatusResult is the payload of GET /api/pages/{id}/links.
type linkStatusResult struct {
	Backlinks       []backlink       `json:"backlinks"`
	BrokenIncoming  []backlink       `json:"broken_incoming"`
	Outgoings       []outgoingLink   `json:"outgoings"`
	BrokenOutgoings []outgoingLink   `json:"broken_outgoings"`
	Counts          linkStatusCounts `json:"counts"`
}

// linkStatusHandler serves the backlink panel of a page.
func (s *Server) linkStatusHandler(e *core.RequestEvent) error {
	page, err := s.tree.Page(pathValue(e, "id"))
	if err != nil {
		return s.fail(e, err)
	}

	result := linkStatusResult{
		Backlinks:       []backlink{},
		BrokenIncoming:  []backlink{},
		Outgoings:       []outgoingLink{},
		BrokenOutgoings: []outgoingLink{},
	}

	for _, row := range s.index.linksTo(page.ID) {
		source, ok := s.index.pageByID(row.FromPageID)
		if !ok {
			continue
		}
		result.Backlinks = append(result.Backlinks, backlink{
			FromPageID: source.PageID,
			FromPath:   source.Path,
			ToPageID:   page.ID,
			FromTitle:  source.Title,
			Broken:     false,
		})
	}

	for _, row := range s.index.linksToPath(page.Path) {
		if row.State != linkStateBroken {
			continue
		}
		source, ok := s.index.pageByID(row.FromPageID)
		if !ok {
			continue
		}
		result.BrokenIncoming = append(result.BrokenIncoming, backlink{
			FromPageID: source.PageID,
			FromPath:   source.Path,
			FromTitle:  source.Title,
			Broken:     true,
		})
	}

	for _, row := range s.index.linksFrom(page.ID) {
		outgoing := outgoingLink{
			FromPageID:  page.ID,
			ToPageID:    row.ToPageID,
			ToPath:      row.ToPath,
			ToPageTitle: row.ToTitle,
			Broken:      row.State == linkStateBroken,
		}
		if target, ok := s.index.pageByID(row.ToPageID); ok {
			outgoing.ToPath = target.Path
			outgoing.ToPageTitle = target.Title
			outgoing.Broken = false
			result.Outgoings = append(result.Outgoings, outgoing)
			continue
		}
		if outgoing.ToPageTitle == "" {
			outgoing.ToPageTitle = outgoing.ToPath
		}
		result.BrokenOutgoings = append(result.BrokenOutgoings, outgoing)
	}

	result.Counts = linkStatusCounts{
		Backlinks:       len(result.Backlinks),
		BrokenIncoming:  len(result.BrokenIncoming),
		Outgoings:       len(result.Outgoings),
		BrokenOutgoings: len(result.BrokenOutgoings),
	}
	return e.JSON(http.StatusOK, result)
}

// brokenLink is one row of the broken-link audit.
type brokenLink struct {
	FromPageID string `json:"from_page_id"`
	FromPath   string `json:"from_path"`
	FromTitle  string `json:"from_title"`
	ToPath     string `json:"to_path"`
}

// brokenLinksHandler serves the admin broken-link audit.
func (s *Server) brokenLinksHandler(e *core.RequestEvent) error {
	links := make([]brokenLink, 0, 8)
	for _, row := range s.index.brokenLinks() {
		source, ok := s.index.pageByID(row.FromPageID)
		if !ok {
			continue
		}
		toPath := strings.TrimPrefix(row.ToPath, wikilinkSentinelPrefix)
		links = append(links, brokenLink{
			FromPageID: source.PageID,
			FromPath:   source.Path,
			FromTitle:  source.Title,
			ToPath:     toPath,
		})
	}
	return e.JSON(http.StatusOK, map[string]any{"links": links})
}
