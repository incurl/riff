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

// createTagForTest posts a tag and returns its id.
func createTagForTest(t *testing.T, handler http.Handler, cookies []*http.Cookie, csrf, body string) *httptest.ResponseRecorder {
	t.Helper()

	return doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags", body, cookies, map[string]string{"X-CSRF-Token": csrf})
}

// tagParentID reads the id a tag resource's `parent` relationship names.
func tagParentID(t *testing.T, relationships map[string]any) string {
	t.Helper()

	parent, ok := relationships["parent"].(map[string]any)
	require.True(t, ok, "a tag always carries a `parent` relationship: %v", relationships)

	if parent["data"] == nil {
		return ""
	}

	data, ok := parent["data"].(map[string]any)
	require.True(t, ok, "the relationship names an identifier: %v", parent)

	id, ok := data["id"].(string)
	require.True(t, ok, "the identifier carries its id: %v", data)

	return id
}

// TestTags_ASecondaryTagCanBeCreatedUnderAPrimary pins the write the admin
// UI needs to create a child.
//
// Upstream's shape is a RELATIONSHIP: the tag's `parent` is the primary tag,
// and the new tag is a CHILD of it — not primary, and with no position of its
// own until the tree is reordered.
func TestTags_ASecondaryTagCanBeCreatedUnderAPrimary(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	recorder := createTagForTest(t, handler, cookies, admin[1].Value,
		`{"data":{"type":"tags","attributes":{"name":"News","slug":"news","isPrimary":true}}}`)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	primaryID := resourceIDOf(t, recorder)

	body := `{"data":{"type":"tags","attributes":{"name":"Updates","slug":"updates","isPrimary":false},` +
		`"relationships":{"parent":{"data":{"type":"tags","id":"` + primaryID + `"}}}}}`
	recorder = createTagForTest(t, handler, cookies, admin[1].Value, body)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	childID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/tags/"+childID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, relationships := tagDocument(t, recorder)
	require.Equal(t, primaryID, tagParentID(t, relationships),
		"the child's parent must be the primary tag's id")
	require.Equal(t, true, attributes["isChild"])
	require.Equal(t, false, attributes["isPrimary"], "a child is not a primary tag")
	require.Nil(t, attributes["position"], "a child has no place in the primary list")
}

// TestTags_TheParentAlsoArrivesAsAnAttribute pins the shape the ADMIN MODAL
// sends.
//
// `Model.save` drops a null relationship, so a relationship cannot express
// "no parent" — the modal therefore sends the primary tag's id as a plain
// attribute, and an empty string to clear it. The server reads both shapes, so
// an upstream-shaped client and this port's own page agree.
func TestTags_TheParentAlsoArrivesAsAnAttribute(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	primaryID := seedTag(t, app, "News", "news").Id

	body := `{"data":{"type":"tags","attributes":{"name":"Updates","slug":"updates","isPrimary":false,"parent":"` + primaryID + `"}}}`
	recorder := createTagForTest(t, handler, cookies, admin[1].Value, body)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	childID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/tags/"+childID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, relationships := tagDocument(t, recorder)
	require.Equal(t, primaryID, tagParentID(t, relationships))
	require.Equal(t, true, attributes["isChild"])
}

// TestTags_AChildMovesBetweenParents pins the PATCH that re-parents a tag.
func TestTags_AChildMovesBetweenParents(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)
	csrf := admin[1].Value

	first := seedTag(t, app, "News", "news")
	second := seedTag(t, app, "Guides", "guides")
	child := seedTag(t, app, "Updates", "updates")

	body := `{"data":{"attributes":{"parent":"` + first.Id + `"}}}`
	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/tags/"+child.Id, body, cookies,
		map[string]string{"X-CSRF-Token": csrf})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	_, relationships := tagDocument(t, recorder)
	require.Equal(t, first.Id, tagParentID(t, relationships))

	body = `{"data":{"attributes":{"parent":"` + second.Id + `"}}}`
	recorder = doWithHeaders(t, handler, http.MethodPatch, "/forum/api/tags/"+child.Id, body, cookies,
		map[string]string{"X-CSRF-Token": csrf})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	_, relationships = tagDocument(t, recorder)
	require.Equal(t, second.Id, tagParentID(t, relationships), "the tag moved to the other primary tag")
}

