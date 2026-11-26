package flarum

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/plugins/tags"
	"github.com/incurl/riff/pb/flarum/repository"
)

// seedTag creates a tag through the plugin's own collection.
//
// The tag is created directly rather than through the API, because the
// forum's v1 scope has no tag-creation endpoint: tags are seeded by the
// operator.
func seedTag(t *testing.T, app core.App, name, slug string) *core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("forum_tags")
	require.NoError(t, err)

	record := core.NewRecord(collection)
	record.Set("name", name)
	record.Set("slug", slug)
	// An UNPLACED tag, which is what upstream calls a secondary tag: it has
	// no place in the tree until an administrator drags it into one. The
	// resource serves `position: null` for it (`is_primary` is false), and
	// the count rules count it as a secondary tag — which is what the tests
	// that use this fixture for the tag-count rules expect.
	//
	// A PRIMARY tag is seeded by setting `is_primary` too, as the tests that
	// need one do.
	record.Set("position", 0)
	require.NoError(t, app.Save(record))

	return record
}

// TestTags_MigrationCreatesTheCollection pins layer 1 of the three-layer
// flow: the plugin's own migration created its collection.
func TestTags_MigrationCreatesTheCollection(t *testing.T) {
	app, _ := newTestForum(t)

	collection, err := app.FindCollectionByNameOrId("forum_tags")
	require.NoError(t, err, "the tags plugin must create its collection")

	// The collection is a plain base collection: the forum does not
	// turn a plugin's data into an auth collection.
	require.Equal(t, core.CollectionTypeBase, collection.Type)
}

// TestTags_ListEndpointServesTheTagResource pins layer 2: the tag is a
// first-class JSON:API resource with the attribute names the frontend
// reads.
func TestTags_ListEndpointServesTheTagResource(t *testing.T) {
	app, handler := newTestForum(t)
	seedTag(t, app, "Announcements", "announcements")

	recorder := do(t, handler, http.MethodGet, "/forum/api/tags", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 1)

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok, "the resource should be an object")
	require.Equal(t, api.TypeTag, resource["type"])

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")
	require.Equal(t, "Announcements", attributes["name"])
	require.Equal(t, "announcements", attributes["slug"])
	require.Contains(t, attributes, "isPrimary")
	require.Contains(t, attributes, "isRestricted")
	require.Contains(t, attributes, "discussionCount")
}

// TestTags_ShowEndpointResolvesBySlug pins that a tag is addressable by
// its slug, which is what the frontend's tag URLs use.
func TestTags_ShowEndpointResolvesBySlug(t *testing.T) {
	app, handler := newTestForum(t)
	seedTag(t, app, "Announcements", "announcements")

	recorder := do(t, handler, http.MethodGet, "/forum/api/tags/announcements", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")
	require.Equal(t, api.TypeTag, resource["type"])
}

// TestTags_DiscussionCarriesTheTagRelationship pins layer 2 for the
// relationship: a discussion exposes its tags as a standard JSON:API
// relationship, not as an embedded attribute.
func TestTags_DiscussionCarriesTheTagRelationship(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Announcements", "announcements")

	// A discussion with the tag attached.
	discussionID, postID := seedDiscussion(t, app, "Welcome", "Hello there")
	require.NotEmpty(t, discussionID)
	require.NotEmpty(t, postID)

	attachTag(t, app, discussionID, tag.Id)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	tags, ok := relationships["tags"].(map[string]any)
	require.True(t, ok, "the discussion must carry a tags relationship")

	data, ok := tags["data"].([]any)
	require.True(t, ok, "the tags relationship must carry identifiers")
	require.Len(t, data, 1)

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok, "the identifier should be an object")
	require.Equal(t, api.TypeTag, identifier["type"])
	require.Equal(t, tag.Id, identifier["id"])
}

// TestTags_TagFilterNarrowsTheDiscussionList pins the tag filter the
// frontend's tag page uses.
func TestTags_TagFilterNarrowsTheDiscussionList(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Announcements", "announcements")

	taggedID, _ := seedDiscussion(t, app, "Tagged", "With a tag")
	untaggedID, _ := seedDiscussion(t, app, "Untagged", "Without a tag")
	attachTag(t, app, taggedID, tag.Id)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions?filter[tag]="+tag.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 1)

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok, "the resource should be an object")
	require.Equal(t, taggedID, resource["id"])
	require.NotEqual(t, untaggedID, resource["id"])
}

