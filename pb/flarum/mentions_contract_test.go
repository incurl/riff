package flarum

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/plugins/mentions"
)

// TestMentions_AnEditDispatchesTheEditedEvent pins Step 3.
//
// `EventPostEdited` was DECLARED and never dispatched: the only
// dispatch sites were the two post CREATION dispatches, neither of
// which is an edit. A plugin that keeps a derived set in step with a
// post's content would therefore never learn that the content
// changed — and would keep the mention the edit REMOVED.
func TestMentions_AnEditDispatchesTheEditedEvent(t *testing.T) {
	app, handler, module := newTestForumModule(t)

	alice := testUser(t, app, "alice")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// A listener on the module's own bus records the call.
	fired := make(chan string, 4)
	module.bus.SubscribeAfter(flarumcore.EventPostEdited, "test", 0,
		func(_ context.Context, event flarumcore.Event) {
			fired <- event.Target
		})

	body := postBody(t, app, alice.Id, "hello")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hello")

	patch := `{"data":{"type":"posts","attributes":{"content":"hello again"}}}`
	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID, patch, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	select {
	case target := <-fired:
		require.Equal(t, postID, target, "the event should name the edited post")
	default:
		t.Fatal("the edit should have dispatched forum.post.edited")
	}
}

// TestMentions_TheNotificationCarriesTheKindsTheSPAReads pins Step 5.
//
// **THE KIND TRAVELS AS `contentType`, NOT `type`.** The vendored SPA
// looks the component up by that name: `NotificationType.tsx` reads
// `notification.contentType()` and uses it as the key into
// `app.notificationComponents`. A notification that carries the kind
// under any other name has an undefined contentType, the component
// lookup misses, and the list renders an EMPTY ROW — while the unread
// badge still counts it, because the badge reads `isRead`.
//
// The symptom is a badge with a blank list.
func TestMentions_TheNotificationCarriesTheKindsTheSPAReads(t *testing.T) {
	app, handler := newTestForum(t)

	// The mentioned account has to EXIST: a mention of an account that
	// does not resolve is inert.
	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	// BOB writes the post, so ALICE is the one who is mentioned.
	bobToken, err := bob.NewAuthToken()
	require.NoError(t, err)
	bobCookies := sessionCookies(bobToken, "csrf-token")

	aliceToken, err := alice.NewAuthToken()
	require.NoError(t, err)
	aliceCookies := sessionCookies(aliceToken, "csrf-token")

	body := postBody(t, app, bob.Id, "hey @alice")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, bobCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	// The notification belongs to the MENTIONED account, so the inbox
	// is read as Alice.
	recorder = do(t, handler, http.MethodGet, "/forum/api/notifications", "", aliceCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the data should be a collection")
	require.Len(t, resources, 1, "the mentioned account should have one notification")

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok)

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the notification should carry attributes")

	contentType, ok := attributes["contentType"].(string)
	require.True(t, ok, "the notification should carry contentType")
	require.Equal(t, mentions.NotificationType, contentType,
		"the SPA looks the component up by contentType")

	_, ok = attributes["content"]
	require.True(t, ok, "the notification should carry content")

	// The notification names the account that did the mentioning.
	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the notification should carry relationships")

	fromUser, ok := relationships["fromUser"].(map[string]any)
	require.True(t, ok, "the notification should carry the fromUser relationship")

	identifier, ok := fromUser["data"].(map[string]any)
	require.True(t, ok, "the relationship should carry an identifier")
	require.Equal(t, bob.Id, identifier["id"], "the notification should name the author")
}

// TestMentions_TheMentionedAccountIsNotifiedOnce pins Step 5.
//
// One notification per mentioning post: a re-save must not duplicate
// it.
func TestMentions_TheMentionedAccountIsNotifiedOnce(t *testing.T) {
	app, handler := newTestForum(t)

	alice := testUser(t, app, "alice")
	bob := testUser(t, app, "bob")

	token, err := alice.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	body := postBody(t, app, alice.Id, "hey @bob")
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts", body, cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := postWithContent(t, app, alice.Id, "hey @bob")
	require.Len(t, notificationRows(t, app, bob.Id), 1, "the mention should notify once")

	// A re-save of the same content must not duplicate the
	// notification.
	patch := `{"data":{"type":"posts","attributes":{"content":"hey @bob"}}}`
	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID, patch, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Len(t, notificationRows(t, app, bob.Id), 1,
		"a re-save should not duplicate the notification")
}
