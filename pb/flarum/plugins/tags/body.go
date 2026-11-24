package tags

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// tagAttributes reads a tag's attributes from the request body.
//
// The body is the JSON:API shape (`{"data":{"attributes":{…}}}`) and the
// flat form (`{"name":…}`), because the admin application posts the
// first and an API consumer the second.
//
// The `present` set names the attributes the body CARRIES, which is what
// makes a partial update possible: an attribute the body says nothing
// about is left alone rather than cleared.
func tagAttributes(e *core.RequestEvent) (*domain.Tag, map[string]bool, error) {
	body := map[string]any{}

	if err := api.DecodeBody(e, &body); err != nil {
		return nil, nil, err
	}

	attributes := body
	if data, ok := body["data"].(map[string]any); ok {
		if nested, ok := data["attributes"].(map[string]any); ok {
			attributes = nested
		}
	}

	// The set of attributes the body NAMES. It is what makes a partial
	// update possible: an attribute the body is silent about is left
	// alone rather than reset to its zero value.
	present := make(map[string]bool, len(attributes))
	for name := range attributes {
		present[name] = true
	}

	tag := &domain.Tag{}

	if value, ok := attributes["name"].(string); ok {
		tag.Name = value
	}
	if value, ok := attributes["slug"].(string); ok {
		tag.Slug = value
	}
	if value, ok := attributes["description"].(string); ok {
		tag.Description = value
	}
	if value, ok := attributes["color"].(string); ok {
		tag.Color = value
	}
	if value, ok := attributes["icon"].(string); ok {
		tag.Icon = value
	}
	if value, ok := attributes["defaultSort"].(string); ok {
		tag.DefaultSort = value
	}
	if value, ok := attributes["isHidden"].(bool); ok {
		tag.IsHidden = value
	}
	if value, ok := attributes["isPrimary"].(bool); ok {
		tag.IsPrimary = value
	}
	if value, ok := attributes["isRestricted"].(bool); ok {
		tag.IsRestricted = value
	}
	if value, ok := attributes["position"].(float64); ok {
		tag.Position = int(value)
	}

	// The parent is a RELATIONSHIP, so it arrives as an identifier
	// object — and the flat form carries the id directly.
	tag.ParentID = relationshipID(body, attributes, "parent")
	if tag.ParentID != "" {
		present["parent"] = true
	}

	return tag, present, nil
}

// relationshipID reads a relationship's id from the request body.
//
// It looks in the JSON:API shape first (`data.relationships.parent.data.id`)
// and then the flat shape (`data.parent` or a bare `parent`).
func relationshipID(body, attributes map[string]any, name string) string {
	if data, ok := body["data"].(map[string]any); ok {
		if relationships, ok := data["relationships"].(map[string]any); ok {
			if relationship, ok := relationships[name].(map[string]any); ok {
				if identifier, ok := relationship["data"].(map[string]any); ok {
					if id, ok := identifier["id"].(string); ok {
						return id
					}
				}
			}
		}
		if identifier, ok := data[name].(map[string]any); ok {
			if id, ok := identifier["id"].(string); ok {
				return id
			}
		}
		if id, ok := data[name].(string); ok {
			return id
		}
	}

	if identifier, ok := attributes[name].(map[string]any); ok {
		if id, ok := identifier["id"].(string); ok {
			return id
		}
	}
	if id, ok := attributes[name].(string); ok {
		return id
	}

	return ""
}

// mergeTag merges the attributes a body carries onto a tag.
//
// An attribute the body says NOTHING about is left alone: a partial
// update must not clear the fields it is silent about. The admin page
// sends the whole attribute set, but an API consumer may send one
// field, and clearing the rest would lose the tag's name.
//
// The body carries the SET of attributes it names, so the merge is
// driven by that set rather than by the values: a boolean the body
// omits is "unchanged", NOT "false".
func mergeTag(existing, updates *domain.Tag, present map[string]bool) *domain.Tag {
	merged := *existing

	if present["name"] {
		merged.Name = updates.Name
	}
	if present["slug"] {
		merged.Slug = updates.Slug
	}
	if present["description"] {
		merged.Description = updates.Description
	}
	if present["color"] {
		merged.Color = updates.Color
	}
	if present["icon"] {
		merged.Icon = updates.Icon
	}
	if present["defaultSort"] {
		merged.DefaultSort = updates.DefaultSort
	}
	if present["isHidden"] {
		merged.IsHidden = updates.IsHidden
	}
	if present["isPrimary"] {
		merged.IsPrimary = updates.IsPrimary
	}
	if present["isRestricted"] {
		merged.IsRestricted = updates.IsRestricted
	}
	if present["position"] {
		merged.Position = updates.Position
	}
	// The parent is cleared when the body names it and it is empty:
	// "no parent" IS a value the admin can set.
	if present["parent"] {
		merged.ParentID = updates.ParentID
	}

	return &merged
}

// tagOrder reads the tag order from the request body.
//
// The body carries the ids in their new order, in the JSON:API shape
// and in the flat form.
func tagOrder(e *core.RequestEvent) ([]string, error) {
	body := map[string]any{}

	if err := api.DecodeBody(e, &body); err != nil {
		return nil, err
	}

	raw, ok := body["order"]
	if !ok {
		if data, isData := body["data"].(map[string]any); isData {
			raw, ok = data["order"]
		}
	}
	if !ok {
		return nil, errBadOrder
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, errBadOrder
	}

	order := make([]string, 0, len(list))
	for _, item := range list {
		id, ok := item.(string)
		if !ok {
			return nil, errBadOrder
		}
		order = append(order, id)
	}

	return order, nil
}

// deleteTagScopedPermissions removes a tag's scoped permission rows.
//
// The rows are named `tag{id}.{permission}`, so they are found by the
// prefix. They are removed when the tag stops being restricted and
// when it is deleted: a scoped row for a scope that no longer exists
// grants nothing, and re-restricting the tag later would silently
// restore an old access list.
func deleteTagScopedPermissions(app core.App, tagID string) error {
	collection, err := app.FindCollectionByNameOrId("forum_permissions")
	if err != nil {
		// The permissions collection is missing: there is nothing to
		// remove.
		return nil //nolint:nilerr // a missing table means no rows
	}

	prefix := "tag" + tagID + "."

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return errors.Wrap(err, "tags: the permissions could not be read")
	}

	for _, record := range records {
		permission := record.GetString("permission")
		if len(permission) <= len(prefix) || permission[:len(prefix)] != prefix {
			continue
		}
		if err := app.Delete(record); err != nil {
			return errors.Wrap(err, "tags: the scoped permission could not be removed")
		}
	}

	return nil
}

// errBadOrder reports an unreadable tag order.
var errBadOrder = errors.New("tags: the tag order could not be read")
