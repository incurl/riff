package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/plugins/tags"
)

// The tags plugin against upstream `flarum/tags`.
//
// The upstream extension's contract is the JSON:API resource: the
// attribute names, the relationships and the endpoints. These tests pin
// the parts a client actually reads, because a missing attribute is
// SILENT — the frontend renders a tag with no colour, no icon and no
// children, and nothing errors.
//
// The upstream reference is `flarum/tags` v2 (the Flarum 2.0 tree), whose
// `TagResource` carries sixteen attributes and three relationships.

// upstreamTagAttributes is upstream's attribute set.
//
// It is spelled out here rather than derived, because the point of the
// test is that the SERVER carries exactly what upstream's resource
// carries — a list built from the server's own output would prove
// nothing.
var upstreamTagAttributes = []string{
	"name",
	"description",
	"slug",
	"storedSlug",
	"color",
	"icon",
	"isHidden",
	"isPrimary",
	"isRestricted",
	"discussionCount",
	"position",
	"defaultSort",
	"isChild",
	"lastPostedAt",
	"canStartDiscussion",
	"canAddToDiscussion",
}

// TestTags_TheResourceCarriesUpstreamsAttributes pins that the tag
// resource carries upstream's attribute set.
//
// A missing attribute is silent: the frontend renders the tag with
// whatever it finds and nothing errors, so the gap only shows as a
// missing badge.
func TestTags_TheResourceCarriesUpstreamsAttributes(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	seedTag(t, app, "Announcements", "announcements")

	// The resource is read as an ADMINISTRATOR, because upstream's
	// `storedSlug` is visible only to a caller who may edit the tag.
	recorder := do(t, handler, http.MethodGet, "/forum/api/tags", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].([]any)
	require.True(t, ok, "the response must carry an array")
	require.Len(t, data, 1, "the seeded tag is listed")

	tag, ok := data[0].(map[string]any)
	require.True(t, ok, "a tag must be an object")

	attributes, ok := tag["attributes"].(map[string]any)
	require.True(t, ok, "a tag must carry attributes")

	for _, name := range upstreamTagAttributes {
		require.Contains(t, attributes, name, "the tag resource must carry the %q attribute", name)
	}
}

// TestTags_TheTagCarriesUpstreamsRelationships pins the relationships
// upstream's resource declares: `parent`, `children` and
// `lastPostedDiscussion`.
func TestTags_TheTagCarriesUpstreamsRelationships(t *testing.T) {
	app, handler := newTestForum(t)

	seedTag(t, app, "Announcements", "announcements")

	recorder := do(t, handler, http.MethodGet, "/forum/api/tags", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].([]any)
	require.True(t, ok, "the response must carry an array")
	require.NotEmpty(t, data)

	tag, ok := data[0].(map[string]any)
	require.True(t, ok, "a tag must be an object")

	relationships, ok := tag["relationships"].(map[string]any)
	require.True(t, ok, "a tag must carry relationships")

	require.Contains(t, relationships, "parent", "the tag must carry the parent relationship")
	require.Contains(t, relationships, "children", "the tag must carry the children relationship")
}