// TestTags_TagFilterAcceptsTheSlug pins the spelling the FRONTEND sends.
//
// The tag page's URL is `/t/<slug>`, and upstream's `TagGambit` resolves a
// slug, so the search param the extension forwards to `filter[tag]` is the
// slug. The port's filter matched a tag RECORD ID only, so a tag page would
// have listed nothing — silently, and under a heading that says the tag has
// no discussions.
func TestTags_TagFilterAcceptsTheSlug(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Guides", "guides")

	taggedID, _ := seedDiscussion(t, app, "Tagged by slug", "With a tag")
	seedDiscussion(t, app, "Untagged by slug", "Without a tag")
	attachTag(t, app, taggedID, tag.Id)

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions?filter[tag]=guides", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 1, "the slug must select the tagged discussion and only it")

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok, "the resource should be an object")
	require.Equal(t, taggedID, resource["id"])
}

// TestTags_TheTagListCarriesEveryTag pins what the LABELS resolve against.
//
// The frontend does not read a discussion's tags out of `included` — it cannot,
// because a tag resource is serialised by the tags PLUGIN and the core's
// `included` array only builds the resources it knows. The store is filled by
// `GET /api/tags` instead, and `discussion.tags()` resolves the relationship's
// identifiers against it. So the guarantee that matters is that the endpoint
// returns the tag with its slug and colour, which is what a label renders.
func TestTags_TheTagListCarriesEveryTag(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Listed", "listed")

	recorder := do(t, handler, http.MethodGet, "/forum/api/tags", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")

	found := false
	for _, entry := range resources {
		resource, ok := entry.(map[string]any)
		if !ok || resource["id"] != tag.Id {
			continue
		}

		attributes, ok := resource["attributes"].(map[string]any)
		require.True(t, ok, "the tag must carry attributes")
		require.Equal(t, "listed", attributes["slug"], "the label links by slug")

		found = true
	}

	require.True(t, found, "the tag must be listed: %v", resources)
}

// TestTags_AFreshDiscussionCarriesTheChosenTags pins the composer's write.
//
// The composer's `data()` sends the chosen tags as the `tags` RELATIONSHIP,
// and the store serialises a model array into `{"data": [{"type": "tags",
// "id": "…"}]}`. The server's relationship writer takes those ids.
//
// This is the whole composer path in one assertion: without the
// relationship the tags are chosen and then silently dropped, and the
// discussion appears untagged.
func TestTags_AFreshDiscussionCarriesTheChosenTags(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Composer", "composer")

	// A PRIMARY tag: `HasPosition` is `position != 0 || is_primary`, and
	// the count rules count the two kinds separately — upstream's own
	// wording for the failure is "the number of primary tags must be
	// exactly 1".
	tagRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), tag.Id)
	require.NoError(t, err)
	tagRecord.Set("is_primary", true)
	require.NoError(t, app.Save(tagRecord))

	_, cookies := memberSession(t, app, "composer_member")

	body := `{"data":{"type":"discussions","attributes":{"title":"With a tag","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + tag.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, cookies, nil)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	tags, ok := relationships["tags"].(map[string]any)
	require.True(t, ok, "the discussion must carry a tags relationship")

	data, ok := tags["data"].([]any)
	require.True(t, ok, "the tags relationship must carry identifiers")
	require.Len(t, data, 1, "the chosen tag must be on the discussion")

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok, "the identifier should be an object")
	require.Equal(t, tag.Id, identifier["id"])
}

// TestTags_AnIncludedDiscussionCarriesItsTags pins the INCLUDED copy.
//
// A discussion also travels as an `included` resource: the posts endpoint
// carries it (`defaultIncludesFor(TypePost)` is `["user", "discussion"]`),
// and so does every other response that names a discussion as a relation.
// The store does not keep two copies — `Model.pushData` MERGES an included
// resource into the model it already holds, and a relationship the included
// copy names is taken from it verbatim.
//
// That copy used to be built with no tag ids at all, so its `tags`
// relationship said "this discussion has no tags". The tag was saved, the
// composer showed it, the create response carried it — and the next payload
// (the post stream's, in the browser) took it out of the store, so the
// discussion page rendered an untagged discussion. The same copy carried
// `canReply: false` and friends, which is why the flags are asserted here
// too: an included resource that lies about them disables controls on a
// discussion that allows them.
func TestTags_AnIncludedDiscussionCarriesItsTags(t *testing.T) {
	app, handler := newTestForum(t)

	tag := seedTag(t, app, "Included", "included")
	tagRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), tag.Id)
	require.NoError(t, err)
	tagRecord.Set("is_primary", true)
	require.NoError(t, app.Save(tagRecord))

	_, cookies := memberSession(t, app, "included_member")

	body := `{"data":{"type":"discussions","attributes":{"title":"Tagged","content":"Hello"},` +
		`"relationships":{"tags":{"data":[{"type":"tags","id":"` + tag.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, cookies, nil)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	created := decodeDocument(t, recorder)
	resource, ok := created.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")
	discussionID, ok := resource["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, discussionID)

	// The SPA's own request for the stream, with no `include` parameter:
	// the default include set is what carries the discussion.
	recorder = doWithHeaders(t, handler, http.MethodGet,
		"/forum/api/posts?filter[discussion]="+discussionID, "", cookies, nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	discussions := includedResourcesOf(t, recorder.Body.String(), "discussions")
	require.Len(t, discussions, 1, "the post list should include the discussion")

	relationships, ok := discussions[0]["relationships"].(map[string]any)
	require.True(t, ok, "the included discussion must carry relationships")

	tags, ok := relationships["tags"].(map[string]any)
	require.True(t, ok, "the included discussion must carry a tags relationship")

	data, ok := tags["data"].([]any)
	require.True(t, ok)
	require.Len(t, data, 1, "an included discussion that claims no tags wipes the store's copy")

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, tag.Id, identifier["id"])

	attributes, ok := discussions[0]["attributes"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, attributes["canReply"], "the included copy must not report false permissions")
}

// The control saves the discussion with the `tags` RELATIONSHIP, and the
// server's writer REPLACES the set — a tag the client left out is removed.
//
// The gate is the author's own, and it needs `allow_tag_change`: the
// setting is explicitly about changing tags AFTER posting, so a create and
// a change are different questions (see `writeDiscussionTags`).
func TestTags_AnExistingDiscussionsTagsCanBeChanged(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "tag_change_author")
	setForumSetting(t, app, "allow_tag_change", "-1")

	primary := seedTag(t, app, "Primary", "primary")
	primaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), primary.Id)
	require.NoError(t, err)
	primaryRecord.Set("is_primary", true)
	require.NoError(t, app.Save(primaryRecord))

	other := seedTag(t, app, "Other", "other")
	otherRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), other.Id)
	require.NoError(t, err)
	otherRecord.Set("is_primary", true)
	require.NoError(t, app.Save(otherRecord))

	// The author's OWN discussion: the author rule is about the author.
	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Tagged", "Hello")
	attachTag(t, app, discussionID, primary.Id)

	// The set REPLACES: the primary tag goes, the other arrives.
	//
	// Both are PRIMARY, because the count rules are about the KIND: a set
	// of one SECONDARY tag breaks the primary minimum (upstream's own
	// wording — "the number of primary tags must be exactly 1"), which is
	// a different rule and is pinned elsewhere.
	body := `{"data":{"type":"discussions","relationships":{"tags":{"data":[{"type":"tags","id":"` +
		other.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID, body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource must carry relationships")

	tags, ok := relationships["tags"].(map[string]any)
	require.True(t, ok, "the discussion must carry a tags relationship")

	data, ok := tags["data"].([]any)
	require.True(t, ok, "the tags relationship must carry identifiers")
	require.Len(t, data, 1, "the set is REPLACED: the old tag is gone")
	require.Equal(t, other.Id, data[0].(map[string]any)["id"])
}

// TestTags_ChangingTagsNeedsTheAllowTagChangeSetting pins the gate.
//
// The setting is upstream's: "when an author may still change a
// discussion's tags after posting it". Its default is "never" — an unset
// setting permits nothing — so a discussion whose author tries to change
// the tags is REFUSED, and the `canTag` flag the control reads is false.
//
// The two halves have to agree: a control that is offered when the save
// would be refused is a dead affordance, and a save that succeeds when the
// control is absent would be a way around the setting.
func TestTags_ChangingTagsNeedsTheAllowTagChangeSetting(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "tag_change_gate_author")

	tag := seedTag(t, app, "Gated", "gated")
	tagRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), tag.Id)
	require.NoError(t, err)
	tagRecord.Set("is_primary", true)
	require.NoError(t, app.Save(tagRecord))

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Gated", "Hello")

	body := `{"data":{"type":"discussions","relationships":{"tags":{"data":[{"type":"tags","id":"` +
		tag.Id + `"}]}}}}`

	// The setting is UNSET, which is upstream's default: never.
	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID, body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	// And the flag the control reads agrees: the author may not tag it.
	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", authorCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the discussion must carry attributes")
	require.Equal(t, false, attributes["canTag"], "the control must not be offered when the save would be refused")
}

