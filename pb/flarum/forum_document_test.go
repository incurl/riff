package flarum

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The `can*` attributes of the forum document are the CALLER's
// permissions.
//
// Upstream computes them from the actor
// (framework/core/src/Api/Resource/ForumResource.php:132):
//
//	Schema\Boolean::make('canStartDiscussion')
//	    ->get(fn ($model, Context $context) => $context->getActor()->can('startDiscussion')),
//
// and the frontend's own affordances are built on them: the "New
// Discussion" button is disabled when `canStartDiscussion` is false
// (framework/core/js/src/forum/components/IndexSidebar.tsx:30,44).
//
// The boot payload's copy of the document used to hardcode the flags to
// false, so the button was disabled for EVERY signed-in account — even
// though the API's `GET /api/` computed the same flags correctly from
// the permissions. The SPA reads the forum document out of the BOOT
// PAYLOAD, not out of `GET /api/`, so the API's correct answer never
// reached the button.

// TestForum_TheMemberMayStartADiscussion pins the reported symptom: the
// "New Discussion" button is enabled for a signed-in member.
//
// The member group holds `startDiscussion` (the seed writes it, see
// `pb/flarum/migrations/seed.go`), so the flag has to be true.
func TestForum_TheMemberMayStartADiscussion(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	shell := do(t, handler, http.MethodGet, "/forum/", "", sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	attributes := forumAttributesOf(t, shell.Body.String())
	require.Equal(t, true, attributes["canStartDiscussion"],
		"a member may start a discussion, so the button must not be disabled")
	require.Equal(t, true, attributes["canViewForum"],
		"a member may view the forum")
	require.Equal(t, true, attributes["canSearchUsers"],
		"a member may search for accounts")
}

// TestForum_ASuperuserWithoutAForumProfileCannotAuthor pins the write that used
// to answer 500.
//
// The author columns (`discussions.user`, `posts.user`, …) are RELATIONS to the
// shared `users` collection, and a PocketBase superuser is an administrator of
// the forum without being a record in it. Stamping one as an author failed the
// relation validation inside the save, and `createDiscussionHandler` turned that
// into "500 The request could not be completed." with nothing logged — while the
// forum document said `canStartDiscussion: true` and the composer opened, so the
// operator's first post was a blank error page.
//
// The refusal is now a 403 with a message the reader can act on, and the flags
// the UI draws its controls from say false, so the composer is not offered for a
// write that can only be refused.
func TestForum_ASuperuserWithoutAForumProfileCannotAuthor(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	// The forum document the SPA boots with.
	shell := do(t, handler, http.MethodGet, "/forum/", "", cookies)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	attributes := forumAttributesOf(t, shell.Body.String())
	require.Equal(t, false, attributes["canStartDiscussion"],
		"a superuser with no `users` row cannot author, so the composer must not be offered")
	require.Equal(t, true, attributes["canViewForum"], "it is still an administrator")

	// And the write itself, rather than a 500.
	body := `{"data":{"type":"discussions","attributes":{"title":"Operator post","content":"Hello"}}}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, cookies, nil)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "no forum profile",
		"the refusal has to say what to do, not that the request could not be completed")
}

// A guest holds only `viewForum`, so the flag has to be false — and the
// frontend's own rule (`canStartDiscussion || !app.session.user`) means
// an anonymous visitor still gets a clickable button, which opens the
// login modal.
func TestForum_TheGuestMayNotStartADiscussion(t *testing.T) {
	_, handler := newTestForum(t)

	shell := do(t, handler, http.MethodGet, "/forum/", "", nil)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	attributes := forumAttributesOf(t, shell.Body.String())
	require.Equal(t, false, attributes["canStartDiscussion"],
		"a guest may not start a discussion")
	require.Equal(t, true, attributes["canViewForum"],
		"a guest may view the forum")
}

// TestForum_TheBootPayloadAndTheAPIAgree pins that the two answers are
// the SAME document.
//
// The drift between them was the bug: the API computed the flags and
// the boot payload hardcoded them, so the two disagreed and only the
// boot payload's answer reached the button.
func TestForum_TheBootPayloadAndTheAPIAgree(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "writer")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	cookies := sessionCookies(token, "csrf-token")

	shell := do(t, handler, http.MethodGet, "/forum/", "", cookies)
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())
	fromBoot := forumAttributesOf(t, shell.Body.String())

	// The forum document is served at `GET /forum/api` — NO trailing
	// slash, because a trailing slash is an anonymous ".../" wildcard in
	// Go's http.ServeMux.
	api := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, api.Code, api.Body.String())
	fromAPI := forumAttributesOf(t, api.Body.String())

	for _, flag := range []string{"canViewForum", "canStartDiscussion", "canSearchUsers"} {
		require.Equal(t, fromAPI[flag], fromBoot[flag],
			"the boot payload and the API must agree on %s", flag)
	}
}

// TestForum_TheAdministratorMayStartADiscussion pins that the flag follows the
// permission for an administrator too.
//
// An administrator holds every permission outright (upstream's
// `Group::hasPermission()` returns true for the administrator group), so the flag
// has to be true — for an administrator who is a MEMBER of the administrator
// group, which means a `users` row.
//
// A PocketBase SUPERUSER is the one administrator the flag is false for, and not
// because of a permission: the author columns are relations to `users`, so a
// superuser that is not a row there cannot be stamped as an author at all (see
// `TestForum_ASuperuserWithoutAForumProfileCannotAuthor`). Offering the composer
// would offer a write that can only be refused.
func TestForum_TheAdministratorMayStartADiscussion(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testUser(t, app, "admin_group_member")
	require.NoError(t, testRepo(t, app).AddUserToGroup(admin.Id, domain.GroupAdminID))

	token, err := admin.NewAuthToken()
	require.NoError(t, err)

	shell := do(t, handler, http.MethodGet, "/forum/", "", sessionCookies(token, "csrf-token"))
	require.Equal(t, http.StatusOK, shell.Code, shell.Body.String())

	attributes := forumAttributesOf(t, shell.Body.String())
	require.Equal(t, true, attributes["canStartDiscussion"],
		"an administrator may start a discussion")
}

// forumAttributesOf reads the forum document's attributes out of a
// response body.
//
// The boot payload carries the document as a RESOURCE (the SPA reads it
// with `store.getById('forums', '1')`), and the API's `GET /api/`
// carries it as a JSON:API document. Both shapes are read here.
func forumAttributesOf(t *testing.T, body string) map[string]any {
	t.Helper()

	// The boot payload: a JSON script tag with the resources.
	const open = `<script id="flarum-json-payload" type="application/json">`
	if start := strings.Index(body, open); start >= 0 {
		rest := body[start+len(open):]
		end := strings.Index(rest, "</script>")
		require.GreaterOrEqual(t, end, 0, "the boot payload should be closed")

		var payload struct {
			Resources []map[string]any `json:"resources"`
		}
		require.NoError(t, json.Unmarshal([]byte(rest[:end]), &payload))

		for _, resource := range payload.Resources {
			if resource["type"] != "forums" {
				continue
			}
			attributes, ok := resource["attributes"].(map[string]any)
			require.True(t, ok, "the forum resource should carry attributes")
			return attributes
		}
		t.Fatal("the boot payload should carry the forum document")
	}

	// The API: a JSON:API document.
	var document struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))
	require.NotEmpty(t, document.Data.Attributes, "the API document should carry attributes")
	return document.Data.Attributes
}
