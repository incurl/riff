package api

import (
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

// The pagination metadata and links of a collection response.
//
// The frontend reads FOUR of them, and every one of them decides
// something visible:
//
//   - `meta.page.total` — how many records match, for the page count;
//   - `meta.page.offset` — where the page starts, for the post stream's
//     window arithmetic (the frontend reads
//     `page.items.payload.meta?.page?.offset`);
//   - `meta.perPage` — how many records a page holds, so the list state
//     sizes its own pages;
//   - `links.next` / `links.prev` — WHETHER THERE IS ANOTHER PAGE.
//
// **THE LAST ONE IS THE "Load more" BUTTON.** The frontend decides
// whether to offer it with `hasNext: !!links?.next`
// (framework/core/js/src/common/states/PaginatedListState.ts:147), and
// the button is only rendered when `state.hasNext()` is true
// (forum/components/PostList.tsx:25). Without `links.next` the
// infinite scroll never loads a second page: the list shows the first
// twenty records and stops.
//
// The links are the URLs of the neighbouring pages, and they are
// absolute: upstream's are too (`$pagination->links()`).

// Paginate builds the metadata and the links of a collection response.
//
// It is the exported form of [pagination], for a plugin's own listing: the
// uploads library paginates exactly like the core's lists, and the
// frontend reads the same four fields from both (`meta.page.total`,
// `meta.page.offset`, `meta.perPage`, and `links.next`/`links.prev` —
// the last of which IS the "load more" button).
func Paginate(e *core.RequestEvent, total, limit, offset int) (*Meta, *Links) {
	return pagination(e, total, limit, offset)
}

// pagination builds the metadata and the links of a collection
// response.
//
// The `total` is the number of records matching the query, ignoring the
// pagination, and `limit`/`offset` are the page's own window.
func pagination(e *core.RequestEvent, total, limit, offset int) (*Meta, *Links) {
	meta := &Meta{
		Page: &PageMeta{
			Total:  total,
			Offset: offset,
			Limit:  limit,
		},
		PerPage: limit,
	}

	return meta, paginationLinks(e, total, limit, offset)
}

// paginationLinks builds the `next` and `prev` links of a page.
//
// A link is present only when there IS a page to go to: `next` is
// absent on the last page and `prev` on the first, and the frontend
// reads their ABSENCE ("hasNext: !!links?.next"), so an empty string
// would read as "there is a next page".
func paginationLinks(e *core.RequestEvent, total, limit, offset int) *Links {
	if limit <= 0 {
		return nil
	}

	links := &Links{}

	// The next page exists when the page does not reach the end: the
	// total is the authority.
	if offset+limit < total {
		links.Next = pageURL(e, offset+limit, limit)
	}
	if offset > 0 {
		previous := offset - limit
		if previous < 0 {
			previous = 0
		}
		links.Prev = pageURL(e, previous, limit)
	}

	if links.Next == "" && links.Prev == "" {
		return nil
	}
	return links
}

// pageURL builds the URL of a page.
//
// It is the request's own URL with the offset replaced, so the query
// the caller sent (the filters, the sort, the include) is preserved:
// the next page of a filtered listing is the next page OF THAT LISTING.
func pageURL(e *core.RequestEvent, offset, limit int) string {
	if e == nil || e.Request == nil {
		return ""
	}

	target := *e.Request.URL

	query := target.Query()
	query.Set("page[offset]", strconv.Itoa(offset))
	query.Set("page[limit]", strconv.Itoa(limit))
	target.RawQuery = query.Encode()

	return target.String()
}
