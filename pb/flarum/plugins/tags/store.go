package tags

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
)

// tagRecord projects a tag record onto the domain model.
func tagRecord(record *core.Record) *domain.Tag {
	return &domain.Tag{
		ID:                   record.Id,
		Name:                 record.GetString("name"),
		Slug:                 record.GetString("slug"),
		Description:          record.GetString("description"),
		ParentID:             record.GetString("parent"),
		Color:                record.GetString("color"),
		Icon:                 record.GetString("icon"),
		Position:             record.GetInt("position"),
		IsHidden:             record.GetBool("is_hidden"),
		IsPrimary:            record.GetBool("is_primary"),
		IsRestricted:         record.GetBool("is_restricted"),
		DiscussionCount:      record.GetInt("discussion_count"),
		DefaultSort:          record.GetString("default_sort"),
		LastPostedAt:         record.GetDateTime("last_posted_at").Time(),
		LastPostedDiscussion: record.GetString("last_posted_discussion"),
		LastPostedUserID:     record.GetString("last_posted_user"),
	}
}

// listTags returns the tags, optionally narrowed to a search prefix.
//
// The search is upstream's `TagSearcher` + the extension's `FulltextFilter`:
// a PREFIX match on the NAME or the SLUG, case-insensitively — not a
// substring match. `filter[q]=gui` finds "Guides" and "guides", not
// "Beginners Guide".
//
// It filters in memory rather than in the query on purpose: `listTags` already
// reads every record (the tag list is a taxonomy, not a feed), and a SQL
// prefix match would mean depending on how PocketBase decides to wrap a LIKE
// parameter — which is the caller's business to escape, and one more thing to
// get wrong for a list this small.
func listTags(app core.App, physical, search string) ([]*domain.Tag, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection)
	if err != nil {
		return nil, err
	}

	prefix := strings.ToLower(strings.TrimSpace(search))

	tags := make([]*domain.Tag, 0, len(records))
	for _, record := range records {
		tag := tagRecord(record)

		if prefix != "" && !hasPrefixFold(tag.Name, prefix) && !hasPrefixFold(tag.Slug, prefix) {
			continue
		}

		tags = append(tags, tag)
	}

	return tags, nil
}

// hasPrefixFold reports whether a value starts with an already-lowercased
// prefix, ignoring case.
func hasPrefixFold(value, lowercasePrefix string) bool {
	return strings.HasPrefix(strings.ToLower(value), lowercasePrefix)
}

// findTag returns a tag by id or slug.
func findTag(app core.App, physical, idOrSlug string) (*domain.Tag, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, idOrSlug)
	if err != nil {
		record, err = app.FindFirstRecordByFilter(collection, "slug = {:slug}", map[string]any{"slug": idOrSlug})
		if err != nil {
			return nil, errors.Wrap(errNotFound, "tags: the tag does not exist")
		}
	}
	return tagRecord(record), nil
}

// discussionTagIDs returns the ids of a discussion's tags.
func discussionTagIDs(app core.App, discussionID string) ([]string, error) {
	physical := "forum_discussion_tags"
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		// The link table is missing: the discussion has no tags.
		return nil, nil //nolint:nilerr // a missing table means no tags
	}
	records, err := app.FindRecordsByFilter(collection, "discussion = {:discussion}", "", 0, 0,
		map[string]any{"discussion": discussionID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.GetString("tag"))
	}
	return ids, nil
}

// attachTag attaches a tag to a discussion.
//
// It is idempotent: attaching a tag the discussion already carries
// succeeds without creating a second link.
func attachTag(app core.App, discussionID, tagID string) error {
	collection, err := app.FindCollectionByNameOrId("forum_discussion_tags")
	if err != nil {
		return errors.Wrap(err, "tags: the discussion_tags collection is missing")
	}

	existing, err := app.FindFirstRecordByFilter(collection,
		"discussion = {:discussion} && tag = {:tag}",
		map[string]any{"discussion": discussionID, "tag": tagID})
	if err == nil && existing != nil {
		return nil
	}

	record := core.NewRecord(collection)
	record.Set("discussion", discussionID)
	record.Set("tag", tagID)
	return app.Save(record)
}

