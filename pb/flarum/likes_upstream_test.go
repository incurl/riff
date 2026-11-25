package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// The likes plugin against upstream `flarum/likes`.
//
// The upstream extension's contract is the post resource: the three
// attributes, the `likes` relationship and the policy that guards the
// act of liking. These tests pin the parts a client actually reads,
// because a missing rule is SILENT — the button appears, the server
// refuses, and nothing logs.
//
// The upstream reference is `flarum/likes` (the Flarum 2.0 tree), whose
// `PostResourceFields` contributes `isLiked`, `canLike`, `likesCount`
// and the `likes` relationship.

// upstreamPostAttributes is upstream's attribute set.
//
// It is spelled out here rather than derived, because the point of the
// test is that the SERVER carries exactly what upstream's resource
// carries — a list built from the server's own output would prove
// nothing.
var upstreamPostAttributes = []string{
	"isLiked",
	"canLike",
	"likesCount",
}

// TestLikes_ThePostCarriesUpstreamsAttributes pins that the post
// resource carries upstream's attribute set.
func TestLikes_ThePostCarriesUpstreamsAttributes(t *testing.T) {
	app, handler := newTestForum(t)
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	for _, name := range upstreamPostAttributes {
		require.Contains(t, attributes, name, "the post resource must carry the %q attribute", name)
	}
}

// TestLikes_ThePostCarriesTheLikesRelationship pins that the post
// carries the `likes` relationship, which is where the likers live.
func TestLikes_ThePostCarriesTheLikesRelationship(t *testing.T) {
	app, handler := newTestForum(t)
	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource should carry relationships")

	require.Contains(t, relationships, "likes", "the post must carry the likes relationship")
}

// TestLikes_TheAuthorMayNotLikeItsOwnPostWhenTheSettingForbidsIt
// pins upstream's `LikePostPolicy::like()`.
//
// The policy reads the `flarum-likes.like_own_post` setting, and
// upstream's default is TRUE — so the rule only bites when an
// administrator turns it off. A setting that is never READ would let
// the author like its own post whatever the operator asked for.
func TestLikes_TheAuthorMayNotLikeItsOwnPostWhenTheSettingForbidsIt(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "likes_author")
	_, postID := seedDiscussionWithAuthor(t, app, author.Id, "Likeable", "Like me")

	// Upstream's default: liking your own post is allowed.
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		authorCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// With the setting off, the author may not like its own post.
	removeLike(t, app, postID, author.Id)
	setForumSetting(t, app, "flarum-likes.like_own_post", "0")

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		authorCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestLikes_TheCanLikeFlagMatchesTheEndpoint pins that the `canLike`
// flag and the write path answer the SAME question.
//
// A flag that says "yes" where the endpoint says "no" is the control
// the reader clicks and the server refuses.
func TestLikes_TheCanLikeFlagMatchesTheEndpoint(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "likes_flag_author")
	_, postID := seedDiscussionWithAuthor(t, app, author.Id, "Likeable", "Like me")

	// The author may like its own post: the flag says so.
	attributes := likePostAttributes(t, handler, postID, authorCookies)
	require.Equal(t, true, attributes["canLike"], "the author may like its own post by default")

	// With the setting off, the flag says no AND the endpoint refuses.
	setForumSetting(t, app, "flarum-likes.like_own_post", "0")

	attributes = likePostAttributes(t, handler, postID, authorCookies)
	require.Equal(t, false, attributes["canLike"], "the flag follows the setting")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		authorCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestLikes_TheLikersAreListedWithTheReaderFirst pins upstream's
// ordering of the `likes` relationship.
//
// Upstream sorts the actor's own row to the FRONT before the
// `created_at` order, so the frontend can tell which of the listed
// likers is the reader without a second lookup.
func TestLikes_TheLikersAreListedWithTheReaderFirst(t *testing.T) {
	app, handler := newTestForum(t)

	reader, readerCookies := memberSession(t, app, "likes_reader")
	other, otherCookies := memberSession(t, app, "likes_other")

	_, postID := seedDiscussion(t, app, "Likeable", "Like me")

	// The other account likes first, so it is the OLDER like.
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		otherCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The reader likes second.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		readerCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The reader is FIRST in the list, the other account second.
	ids := likerIDsOf(t, handler, postID, readerCookies)
	require.Len(t, ids, 2, "both likers are listed")
	require.Equal(t, reader.Id, ids[0], "the reader's like comes first")
	require.Equal(t, other.Id, ids[1], "the other liker follows")
}

