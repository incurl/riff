package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
	"github.com/incurl/riff/pb/flarum/session"
)

// TestForum_AnotherMemberCannotEditAPost pins the AUTHOR rule.
//
// The permission rows are GROUP-scoped: `editPost` is seeded for the
// member group, which every signed-in caller belongs to. A permission
// check alone therefore answers "yes" for ANY member and ANY post, so
// the check has to name the author as well — otherwise every member
// could rewrite every post in the forum.
//
// This is the same shape of bug as the one the tags plugin had: a
// handler that asked the permission service a question the permission
// service cannot answer.
func TestForum_AnotherMemberCannotEditAPost(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "post_author")
	intruder := testUser(t, app, "post_intruder")

	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	// The author writes a post.
	recorder := do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","attributes":{"content":"mine","discussion":"nope"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	_ = recorder

	// A post needs a discussion, so make one through the API.
	recorder = do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Ownership","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","attributes":{"content":"mine","discussion":"`+discussionID+`"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	postID := resourceIDOf(t, recorder)

	// The intruder may not edit it.
	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","attributes":{"content":"tampered"}}}`,
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The author may, while the `allow_post_editing` setting permits
	// it. Upstream's `PostPolicy::edit()` gates the author's edit on
	// that setting, and its default is "reply" — while the post is the
	// last one, so the author may fix a typo until somebody answers.
	setForumSetting(t, app, "allow_post_editing", "-1")

	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","attributes":{"content":"edited by the author"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// TestForum_AnotherMemberCannotDeleteAPost pins the author rule on the