// TestForum_RenamingADiscussionRecordsAnEventPost pins the stream's record
// of a rename.
//
// Upstream keeps the rename in the POST HISTORY: a post of the type
// `discussionRenamed` whose content is `[oldTitle, newTitle]`. The port had
// the post type and the frontend COMPONENT — and never created the post, so
// a rename was invisible in the stream.
//
// The content must reach the frontend as an ARRAY: `post.content()[0]` and
// `post.content()[1]` are the two titles, and a string would make them its
// first two CHARACTERS.
func TestForum_RenamingADiscussionRecordsAnEventPost(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "rename_author")
	setForumSetting(t, app, "allow_renaming", "-1")

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Old title", "Hello")

	body := `{"data":{"type":"discussions","attributes":{"title":"New title"}}}`

	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID, body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/posts?filter[discussion]="+discussionID, "", authorCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 2, "the first post and the rename event post")

	var eventPost map[string]any

	for _, entry := range resources {
		candidate, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		attributes, ok := candidate["attributes"].(map[string]any)
		if !ok {
			continue
		}

		if attributes["type"] == "discussionRenamed" {
			eventPost = attributes
		}
	}

	require.NotNil(t, eventPost, "the stream must carry the rename event post: %v", resources)

	content, ok := eventPost["content"].([]any)
	require.True(t, ok, "the event post's content must be an ARRAY, not a string: %v", eventPost["content"])
	require.Len(t, content, 2, "the content is [oldTitle, newTitle]")
	require.Equal(t, "Old title", content[0])
	require.Equal(t, "New title", content[1])
}