// TestLikes_TheLikeNotifiesThePostsAuthor pins upstream's
// `SendNotificationWhenPostIsLiked`.
//
// The AUTHOR of the post is notified, and NOT the liker: a like on
// one's own post notifies nobody.
func TestLikes_TheLikeNotifiesThePostsAuthor(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "likes_notify_author")
	_, likerCookies := memberSession(t, app, "likes_notifier")

	_, postID := seedDiscussionWithAuthor(t, app, author.Id, "Likeable", "Like me")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		likerCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The author has a notification about it.
	notifications := listNotifications(t, handler, authorCookies)
	require.Len(t, notifications, 1, "the author is notified")

	notification, ok := notifications[0].(map[string]any)
	require.True(t, ok, "a notification must be an object")

	attributes, ok := notification["attributes"].(map[string]any)
	require.True(t, ok, "a notification must carry attributes")
	require.Equal(t, "postLiked", attributes["type"], "the notification is upstream's kind")

	// The post is a RELATIONSHIP, which is where upstream's blueprint
	// puts it: the notification's SUBJECT is the post.
	relationships, ok := notification["relationships"].(map[string]any)
	require.True(t, ok, "a notification must carry relationships")

	post, ok := relationships["post"].(map[string]any)
	require.True(t, ok, "the notification must carry the post relationship")

	data, ok := post["data"].(map[string]any)
	require.True(t, ok, "the relationship must carry its identifier")
	require.Equal(t, postID, data["id"], "the notification names the post")

	// The liker is NOT notified: nobody is notified about their own act.
	require.Empty(t, listNotifications(t, handler, likerCookies), "the liker is not notified")
}

// TestLikes_TheLikeIsNotNotifiedForItsOwnPost pins that a like on one's
// own post notifies nobody.
func TestLikes_TheLikeIsNotNotifiedForItsOwnPost(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "likes_self_author")
	_, postID := seedDiscussionWithAuthor(t, app, author.Id, "Likeable", "Like me")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		authorCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Empty(t, listNotifications(t, handler, authorCookies), "the author is not notified about their own like")
}

// TestLikes_ThePermissionKeyTranslates pins that upstream's permission
// name maps onto the forum's own.
//
// Without the translation the admin's permission page is inert: the
// key it writes is a key the forum never reads, so granting a
// permission changes nothing.
func TestLikes_ThePermissionKeyTranslates(t *testing.T) {
	require.Equal(t, domain.PermissionLikePost, domain.ForumPermissionKey("discussion.likePosts"))
	require.Equal(t, "discussion.likePosts", domain.UpstreamPermissionKey(domain.PermissionLikePost))
}

// likePostAttributes fetches a post and returns its attributes.
func likePostAttributes(t *testing.T, handler http.Handler, postID string, cookies []*http.Cookie) map[string]any {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	return attributes
}

// likerIDsOf fetches a post's `likes` relationship and returns the ids
// in the order the server listed them.
func likerIDsOf(t *testing.T, handler http.Handler, postID string, cookies []*http.Cookie) []string {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object")

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok, "the resource should carry relationships")

	likes, ok := relationships["likes"].(map[string]any)
	require.True(t, ok, "the post must carry the likes relationship")

	identifiers, ok := likes["data"].([]any)
	require.True(t, ok, "the relationship must carry its identifiers")

	ids := make([]string, 0, len(identifiers))
	for _, item := range identifiers {
		identifier, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := identifier["id"].(string)
		if !ok {
			continue
		}
		ids = append(ids, id)
	}

	return ids
}

// listNotifications fetches an account's notifications.
func listNotifications(t *testing.T, handler http.Handler, cookies []*http.Cookie) []any {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/api/notifications", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].([]any)
	if !ok {
		return nil
	}

	return data
}

// removeLike takes a like back, for a test that needs a clean slate.
func removeLike(t *testing.T, app core.App, postID, userID string) {
	t.Helper()

	collection := mustCollection(t, app, "forum_likes")

	record, err := app.FindFirstRecordByFilter(collection, "post = {:post} && user = {:user}",
		map[string]any{"post": postID, "user": userID})
	if err != nil {
		// No like to remove.
		return
	}

	require.NoError(t, app.Delete(record))
}

