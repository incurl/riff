package flarum

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// The admin surface: the routes the admin application calls to change
// the forum's configuration.
//
// The wire format is upstream's, and it is NOT the JSON:API resource
// format the rest of the forum's API speaks. Upstream's admin API is a
// set of plain command endpoints: they take a bare JSON object and
// answer with an empty 204.
//
// The bodies in these tests are COPIED from the vendored frontend's own
// calls, because that is what the server has to satisfy:
//
//	framework/core/js/src/admin/utils/saveSettings.js
//	    POST {apiUrl}/settings   body: {key: value, …}
//	framework/core/js/src/admin/components/ResetExtensionSettingsModal.tsx
//	    DELETE {apiUrl}/settings body: {keys: [...], extensionId: ''}
//	framework/core/js/src/admin/components/PermissionDropdown.tsx
//	    POST {apiUrl}/permission body: {permission, groupIds}
//	framework/core/js/src/admin/components/GroupBar.tsx
//	    POST {apiUrl}/groups/order body: {order: [...]}

// TestForum_TheAdminSavesSettings pins the settings save.
//
// The body is the settings map ITSELF, not a JSON:API resource
// document: upstream's `SetSettingsController` reads the parsed body as
// the settings, and the vendored `saveSettings.js` posts exactly that.
// A handler that demanded a JSON:API envelope would reject every save,
// and the admin's "Save Changes" button would report a validation error
// while changing nothing.
//
// The answer is an empty 204, which is upstream's too.
func TestForum_TheAdminSavesSettings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"forum_title":"Riff","theme_primary_color":"#123456"}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	require.Empty(t, recorder.Body.String(), "the answer should be empty")

	// The settings were stored.
	repo := testRepo(t, app)

	require.Equal(t, "Riff", settingText(t, repo, "forum_title"))
	require.Equal(t, "#123456", settingText(t, repo, "theme_primary_color"))
}

// settingText reads a stored setting as the text the policies compare
// against.
//
// The value column is a PocketBase JSONField, so it comes back as a
// `types.JSONRaw` — the JSON document the client sent. Upstream's
// `settings` table is a TEXT column, so everything round-trips as text
// there; here a client that posts a bare number stores a JSON number,
// and this helper decodes it the same way the forum's policies do
// (`pb/flarum/api/api.go::settingString`: a JSON string first, then a
// `json.Number`).
func settingText(t *testing.T, repo *repository.Repository, key string) string {
	t.Helper()

	value, err := repo.SettingValue(key, nil)
	require.NoError(t, err)

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	var text string
	if err := json.Unmarshal(encoded, &text); err == nil {
		return text
	}

	var number json.Number
	require.NoError(t, json.Unmarshal(encoded, &number),
		"the setting %q should decode to a string or a number: %s", key, encoded)
	return number.String()
}

// TestForum_TheAdminResetsSettings pins the settings reset.
//
// The body names the keys to reset, and resetting means DELETING the
// row, so the reader falls back to upstream's installed default. That
// is upstream's `DeleteSettingsController`, which calls
// `SettingsRepository::delete()`.
//
// The frontend sends this as a POST with an `X-HTTP-Method-Override`
// header, because it rewrites every method other than GET and POST.
func TestForum_TheAdminResetsSettings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"forum_title":"Riff"}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// Reset it, with the method override the frontend sends.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/settings",
		`{"keys":["forum_title"],"extensionId":""}`,
		sessionCookies(admin[0].Value, admin[1].Value),
		map[string]string{"X-HTTP-Method-Override": "DELETE"})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// The setting is gone, so the reader falls back.
	repo := testRepo(t, app)

	value, err := repo.SettingValue("forum_title", "the default")
	require.NoError(t, err)
	require.Equal(t, "the default", value)
}