// TestForum_ARenameDoesNotChangeTheCommentCount pins that an event post is
// not a reply.
//
// Upstream's `AbstractEventPost` is excluded from the discussion's comment
// count: it records an event, it is not something anyone said. A rename
// that raised the count would make the discussion's reply count disagree
// with the number of replies in its stream.
func TestForum_ARenameDoesNotChangeTheCommentCount(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "rename_count_author")
	setForumSetting(t, app, "allow_renaming", "-1")

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Before", "Hello")

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", authorCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	before := decodeDocument(t, recorder)
	beforeResource, ok := before.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")
	beforeAttributes, ok := beforeResource["attributes"].(map[string]any)
	require.True(t, ok, "the discussion must carry attributes")
	beforeCount := beforeAttributes["commentCount"]

	body := `{"data":{"type":"discussions","attributes":{"title":"After"}}}`

	recorder = doWithHeaders(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID, body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", authorCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	after := decodeDocument(t, recorder)
	afterResource, ok := after.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")
	afterAttributes, ok := afterResource["attributes"].(map[string]any)
	require.True(t, ok, "the discussion must carry attributes")

	require.Equal(t, beforeCount, afterAttributes["commentCount"],
		"an event post is not a reply and must not raise the comment count")
}

// TestTags_ChangingTheTagsRecordsAnEventPost pins the stream's record of it.
//
// Upstream creates a `discussionTagged` post when a discussion's tags
// change, so a reader can see that a discussion was retagged and when. Its
// content is `[oldTagIds, newTagIds]` — TWO ARRAYS — and the frontend
// diffs them to say which tags were added and which removed.
//
// The content must reach the frontend as an ARRAY: `post.content()[0]` is
// the first element, and a string would make it the first CHARACTER.
func TestTags_ChangingTheTagsRecordsAnEventPost(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "event_post_author")
	setForumSetting(t, app, "allow_tag_change", "-1")

	first := seedTag(t, app, "First", "first")
	firstRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), first.Id)
	require.NoError(t, err)
	firstRecord.Set("is_primary", true)
	require.NoError(t, app.Save(firstRecord))

	second := seedTag(t, app, "Second", "second")
	secondRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), second.Id)
	require.NoError(t, err)
	secondRecord.Set("is_primary", true)
	require.NoError(t, app.Save(secondRecord))

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Event", "Hello")
	attachTag(t, app, discussionID, first.Id)

	body := `{"data":{"type":"discussions","relationships":{"tags":{"data":[{"type":"tags","id":"` +
		second.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID, body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The STREAM carries the event post.
	recorder = do(t, handler, http.MethodGet, "/forum/api/posts?filter[discussion]="+discussionID, "", authorCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 2, "the first post and the event post")

	// The event post's content is an ARRAY.
	var eventPost map[string]any

	for _, entry := range resources {
		candidate, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		attributes, ok := candidate["attributes"].(map[string]any)
		if !ok {
			continue
		}

		if attributes["type"] == "discussionTagged" {
			eventPost = attributes
		}
	}

	require.NotNil(t, eventPost, "the stream must carry the event post: %v", resources)

	content, ok := eventPost["content"].([]any)
	require.True(t, ok, "the event post's content must be an ARRAY, not a string: %v", eventPost["content"])
	require.Len(t, content, 2, "the content is [oldTagIds, newTagIds]")

	oldIDs, ok := content[0].([]any)
	require.True(t, ok, "the old set must be an array")
	require.Len(t, oldIDs, 1)
	require.Equal(t, first.Id, oldIDs[0])

	newIDs, ok := content[1].([]any)
	require.True(t, ok, "the new set must be an array")
	require.Len(t, newIDs, 1)
	require.Equal(t, second.Id, newIDs[0])
}