// TestTags_TheTagCarriesItsChildren pins that a parent tag lists its
// children, which is what upstream's `children` relationship carries.
func TestTags_TheTagCarriesItsChildren(t *testing.T) {
	app, handler := newTestForum(t)

	parent := seedTag(t, app, "Parent", "parent")
	child := seedTag(t, app, "Child", "child")

	// The child names the parent.
	childRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), child.Id)
	require.NoError(t, err)
	childRecord.Set("parent", parent.Id)
	require.NoError(t, app.Save(childRecord))

	recorder := do(t, handler, http.MethodGet, "/forum/api/tags/"+parent.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	relationships, ok := data["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	children, ok := relationships["children"].(map[string]any)
	require.True(t, ok, "the tag must carry the children relationship")

	identifiers, ok := children["data"].([]any)
	require.True(t, ok, "the relationship must carry its identifiers")

	require.Len(t, identifiers, 1, "the parent lists its one child")
}

// TestTags_TheTagIsCreatedThroughItsEndpoint pins upstream's create
// endpoint: `POST /api/tags` with the tag's attributes.
func TestTags_TheTagIsCreatedThroughItsEndpoint(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	body := `{"data":{"attributes":{"name":"Support","slug":"support","description":"Where to ask for help","color":"#3498db","icon":"fas fa-life-ring"}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags", body,
		sessionCookies(admin[0].Value, admin[1].Value),
		map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := data["attributes"].(map[string]any)
	require.True(t, ok, "the resource must carry attributes")

	require.Equal(t, "Support", attributes["name"])
	require.Equal(t, "support", attributes["slug"])
	require.Equal(t, "Where to ask for help", attributes["description"])
	require.Equal(t, "#3498db", attributes["color"])
}

// TestTags_AMemberWithoutThePermissionCannotCreateATag pins that the
// create endpoint is governed by a permission.
func TestTags_AMemberWithoutThePermissionCannotCreateATag(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "tag_creator")

	body := `{"data":{"attributes":{"name":"Support","slug":"support"}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestTags_TheTagIsUpdatedThroughItsEndpoint pins upstream's update
// endpoint: `PATCH /api/tags/{id}`.
//
// The route carries a POST twin, because the frontend rewrites a PATCH
// into a POST with the method in a header and PocketBase's router
// matches on the REAL method before any handler runs.
func TestTags_TheTagIsUpdatedThroughItsEndpoint(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	tag := seedTag(t, app, "Old Name", "old-name")

	body := `{"data":{"attributes":{"name":"New Name"}}}`

	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/tags/"+tag.Id, body,
		sessionCookies(admin[0].Value, admin[1].Value),
		map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := data["attributes"].(map[string]any)
	require.True(t, ok, "the resource must carry attributes")

	require.Equal(t, "New Name", attributes["name"], "the name is updated")
	require.Equal(t, "old-name", attributes["slug"], "a partial update leaves the other fields alone")
}

// TestTags_APlainPostIsRefused pins that the tag write route answers
// one verb: a POST that names no override is not a patch.
func TestTags_APlainPostIsRefused(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	tag := seedTag(t, app, "Old Name", "old-name")

	body := `{"data":{"attributes":{"name":"New Name"}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags/"+tag.Id, body,
		sessionCookies(admin[0].Value, admin[1].Value),
		map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code, recorder.Body.String())
}

// TestTags_TheTagIsDeletedThroughItsEndpoint pins upstream's delete
// endpoint.
func TestTags_TheTagIsDeletedThroughItsEndpoint(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	tag := seedTag(t, app, "Doomed", "doomed")

	recorder := doWithHeaders(t, handler, http.MethodDelete, "/forum/api/tags/"+tag.Id, "",
		sessionCookies(admin[0].Value, admin[1].Value),
		map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// The tag is gone.
	recorder = do(t, handler, http.MethodGet, "/forum/api/tags/"+tag.Id, "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

// TestTags_TheTagsAreOrdered pins upstream's order endpoint: the body
// names the tags in their new order, and the positions are rewritten.
func TestTags_TheTagsAreOrdered(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	first := seedTag(t, app, "First", "first")
	second := seedTag(t, app, "Second", "second")

	body := `{"data":{"order":["` + second.Id + `","` + first.Id + `"]}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags/order", body,
		sessionCookies(admin[0].Value, admin[1].Value),
		map[string]string{"X-CSRF-Token": admin[1].Value})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	collection := mustCollection(t, app, "forum_tags")

	secondRecord, err := app.FindRecordById(collection, second.Id)
	require.NoError(t, err)
	require.Equal(t, 0, secondRecord.GetInt("position"), "the first tag in the order takes position 0")

	firstRecord, err := app.FindRecordById(collection, first.Id)
	require.NoError(t, err)
	require.Equal(t, 1, firstRecord.GetInt("position"), "the second tag in the order takes position 1")
}

// TestTags_TheDiscussionCarriesTheTagsRelationship pins that the
// discussion's `tags` relationship is WRITABLE, the way upstream's is.
//
// Upstream's frontend saves the discussion with the relationship
// (`discussion.save({ relationships: { tags } })`), so a read-only
// relationship means the frontend's tag control cannot save.
func TestTags_TheDiscussionCarriesTheTagsRelationship(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "tag_author")
	_ = author

	tag := seedTag(t, app, "Announcements", "announcements")

	// The tag is a PRIMARY one: the default count rules want one
	// primary tag on a discussion.
	tagRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), tag.Id)
	require.NoError(t, err)
	tagRecord.Set("is_primary", true)
	require.NoError(t, app.Save(tagRecord))

	// The author may tag their own discussion once the setting permits.
	setForumSetting(t, app, "allow_tag_change", "-1")

	body := `{"data":{"attributes":{"title":"Tagged","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + tag.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	// The discussion carries the tag.
	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	created, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	discussionID, ok := created["id"].(string)
	require.True(t, ok, "the discussion must carry an id")
	require.NotEmpty(t, discussionID)

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document = map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	relationships, ok := data["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	tags, ok := relationships["tags"].(map[string]any)
	require.True(t, ok, "the discussion must carry a tags relationship")

	identifiers, ok := tags["data"].([]any)
	require.True(t, ok, "the relationship must carry its identifiers")

	require.Len(t, identifiers, 1, "the discussion carries its one tag")
}

// TestTags_TheTagCountsAreEnforced pins upstream's count rules.
//
// The minimum is enforced where the WHOLE set is visible — the
// discussion write path — because a minimum is a property of a
// finished tag set.
func TestTags_TheTagCountsAreEnforced(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "tag_counter")
	_ = author

	setForumSetting(t, app, "allow_tag_change", "-1")

	// The default minimum is one PRIMARY tag, and the default maximum
	// is one secondary tag.
	primary := seedTag(t, app, "Primary", "primary")
	primaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), primary.Id)
	require.NoError(t, err)
	primaryRecord.Set("is_primary", true)
	primaryRecord.Set("position", 0)
	require.NoError(t, app.Save(primaryRecord))

	secondary := seedTag(t, app, "Secondary", "secondary")
	secondaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), secondary.Id)
	require.NoError(t, err)
	secondaryRecord.Set("position", nil)
	require.NoError(t, app.Save(secondaryRecord))

	// A discussion with a SECONDARY tag only breaks the primary
	// minimum: the rule counts the WHOLE set, and a secondary tag is
	// not a primary one.
	body := `{"data":{"attributes":{"title":"Counted","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + secondary.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	// With the primary tag it passes.
	body = `{"data":{"attributes":{"title":"Counted","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + primary.Id + `"}]}}}}`

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
}

// TestTags_TheCountSettingsAreHonoured pins upstream's four count
// settings: `flarum-tags.min_primary_tags`,
// `flarum-tags.max_primary_tags`, `flarum-tags.min_secondary_tags` and
// `flarum-tags.max_secondary_tags`.
//
// The rules are enforced on the WHOLE tag set, so the test raises the
// PRIMARY maximum to two and attaches two primary tags: with the
// default maximum of one, that set is refused — and with the setting
// raised, it is accepted.
//
// The setting has to be READ for that to happen. The settings column
// is a JSON field, so a stored value arrives as raw JSON bytes
// (`types.JSONRaw`), and `types.JSONRaw` is a DISTINCT NAMED TYPE —
// a switch on `case []byte:` does NOT match it. A reader that handles
// only the plain byte slice falls through to its default, the setting
// reads as UNSET, and the forum silently ignores what the operator
// saved.
func TestTags_TheCountSettingsAreHonoured(t *testing.T) {
	app, handler := newTestForum(t)

	_, authorCookies := memberSession(t, app, "tag_setting")

	setForumSetting(t, app, "allow_tag_change", "-1")

	// Two primary tags.
	first := seedTag(t, app, "First", "first")
	second := seedTag(t, app, "Second", "second")

	for _, tag := range []*core.Record{first, second} {
		record, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), tag.Id)
		require.NoError(t, err)
		record.Set("is_primary", true)
		record.Set("position", 0)
		require.NoError(t, app.Save(record))
	}

	body := `{"data":{"attributes":{"title":"Two","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + first.Id + `"},{"type":"tags","id":"` + second.Id + `"}]}}}}`

	// The default maximum is ONE primary tag, so the set is refused.
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	// Raising the maximum admits it.
	setForumSetting(t, app, tags.SettingMaxPrimaryTags, "2")

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
}

// TestTags_TheCountSettingsHonourTheMinimum pins that raising the
// MAXIMUM does not remove the MINIMUM.
//
// The rule is a RANGE when the two differ: `min <= count <= max`. A
// reader that only checked the maximum would admit a discussion with
// NO primary tag as soon as the maximum was raised — and the minimum
// is the setting that says "a discussion must carry a primary tag".
func TestTags_TheCountSettingsHonourTheMinimum(t *testing.T) {
	app, handler := newTestForum(t)

	_, authorCookies := memberSession(t, app, "tag_minimum")

	setForumSetting(t, app, "allow_tag_change", "-1")

	primary := seedTag(t, app, "Primary", "primary")
	primaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), primary.Id)
	require.NoError(t, err)
	primaryRecord.Set("is_primary", true)
	primaryRecord.Set("position", 0)
	require.NoError(t, app.Save(primaryRecord))

	// The minimum is 1 and the maximum is 2, so the range applies.
	setForumSetting(t, app, tags.SettingMaxPrimaryTags, "2")

	// A SECONDARY tag is not a primary one, so a set of one breaks
	// the primary minimum.
	secondary := seedTag(t, app, "Secondary", "secondary")
	secondaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), secondary.Id)
	require.NoError(t, err)
	secondaryRecord.Set("position", nil)
	require.NoError(t, app.Save(secondaryRecord))

	body := `{"data":{"attributes":{"title":"NoTag","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + secondary.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	// Lowering the minimum to zero admits it.
	setForumSetting(t, app, tags.SettingMinPrimaryTags, "0")

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
}

// TestTags_TheAdminPageCarriesTheSortMaps pins that the admin payload
// carries the SORT MAPS.
//
// The tags extension page lets an administrator pick a tag's default
// sort, and upstream lists the choices from `app.data.sortMaps` — so
// that a sort an extension added can be chosen for a tag without that
// extension knowing anything about tags.
//
// An empty map is not a harmless omission: the dropdown would offer
// only "No preference", and the operator could not set a default sort
// at all.
func TestTags_TheAdminPageCarriesTheSortMaps(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)
	require.NotNil(t, payload.Admin, "the admin shell should carry the admin data")

	sorts, ok := payload.SortMaps["discussions"].(map[string]any)
	require.True(t, ok, "the admin payload should carry the discussion sorts: %#v", payload.SortMaps)

	// The sorts are the forum's own: the URL key maps to the API sort
	// behind it, which is the shape the forum's `sortMap()` builds.
	require.Equal(t, "-lastPostedAt", sorts["latest"])
	require.Equal(t, "-commentCount", sorts["top"])
	require.Equal(t, "title", sorts["az"])
}

// TestTags_TheAdminPageCanCreateATag pins that the tag creation the
// admin page offers works.
//
// The page's "Create primary tag" button opens a modal that POSTs to
// `/api/tags`, so the endpoint is what makes the button real. The
// tag it creates must be PRIMARY — the button that opened the modal
// said so, and the server must agree.
func TestTags_TheAdminPageCanCreateATag(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	body := `{"data":{"attributes":{"name":"Announcements","slug":"announcements","isPrimary":true}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/tags", body, admin,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	created, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := created["attributes"].(map[string]any)
	require.True(t, ok, "the tag must carry its attributes")

	require.Equal(t, "Announcements", attributes["name"])
	require.Equal(t, true, attributes["isPrimary"], "the tag must be primary")
}

// TestTags_TheAdminPageSavesTheCountSettings pins the settings tab's
// save round-trip.
//
// The page's settings streams are saved through the admin surface's
// `POST /api/settings`, and the settings that land must be the ones
// the SERVER reads — a number here, because the count rules compare
// the count against the setting, and a string that does not parse
// would silently fall back to the default.
func TestTags_TheAdminPageSavesTheCountSettings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"flarum-tags.min_primary_tags":1,"flarum-tags.max_primary_tags":3,"flarum-tags.min_secondary_tags":0,"flarum-tags.max_secondary_tags":5}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	repo := testRepo(t, app)

	require.Equal(t, "1", settingText(t, repo, tags.SettingMinPrimaryTags))
	require.Equal(t, "3", settingText(t, repo, tags.SettingMaxPrimaryTags))
	require.Equal(t, "0", settingText(t, repo, tags.SettingMinSecondaryTags))
	require.Equal(t, "5", settingText(t, repo, tags.SettingMaxSecondaryTags))

	// The saved maximum is now the one the forum enforces, which is
	// the whole point of the page: a set of two primary tags is
	// accepted where the default maximum of one refused it.
	primary := seedTag(t, app, "Primary", "primary")
	primaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), primary.Id)
	require.NoError(t, err)
	primaryRecord.Set("is_primary", true)
	primaryRecord.Set("position", 0)
	require.NoError(t, app.Save(primaryRecord))

	second := seedTag(t, app, "Second", "second")
	secondRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), second.Id)
	require.NoError(t, err)
	secondRecord.Set("is_primary", true)
	secondRecord.Set("position", 1)
	require.NoError(t, app.Save(secondRecord))

	_, authorCookies := memberSession(t, app, "tag_admin_page")

	// The author may tag their own discussion once the setting permits.
	setForumSetting(t, app, "allow_tag_change", "-1")

	body := `{"data":{"attributes":{"title":"Two","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + primary.Id + `"},{"type":"tags","id":"` + second.Id + `"}]}}}}`

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
}

