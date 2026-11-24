package tags

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// The setting keys the plugin reads.
//
// They are upstream's own keys, so an operator's saved settings carry
// over and the values mean what they mean upstream.
const (
	// SettingMinPrimaryTags is the least number of primary tags a
	// discussion must carry.
	SettingMinPrimaryTags = "flarum-tags.min_primary_tags"
	// SettingMaxPrimaryTags is the most number of primary tags a
	// discussion may carry.
	SettingMaxPrimaryTags = "flarum-tags.max_primary_tags"
	// SettingMinSecondaryTags is the least number of secondary tags a
	// discussion must carry.
	SettingMinSecondaryTags = "flarum-tags.min_secondary_tags"
	// SettingMaxSecondaryTags is the most number of secondary tags a
	// discussion may carry.
	SettingMaxSecondaryTags = "flarum-tags.max_secondary_tags"
)

// The upstream defaults, from the extension's own migration.
//
// A fresh instance has the same count rules an upstream one does: one
// primary tag, up to three secondary ones, and no secondary tag
// required.
const (
	defaultMinPrimaryTags   = 1
	defaultMaxPrimaryTags   = 1
	defaultMinSecondaryTags = 0
	defaultMaxSecondaryTags = 3
)

// tagCounts reads the four count settings.
//
// The settings table's `value` column is a JSON field, so a value
// arrives as RAW JSON BYTES rather than as a Go int — a reader that
// only handles the int reads every setting as its fallback.
//
// The settings are read through the APP rather than through a request
// event, because the relationship writer has no event: it runs from
// the mutation handler, and it is the write path that enforces the
// minimums.
func tagCounts(app core.App) (minPrimary, maxPrimary, minSecondary, maxSecondary int) {
	minPrimary = settingInt(app, SettingMinPrimaryTags, defaultMinPrimaryTags)
	maxPrimary = settingInt(app, SettingMaxPrimaryTags, defaultMaxPrimaryTags)
	minSecondary = settingInt(app, SettingMinSecondaryTags, defaultMinSecondaryTags)
	maxSecondary = settingInt(app, SettingMaxSecondaryTags, defaultMaxSecondaryTags)

	return minPrimary, maxPrimary, minSecondary, maxSecondary
}

// settingInt reads an integer forum setting.
func settingInt(app core.App, key string, fallback int) int {
	value, err := api.SettingValueAt(app, key, fallback)
	if err != nil {
		return fallback
	}

	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case []byte:
		return parseInt(string(typed), fallback)
	case string:
		return parseInt(typed, fallback)
	default:
		return fallback
	}
}

// parseInt reads an integer out of a textual value.
//
// The JSON quotes are stripped first, because a JSON-encoded string
// arrives as `"1"` — with the quotes — and a bare comparison against
// "1" would miss it.
func parseInt(raw string, fallback int) int {
	text := raw
	for len(text) > 0 && (text[0] == '"' || text[0] == ' ') {
		text = text[1:]
	}
	for len(text) > 0 && (text[len(text)-1] == '"' || text[len(text)-1] == ' ') {
		text = text[:len(text)-1]
	}

	value := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			return fallback
		}
		value = value*10 + int(r-'0')
	}

	if text == "" {
		return fallback
	}

	return value
}

// countTagsByKind counts a discussion's tags.
//
// Upstream's distinction is NOT "has a position": it is "has a
// position AND has no parent". A PRIMARY PARENT is a root tag with a
// position; everything else — a secondary tag, and a CHILD of a
// primary tag — counts towards the SECONDARY total.
//
// The difference matters: a child tag carries a position of its own
// (it is ordered among its siblings), so counting by position alone
// would count it as a primary tag and let a discussion satisfy the
// primary minimum with a tag that is not a root.
func countTagsByKind(tags []*domain.Tag) (primary, secondary int) {
	for _, tag := range tags {
		if tag.HasPosition() && tag.ParentID == "" {
			primary++
			continue
		}
		secondary++
	}

	return primary, secondary
}

// validateTagCounts refuses a tag set that breaks the count rules.
//
// It is upstream's `validateTagCount`: the count must be between the
// minimum and the maximum, and a minimum equal to the maximum is an
// exact count rather than a range.
//
// The rules are skipped for a caller who may BYPASS them, which is
// upstream's `bypassTagCounts` permission.
func validateTagCounts(app core.App, tags []*domain.Tag) error {
	if err := validateTagMinimums(app, tags); err != nil {
		return err
	}

	return validateTagMaximums(app, tags)
}

// validateTagMinimums refuses a tag set that is below the minimums.
func validateTagMinimums(app core.App, tags []*domain.Tag) error {
	minPrimary, _, minSecondary, _ := tagCounts(app)

	primary, secondary := countTagsByKind(tags)

	if err := checkTagCount("primary", primary, minPrimary, maxOf(minPrimary)); err != nil {
		return err
	}

	return checkTagCount("secondary", secondary, minSecondary, maxOf(minSecondary))
}