// TestTags_ResendingTheSameTagsRecordsNoPost pins that nothing changed
// means nothing is recorded.
//
// Upstream returns early when the old and new tag ids are equal, and a post
// saying "the tags did not change" is noise in the stream. The comparison
// is order-insensitive too: a client that re-sends the same tags in a
// different order has not changed anything.
func TestTags_ResendingTheSameTagsRecordsNoPost(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "no_event_author")
	setForumSetting(t, app, "allow_tag_change", "-1")

	first := seedTag(t, app, "Same", "same")
	firstRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), first.Id)
	require.NoError(t, err)
	firstRecord.Set("is_primary", true)
	require.NoError(t, app.Save(firstRecord))

	discussionID, _ := seedDiscussionWithAuthor(t, app, author.Id, "Unchanged", "Hello")
	attachTag(t, app, discussionID, first.Id)

	// The SAME set. (A set of two PRIMARY tags would break the maximum of
	// one, so the "same set in another order" case needs a SECONDARY tag —
	// which is what a re-send of a realistic set looks like.)
	secondary := seedTag(t, app, "Secondary", "secondary")
	secondaryRecord, err := app.FindRecordById(mustCollection(t, app, "forum_tags"), secondary.Id)
	require.NoError(t, err)
	secondaryRecord.Set("position", nil)
	require.NoError(t, app.Save(secondaryRecord))
	attachTag(t, app, discussionID, secondary.Id)

	// The SAME set, in the other order.
	body := `{"data":{"type":"discussions","relationships":{"tags":{"data":[{"type":"tags","id":"` +
		secondary.Id + `"},{"type":"tags","id":"` + first.Id + `"}]}}}}`

	recorder := doWithHeaders(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID, body, authorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/posts?filter[discussion]="+discussionID, "", authorCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a resource collection")
	require.Len(t, resources, 1, "nothing changed, so no event post: %v", resources)
}

