package api

import (
	"net/http"
	"strconv"

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
//	type              filter by memos.type (note, patch, sample_pack, link)
//	tag               filter by memos.tags json array containing the tag
//	bpm_min, bpm_max  inclusive range on memos.bpm
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
	if typeQ := q.Get("type"); typeQ != "" {
		if filter != "" {
			filter += " && "
		}
		filter += "type = {:type}"
		params["type"] = typeQ
	}
	if tagQ := q.Get("tag"); tagQ != "" {
		if filter != "" {
			filter += " && "
		}
		filter += "tags ?= {:tag}"
		params["tag"] = tagQ
	}
	if v := q.Get("bpm_min"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "bpm_min must be an integer")
		}
		if filter != "" {
			filter += " && "
		}
		filter += "bpm >= {:bmin}"
		params["bmin"] = float64(n)
	}
	if v := q.Get("bpm_max"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "bpm_max must be an integer")
		}
		if filter != "" {
			filter += " && "
		}
		filter += "bpm <= {:bmax}"
		params["bmax"] = float64(n)
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
