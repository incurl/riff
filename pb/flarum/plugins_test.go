package flarum

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
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