// validateTagMaximums refuses a tag set that is above the maximums.
func validateTagMaximums(app core.App, tags []*domain.Tag) error {
	_, maxPrimary, _, maxSecondary := tagCounts(app)

	primary, secondary := countTagsByKind(tags)

	if primary > maxPrimary {
		return errTagCount("primary", primary, 0, maxPrimary)
	}
	if secondary > maxSecondary {
		return errTagCount("secondary", secondary, 0, maxSecondary)
	}

	return nil
}

// bypassTagCounts reports whether the caller may skip the count rules.
//
// It is upstream's `bypassTagCounts` permission.
func bypassTagCounts(e *core.RequestEvent) bool {
	allowed, err := api.Can(e, domain.PermissionBypassTagCounts, api.TypeTag, "")
	if err != nil {
		return false
	}

	return allowed
}

// mayBypassTagCounts reports whether the caller may skip the count
// rules, through a permission check the caller supplies.
//
// It is the form the relationship writer uses, because it has no
// request event.
func mayBypassTagCounts(can func(action, subjectType, subjectID string) bool) bool {
	if can == nil {
		return false
	}

	return can(domain.PermissionBypassTagCounts, api.TypeTag, "")
}

// maxOf returns the value itself, so a minimum can be checked as an
// exact count.
func maxOf(value int) int { return value }

// checkTagCount checks one kind's count against its bounds.
func checkTagCount(kind string, count, min, max int) error {
	if min == max {
		if count == min {
			return nil
		}
		return errTagCount(kind, count, min, max)
	}

	if count < min {
		return errTagCount(kind, count, min, max)
	}
	if count > max {
		return errTagCount(kind, count, min, max)
	}

	return nil
}

// errTagCount reports a tag set that breaks the count rules.
func errTagCount(kind string, count, min, max int) error {
	return &tagCountError{kind: kind, count: count, min: min, max: max}
}

// tagCountError is a tag count that breaks the rules.
type tagCountError struct {
	// kind is "primary" or "secondary".
	kind string
	// count is the number of tags of that kind.
	count int
	// min is the least allowed.
	min int
	// max is the most allowed.
	max int
}

// Error renders the failure.
func (e *tagCountError) Error() string {
	if e.min == e.max {
		return "The number of " + e.kind + " tags must be exactly " + itoa(e.min) + "."
	}

	return "The number of " + e.kind + " tags must be between " + itoa(e.min) + " and " + itoa(e.max) + "."
}

// itoa renders an integer.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	negative := value < 0
	if negative {
		value = -value
	}

	digits := make([]byte, 0, 12)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}

	if negative {
		return "-" + string(digits)
	}

	return string(digits)
}

// checkCountsAfterAttaching refuses an attachment that breaks the
// MAXIMUM counts.
//
// Only the maxima bind an incremental change. A MINIMUM is a property
// of a discussion's FINISHED tag set, and the discussion write path is
// what enforces it: upstream validates the counts in the `tags`
// relationship setter, which sees the whole set at once. Checking the
// minimum here would refuse the first attachment to a discussion that
// has no primary tag yet — even though the author is about to add
// one — and the reader would see a validation error for a tag set that
// is not finished.
func (p *Plugin) checkCountsAfterAttaching(e *core.RequestEvent, discussionID string, tag *domain.Tag) error {
	if bypassTagCounts(e) {
		return nil
	}

	tags, err := p.discussionTags(e, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be read.")
	}

	for _, existing := range tags {
		if existing.ID == tag.ID {
			// The tag is already attached: the set does not change.
			return nil
		}
	}

	if err := validateTagMaximums(e.App, append(tags, tag)); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}

	return nil
}

// checkCountsAfterDetaching refuses a removal that breaks the count
// rules.
//
// Only the MAXIMA could be broken by an addition, so only the maxima
// are checked here — a removal can never break them.
//
// The MINIMUM is NOT checked on a removal, and that is upstream's
// shape: the minimum is a property of the tag set a discussion is
// CREATED or UPDATED with, and it is enforced by the discussion write
// path, which sees the whole set at once. A discussion can therefore
// carry no tags at all, which is what an untagged forum looks like, and
// refusing to remove the last tag would trap the author with it.
func (p *Plugin) checkCountsAfterDetaching(e *core.RequestEvent, discussionID, tagID string) error {
	if bypassTagCounts(e) {
		return nil
	}

	tags, err := p.discussionTags(e, discussionID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The tags could not be read.")
	}

	remaining := make([]*domain.Tag, 0, len(tags))
	for _, tag := range tags {
		if tag.ID == tagID {
			continue
		}
		remaining = append(remaining, tag)
	}

	if err := validateTagMaximums(e.App, remaining); err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}

	return nil
}

// discussionTags returns a discussion's tags.
func (*Plugin) discussionTags(e *core.RequestEvent, discussionID string) ([]*domain.Tag, error) {
	ids, err := discussionTagIDs(e.App, discussionID)
	if err != nil {
		return nil, err
	}

	tags := make([]*domain.Tag, 0, len(ids))
	for _, id := range ids {
		tag, err := findTag(e.App, physicalName(e), id)
		if err != nil {
			// A link to a tag that no longer exists is skipped rather
			// than failing the whole request.
			continue
		}
		tags = append(tags, tag)
	}

	return tags, nil
}