// TestTags_AScopedPermissionIsStoredWithItsScope pins the write the admin
// grid's tag column makes.
//
// The grid's dropdown sends the SCOPED KEY (`tag{id}.{permission}`), and
// the server splits it into the forum's own permission name and the tag's
// id, which it stores in the row's own scope column.
//
// The failure this pins is the one the scope exists to prevent: a scoped
// grant stored as a GLOBAL one grants access to EVERYBODY, and a
// restricted tag stops being restricted.
func TestTags_AScopedPermissionIsStoredWithItsScope(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	tag := seedTag(t, app, "Restricted", "restricted")

	// The grid's dropdown: the scoped key, and the group it is granted to.
	body := `{"permission":"tag` + tag.Id + `.viewForum","groupIds":["` + domain.GroupMemberID + `"]}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/permission", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// The row carries the scope.
	records, err := app.FindAllRecords(mustCollection(t, app, "forum_permissions"))
	require.NoError(t, err)

	matched := false

	for _, row := range records {
		if row.GetString("permission") != domain.PermissionViewForum {
			continue
		}

		if row.GetString("scope") == tag.Id {
			matched = true
		}
	}

	require.True(t, matched, "the scoped row must carry the tag's id as its scope")
}

// setTagRestricted makes a tag restricted.
//
// A restricted tag has its own permission scope: who may attach it is
// asked about the tag ITSELF.
func setTagRestricted(t *testing.T, app core.App, tagID string, restricted bool) error {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_tags")
	require.NoError(t, err)

	record, err := app.FindRecordById(collection, tagID)
	require.NoError(t, err)

	record.Set("is_restricted", restricted)

	return app.Save(record)
}

// TestTags_AScopedPermissionIsEnforcedForThatTagOnly pins the POINT of
// the scope.
//
// A restricted tag asks the permission service about the tag ITSELF, so
// the grant that matters is the one scoped to that tag. A grant stored
// globally would let every member attach the tag — the restricted tag
// would restrict nobody.
//
// The three steps are the whole chain: the admin grid writes a scoped
// row, the row carries the tag's id, and the permission service applies
// a scoped row ONLY to its own subject.
func TestTags_AScopedPermissionIsEnforcedForThatTagOnly(t *testing.T) {
	app, handler := newTestForum(t)

	adminCookies := testSuperuser(t, app)

	restricted := seedTag(t, app, "Restricted", "restricted")
	require.NoError(t, setTagRestricted(t, app, restricted.Id, true))

	other := seedTag(t, app, "Other", "other")
	require.NoError(t, setTagRestricted(t, app, other.Id, true))

	// The admin grid's tag column: the scoped key. `startDiscussion` is
	// upstream's own scoped permission — it is what a restricted tag
	// gates: who may start a discussion in it.
	body := `{"permission":"tag` + restricted.Id + `.startDiscussion","groupIds":["` + domain.GroupMemberID + `"]}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/permission", body, adminCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	_, memberCookies := memberSession(t, app, "scope_member")

	// The tag's own resource says whether the member may start a
	// discussion in it — and that answer is the SCOPED one.
	restrictedAttributes := resourceAttributes(t, handler, "tags/"+restricted.Id, memberCookies)
	require.Equal(t, true, restrictedAttributes["canStartDiscussion"],
		"the scoped grant must let the member start a discussion in that tag")

	// ...and NOT in the tag they were not granted: the scope is the
	// tag's own id, so a grant for one tag says nothing about another.
	// Upstream's TagPolicy answers a restricted tag from its scoped
	// permission ALONE, so a restricted tag with no grant is closed.
	otherAttributes := resourceAttributes(t, handler, "tags/"+other.Id, memberCookies)
	require.Equal(t, false, otherAttributes["canStartDiscussion"],
		"a restricted tag with no scoped grant is closed to the member")
}

