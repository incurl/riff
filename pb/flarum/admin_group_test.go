package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The administrator flag is derived from GROUP MEMBERSHIP.
//
// Upstream's `User::isAdmin()` is exactly this:
//
//	framework/core/src/User/User.php:527
//	    public function isAdmin(): bool
//	    {
//	        return $this->groups->contains(Group::ADMINISTRATOR_ID);
//	    }
//
// and upstream's admin routes carry the `RequireAdministrateAbility`
// middleware, which is `assertAdmin()` — the same flag. The forum's
// administrator group (`forumgroupadmin`) is a real, assignable row, so
// an operator promotes somebody into it through the admin's own user
// editor.
//
// Deriving the flag from the PocketBase collection alone therefore made
// every promoted account a non-administrator: the admin panel answered
// 403 and every admin command answered "You are not allowed to do that".

// TestForum_TheAdminGroupGetsTheAdminShell pins the reported symptom: an
// account in the administrator group gets the admin application.
//
// The 403 came from the frontend's admin gate
// (`pb/flarum/frontend/routes.go::spaHandler`), which asks the module
// whether the caller administers the forum.
func TestForum_TheAdminGroupGetsTheAdminShell(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testUser(t, app, "group_admin")
	require.NoError(t, testRepo(t, app).AddUserToGroup(admin.Id, domain.GroupAdminID))

	token, err := admin.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusOK, recorder.Code,
		"a member of the administrator group must get the admin shell: %s", recorder.Body.String())
}

// TestForum_TheAdminGroupRunsTheAdminCommands pins that the administrator
// group is not merely allowed past the gate: the admin COMMANDS accept it
// too.
//
// The gate and the commands read the flag through different paths — the
// frontend's gate asks the module, the commands ask the API server — so
// a fix that only touched one of them would leave the other refusing.
func TestForum_TheAdminGroupRunsTheAdminCommands(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testUser(t, app, "group_admin")
	require.NoError(t, testRepo(t, app).AddUserToGroup(admin.Id, domain.GroupAdminID))

	token, err := admin.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"forum_title":"Riff"}`,
		sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusNoContent, recorder.Code,
		"a member of the administrator group must be allowed to save the settings: %s", recorder.Body.String())
}

// TestForum_TheAdminGroupIsAnAdminEverywhere pins the flag on the user
// resource.
//
// The frontend reads `user.isAdmin()` and uses it for its own admin
// checks (`app.session.user?.isAdmin()`), so the attribute has to agree
// with the server. When the flag came from the collection alone, the
// resource said "no" for an administrator and the frontend hid the
// admin affordances from a caller the server would have allowed.
func TestForum_TheAdminGroupIsAnAdminEverywhere(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testUser(t, app, "group_admin")
	require.NoError(t, testRepo(t, app).AddUserToGroup(admin.Id, domain.GroupAdminID))

	token, err := admin.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/api/users/"+admin.Id, "", sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var document struct {
		Data struct {
			Attributes struct {
				IsAdmin bool `json:"isAdmin"`
			} `json:"attributes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	require.True(t, document.Data.Attributes.IsAdmin,
		"the user resource must report the administrator group membership as isAdmin")
}

// TestForum_AMemberIsStillNotAnAdmin pins that the fix did not widen the
// flag to every signed-in account.
func TestForum_AMemberIsStillNotAnAdmin(t *testing.T) {
	app, handler := newTestForum(t)

	member := testUser(t, app, "ordinary")
	token, err := member.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusForbidden, recorder.Code,
		"an ordinary member must not get the admin shell")
}

// TestForum_AModeratorIsStillNotAnAdmin pins that the moderator group
// does not confer administrator-ness.
//
// The moderator group holds the MODERATION powers; the administrator
// group is a different group, and upstream's `isAdmin()` asks about the
// administrator group alone.
func TestForum_AModeratorIsStillNotAnAdmin(t *testing.T) {
	app, handler := newTestForum(t)

	moderator := testUser(t, app, "moderator")
	require.NoError(t, testRepo(t, app).AddUserToGroup(moderator.Id, domain.GroupModeratorID))

	token, err := moderator.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusForbidden, recorder.Code,
		"a moderator must not get the admin shell")
}

