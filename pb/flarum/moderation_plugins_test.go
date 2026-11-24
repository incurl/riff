package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The Lock, Sticky and Suspend plugins each own a column on a CORE
// collection, so the two failure modes worth pinning are the ones a
// column change invites:
//
//   - the write path and the reported flag disagree (the UI offers a
//     control whose endpoint refuses, or hides one that would work);
//   - a flag that used to be encoded in `state` is lost, which for lock is
//     a moderation failure (a closed discussion reopens) and for sticky is
//     a silent unpin.

// moderatorSession signs a moderator in and returns the record plus its
// cookies.
func moderatorSession(t *testing.T, app core.App, username string) (*core.Record, []*http.Cookie) {
	t.Helper()

	user := testUser(t, app, username)
	require.NoError(t, testRepo(t, app).AddUserToGroup(user.Id, domain.GroupModeratorID))

	token, err := user.NewAuthToken()
	require.NoError(t, err)

	return user, sessionCookies(token, "csrf-token")
}

// memberSession signs an ordinary member in.
func memberSession(t *testing.T, app core.App, username string) (*core.Record, []*http.Cookie) {
	t.Helper()

	user := testUser(t, app, username)

	token, err := user.NewAuthToken()
	require.NoError(t, err)

	return user, sessionCookies(token, "csrf-token")
}

