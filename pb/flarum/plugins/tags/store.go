package tags

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
)

// tagRecord projects a tag record onto the domain model.
func tagRecord(record *core.Record) *domain.Tag {
	return &domain.Tag{
		ID:              record.Id,
		Name:            record.GetString("name"),
		Slug:            record.GetString("slug"),
		Description:     record.GetString("description"),
		ParentID:        record.GetString("parent"),
		Color:           record.GetString("color"),
		Icon:            record.GetString("icon"),
		Position:        record.GetInt("position"),
		IsHidden:        record.GetBool("is_hidden"),
		IsPrimary:       record.GetBool("is_primary"),
		IsRestricted:    record.GetBool("is_restricted"),
		DiscussionCount: record.GetInt("discussion_count"),
	}
}

// listTags returns every tag, ordered by position then name.
func listTags(app core.App, physical string) ([]*domain.Tag, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "tags: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection)
	if err != nil {
		return nil, err
	}

	tags := make([]*domain.Tag, 0, len(records))
	for _, record := range records {
		tags = append(tags, tagRecord(record))
	}
	return tags, nil
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