// TestLikes_TheUnlikeWorksThroughTheMethodOverride pins the route the way the
// SPA reaches it.
//
// The frame's frontend rewrites every method other than GET and POST into a
// POST carrying `X-HTTP-Method-Override`, and PocketBase bakes the method into
// the route pattern — so `DELETE /api/posts/{id}/like` is unreachable from the
// browser. The route answered that rewrite with the LIKE handler, which is
// idempotent: the button could like and could never unlike, and the response
// said 200 both times.
func TestLikes_TheUnlikeWorksThroughTheMethodOverride(t *testing.T) {
	app, handler := newTestForum(t)

	author, _ := memberSession(t, app, "likes_override_author")
	_, postID := seedDiscussionWithAuthor(t, app, author.Id, "Likeable", "Like me")

	_, likerCookies := memberSession(t, app, "likes_override_liker")
	csrf := map[string]string{"X-CSRF-Token": "csrf-token"}

	override := map[string]string{
		"X-CSRF-Token":           "csrf-token",
		"X-HTTP-Method-Override": http.MethodDelete,
	}

	// A plain POST likes.
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", likerCookies, csrf)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, true, likePostAttributes(t, handler, postID, likerCookies)["isLiked"], "the post must be liked")

	// The SPA's unlike: POST + override.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", likerCookies, override)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, false, likePostAttributes(t, handler, postID, likerCookies)["isLiked"],
		"the override must UNLIKE: the frontend never sends a real DELETE")

	// A real DELETE still works, for a client that can send one.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}", likerCookies, csrf)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = doWithHeaders(t, handler, http.MethodDelete, "/forum/api/posts/"+postID+"/like", "", likerCookies, csrf)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, false, likePostAttributes(t, handler, postID, likerCookies)["isLiked"], "a real DELETE must unlike too")
}

// TestLikes_TheNotificationPreferenceIsPublishedToItsOwner pins that the
// settings page has a preference to toggle.
//
// The grid renders a checkbox per (kind, driver) pair and DISABLES the ones
// the user's `preferences` map does not name, so a key the server never
// publishes is a dead control. Upstream exposes `preferences` to the account
// itself only.
func TestLikes_TheNotificationPreferenceIsPublishedToItsOwner(t *testing.T) {
	app, handler := newTestForum(t)

	owner, ownerCookies := memberSession(t, app, "likes_pref_owner")
	_, otherCookies := memberSession(t, app, "likes_pref_other")

	attributes := userAttributes(t, handler, owner.Id, ownerCookies)
	preferences, ok := attributes["preferences"].(map[string]any)
	require.True(t, ok, "the user resource must carry a preferences map")
	require.Equal(t, true, preferences["notify_postLiked_alert"],
		"the like notification's preference must default to on")

	// Somebody else's preferences are not exposed.
	require.Empty(t, userAttributes(t, handler, owner.Id, otherCookies)["preferences"],
		"preferences belong to the account itself")
}

// TestLikes_TheNotificationHonoursThePreference pins the other half: the
// checkbox has to change what happens.
//
// A preference the reader turns off is read where the notification is
// WRITTEN (`CreateNotification`), so the kind cannot arrive through some
// other producer that forgot to ask.
func TestLikes_TheNotificationHonoursThePreference(t *testing.T) {
	app, handler := newTestForum(t)

	author, authorCookies := memberSession(t, app, "likes_pref_author")
	_, postID := seedDiscussionWithAuthor(t, app, author.Id, "Likeable", "Like me")
	_, likerCookies := memberSession(t, app, "likes_pref_liker")

	repo := testRepo(t, app)
	require.NoError(t, repo.SaveUserProfile(&repository.UserProfile{
		UserID: author.Id,
		// A stored `false` beats the default.
		Preferences: map[string]any{repository.NotificationPreferenceKey("postLiked", "alert"): false},
	}))

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		likerCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Empty(t, listNotifications(t, handler, authorCookies),
		"a reader who turned the kind off must not be notified")

	// Turning it back on is enough: the like itself is untouched.
	require.NoError(t, repo.SaveUserProfile(&repository.UserProfile{
		UserID:      author.Id,
		Preferences: map[string]any{repository.NotificationPreferenceKey("postLiked", "alert"): true},
	}))

	removeLike(t, app, postID, author.Id)

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/like", "{}",
		likerCookies, map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Len(t, listNotifications(t, handler, authorCookies), 1, "the preference must be re-read, not cached")
}
