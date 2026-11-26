package tags

import (
	"strings"

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

	// The parent arrives as a RELATIONSHIP (JSON:API) or as a plain
	// attribute, and its PRESENCE matters on its own: a body that says
	// `parent: null` is asking to CLEAR the relation, and a reader that
	// only reports a non-empty id cannot tell that from silence.
	parentID, parentNamed := parentFromBody(body, attributes)
	tag.ParentID = parentID
	if parentNamed {
		present["parent"] = true
	}

	return tag, present, nil
}

// parentFromBody reads the parent a body names, and whether it names one.
//
// Three shapes reach this port, and all three mean the same thing:
//
//	{"data":{"relationships":{"parent":{"data":{"id":"…"}}}}}   JSON:API
//	{"data":{"relationships":{"parent":{"data":null}}}}         JSON:API, clear
//	{"data":{"attributes":{"parent":"…"}}} / {"parent":"…"}     the flat form
//
// The second is the one that used to be LOST: the old reader answered "" both
// for "no relation named" and for "the relation is null", so a PATCH that
// cleared a parent looked like a PATCH that said nothing about it, and the old
// parent survived — a tag the admin had just pulled out of a primary tag's
// list stayed nested under it.
func parentFromBody(body, attributes map[string]any) (string, bool) {
	// JSON:API names the relationship on `data`.
	if data, ok := body["data"].(map[string]any); ok {
		if relationships, ok := data["relationships"].(map[string]any); ok {
			if relationship, ok := relationships["parent"].(map[string]any); ok {
				return identifierID(relationship["data"]), true
			}
		}
		if raw, ok := data["parent"]; ok {
			return identifierID(raw), true
		}
	}

	// The flat form. `attributes` is the nested attributes map for a
	// JSON:API body and the body itself for a flat one, so this check
	// covers both.
	if raw, ok := attributes["parent"]; ok {
		return identifierID(raw), true
	}

	return "", false
}

// identifierID reads a relationship's id out of one of its shapes: a bare id,
// an identifier object, or `null` (which means "no relation").
func identifierID(raw any) string {
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		id, ok := typed["id"].(string)
		if !ok {
			return ""
		}
		return strings.TrimSpace(id)
	default:
		return ""
	}
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

// tagOrderEntry is one entry of the order payload: a tag and its children.
//
// It is upstream's `{id, children}` pair. The child ids are the tags the
// admin dragged INTO this tag's child list, and the order they appear in is
// their position among the siblings.
type tagOrderEntry struct {
	// ID is the tag's id.
	ID string
	// Children are the ids of the tags nested under it, in order.
	Children []string
}

// tagOrder reads the tag tree from the request body.
//
// The body is upstream's `{"order": [{"id": …, "children": [id, …]}, …]}`
// (`OrderTagsController`), and it also accepts that list under `data` and a
// bare id in place of an entry — a flat `{"order": ["id", …]}` is the same
// statement as a list of childless entries, and the port's own tests wrote
// it that way before the admin page's shape was known.
//
// THE NESTED SHAPE IS THE ONE THE ADMIN PAGE SENDS (`TagsPage.onSortUpdate`
// reads `.TagList--primary > li` and, for each, its
// `.TagListItem-children > li` ids). A parser that only accepts strings
// refuses the drag with a 422, which is what "dragging does nothing" looked
// like.
func tagOrder(e *core.RequestEvent) ([]tagOrderEntry, error) {
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

	order := make([]tagOrderEntry, 0, len(list))

	for _, item := range list {
		switch typed := item.(type) {
		case string:
			if typed == "" {
				return nil, errBadOrder
			}
			order = append(order, tagOrderEntry{ID: typed})
		case map[string]any:
			entry := tagOrderEntry{ID: stringField(typed["id"])}
			if entry.ID == "" {
				return nil, errBadOrder
			}

			if children, ok := typed["children"].([]any); ok {
				for _, child := range children {
					id, ok := child.(string)
					if !ok || id == "" {
						return nil, errBadOrder
					}
					entry.Children = append(entry.Children, id)
				}
			}

			order = append(order, entry)
		default:
			return nil, errBadOrder
		}
	}

	return order, nil
}

// stringField reads a string out of a decoded JSON value.
func stringField(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
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