// TestTags_TheForumDocumentCarriesTheCountSettings pins the limits the
// composer's picker enforces.
//
// The picker reads them from the forum document (`app.forum.attribute(
// 'minPrimaryTags')`), and the SERVER enforces the same counts. A modal
// that ignored them would let an author compose a discussion the server
// then refuses — the worst way to enforce a minimum.
//
// The values are the settings' DEFAULTS, because the settings are unset on
// a fresh instance and the server falls back to them.
func TestTags_TheForumDocumentCarriesTheCountSettings(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the forum document must carry attributes")

	require.Equal(t, float64(tags.DefaultMinPrimaryTags), attributes["minPrimaryTags"])
	require.Equal(t, float64(tags.DefaultMaxPrimaryTags), attributes["maxPrimaryTags"])
	require.Equal(t, float64(tags.DefaultMinSecondaryTags), attributes["minSecondaryTags"])
	require.Equal(t, float64(tags.DefaultMaxSecondaryTags), attributes["maxSecondaryTags"])
}

// TestLikes_MigrationCreatesTheCollection pins layer 1 for the Likes
// plugin.
func TestLikes_MigrationCreatesTheCollection(t *testing.T) {
	app, _ := newTestForum(t)

	collection, err := app.FindCollectionByNameOrId("forum_likes")
	require.NoError(t, err, "the likes plugin must create its collection")

	// The unique index on (post, user) is what prevents a duplicate
	// like.
	index := collection.GetIndex("idx_forum_likes_post_user")
	require.NotEmpty(t, index, "the likes collection must have the unique (post, user) index")
}

// TestLikes_PostCarriesTheLikeAttributes pins layer 2 for the Likes
// plugin: the like state and the count are attributes on the post
// resource, which is where the frontend's like button reads them.
func TestLikes_PostCarriesTheLikeAttributes(t *testing.T) {
	app, handler := newTestForum(t)
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")
	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	require.Contains(t, attributes, "likesCount")
	require.Contains(t, attributes, "isLiked")
	require.Contains(t, attributes, "canLike")
	require.Equal(t, float64(0), attributes["likesCount"])
	require.Equal(t, false, attributes["isLiked"])
}

// TestLikes_LikeRequiresAuthentication pins the permission gate.
func TestLikes_LikeRequiresAuthentication(t *testing.T) {
	app, handler := newTestForum(t)
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	recorder := do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", nil)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
}

// TestLikes_LikeIsIdempotent pins that a second like does not inflate
// the count: the unique index and the idempotent insert both guard it.
func TestLikes_LikeIsIdempotent(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "liker")
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	// Sign in as the user so the request carries a session.
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	first := do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", cookies)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", cookies)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	collection, err := app.FindCollectionByNameOrId("forum_likes")
	require.NoError(t, err)
	records, err := app.FindAllRecords(collection)
	require.NoError(t, err)
	require.Len(t, records, 1, "a duplicate like must not create a second row")
}

// TestLikes_UnlikeRemovesTheRow pins the unlike path.
func TestLikes_UnlikeRemovesTheRow(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "liker")
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	like := do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", cookies)
	require.Equal(t, http.StatusOK, like.Code, like.Body.String())

	unlike := do(t, handler, http.MethodDelete, "/forum/api/posts/"+postID+"/like", "", cookies)
	require.Equal(t, http.StatusOK, unlike.Code, unlike.Body.String())

	collection, err := app.FindCollectionByNameOrId("forum_likes")
	require.NoError(t, err)
	records, err := app.FindAllRecords(collection)
	require.NoError(t, err)
	require.Empty(t, records)
}

// TestLikes_PostCarriesTheLikersRelationship pins layer 2 for the
// relationship: the likers are exposed as a standard JSON:API
// relationship on the post.
func TestLikes_PostCarriesTheLikersRelationship(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "liker")
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	token, err := user.NewAuthToken()
	require.NoError(t, err)

	like := do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusOK, like.Code, like.Body.String())

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource should carry relationships")
	likes, ok := relationships["likes"].(map[string]any)
	require.True(t, ok, "the post must carry a likes relationship")

	data, ok := likes["data"].([]any)
	require.True(t, ok, "the likes relationship should carry identifiers")
	require.Len(t, data, 1)

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok, "the identifier should be an object")
	require.Equal(t, api.TypeUser, identifier["type"])
	require.Equal(t, user.Id, identifier["id"])
}