// delete path, for the same reason.
func TestForum_AnotherMemberCannotDeleteAPost(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "delete_author")
	intruder := testUser(t, app, "delete_intruder")

	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"To delete","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","attributes":{"content":"mine","discussion":"`+discussionID+`"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	postID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodDelete, "/forum/api/posts/"+postID, "",
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The author may delete their own post.
	recorder = do(t, handler, http.MethodDelete, "/forum/api/posts/"+postID, "",
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}

// TestForum_AnotherMemberCannotRenameADiscussion pins the author rule
// on the discussion rename path.
func TestForum_AnotherMemberCannotRenameADiscussion(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "rename_author")
	intruder := testUser(t, app, "rename_intruder")

	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Original","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","attributes":{"title":"Renamed by an intruder"}}}`,
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The author may, while the `allow_renaming` setting permits it.
	// Upstream's `DiscussionPolicy::rename()` gates the author's rename
	// on that setting.
	setForumSetting(t, app, "allow_renaming", "-1")

	recorder = do(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","attributes":{"title":"Renamed by the author"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// TestForum_TheAuthorMayNotRenameAfterTheWindow pins upstream's
// time window.
//
// Upstream's `DiscussionPolicy::rename()` lets the author rename their
// own discussion only while the `allow_renaming` setting permits it,
// and the setting has a time-window shape: a number of minutes after
// the discussion was created.
func TestForum_TheAuthorMayNotRenameAfterTheWindow(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "window_author")
	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Original","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	// A window of zero minutes has already elapsed.
	setForumSetting(t, app, "allow_renaming", "0")

	recorder = do(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","attributes":{"title":"Renamed"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// An open window permits it.
	setForumSetting(t, app, "allow_renaming", "-1")

	recorder = do(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","attributes":{"title":"Renamed"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// TestForum_LoginDoesNotLeakAccountExistence pins that a login for an
// unknown identifier and a login with a wrong password are
// indistinguishable.
//
// The RESPONSE was always the same (a 401 with one message); the
// RESPONSE TIME was not. The unknown-identifier path never reached the
// bcrypt comparison, so it answered in about a millisecond while a
// known identifier took about fifty — a user-enumeration oracle.
func TestForum_LoginDoesNotLeakAccountExistence(t *testing.T) {
	app, _ := newTestForum(t)

	_ = testUser(t, app, "known_user")

	resolver := session.NewResolver(app, "/forum")

	// The unknown identifier must cost the same as the known one.
	unknownStart := timeNow()
	_, _, unknownErr := resolver.Login(nil, sessionLoginRequest("no_such_user", "password12345"))
	unknownElapsed := timeSince(unknownStart)

	knownStart := timeNow()
	_, _, knownErr := resolver.Login(nil, sessionLoginRequest("known_user", "wrong-password"))
	knownElapsed := timeSince(knownStart)

	require.Error(t, unknownErr)
	require.Error(t, knownErr)

	// The ratio is what an attacker measures. bcrypt dominates both
	// paths now, so they land within a small factor of each other; the
	// unknown path used to be ~40x faster.
	require.Greater(t, unknownElapsed, knownElapsed/4,
		"an unknown identifier must not answer faster than a wrong password")
}

// resourceIDOf reads the id of the primary resource in a JSON:API
// document.
func resourceIDOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var document struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
	require.NotEmpty(t, document.Data.ID)
	return document.Data.ID
}

// timeNow and timeSince are the clock the timing assertion uses.
func timeNow() time.Time { return time.Now() }

func timeSince(start time.Time) time.Duration { return time.Since(start) }

// sessionLoginRequest builds the login request the resolver takes.
func sessionLoginRequest(identification, password string) session.LoginRequest {
	return session.LoginRequest{Identification: identification, Password: password}
}

// TestForum_AModeratorMayChangeAnotherMembersPost pins upstream's
// moderator model.
//
// Upstream's default group permissions put the moderation powers —
// renaming a discussion, deleting one, editing or hiding any post —
// in the MODERATOR group, and the member group holds only the powers
// every member has. A row for the member group is inherited by every
// signed-in caller, so a moderation row there would grant the power to
// all of them.
//
// This is the other half of the author rule: the author may change
// their own, and a moderator may change anyone's.
func TestForum_AModeratorMayChangeAnotherMembersPost(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "mod_author")
	moderator := testUser(t, app, "mod_moderator")

	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)
	moderatorToken, err := moderator.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Moderated","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","attributes":{"content":"mine","discussion":"`+discussionID+`"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	postID := resourceIDOf(t, recorder)

	// The moderator is in the moderator group.
	repo := repository.New(app, repository.NewRegistry(repository.DefaultPrefix))
	require.NoError(t, repo.AddUserToGroup(moderator.Id, domain.GroupModeratorID))

	// The moderator may edit the author's post.
	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","attributes":{"content":"moderated"}}}`,
		sessionCookies(moderatorToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// And the author's own post is untouched by the member rule: a
	// plain member still may not.
	intruder := testUser(t, app, "mod_intruder")
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","attributes":{"content":"tampered"}}}`,
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestForum_TheMemberGroupHoldsNoModerationPowers pins the seed.
//
// Upstream's default group permissions give the member group only
// `startDiscussion`, `discussion.reply` and `searchUsers`. The
// moderation powers live in the moderator group.
//
// A row for the member group is inherited by EVERY signed-in caller, so
// a moderation row there is the vulnerability this test guards.
func TestForum_TheMemberGroupHoldsNoModerationPowers(t *testing.T) {
	app, _ := newTestForum(t)

	repo := repository.New(app, repository.NewRegistry(repository.DefaultPrefix))

	for _, permission := range []string{
		domain.PermissionEditPost,
		domain.PermissionDeletePost,
		domain.PermissionHidePost,
	} {
		rows, err := repo.PermissionRowsFor([]string{domain.GroupMemberID}, permission)
		require.NoError(t, err)
		require.Empty(t, rows, "the member group must not hold %q", permission)
	}
}

// TestForum_AnAdministratorCanAssignTheModeratorGroup pins that the
// moderator group is reachable.
//
// The moderation powers live in that group, so without a way to assign
// it they would be unreachable: the group would exist and no account
// could ever be in it. Upstream manages the membership through the
// same user resource (`Schema\\Relationship\\ToMany::make('groups')` is
// writable there), and so does the forum.
func TestForum_AnAdministratorCanAssignTheModeratorGroup(t *testing.T) {
	app, handler := newTestForum(t)

	moderator := testUser(t, app, "assigned_moderator")
	moderatorToken, err := moderator.NewAuthToken()
	require.NoError(t, err)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPatch, "/forum/api/users/"+moderator.Id,
		`{"data":{"type":"users","id":"`+moderator.Id+`","relationships":{"groups":{"data":[{"type":"groups","id":"`+domain.GroupModeratorID+`"}]}}}}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The account is now a moderator, so it may edit a post that is
	// not its own.
	author := testUser(t, app, "moderated_author")
	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)

	recorder = do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Moderated","content":"body"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodPost, "/forum/api/posts",
		`{"data":{"type":"posts","attributes":{"content":"mine","discussion":"`+discussionID+`"}}}`,
		sessionCookies(authorToken, "csrf-token"))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	postID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","attributes":{"content":"moderated"}}}`,
		sessionCookies(moderatorToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// TestForum_AMemberMayNotAssignGroups pins that the membership is an
// administrator's business.
//
// It is what makes an account a moderator, so a member may not grant
// it to itself.
func TestForum_AMemberMayNotAssignGroups(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "group_grabber")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPatch, "/forum/api/users/"+user.Id,
		`{"data":{"type":"users","id":"`+user.Id+`","relationships":{"groups":{"data":[{"type":"groups","id":"`+domain.GroupModeratorID+`"}]}}}}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}