// detachTag removes a tag from a discussion.
func detachTag(app core.App, discussionID, tagID string) error {
	collection, err := app.FindCollectionByNameOrId("forum_discussion_tags")
	if err != nil {
		return errors.Wrap(err, "tags: the discussion_tags collection is missing")
	}
	record, err := app.FindFirstRecordByFilter(collection,
		"discussion = {:discussion} && tag = {:tag}",
		map[string]any{"discussion": discussionID, "tag": tagID})
	if err != nil {
		// The link is already gone: detaching twice is not a failure.
		return nil //nolint:nilerr // an absent link is already detached
	}
	return app.Delete(record)
}

// createTag creates a tag.
//
// The position is assigned by the caller, because upstream's rule is
// positional: a PRIMARY tag is placed after the last primary tag of
// its parent, and a secondary tag has no position at all.
func createTag(app core.App, physical string, tag *domain.Tag) (*domain.Tag, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	record := core.NewRecord(collection)
	applyTag(record, tag)

	if err := app.Save(record); err != nil {
		return nil, errors.Wrap(err, "tags: the tag could not be saved")
	}

	return tagRecord(record), nil
}

// updateTag writes a tag's fields.
//
// It returns the updated tag, and whether the tag was RESTRICTED and is
// now not: upstream deletes the tag's scoped permission rows when it
// stops being restricted, because those rows grant access to a scope
// that no longer exists.
func updateTag(app core.App, physical, tagID string, tag *domain.Tag) (*domain.Tag, bool, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, false, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, tagID)
	if err != nil {
		return nil, false, errors.Wrap(errNotFound, "tags: the tag does not exist")
	}

	wasRestricted := record.GetBool("is_restricted")

	applyTag(record, tag)

	if err := app.Save(record); err != nil {
		return nil, false, errors.Wrap(err, "tags: the tag could not be saved")
	}

	// A tag that stops being restricted keeps its scoped permission
	// rows otherwise: the rows would grant a scope that no longer
	// exists, and re-restricting the tag later would silently restore
	// an old access list.
	unrestricted := wasRestricted && !record.GetBool("is_restricted")

	return tagRecord(record), unrestricted, nil
}

// deleteTag removes a tag.
//
// The discussion links are removed with it, and the tag's scoped
// permission rows go too — upstream deletes them on delete, because a
// scoped row for a tag that no longer exists is dead weight that
// reappears if the id is ever reused.
func deleteTag(app core.App, physical, tagID string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, tagID)
	if err != nil {
		return errors.Wrap(errNotFound, "tags: the tag does not exist")
	}

	if err := deleteTagLinks(app, tagID); err != nil {
		return err
	}

	if err := app.Delete(record); err != nil {
		return errors.Wrap(err, "tags: the tag could not be deleted")
	}

	return nil
}

// deleteTagLinks removes a tag's discussion links.
func deleteTagLinks(app core.App, tagID string) error {
	collection, err := app.FindCollectionByNameOrId("forum_discussion_tags")
	if err != nil {
		// The link table is missing: there is nothing to remove.
		return nil //nolint:nilerr // a missing table means no links
	}

	records, err := app.FindRecordsByFilter(collection, "tag = {:tag}", "", 0, 0, map[string]any{"tag": tagID})
	if err != nil {
		return errors.Wrap(err, "tags: the discussion tags could not be read")
	}

	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return errors.Wrap(err, "tags: the discussion tag could not be removed")
		}
	}

	return nil
}

// applyTag writes a tag's fields onto a record.
func applyTag(record *core.Record, tag *domain.Tag) {
	record.Set("name", tag.Name)
	record.Set("slug", tag.Slug)
	record.Set("description", tag.Description)
	record.Set("color", tag.Color)
	record.Set("icon", tag.Icon)
	record.Set("default_sort", tag.DefaultSort)
	record.Set("is_hidden", tag.IsHidden)
	record.Set("is_primary", tag.IsPrimary)
	record.Set("is_restricted", tag.IsRestricted)

	// An empty parent clears the relation: a root tag has no parent.
	record.Set("parent", tag.ParentID)

	// A PRIMARY tag carries a position; a secondary tag does not, and
	// upstream's count rules are built on that distinction: a tag with
	// a position is a primary tag.
	// A primary tag carries a position; a secondary tag does not, and
	// upstream's count rules are built on that distinction: a tag with
	// a position IS a primary tag. The column is therefore cleared for
	// a secondary tag rather than set to zero.
	if tag.IsPrimary {
		record.Set("position", tag.Position)
	} else {
		record.Set("position", nil)
	}
}