// postAs posts a reply and returns the recorder.
func postAs(t *testing.T, handler http.Handler, discussionID, content string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	body := `{"data":{"type":"posts","attributes":{"content":"` + content + `"},` +
		`"relationships":{"discussion":{"data":{"type":"discussions","id":"` + discussionID + `"}}}}}`

	return doWithHeaders(t, handler, http.MethodPost, "/forum/api/posts", body, cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
}

// resourceAttributes fetches a resource and returns its attributes.
func resourceAttributes(t *testing.T, handler http.Handler, path string, cookies []*http.Cookie) map[string]any {
	t.Helper()

	recorder := do(t, handler, http.MethodGet, "/forum/api/"+path, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := data["attributes"].(map[string]any)
	require.True(t, ok, "the resource must carry attributes")

	return attributes
}

// discussionAttributes fetches a discussion's attributes.
func discussionAttributes(t *testing.T, handler http.Handler, discussionID string, cookies []*http.Cookie) map[string]any {
	t.Helper()
	return resourceAttributes(t, handler, "discussions/"+discussionID, cookies)
}

// userAttributes fetches a user's attributes.
func userAttributes(t *testing.T, handler http.Handler, userID string, cookies []*http.Cookie) map[string]any {
	t.Helper()
	return resourceAttributes(t, handler, "users/"+userID, cookies)
}

// TestLock_TheToggleSetsAndClearsTheFlag pins the write path and the
// reported attribute together.
func TestLock_TheToggleSetsAndClearsTheFlag(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := moderatorSession(t, app, "mod_lock")
	discussionID, _ := seedDiscussion(t, app, "Lockable", "Hello")

	require.False(t, discussionAttributes(t, handler, discussionID, cookies)["isLocked"].(bool), "a new discussion is not locked")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/lock", "", cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.True(t, discussionAttributes(t, handler, discussionID, cookies)["isLocked"].(bool), "the flag must reach the resource")

	recorder = doWithHeaders(t, handler, http.MethodDelete, "/forum/api/discussions/"+discussionID+"/lock", "", cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.False(t, discussionAttributes(t, handler, discussionID, cookies)["isLocked"].(bool))
}

// TestLock_ALockedDiscussionRefusesRepliesButNotFromAModerator pins
// upstream's `Lock\Access\DiscussionPolicy::reply()`: a locked discussion
// takes no new replies, except from a caller who could unlock it.
func TestLock_ALockedDiscussionRefusesRepliesButNotFromAModerator(t *testing.T) {
	app, handler := newTestForum(t)

	member, memberCookies := memberSession(t, app, "lock_member")
	_, moderatorCookies := moderatorSession(t, app, "lock_mod")
	discussionID, _ := seedDiscussionWithAuthor(t, app, member.Id, "Locked thread", "Hello")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/lock", "", moderatorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// A member can no longer reply ...
	recorder = postAs(t, handler, discussionID, "let me in", memberCookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, "a member must not reply in a locked discussion: %s", recorder.Body.String())

	// ... but the moderator can, which is what makes a lock-with-a-note
	// workflow possible.
	recorder = postAs(t, handler, discussionID, "closing this, see the note", moderatorCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, "a moderator must still be able to reply: %s", recorder.Body.String())
}

// TestLock_AMemberCannotLock pins that locking is a moderator power: it is
// not an author power, and upstream's policy has no ownership rule.
func TestLock_AMemberCannotLock(t *testing.T) {
	app, handler := newTestForum(t)

	member, memberCookies := memberSession(t, app, "lock_author")
	discussionID, _ := seedDiscussionWithAuthor(t, app, member.Id, "My thread", "Hello")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/lock", "", memberCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code, "the author must not lock without the permission")

	// The flag also says so, so the UI does not offer the control.
	require.False(t, discussionAttributes(t, handler, discussionID, memberCookies)["canLock"].(bool))
}

// TestSticky_TheToggleSetsAndClearsTheFlag pins the same round trip for
// pinning.
func TestSticky_TheToggleSetsAndClearsTheFlag(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := moderatorSession(t, app, "mod_sticky")
	discussionID, _ := seedDiscussion(t, app, "Pinnable", "Hello")

	require.False(t, discussionAttributes(t, handler, discussionID, cookies)["isSticky"].(bool))

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/sticky", "", cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.True(t, discussionAttributes(t, handler, discussionID, cookies)["isSticky"].(bool))

	recorder = doWithHeaders(t, handler, http.MethodDelete, "/forum/api/discussions/"+discussionID+"/sticky", "", cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.False(t, discussionAttributes(t, handler, discussionID, cookies)["isSticky"].(bool))
}

// TestSticky_LockingDoesNotUnpin pins the reason the two flags have their
// own columns.
//
// They used to share the `state` select, so pinning a locked discussion
// silently reopened it and locking a pinned one silently unpinned it. Both
// are moderation failures that nothing reports.
func TestSticky_LockingDoesNotUnpin(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := moderatorSession(t, app, "mod_both")
	discussionID, _ := seedDiscussion(t, app, "Both", "Hello")

	for _, path := range []string{"/sticky", "/lock"} {
		recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+path, "", cookies,
			map[string]string{"X-CSRF-Token": "csrf-token"})
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	}

	attributes := discussionAttributes(t, handler, discussionID, cookies)
	require.True(t, attributes["isSticky"].(bool), "locking must not unpin")
	require.True(t, attributes["isLocked"].(bool), "pinning must not unlock")
}

// TestSticky_AMemberCannotSticky pins the permission boundary.
func TestSticky_AMemberCannotSticky(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "sticky_member")
	discussionID, _ := seedDiscussion(t, app, "Not for me", "Hello")

	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions/"+discussionID+"/sticky", "", cookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

// TestSuspend_ASuspendedAccountLosesItsParticipation pins upstream's
// `RevokeAccessFromSuspendedUsers`: the account keeps its identity but is
// treated as a guest, so every permission is revoked at once.
func TestSuspend_ASuspendedAccountLosesItsParticipation(t *testing.T) {
	app, handler := newTestForum(t)

	member, memberCookies := memberSession(t, app, "suspend_target")
	_, moderatorCookies := moderatorSession(t, app, "suspend_mod")
	discussionID, _ := seedDiscussion(t, app, "Open thread", "Hello")

	// Before the suspension the member can reply.
	recorder := postAs(t, handler, discussionID, "before", memberCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	body := `{"data":{"attributes":{"suspendedUntil":"2999-01-01T00:00:00Z","suspendReason":"spam","suspendMessage":"see you later"}}}`
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/users/"+member.Id+"/suspend", body, moderatorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// After it, the reply is refused.
	recorder = postAs(t, handler, discussionID, "after", memberCookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, "a suspended account must not reply: %s", recorder.Body.String())
}

// TestSuspend_TheAccountSeesItsOwnSuspension pins the visibility rules:
// the date and the message are the suspended account's business, the
// reason is the moderator's note.
func TestSuspend_TheAccountSeesItsOwnSuspension(t *testing.T) {
	app, handler := newTestForum(t)

	member, memberCookies := memberSession(t, app, "suspend_self")
	_, moderatorCookies := moderatorSession(t, app, "suspend_mod2")

	body := `{"data":{"attributes":{"suspendedUntil":"2999-01-01T00:00:00Z","suspendReason":"spam","suspendMessage":"see you later"}}}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/users/"+member.Id+"/suspend", body, moderatorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	own := userAttributes(t, handler, member.Id, memberCookies)
	require.Equal(t, "see you later", own["suspendMessage"], "the account must see the message")
	require.Contains(t, own["suspendedUntil"], "2999", "the account must see the date")
	require.Nil(t, own["suspendReason"], "the reason is a moderator's note")

	mod := userAttributes(t, handler, member.Id, moderatorCookies)
	require.Equal(t, "spam", mod["suspendReason"], "a moderator must see the reason")

	// A third party sees neither.
	_, observerCookies := memberSession(t, app, "suspend_observer")
	observed := userAttributes(t, handler, member.Id, observerCookies)
	require.Nil(t, observed["suspendedUntil"])
	require.Nil(t, observed["suspendMessage"])
	require.Nil(t, observed["suspendReason"])
}

// TestSuspend_AnAdministratorIsNotSuspendable pins upstream's rule, which
// refuses the suspension however the permission is granted.
func TestSuspend_AnAdministratorIsNotSuspendable(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testUser(t, app, "suspend_admin_target")
	require.NoError(t, testRepo(t, app).AddUserToGroup(admin.Id, domain.GroupAdminID))

	_, moderatorCookies := moderatorSession(t, app, "suspend_mod3")

	body := `{"data":{"attributes":{"suspendedUntil":"2999-01-01T00:00:00Z"}}}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/users/"+admin.Id+"/suspend", body, moderatorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

// TestSuspend_TheSuspensionCanBeLifted pins that unsuspending clears the
// note as well as the date: a stale note would be shown to the account.
func TestSuspend_TheSuspensionCanBeLifted(t *testing.T) {
	app, handler := newTestForum(t)

	member, memberCookies := memberSession(t, app, "suspend_lift")
	_, moderatorCookies := moderatorSession(t, app, "suspend_mod4")
	discussionID, _ := seedDiscussion(t, app, "Open again", "Hello")

	body := `{"data":{"attributes":{"suspendedUntil":"2999-01-01T00:00:00Z","suspendReason":"spam"}}}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/users/"+member.Id+"/suspend", body, moderatorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = doWithHeaders(t, handler, http.MethodDelete, "/forum/api/users/"+member.Id+"/suspend", "", moderatorCookies,
		map[string]string{"X-CSRF-Token": "csrf-token"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	own := userAttributes(t, handler, member.Id, memberCookies)
	require.Nil(t, own["suspendedUntil"], "the date must be cleared")
	require.Nil(t, own["suspendReason"], "the note must be cleared")

	recorder = postAs(t, handler, discussionID, "back again", memberCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, "a reinstated account may reply again: %s", recorder.Body.String())
}

// TestModerationPlugins_ThePermissionKeysTranslate pins that the admin
// application's upstream key names reach the forum's own keys, which is
// what makes the permission grid grant the action the plugin checks.
func TestModerationPlugins_ThePermissionKeysTranslate(t *testing.T) {
	cases := map[string]string{
		"discussion.lock":   domain.PermissionLockDiscussion,
		"discussion.sticky": domain.PermissionStickyDiscussion,
		"user.suspend":      domain.PermissionSuspendUser,
	}

	for upstream, forum := range cases {
		require.Equal(t, forum, domain.ForumPermissionKey(upstream), "the admin's %q must reach %q", upstream, forum)
	}
}