// TestPlugin_MigrationVersionIsRecorded pins that the applied migration
// versions are recorded per plugin.
func TestPlugin_MigrationVersionIsRecorded(t *testing.T) {
	app, _ := newTestForum(t)

	collection, err := app.FindCollectionByNameOrId("forum_plugin_migrations")
	require.NoError(t, err)

	records, err := app.FindAllRecords(collection)
	require.NoError(t, err)
	require.NotEmpty(t, records)

	plugins := map[string]bool{}
	for _, record := range records {
		plugins[record.GetString("plugin_id")] = true
	}
	require.True(t, plugins["riff/tags"], "the tags plugin's migration must be recorded")
	require.True(t, plugins["riff/likes"], "the likes plugin's migration must be recorded")
}

// TestPlugin_MigrationIsIdempotent pins that loading the forum twice
// does not create a second migration row.
func TestPlugin_MigrationIsIdempotent(t *testing.T) {
	app := newTestApp(t)

	module, err := newModule(app, Options{BasePath: DefaultBasePath, Logger: slogDiscard()})
	require.NoError(t, err)
	require.NoError(t, module.boot(context.Background()))

	collection, err := app.FindCollectionByNameOrId("forum_plugin_migrations")
	require.NoError(t, err)
	first, err := app.FindAllRecords(collection)
	require.NoError(t, err)

	// A second module over the same app re-runs the migrations.
	second, err := newModule(app, Options{BasePath: DefaultBasePath, Logger: slogDiscard()})
	require.NoError(t, err)
	require.NoError(t, second.boot(context.Background()))

	after, err := app.FindAllRecords(collection)
	require.NoError(t, err)
	require.Len(t, after, len(first), "the migration rows must not be duplicated")
}

// TestTags_AnotherMemberCannotTagADiscussion pins the AUTHOR rule on
// the tag routes.
//
// Attaching and detaching a tag CHANGES a discussion, so the caller
// must be allowed to change it. The handler used to check only that
// the caller was signed in, which meant every member could tag — and
// untag — any discussion in the forum.
func TestTags_AnotherMemberCannotTagADiscussion(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Announcements", "announcements")

	author := testUser(t, app, "tag_author")
	intruder := testUser(t, app, "tag_intruder")

	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	// The author starts a discussion.
	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Mine","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	discussionID := created.Data.ID
	require.NotEmpty(t, discussionID)

	body := `{"data":{"attributes":{"tag":"announcements"}}}`

	// The intruder may not attach a tag to it.
	recorder = do(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/tags",
		body, sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The intruder may not detach one either.
	recorder = do(t, handler, http.MethodDelete,
		"/forum/api/discussions/"+discussionID+"/tags/"+tag.Id, "",
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The author may attach one — once the setting permits it.
	setForumSetting(t, app, "allow_tag_change", "-1")

	recorder = do(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/tags",
		body, sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	// And detach it again.
	recorder = do(t, handler, http.MethodDelete,
		"/forum/api/discussions/"+discussionID+"/tags/"+tag.Id, "",
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}

// TestTags_TheAuthorMayNotTagUntilTheSettingPermits pins upstream's
// default for the tag policy.
//
// Upstream's tags extension has the setting OFF until an
// administrator picks an option: "Until an option is saved, authors
// can't change their tags" (the upstream Permissions page). The
// policy permits three shapes — "-1" (always), "reply" (while the
// discussion has one participant) and a number of minutes — and nothing
// else.
func TestTags_TheAuthorMayNotTagUntilTheSettingPermits(t *testing.T) {
	app, handler := newTestForum(t)
	tag := seedTag(t, app, "Announcements", "announcements")

	author := testUser(t, app, "tag_policy_author")
	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Mine","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	discussionID := created.Data.ID

	body := `{"data":{"attributes":{"tag":"announcements"}}}`

	// The default is "Never", so even the AUTHOR may not tag.
	recorder = do(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/tags",
		body, sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// A number of minutes permits it while the discussion is that
	// young.
	setForumSetting(t, app, "allow_tag_change", "10")

	recorder = do(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/tags",
		body, sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	// "reply" permits it while nobody has answered.
	setForumSetting(t, app, "allow_tag_change", "reply")

	recorder = do(t, handler, http.MethodDelete,
		"/forum/api/discussions/"+discussionID+"/tags/"+tag.Id, "",
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}

// setForumSetting writes a forum setting through the repository.
func setForumSetting(t *testing.T, app core.App, key, value string) {
	t.Helper()

	repo := repository.New(app, repository.NewRegistry(repository.DefaultPrefix))
	require.NoError(t, repo.SaveSetting(key, value, ""))
}
