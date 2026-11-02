package leafwiki

import (
	"cmp"
	"slices"
	"strings"
)

// tagCount is one facet of a tag list.
type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// searchResultItem is one hit of a search.
type searchResultItem struct {
	PageID  string   `json:"page_id"`
	Path    string   `json:"path"`
	Title   string   `json:"title"`
	Kind    NodeKind `json:"kind"`
	Excerpt string   `json:"excerpt"`
	Rank    int      `json:"rank"`
	Tags    []string `json:"tags"`
}

// searchResult is the payload of GET /api/search.
type searchResult struct {
	Count     int                `json:"count"`
	Items     []searchResultItem `json:"items"`
	Limit     int                `json:"limit"`
	Offset    int                `json:"offset"`
	TagFacets []tagCount         `json:"tag_facets"`
}

// Search finds pages by text and/or tag set.
//
// Ranking is deliberately simple and deterministic: a title hit outranks a
// body hit, then the more recently updated page wins. LeafWiki's SQLite index
// produced a bm25-style score; nothing in the SPA depends on its exact value,
// only on the ordering, which this preserves.
func (i *index) search(query string, tags []string, limit, offset int) searchResult {
	needle := strings.ToLower(strings.TrimSpace(query))
	selected := normaliseTagSet(tags)

	result := searchResult{Items: []searchResultItem{}, Limit: limit, Offset: offset, TagFacets: []tagCount{}}
	if needle == "" && len(selected) == 0 {
		return result
	}

	type hit struct {
		entry indexedPage
		rank  int
	}

	hits := make([]hit, 0, 16)
	for _, entry := range i.allPages() {
		if !hasAllTags(entry.Tags, selected) {
			continue
		}
		rank := 0
		if needle != "" {
			if strings.Contains(strings.ToLower(entry.Title), needle) {
				rank = 2
			} else if strings.Contains(strings.ToLower(entry.Content), needle) {
				rank = 1
			} else {
				continue
			}
		}
		hits = append(hits, hit{entry: entry, rank: rank})
	}

	slices.SortStableFunc(hits, func(a, b hit) int {
		if byRank := cmp.Compare(b.rank, a.rank); byRank != 0 {
			return byRank
		}
		if byUpdated := cmp.Compare(b.entry.UpdatedAt, a.entry.UpdatedAt); byUpdated != 0 {
			return byUpdated
		}
		return strings.Compare(strings.ToLower(a.entry.Title), strings.ToLower(b.entry.Title))
	})

	result.Count = len(hits)
	result.TagFacets = facetCounts(hits, func(h hit) []string { return h.entry.Tags })

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 10
	}
	for index := offset; index < len(hits) && index < offset+limit; index++ {
		entry := hits[index].entry
		tags := entry.Tags
		if tags == nil {
			tags = []string{}
		}
		result.Items = append(result.Items, searchResultItem{
			PageID:  entry.PageID,
			Path:    entry.Path,
			Title:   entry.Title,
			Kind:    entry.Kind,
			Excerpt: entry.Excerpt,
			Rank:    hits[index].rank,
			Tags:    tags,
		})
	}
	return result
}

// normaliseTagSet lower-cases, trims and de-duplicates a tag list.
func normaliseTagSet(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	normalised := make([]string, 0, len(tags))
	for _, tag := range tags {
		value := strings.ToLower(strings.TrimSpace(tag))
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalised = append(normalised, value)
	}
	return normalised
}

// hasAllTags reports whether a page carries every tag of the set.
func hasAllTags(pageTags, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	present := make(map[string]struct{}, len(pageTags))
	for _, tag := range pageTags {
		present[strings.ToLower(strings.TrimSpace(tag))] = struct{}{}
	}
	for _, tag := range selected {
		if _, ok := present[tag]; !ok {
			return false
		}
	}
	return true
}

// facetCounts counts the tags of a hit list.
func facetCounts[T any](hits []T, tagsOf func(T) []string) []tagCount {
	counts := map[string]int{}
	for _, hit := range hits {
		for _, tag := range tagsOf(hit) {
			value := strings.ToLower(strings.TrimSpace(tag))
			if value == "" {
				continue
			}
			counts[value]++
		}
	}
	return sortedTagCounts(counts, "")
}

// sortedTagCounts turns a count map into a deterministic facet list.
func sortedTagCounts(counts map[string]int, filter string) []tagCount {
	needle := strings.ToLower(strings.TrimSpace(filter))
	facets := make([]tagCount, 0, len(counts))
	for tag, count := range counts {
		if needle != "" && !strings.Contains(tag, needle) {
			continue
		}
		facets = append(facets, tagCount{Tag: tag, Count: count})
	}
	slices.SortStableFunc(facets, compareTagCounts)
	return facets
}

// Tags returns tag facets across every indexed page. Tags in selected are
// always included, even when they do not match filter, so the chip the user
// just picked never disappears from the list.
func (i *index) tagFacets(filter string, limit int, selected []string) []tagCount {
	counts := map[string]int{}
	for _, entry := range i.allPages() {
		for _, tag := range entry.Tags {
			value := strings.ToLower(strings.TrimSpace(tag))
			if value == "" {
				continue
			}
			counts[value]++
		}
	}

	required := normaliseTagSet(selected)
	facets := sortedTagCounts(counts, filter)

	if len(required) == 0 {
		if limit > 0 && len(facets) > limit {
			facets = facets[:limit]
		}
		return facets
	}

	requiredSet := make(map[string]struct{}, len(required))
	for _, tag := range required {
		requiredSet[tag] = struct{}{}
	}

	kept := make([]tagCount, 0, len(facets)+len(required))
	for _, facet := range facets {
		if _, alreadySelected := requiredSet[facet.Tag]; alreadySelected {
			continue
		}
		kept = append(kept, facet)
	}
	for _, tag := range required {
		kept = append(kept, tagCount{Tag: tag, Count: counts[tag]})
	}
	slices.SortStableFunc(kept, compareTagCounts)
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}

// PagesByTags returns every page carrying all of the given tags.
func (i *index) pagesByTags(tags []string) []indexedPage {
	selected := normaliseTagSet(tags)
	if len(selected) == 0 {
		return []indexedPage{}
	}
	matches := make([]indexedPage, 0, 16)
	for _, entry := range i.allPages() {
		if hasAllTags(entry.Tags, selected) {
			matches = append(matches, entry)
		}
	}
	slices.SortStableFunc(matches, func(a, b indexedPage) int {
		if byUpdated := cmp.Compare(b.UpdatedAt, a.UpdatedAt); byUpdated != 0 {
			return byUpdated
		}
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
	return matches
}

// compareTagCounts orders facets by count (descending) and then by tag.
func compareTagCounts(a, b tagCount) int {
	if byCount := cmp.Compare(b.Count, a.Count); byCount != 0 {
		return byCount
	}
	return strings.Compare(a.Tag, b.Tag)
}