// TestTags_AClearedParentActuallyClears pins the half that used to be lost:
// a body that names `parent` as empty means "no parent", not "no change".
func TestTags_AClearedParentActuallyClears(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)
	csrf := admin[1].Value

	primary := seedTag(t, app, "News", "news")
	child := seedTag(t, app, "Updates", "updates")

	// Seed the parent relation directly: this test is about clearing it.
	collection := mustCollection(t, app, "forum_tags")
	record, err := app.FindRecordById(collection, child.Id)
	require.NoError(t, err)
	record.Set("parent", primary.Id)
	record.Set("is_primary", false)
	require.NoError(t, app.Save(record))

	body := `{"data":{"attributes":{"parent":""}}}`
	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/tags/"+child.Id, body, cookies,
		map[string]string{"X-CSRF-Token": csrf})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, relationships := tagDocument(t, recorder)
	require.Empty(t, tagParentID(t, relationships), "an empty parent clears the relation")
	require.Equal(t, false, attributes["isChild"])
	require.Nil(t, attributes["position"], "a tag with no parent and no place is unplaced")
}

// TestTags_APromotedChildBecomesAPrimaryRoot pins the mirror statement.
//
// Asking for `isPrimary: true` makes the tag a ROOT, so it loses its parent —
// otherwise the tag would carry a position AND a parent, which is exactly the
// state that renders nowhere: the primary list excludes anything with a
// parent, and a child is only drawn under one.
//
// It also takes a place of its own: the position it carried as a child is 0
// (never dragged), and keeping it would put the promoted tag first and
// collide with the tag already there.
func TestTags_APromotedChildBecomesAPrimaryRoot(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)
	csrf := admin[1].Value

	// A REAL primary tag: the `seedTag` fixture writes a position without
	// the flag, and the position a promoted tag takes is computed from the
	// placed ones.
	recorder := createTagForTest(t, handler, cookies, csrf,
		`{"data":{"type":"tags","attributes":{"name":"News","slug":"news","isPrimary":true}}}`)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	primaryID := resourceIDOf(t, recorder)

	body := `{"data":{"type":"tags","attributes":{"name":"Updates","slug":"updates","isPrimary":false,"parent":"` + primaryID + `"}}}`
	recorder = createTagForTest(t, handler, cookies, csrf, body)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	childID := resourceIDOf(t, recorder)

	recorder = doWithHeaders(t, handler, http.MethodPatch, "/forum/api/tags/"+childID,
		`{"data":{"attributes":{"isPrimary":true}}}`, cookies, map[string]string{"X-CSRF-Token": csrf})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes, relationships := tagDocument(t, recorder)
	require.Empty(t, tagParentID(t, relationships), "a primary tag is a root")
	require.Equal(t, false, attributes["isChild"])
	require.Equal(t, true, attributes["isPrimary"])
	require.EqualValues(t, 1, attributes["position"],
		"the promoted tag is placed after the tag that already held position 0")
}

// TestTags_ATagWithAParentCannotBeAPrimaryTag pins the contradiction.
//
// A body that asks for both is refused rather than guessed at: either answer
// leaves the tag somewhere the administrator did not ask for, and one of the
// two makes it disappear from the page entirely.
func TestTags_ATagWithAParentCannotBeAPrimaryTag(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)

	primary := seedTag(t, app, "News", "news")

	body := `{"data":{"type":"tags","attributes":{"name":"Broken","slug":"broken","isPrimary":true,"parent":"` + primary.Id + `"}}}`
	recorder := createTagForTest(t, handler, cookies, admin[1].Value, body)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
}

// TestTags_AParentMustBeARootTag pins the tree's depth.
//
// A child cannot have children: the admin tree renders one level of nesting,
// and a grandchild would be listed nowhere — present in the store, absent from
// the page, with no error anywhere.
func TestTags_AParentMustBeARootTag(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	cookies := sessionCookies(admin[0].Value, admin[1].Value)
	csrf := admin[1].Value

	primary := seedTag(t, app, "News", "news")

	body := `{"data":{"type":"tags","attributes":{"name":"Updates","slug":"updates","isPrimary":false,"parent":"` + primary.Id + `"}}}`
	recorder := createTagForTest(t, handler, cookies, csrf, body)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	childID := resourceIDOf(t, recorder)

	// A grandchild, through the child.
	body = `{"data":{"type":"tags","attributes":{"name":"Grandchild","slug":"grandchild","isPrimary":false,"parent":"` + childID + `"}}}`
	recorder = createTagForTest(t, handler, cookies, csrf, body)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	// And a tag may not be its own parent.
	recorder = doWithHeaders(t, handler, http.MethodPatch, "/forum/api/tags/"+childID,
		`{"data":{"attributes":{"parent":"`+childID+`"}}}`, cookies, map[string]string{"X-CSRF-Token": csrf})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
}