// TestForum_AMemberMayNotSaveSettings pins that the settings are an
// administrator's business.
//
// Upstream's controller calls `assertAdmin()` before it reads the
// body, so an ordinary member is refused.
func TestForum_AMemberMayNotSaveSettings(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "settings_writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"forum_title":"Hijacked"}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestForum_TheAdminSavesAPermission pins the permission grid's save.
//
// The body names the permission and the groups that hold it, and the
// rows for that permission are REPLACED — upstream's
// `SetPermissionController` deletes every row for the permission and
// inserts one row per group, so the request names the complete set.
//
// The permission key is upstream's name (`discussion.reply`), and the
// row that lands carries the forum's own name (`reply`), because that
// is the key the forum's handlers ask the permission service for. The
// translation is the point of the test: without it the grid writes a
// key nothing reads, and granting a permission changes nothing.
func TestForum_TheAdminSavesAPermission(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/permission",
		`{"permission":"discussion.reply","groupIds":["`+domain.GroupGuestID+`"]}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	repo := testRepo(t, app)

	// The row carries the FORUM's key, not upstream's.
	rows, err := repo.PermissionRowsFor([]string{domain.GroupGuestID}, domain.PermissionReply)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the guest group should now hold the reply permission")
	require.True(t, rows[0].Allowed)

	// And the upstream key holds nothing, which is what would have
	// happened without the translation.
	rows, err = repo.PermissionRowsFor([]string{domain.GroupGuestID}, "discussion.reply")
	require.NoError(t, err)
	require.Empty(t, rows, "the upstream key must not be stored as-is")
}

// TestForum_SavingAPermissionReplacesTheGroups pins the replacement
// semantics.
//
// The request names the COMPLETE set of groups, so a group that is no
// longer named loses the permission. This is upstream's model, and it
// is what makes the grid's checkboxes meaningful: the frontend toggles
// one group and POSTs the whole set.
func TestForum_SavingAPermissionReplacesTheGroups(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/permission",
		`{"permission":"discussion.reply","groupIds":["`+domain.GroupGuestID+`","`+domain.GroupMemberID+`"]}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	// Now name only the member group: the guest group loses it.
	recorder = do(t, handler, http.MethodPost, "/forum/api/permission",
		`{"permission":"discussion.reply","groupIds":["`+domain.GroupMemberID+`"]}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	repo := testRepo(t, app)

	rows, err := repo.PermissionRowsFor([]string{domain.GroupGuestID}, domain.PermissionReply)
	require.NoError(t, err)
	require.Empty(t, rows, "the guest group should have lost the reply permission")

	rows, err = repo.PermissionRowsFor([]string{domain.GroupMemberID}, domain.PermissionReply)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the member group should still hold it")
}

// TestForum_AMemberMayNotSaveAPermission pins that a permission is an
// administrator's business.
func TestForum_AMemberMayNotSaveAPermission(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "permission_writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := do(t, handler, http.MethodPost, "/forum/api/permission",
		`{"permission":"discussion.reply","groupIds":["`+domain.GroupGuestID+`"]}`,
		sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestForum_SavingAPermissionRejectsAnUnknownGroup pins the referential
// integrity of the rows.
//
// A permission row is a RELATION to a group, so a row naming a group
// that does not exist is a dangling pointer that no reader can resolve.
// The request is refused rather than written.
func TestForum_SavingAPermissionRejectsAnUnknownGroup(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/permission",
		`{"permission":"discussion.reply","groupIds":["nosuchgroup"]}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
}

// TestForum_TheAdminReordersTheGroups pins the group reorder.
//
// The body names the groups in their new order, and a group's position
// is its index in that list. The groups themselves must SURVIVE the
// reorder — the position is a column on the group, so clearing it must
// not remove the row.
func TestForum_TheAdminReordersTheGroups(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	repo := testRepo(t, app)

	before, err := repo.ListGroups()
	require.NoError(t, err)
	require.NotEmpty(t, before)

	// The moderator group goes first.
	recorder := do(t, handler, http.MethodPost, "/forum/api/groups/order",
		`{"order":["`+domain.GroupModeratorID+`","`+domain.GroupGuestID+`"]}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	after, err := repo.ListGroups()
	require.NoError(t, err)
	require.Len(t, after, len(before), "the reorder must not remove a group")

	positions := map[string]int{}
	for _, group := range after {
		positions[group.ID] = group.Position
	}

	require.Equal(t, 0, positions[domain.GroupModeratorID])
	require.Equal(t, 1, positions[domain.GroupGuestID])
}

// TestForum_TheAdminPayloadCarriesThePermissions pins the boot
// payload's permissions map.
//
// Upstream's admin payload carries `Permission::map()`
// (`$document->payload['permissions'] = Permission::map();`), and the
// admin application reads it into `app.data.permissions`.
//
// The map is NOT optional: `PermissionDropdown.tsx` reads it for the
// permission being edited and for every permission that one requires,
// and uses the answer to decide which groups a permission may be
// granted to. An empty map makes the whole grid inert.
//
// The keys are upstream's names, because the admin application
// registers upstream's names and looks them up verbatim.
func TestForum_TheAdminPayloadCarriesThePermissions(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)

	require.NotEmpty(t, payload.Permissions, "the permissions map must not be empty")

	// The seeded reply permission is held by the member group, and the
	// admin application knows it by upstream's name.
	require.Contains(t, payload.Permissions["discussion.reply"], domain.GroupMemberID)
}

// TestForum_ThePermissionMapIsAnArray pins the shape the admin
// application expects.
//
// `app.data.permissions[permission]` is a LIST of group ids, and the
// dropdown does `groupIds.indexOf(...)` and `groupIds.filter(...)` on
// it. A single group id where a list is expected would break every one
// of those calls.
func TestForum_ThePermissionMapIsAnArray(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)

	require.NotEmpty(t, payload.Permissions)
	for permission, groups := range payload.Permissions {
		require.NotNil(t, groups, "the groups for %q should be a list", permission)
	}
}

// TestForum_TheSettingsMapIsNotAJSONAPIResource pins the shape of the
// settings body.
//
// A JSON:API document — `{"data": {"attributes": {"settings": {...}}}}`
// — is NOT what the admin application sends, and accepting only that
// shape is what made every settings save fail. The body is the flat
// map, so a flat body must be accepted.
func TestForum_TheSettingsMapIsNotAJSONAPIResource(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"forum_description":"A forum about samplers"}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	repo := testRepo(t, app)

	require.Equal(t, "A forum about samplers", settingText(t, repo, "forum_description"))
}

// TestForum_ThePermissionKeysAreTranslated pins the two dialects.
//
// The admin application speaks upstream's names and the forum stores
// its own, and the translation has to work in BOTH directions: a key
// that arrives from the admin page is stored under the forum's name,
// and a key that goes out to the admin page carries upstream's name.
//
// A key the forum does not know is passed through unchanged, so an
// extension's own permission round-trips.
func TestForum_ThePermissionKeysAreTranslated(t *testing.T) {
	// The keys the admin application registers.
	for upstream, forum := range map[string]string{
		"viewForum":              domain.PermissionViewForum,
		"startDiscussion":        domain.PermissionStartDiscussion,
		"searchUsers":            domain.PermissionSearchUsers,
		"discussion.reply":       domain.PermissionReply,
		"discussion.rename":      domain.PermissionEditDiscussion,
		"discussion.hide":        domain.PermissionHideDiscussion,
		"discussion.delete":      domain.PermissionDeleteDiscussion,
		"discussion.editPosts":   domain.PermissionEditPost,
		"discussion.hidePosts":   domain.PermissionHidePost,
		"discussion.deletePosts": domain.PermissionDeletePost,
	} {
		require.Equal(t, forum, domain.ForumPermissionKey(upstream), "the upstream key %q", upstream)
		require.Equal(t, upstream, domain.UpstreamPermissionKey(forum), "the forum key %q", forum)
	}

	// An unknown key round-trips.
	require.Equal(t, "acme.custom", domain.ForumPermissionKey("acme.custom"))
	require.Equal(t, "acme.custom", domain.UpstreamPermissionKey("acme.custom"))
}

// TestForum_ThePermissionMapHoldsOnlyGlobalGrants pins what the admin
// payload's map holds.
//
// Upstream's `group_permission` table holds GRANTS only: a row IS a
// grant, and the absence of a row is the denial. The port's
// `forum_permissions.allowed` column is a PocketBase `BoolField` with
// `Required: true`, which PocketBase reads as "must always be true", so
// a denial row cannot be stored either.
//
// A SCOPED row belongs to a subject (a restricted tag), not to the
// admin's global grid, so it is not in the map.
func TestForum_ThePermissionMapHoldsOnlyGlobalGrants(t *testing.T) {
	app, _ := newTestForum(t)

	repo := testRepo(t, app)

	// A scoped grant: it belongs to a tag, not to the grid.
	//
	// The permission is one that is NOT seeded globally, so the only
	// row for it is the scoped one. A seeded permission (the tag
	// administration powers, for instance) would be in the map anyway,
	// and the check would prove nothing.
	require.NoError(t, repo.SetPermission(domain.GroupGuestID, domain.PermissionCreateGroup, "some-tag", true))

	permissions, err := repo.PermissionMap()
	require.NoError(t, err)

	require.NotContains(t, permissions, domain.PermissionCreateGroup, "a scoped row is not a global grant")

	// The seeded global grants ARE in the map.
	require.Contains(t, permissions, domain.PermissionViewForum)
}

// TestForum_TheAdminPayloadCarriesTheSettings pins that the admin
// application can read the settings it is going to change.
//
// The settings reach the admin application through the boot payload
// (upstream's `AdminPayload` carries `$this->settings->all()`), and
// the forum's own settings win over the defaults.
func TestForum_TheAdminPayloadCarriesTheSettings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	setForumSetting(t, app, "forum_title", "The Renamed Forum")

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)

	require.Equal(t, "The Renamed Forum", payload.Settings["forum_title"])
}

// TestForum_TheGroupIDsMatchTheFrontend pins that the vendored
// frontend's group-id constants match the backend's.
//
// The frontend compares group ids against `Group.ADMINISTRATOR_ID`,
// `Group.GUEST_ID` and `Group.MEMBER_ID` in a dozen places, and those
// constants were upstream's integers ('1', '2', '3') while the forum's
// ids are strings. Every comparison therefore failed: the admin UI
// offered the guest and member groups for assignment (the server
// rejects them), the "is this account an administrator" check never
// matched, and the permission dropdown's "everyone" and "members"
// badges never lit up.
//
// The check reads the CONSTANTS OUT OF THE FRONTEND SOURCE, because the
// frontend is vendored TypeScript and the constants are the contract
// between the two sides.
func TestForum_TheGroupIDsMatchTheFrontend(t *testing.T) {
	source := readVendoredFrontendFile(t, "pb/flarum/frontend/packages/core/src/common/models/Group.ts")

	for constant, id := range map[string]string{
		"ADMINISTRATOR_ID": domain.GroupAdminID,
		"GUEST_ID":         domain.GroupGuestID,
		"MEMBER_ID":        domain.GroupMemberID,
	} {
		require.Contains(t, source, constant+" = '"+id+"'",
			"the frontend's %s must be the forum's %q", constant, id)
	}
}

// TestForum_TheAdminSurfaceIsNotReachableByAMember pins that every
// command endpoint on the admin surface is closed to ordinary members.
//
// The routes are the ones the admin application calls, and each of them
// changes the forum's configuration.
func TestForum_TheAdminSurfaceIsNotReachableByAMember(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "surface_member")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	cookies := sessionCookies(token, "csrf-token")

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/forum/api/settings", `{"forum_title":"Hijacked"}`},
		{http.MethodPost, "/forum/api/permission", `{"permission":"viewForum","groupIds":[]}`},
		{http.MethodPost, "/forum/api/groups/order", `{"order":[]}`},
	} {
		recorder := do(t, handler, tc.method, tc.path, tc.body, cookies)
		require.Equal(t, http.StatusForbidden, recorder.Code,
			"%s should be closed to a member: %s", tc.path, recorder.Body.String())
	}
}

// TestForum_TheSettingsSaveKeepsTheNumericForm pins that a numeric
// setting keeps its exact text.
//
// The upstream settings are compared against "10" and "-1" AS TEXT
// (the `allow_renaming` setting is "10" minutes, and "-1" means
// "always"). A float64 round-trip turns a 10 into a 10.0, and the
// comparison against "10" then fails — so the author of a discussion
// could never rename it.
func TestForum_TheSettingsSaveKeepsTheNumericForm(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodPost, "/forum/api/settings",
		`{"allow_renaming":10,"allow_hide_own_posts":-1}`,
		sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	repo := testRepo(t, app)

	// The stored value is JSON, and it holds the number 10 — not 10.0.
	require.Equal(t, "10", settingText(t, repo, "allow_renaming"), "the setting must keep its text form")
	require.Equal(t, "-1", settingText(t, repo, "allow_hide_own_posts"), "the setting must keep its text form")
}