// setTagOrder writes the tag tree.
//
// It is upstream's `OrderTagsController`, and the RESET is the half that is
// easy to miss: EVERY tag is cleared first, so a tag the payload does not
// mention becomes an unplaced secondary tag. That is how dragging a tag out
// of the primary list demotes it, and how a child dragged back to the root
// stops being anybody's child — the payload says where the placed tags go,
// and silence means "nowhere".
//
// A placed tag gets its index as its position; a CHILD gets its index among
// its siblings and its parent's id, and upstream marks it `is_primary` too
// (a child is positioned, just not at the root). `isChild` — which the
// frontend sorts on — is the parent relation, so the two agree.
func setTagOrder(app core.App, physical string, order []tagOrderEntry) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return errors.Wrap(err, "tags: the tags could not be read")
	}

	byID := make(map[string]*core.Record, len(records))
	for _, record := range records {
		byID[record.Id] = record
	}

	// The reset. `position` is a non-nullable column here, so clearing it
	// stores 0; the RESOURCE serves null for a tag that is not primary, and
	// that is what the admin page reads. `is_primary` is the flag that says
	// which of the two a tag is.
	for _, record := range records {
		record.Set("position", nil)
		record.Set("parent", "")
		record.Set("is_primary", false)
		if err := app.Save(record); err != nil {
			return errors.Wrap(err, "tags: the order could not be cleared")
		}
	}

	place := func(id string, position int, parentID string) error {
		record, ok := byID[id]
		if !ok {
			// A tag that no longer exists is skipped rather than
			// failing the whole reorder.
			return nil
		}

		record.Set("position", position)
		record.Set("parent", parentID)
		record.Set("is_primary", true)

		if err := app.Save(record); err != nil {
			return errors.Wrap(err, "tags: the position could not be saved")
		}
		return nil
	}

	for index, entry := range order {
		if entry.ID == "" {
			continue
		}

		if err := place(entry.ID, index, ""); err != nil {
			return err
		}

		for childIndex, childID := range entry.Children {
			if childID == "" || childID == entry.ID {
				continue
			}
			if err := place(childID, childIndex, entry.ID); err != nil {
				return err
			}
		}
	}

	return nil
}

// childTagIDs returns the ids of a tag's children.
func childTagIDs(app core.App, physical, parentID string) ([]string, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "tags: the tags could not be read")
	}

	ids := make([]string, 0)
	for _, record := range records {
		if record.GetString("parent") == parentID {
			ids = append(ids, record.Id)
		}
	}

	return ids, nil
}

// nextPrimaryPosition returns the position a new primary tag takes.
//
// It is the position after the LAST primary tag of the same parent, so
// a new primary tag lands at the end of its group.
func nextPrimaryPosition(app core.App, physical, parentID string) (int, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return 0, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return 0, errors.Wrap(err, "tags: the tags could not be read")
	}

	position := 0
	for _, record := range records {
		if !record.GetBool("is_primary") {
			continue
		}
		if parentID != "" && record.GetString("parent") != parentID {
			continue
		}
		if parentID == "" && record.GetString("parent") != "" {
			continue
		}
		if record.GetInt("position") >= position {
			position = record.GetInt("position") + 1
		}
	}

	return position, nil
}

// setDiscussionTags replaces a discussion's tags.
//
// The relationship names the discussion's COMPLETE tag set, so a tag
// the client left out is removed — upstream's `sync()`, which replaces
// the pivot rows rather than adding to them.
func setDiscussionTags(app core.App, discussionID string, tags []*domain.Tag) error {
	collection, err := app.FindCollectionByNameOrId("forum_discussion_tags")
	if err != nil {
		return errors.Wrap(err, "tags: the discussion_tags collection is missing")
	}

	wanted := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		wanted[tag.ID] = struct{}{}
	}

	// The links the client left out are removed.
	existing, err := app.FindRecordsByFilter(collection, "discussion = {:discussion}", "", 0, 0,
		map[string]any{"discussion": discussionID})
	if err != nil {
		return errors.Wrap(err, "tags: the discussion tags could not be read")
	}

	for _, record := range existing {
		tagID := record.GetString("tag")
		if _, keep := wanted[tagID]; keep {
			delete(wanted, tagID)
			continue
		}
		if err := app.Delete(record); err != nil {
			return errors.Wrap(err, "tags: the discussion tag could not be removed")
		}
	}

	// The links the client named are created.
	for tagID := range wanted {
		record := core.NewRecord(collection)
		record.Set("discussion", discussionID)
		record.Set("tag", tagID)
		if err := app.Save(record); err != nil {
			return errors.Wrap(err, "tags: the discussion tag could not be attached")
		}
	}

	return nil
}
