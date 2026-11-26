package flarum

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// Sticky, lock and suspend, aligned with upstream's `flarum/sticky`,
// `flarum/lock` and `flarum/suspend` (the framework monorepo, `2.x`).
//
// The three carry one behaviour each that a flag alone does not deliver, and
// this file pins those:
//
//   - STICKY: a pinned discussion comes FIRST in a listing
//     (`PinStickiedDiscussionsToTop`). A pinned discussion that only carries a
//     badge is indistinguishable from one that is not pinned at all.
//   - SUSPEND: the forum document says whether the caller may suspend ANYBODY
//     (`canSuspendUsers`), which is a different question from the per-user
//     `canSuspend`.
//   - LOCK: a locked discussion still accepts a reply from whoever may lock it
//     (upstream's `DiscussionPolicy::reply()`), which the reply path and the
//     `canReply` attribute already did.

// TestSticky_AStickyDiscussionComesFirst pins upstream's pinning.
//
// Without it `is_sticky` is a badge and nothing else: the discussion keeps its
// place in date order, and a reader looking for the pinned announcement finds
// it wherever it fell.
func TestSticky_AStickyDiscussionComesFirst(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	adminCookies := sessionCookies(admin[0].Value, admin[1].Value)

	// The FIRST discussion is the oldest, so date order puts it LAST.
	oldest, _ := seedDiscussion(t, app, "Announcement", "Read me")
	seedDiscussion(t, app, "Middle", "Body")
	seedDiscussion(t, app, "Newest", "Body")

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions/"+oldest+"/sticky", "", adminCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles := discussionTitlesOf(t, recorder.Body.String())
	require.Len(t, titles, 3)
	require.Equal(t, "Announcement", titles[0],
		"a pinned discussion must come first; got %v", titles)
}

// TestSticky_TheSettingTurnsPinningOff pins the switch.
//
// `flarum-sticky.pin_sticky_on_all_discussions` is upstream's, default ON; off
// means the All Discussions page leaves pinned discussions in date order (a tag
// page still pins — see the next test).
func TestSticky_TheSettingTurnsPinningOff(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	adminCookies := sessionCookies(admin[0].Value, admin[1].Value)

	oldest, _ := seedDiscussion(t, app, "Announcement", "Read me")
	seedDiscussion(t, app, "Newest", "Body")

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions/"+oldest+"/sticky", "", adminCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	setForumSetting(t, app, "flarum-sticky.pin_sticky_on_all_discussions", "0")

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles := discussionTitlesOf(t, recorder.Body.String())
	require.Len(t, titles, 2)
	require.Equal(t, "Newest", titles[0],
		"with the setting off the pinned discussion keeps its date position; got %v", titles)
}

// TestSticky_ATagPageAlwaysPins pins upstream's exception.
//
// A tag page is a PLACE rather than a ranking, so upstream pins there even
// when the operator turned the All Discussions pinning off.
func TestSticky_ATagPageAlwaysPins(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)
	adminCookies := sessionCookies(admin[0].Value, admin[1].Value)

	tag := seedTag(t, app, "Announcements", "announcements")

	oldest, _ := seedDiscussion(t, app, "Tagged announcement", "Read me")
	newest, _ := seedDiscussion(t, app, "Tagged newest", "Body")

	attachTag(t, app, oldest, tag.Id)
	attachTag(t, app, newest, tag.Id)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions/"+oldest+"/sticky", "", adminCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The All Discussions page no longer pins ...
	setForumSetting(t, app, "flarum-sticky.pin_sticky_on_all_discussions", "0")

	// ... but the tag page does.
	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions?filter[tag]="+tag.Id, "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles := discussionTitlesOf(t, recorder.Body.String())
	require.Len(t, titles, 2)
	require.Equal(t, "Tagged announcement", titles[0],
		"a tag page pins regardless of the setting; got %v", titles)
}

// TestSuspend_TheForumDocumentCarriesCanSuspendUsers pins the forum-wide flag.
//
// It is a different question from the per-user `canSuspend`: this one is "may
// this caller suspend ANYBODY", which is what a user-list control branches on,
// and upstream serves it on the forum document
// (`$actor->hasPermission('user.suspend')`).
func TestSuspend_TheForumDocumentCarriesCanSuspendUsers(t *testing.T) {
	app, handler := newTestForum(t)

	member := testUser(t, app, "suspendmember")
	memberToken, err := member.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/api", "",
		sessionCookies(memberToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, false, forumAttributesOf(t, recorder.Body.String())["canSuspendUsers"],
		"a member holds no suspend permission")

	moderator := testUser(t, app, "suspendmoderator")
	require.NoError(t, testRepo(t, app).AddUserToGroup(moderator.Id, domain.GroupModeratorID))
	moderatorToken, err := moderator.NewAuthToken()
	require.NoError(t, err)

	recorder = do(t, handler, http.MethodGet, "/forum/api", "",
		sessionCookies(moderatorToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, true, forumAttributesOf(t, recorder.Body.String())["canSuspendUsers"],
		"the moderator group holds user.suspend")
}

// TestModeration_TheOperatorsFlagsAgreeWithTheEndpoints pins the actor
// resolution every plugin `can*` attribute is computed through.
//
// A PocketBase SUPERUSER is not a `users` record, and the module's plugin
// actor used to be resolved from the `users` collection alone — so an
// administrator's own `canLock`, `canSticky` and `canSuspendUsers` all read
// FALSE while the endpoints accepted every one of those writes. The UI draws
// its controls from those flags, so the instance's own operator was shown a
// forum with the moderation controls missing.
func TestModeration_TheOperatorsFlagsAgreeWithTheEndpoints(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	discussionID, _ := seedDiscussion(t, app, "Moderate me", "Body")

	attributes := discussionAttributes(t, handler, discussionID, admin)
	require.True(t, attributes["canLock"].(bool), "an administrator may lock")
	require.True(t, attributes["canSticky"].(bool), "an administrator may pin")

	// And the write is accepted, which is what the flags promise.
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/lock", "", admin,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.True(t, discussionAttributes(t, handler, discussionID, admin)["isLocked"].(bool),
		"the lock the flag promised is the lock the route applied")
}

// TestForum_TheNewestSortWorks pins the sort the index page's "Newest"
// option sends.
//
// `sort=-createdAt` names a column, and the forum's own collections stamp
// `created_at` — `created` is PocketBase's system-field name on its built-in
// collections. Ordering by the wrong one answered 500, so one of the six
// sort options was a broken request rather than a sort.
func TestForum_TheNewestSortWorks(t *testing.T) {
	app, handler := newTestForum(t)

	seedDiscussion(t, app, "Oldest", "Body")
	seedDiscussion(t, app, "Newest", "Body")

	recorder := do(t, handler, http.MethodGet, "/forum/api/discussions?sort=-createdAt", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles := discussionTitlesOf(t, recorder.Body.String())
	require.Len(t, titles, 2)
	require.Equal(t, "Newest", titles[0], "newest first; got %v", titles)

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions?sort=createdAt", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	titles = discussionTitlesOf(t, recorder.Body.String())
	require.Len(t, titles, 2)
	require.Equal(t, "Oldest", titles[0], "oldest first; got %v", titles)
}