// TestForum_ASuperuserIsStillAnAdmin pins that the PocketBase superuser
// route still works.
//
// The forum's superusers are the instance's operators, and upstream's
// bootstrap administrator is the same idea (upstream's install writes a
// `group_user` row for the administrator group, so its first
// administrator is an administrator by group membership). The flag is the
// union of the two sources, so neither source is dropped.
func TestForum_ASuperuserIsStillAnAdmin(t *testing.T) {
	app, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", testSuperuser(t, app))

	require.Equal(t, http.StatusOK, recorder.Code,
		"a superuser must still get the admin shell: %s", recorder.Body.String())
}

// TestForum_AnAdminMayNotDemoteThemselves pins upstream's
// SelfDemotionGuard.
//
// Upstream's `User\SelfDemotionGuard` refuses an administrator who
// removes their OWN administrator group:
//
//	framework/core/src/User/SelfDemotionGuard.php:37
//	    if ($event->actor->id !== $event->user->id) {
//	        return;
//	    }
//
// The reason is the same one the guard is named for: the administrator
// group is the only route to the admin panel, so an administrator who
// drops it locks themselves out — and if they were the last one, they
// lock EVERYONE out.
//
// Upstream allows demoting ANOTHER administrator, because the actor is
// still an administrator afterwards. So does this.
func TestForum_AnAdminMayNotDemoteThemselves(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testUser(t, app, "group_admin")
	repo := testRepo(t, app)
	require.NoError(t, repo.AddUserToGroup(admin.Id, domain.GroupAdminID))

	token, err := admin.NewAuthToken()
	require.NoError(t, err)

	// The administrator asks to drop their own administrator group.
	recorder := do(t, handler, http.MethodPatch, "/forum/api/users/"+admin.Id,
		`{"data":{"type":"users","id":"`+admin.Id+`","relationships":{"groups":{"data":[]}}}}`,
		sessionCookies(token, "csrf-token"))

	require.Equal(t, http.StatusForbidden, recorder.Code,
		"an administrator must not remove their own administrator group: %s", recorder.Body.String())

	// The membership survived.
	groups, err := repo.UserGroupIDs(admin.Id)
	require.NoError(t, err)
	require.Contains(t, groups, domain.GroupAdminID)
}

// TestForum_AnAdminMayDemoteAnotherAdmin pins the other half of
// upstream's guard: demoting SOMEBODY ELSE is allowed, because the actor
// is still an administrator afterwards.
//
// This is the case a naive "the last administrator must be protected"
// rule would wrongly refuse, and it is the case upstream explicitly
// permits.
func TestForum_AnAdminMayDemoteAnotherAdmin(t *testing.T) {
	app, handler := newTestForum(t)

	repo := testRepo(t, app)

	actor := testUser(t, app, "group_admin")
	require.NoError(t, repo.AddUserToGroup(actor.Id, domain.GroupAdminID))

	other := testUser(t, app, "other_admin")
	require.NoError(t, repo.AddUserToGroup(other.Id, domain.GroupAdminID))

	token, err := actor.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPatch, "/forum/api/users/"+other.Id,
		`{"data":{"type":"users","id":"`+other.Id+`","relationships":{"groups":{"data":[]}}}}`,
		sessionCookies(token, "csrf-token"))

	// The handler answers with the updated user document.
	require.Equal(t, http.StatusOK, recorder.Code,
		"an administrator may demote another administrator: %s", recorder.Body.String())

	groups, err := repo.UserGroupIDs(other.Id)
	require.NoError(t, err)
	require.NotContains(t, groups, domain.GroupAdminID)
}
