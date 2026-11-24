// Package api implements the forum's JSON:API surface.
//
// The wire format is Flarum's, because the reused frontend speaks it: a
// document is `{"data": ...}` with `included`, `links` and `meta` beside
// it, and every response carries the `application/vnd.api+json` content
// type. Errors are `{"errors": [{"status", "code", "detail"}]}`.
//
// The resource types are `forums`, `users`, `groups`, `discussions`,
// `posts`, `notifications`, plus the plugin-owned `tags`.
package api

import (
	"encoding/json"
)

// Document is a JSON:API top-level document.
type Document struct {
	// Data is the primary resource (or resource collection).
	Data any `json:"data"`
	// Included holds the related resources the client asked for.
	Included []Resource `json:"included,omitempty"`
	// Links carries pagination links.
	Links *Links `json:"links,omitempty"`
	// Meta carries pagination counts and other metadata.
	Meta *Meta `json:"meta,omitempty"`
}

// Links carries the pagination links of a collection response.
type Links struct {
	// First is the URL of the first page.
	First string `json:"first,omitempty"`
	// Next is the URL of the next page, absent on the last page.
	Next string `json:"next,omitempty"`
	// Prev is the URL of the previous page, absent on the first page.
	Prev string `json:"prev,omitempty"`
}

// Meta carries the metadata of a response.
//
// The frontend reads `meta.page.total` to render a page count and
// `meta.perPage` to learn the page size, so both are always present on
// a collection response.
type Meta struct {
	// Page carries the pagination metadata.
	Page *PageMeta `json:"page,omitempty"`
	// PerPage is the number of records in the page.
	//
	// The frontend reads it to size its own pages: the list state
	// falls back to its default page size when the server does not
	// say how many records a page holds, and a server that pages by a
	// different size would then fetch the wrong window.
	PerPage int `json:"perPage,omitempty"`
	// Extra holds any additional metadata.
	Extra map[string]any `json:"-"`
}

// PageMeta is the pagination metadata of a collection response.
type PageMeta struct {
	// Total is the number of records matching the query, ignoring
	// pagination.
	Total int `json:"total"`
	// Offset is the number of records the page starts after.
	//
	// The frontend reads it to work out the offset of a page it did
	// not request itself (the post stream's window arithmetic), so a
	// page that does not say where it starts cannot be placed.
	Offset int `json:"offset"`
	// Limit is the number of records the page holds.
	Limit int `json:"limit"`
}

// Resource is a JSON:API resource object.
type Resource struct {
	// Type is the resource type, e.g. "discussions".
	Type string `json:"type"`
	// ID is the resource's id.
	ID string `json:"id"`
	// Attributes are the resource's fields.
	Attributes map[string]any `json:"attributes,omitempty"`
	// Relationships are the resource's related resources.
	Relationships map[string]Relationship `json:"relationships,omitempty"`
}

// Relationship is a JSON:API relationship object.
//
// The frontend's Model.hasOne/hasMany read `data` directly, so a
// relationship always carries its identifier(s) even when the related
// resource itself was not included.
type Relationship struct {
	// Data holds the related resource identifier(s).
	Data any `json:"data"`
	// Meta carries relationship metadata.
	Meta map[string]any `json:"meta,omitempty"`
}

// Identifier is a JSON:API resource identifier object.
type Identifier struct {
	// Type is the resource type.
	Type string `json:"type"`
	// ID is the resource's id.
	ID string `json:"id"`
}

// MarshalJSON renders a meta map that also carries the pagination block.
//
// The pagination metadata and any extra metadata share the `meta` object,
// which is what the JSON:API specification asks for and what the
// frontend's `ApiQueryParams.meta` type describes.
func (m Meta) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for key, value := range m.Extra {
		out[key] = value
	}
	if m.Page != nil {
		out["page"] = m.Page
	}
	// The page size travels beside the page block: the frontend reads
	// `meta.perPage` to size its own pages, and it is a SIBLING of
	// `meta.page`, not a member of it.
	if m.PerPage > 0 {
		out["perPage"] = m.PerPage
	}
	return json.Marshal(out)
}
