package flarum

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tag HIERARCHY: primary tags, secondary tags, and children.
//
// Upstream's model is two nullable columns and one flag (`flarum/tags`,
// `Tag.php`):
//
//	position    null = not placed; an integer = the tag's place among its
//	            siblings
//	parent_id   null = a root tag; an id = it sits under that tag
//	is_primary  a positioned tag (a CHILD is positioned too — see
//	            `OrderTagsController`)
//
// The admin page splits its lists on exactly those: the primary list is
// `position() !== null && !isChild()`, and the "Secondary tags" list is
// `position() === null`. Both halves were broken here: `position` was served
// as the stored 0 for every tag (so the secondary list was always empty), and
// the order endpoint expected a flat `["id", …]` that the page never sends (so
// a drag was refused with a 422).

// tagDocument reads a tag resource's attributes and relationships.
func tagDocument(t *testing.T, recorder *httptest.ResponseRecorder) (map[string]any, map[string]any) {
	t.Helper()

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the tag endpoint answers one resource: %s", recorder.Body.String())

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok)

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok)

	return attributes, relationships
}

// TestTags_AnUnplacedTagIsASecondaryTag pins the half that made secondary
// tags unusable: the tag a "Create secondary tag" produces must be served
// with a NULL position, because that null is what puts it in the admin's
// "Secondary tags" list.
func TestTags_AnUnplacedTagIsASecondaryTag(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags",
		`{"data":{"type":"tags","attributes":{"name":"Guides","slug":"guides","isPrimary":false}}}`,
		cookies, map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	tagID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/tags/"+tagID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, _ := tagDocument(t, recorder)
	require.Nil(t, attributes["position"],
		"an unplaced tag must carry a NULL position, or the admin page lists it as a primary tag")
	require.Equal(t, false, attributes["isPrimary"])
	require.Equal(t, false, attributes["isChild"])
}

// TestTags_APrimaryTagCarriesItsPosition pins the other side of the pair.
func TestTags_APrimaryTagCarriesItsPosition(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags",
		`{"data":{"type":"tags","attributes":{"name":"News","slug":"news","isPrimary":true}}}`,
		cookies, map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	tagID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/tags/"+tagID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, _ := tagDocument(t, recorder)
	require.EqualValues(t, 0, attributes["position"], "the first primary tag takes position 0")
	require.Equal(t, true, attributes["isPrimary"])
}

// TestTags_TheOrderPayloadNestsATag pins upstream's order payload and the
// nesting it writes.
//
// The payload is the ADMIN PAGE's shape — an entry per primary tag, with the
// ids of the tags dropped into its child list — not a flat list of ids
// (`TagsPage.onSortUpdate` builds it from the DOM).
func TestTags_TheOrderPayloadNestsATag(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)
	headers := map[string]string{"X-CSRF-Token": admin[1].Value}

	parent := seedTag(t, app, "Parent", "parent")
	other := seedTag(t, app, "Other", "other")
	child := seedTag(t, app, "Child", "child")

	body := `{"order":[{"id":"` + parent.Id + `","children":["` + child.Id + `"]},{"id":"` + other.Id + `","children":[]}]}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags/order", body, cookies, headers)
	require.Equal(t, http.StatusNoContent, recorder.Code,
		"the admin page's nested payload must be accepted: %s", recorder.Body.String())

	collection := mustCollection(t, app, "forum_tags")

	parentRecord, err := app.FindRecordById(collection, parent.Id)
	require.NoError(t, err)
	require.Equal(t, 0, parentRecord.GetInt("position"))
	require.True(t, parentRecord.GetBool("is_primary"))
	require.Empty(t, parentRecord.GetString("parent"))

	otherRecord, err := app.FindRecordById(collection, other.Id)
	require.NoError(t, err)
	require.Equal(t, 1, otherRecord.GetInt("position"), "the second primary tag takes position 1")

	childRecord, err := app.FindRecordById(collection, child.Id)
	require.NoError(t, err)
	require.Equal(t, parent.Id, childRecord.GetString("parent"),
		"a tag dropped into another's child list becomes its child")
	require.Equal(t, 0, childRecord.GetInt("position"),
		"the child's position is its index among the siblings")
	require.True(t, childRecord.GetBool("is_primary"),
		"a positioned tag is primary, child or not — that is upstream's OrderTagsController")

	// The RESOURCE reports it as a child, which is what the admin tree renders
	// under its parent.
	recorder = do(t, handler, http.MethodGet, "/forum/api/tags/"+child.Id, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, relationships := tagDocument(t, recorder)
	require.Equal(t, true, attributes["isChild"])

	parentRelationship, ok := relationships["parent"].(map[string]any)
	require.True(t, ok, "the tag must carry its parent relationship: %#v", relationships)
	parentData, ok := parentRelationship["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, parent.Id, parentData["id"])
}

// TestTags_TheOrderDemotesWhatItDoesNotMention pins the RESET.
//
// Upstream clears every tag first and then places the ones the payload names,
// so a tag left out becomes unplaced. That is how dragging a tag into the
// "Secondary tags" list demotes it, and how a child dragged back to the root
// stops being anybody's child.
func TestTags_TheOrderDemotesWhatItDoesNotMention(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)
	headers := map[string]string{"X-CSRF-Token": admin[1].Value}

	parent := seedTag(t, app, "Parent", "parent")
	child := seedTag(t, app, "Child", "child")
	dropped := seedTag(t, app, "Dropped", "dropped")

	body := `{"order":[{"id":"` + parent.Id + `","children":["` + child.Id + `"]}]}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags/order", body, cookies, headers)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// Now drag the child back to the root and leave `dropped` out entirely.
	body = `{"order":[{"id":"` + parent.Id + `","children":[]},{"id":"` + child.Id + `"}]}`
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags/order", body, cookies, headers)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	collection := mustCollection(t, app, "forum_tags")

	childRecord, err := app.FindRecordById(collection, child.Id)
	require.NoError(t, err)
	require.Empty(t, childRecord.GetString("parent"), "a tag back at the root has no parent")
	require.Equal(t, 1, childRecord.GetInt("position"))

	droppedRecord, err := app.FindRecordById(collection, dropped.Id)
	require.NoError(t, err)
	require.False(t, droppedRecord.GetBool("is_primary"),
		"a tag the payload does not mention is unplaced, which is upstream's reset")
}