// TestTags_ThePermissionKeysTranslate pins that upstream's permission
// names map onto the forum's own, and that a TAG-SCOPED key carries
// its scope.
//
// Upstream encodes the scope IN THE KEY (`tag5.viewForum`), and the
// forum keeps the scope in its own column — so the two have to be
// split apart, or a scoped grant would be stored as a global one.
func TestTags_ThePermissionKeysTranslate(t *testing.T) {
	require.Equal(t, domain.PermissionTagDiscussion, domain.ForumPermissionKey("discussion.tag"))
	require.Equal(t, domain.PermissionBypassTagCounts, domain.ForumPermissionKey("bypassTagCounts"))

	// A tag-scoped key carries its scope.
	key, scope := domain.ForumPermissionKeyAndScope("tag5.viewForum")
	require.Equal(t, domain.PermissionViewForum, key)
	require.Equal(t, "5", scope)

	// A key that names no scope has none.
	key, scope = domain.ForumPermissionKeyAndScope("discussion.reply")
	require.Equal(t, domain.PermissionReply, key)
	require.Empty(t, scope)

	// THIS PORT'S tag ids are PocketBase ids, not upstream's integers, so
	// a scoped key carries a 15-character alphanumeric id. A parser that
	// demanded a NUMERIC id would not split it, and the grant would be
	// stored as a GLOBAL row — a restricted tag granting access to
	// everybody.
	key, scope = domain.ForumPermissionKeyAndScope("tagabc123def456ghi.viewForum")
	require.Equal(t, domain.PermissionViewForum, key)
	require.Equal(t, "abc123def456ghi", scope)

	// A permission whose name merely STARTS with "tag" is not a scoped
	// key: it names no tag, so it is passed through unchanged — a key the
	// forum does not know round-trips under its own name.
	key, scope = domain.ForumPermissionKeyAndScope("tagBypassTagCounts")
	require.Equal(t, "tagBypassTagCounts", key)
	require.Empty(t, scope)

	// A key that names no tag at all is left alone.
	key, scope = domain.ForumPermissionKeyAndScope("tag.discussion.reply")
	require.Equal(t, "tag.discussion.reply", key)
	require.Empty(t, scope)
}
