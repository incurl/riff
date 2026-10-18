package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// FeedResponse is the JSON envelope returned by /api/po33/feed.
type FeedResponse struct {
	Items      []map[string]any `json:"items"`
	Page       int              `json:"page"`
	PerPage    int              `json:"per_page"`
	TotalItems int              `json:"total_items"`
}

// feedHandler serves GET /api/po33/feed.
//
// Query params (all optional):
//
//	page, per_page    standard pagination (defaults 1, 30; max 100)
//	space             restrict to a specific space uid or id;
//	                   "unassigned" means no space
//	tag               filter by memos.tags json array containing the tag
//
// The 1860xxx series dropped the `type` and
// `bpm_min`/`bpm_max` filters alongside the
// columns they referenced. The `type` filter
// was riff-only and the SPA never used it.
// `bpm_min`/`bpm_max` could not be ported to
// a JSON column without a post-query Go
// filter, and the SPA never used them either;
// they were a future-API stub that the
// realignment retired.
//
// Returns a paginated list of memos the caller can read. The same
// memos collection view rule applies: PRIVATE (own only),
// PROTECTED, PUBLIC, SPACE (active members only).
func feedHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}

	memos, err := e.App.FindCollectionByNameOrId("memos")
	if err != nil {
		return err
	}

	page, perPage, err := paginationFromQuery(e, 30, 100)
	if err != nil {
		return NewAPIError(http.StatusBadRequest, err.Error())
	}

	// Build the filter by composing user-supplied conditions.
	filter := ""
	params := map[string]any{}

	q := e.Request.URL.Query()
	if spaceQ := q.Get("space"); spaceQ != "" {
		if spaceQ == "unassigned" {
			if filter != "" {
				filter += " && "
			}
			filter += "space = null"
		} else {
			// Look up the space by uid; the URL is by uid
			// since the frontend uses uid slugs.
			spaces, err := e.App.FindRecordsByFilter("spaces", "uid={:uid}", "", 1, 0, map[string]any{"uid": spaceQ})
			if err != nil {
				return err
			}
			if len(spaces) == 0 {
				// Unknown space — empty result, not an error.
				return e.JSON(http.StatusOK, FeedResponse{
					Items: []map[string]any{}, Page: page, PerPage: perPage, TotalItems: 0,
				})
			}
			if filter != "" {
				filter += " && "
			}
			filter += "space = {:sid}"
			params["sid"] = spaces[0].Id
		}
	}
	if tagQ := q.Get("tag"); tagQ != "" {
		if filter != "" {
			filter += " && "
		}
		filter += "tags ?= {:tag}"
		params["tag"] = tagQ
	}

	// We over-fetch by a small factor to compensate for the
	// visibility filter applied below. Caller-side access
	// checks are mandatory because FindRecordsByFilter does
	// not apply access rules.
	overfetch := perPage * 2
	if overfetch < 60 {
		overfetch = 60
	}
	records, err := e.App.FindRecordsByFilter(memos, filter, "-created", overfetch, 0, params)
	if err != nil {
		return err
	}

	requestInfo, _ := e.RequestInfo()
	out := []map[string]any{}
	for _, r := range records {
		ok, err := e.App.CanAccessRecord(r, requestInfo, memos.ViewRule)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		out = append(out, r.PublicExport())
		if len(out) >= perPage {
			break
		}
	}

	return e.JSON(http.StatusOK, FeedResponse{
		Items:      out,
		Page:       page,
		PerPage:    perPage,
		TotalItems: len(out), // exact only when result set ≤ overfetch; this is an approximation
	})
}
