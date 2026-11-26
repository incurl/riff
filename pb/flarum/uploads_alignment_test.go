package flarum

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The Uploads plugin, aligned with upstream `fof/upload` 2.x.
//
// The extension's contract (its `src/Api/Resource/FileResource.php`,
// `src/Api/Handlers/UploadHandler.php`, `src/Access/FilePolicy.php` and
// `extend.php` route table) is what these tests pin. Three things have to
// agree, and none of them is visible when it is wrong: the RESOURCE (every
// attribute), the ROUTES (upstream's `/fof/...` spellings, not only the
// port's own) and the PERMISSIONS (the six `fof-upload.*` keys, with
// ownership layered on top rather than replacing them).

// uploadMany posts several files in ONE multipart request, the way
// upstream's frontend does (a repeated `files` field).
func uploadMany(t *testing.T, handler http.Handler, cookies []*http.Cookie, path string, extra map[string]string, filenames ...string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for _, name := range filenames {
		part, err := writer.CreateFormFile("files", name)
		require.NoError(t, err)
		_, err = part.Write([]byte(strings.Repeat("x", 4)))
		require.NoError(t, err)
	}

	for key, value := range extra {
		require.NoError(t, writer.WriteField(key, value))
	}
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for _, cookie := range cookies {
		request.AddCookie(cookie)
		if cookie.Name == "forum_csrf" {
			request.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// uploadedCollection decodes an upload response's resources.
func uploadedCollection(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "an upload answers a collection: %s", recorder.Body.String())

	out := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		typed, ok := resource.(map[string]any)
		require.True(t, ok)
		out = append(out, typed)
	}
	return out
}

// uploadedCollectionOfList decodes a LISTING response's resources.
//
// It is the same shape an upload answers with, so it reuses the same
// decoder; the name says which endpoint the recorder came from.
func uploadedCollectionOfList(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	return uploadedCollection(t, recorder)
}

// grantGroupPermission writes a permission row for a group.
//
// The row's `allowed` flag is REQUIRED by the collection and is what the
// permission service reads, so a row written by hand without it is a
// refused save, not a grant.
func grantGroupPermission(t *testing.T, app core.App, groupID, permission string) {
	t.Helper()

	require.NoError(t, testRepo(t, app).SetPermission(groupID, permission, "", true))
}

// TestUploads_TheResourceCarriesEveryUpstreamAttribute pins the resource
// against upstream's `FileResource::fields()`.
//
// The port served a subset, and a missing attribute is SILENT: the frontend
// reads `file.thumbnailUrl()` and gets `undefined`, which renders as "no
// value" rather than as an error. Every key is named here, so dropping one
// fails the test.
func TestUploads_TheResourceCarriesEveryUpstreamAttribute(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "attributes")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "notes.txt", "hello", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	resource := uploadedResource(t, recorder)
	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok)

	for _, key := range []string{
		"baseName", "path", "url", "type", "size", "humanSize", "createdAt",
		"uuid", "tag", "hidden", "bbcode", "shared", "imageWidth", "imageHeight",
		"thumbnailUrl", "thumbnailWidth", "thumbnailHeight", "isPrivateShared",
		"canViewInfo", "canHide", "canDelete",
	} {
		require.Contains(t, attributes, key,
			"the file resource must carry upstream's %q attribute", key)
	}

	// The three the port does not implement are upstream's answers for a
	// forum without the feature, not zero values.
	require.Nil(t, attributes["thumbnailUrl"])
	require.Nil(t, attributes["thumbnailWidth"])
	require.Nil(t, attributes["thumbnailHeight"])
	require.Equal(t, false, attributes["isPrivateShared"])
	// `FilePolicy::viewInfo()` denies for everybody.
	require.Equal(t, false, attributes["canViewInfo"])

	// A file that is not an image has NO dimensions: null, not 0.
	require.Nil(t, attributes["imageWidth"])
	require.Nil(t, attributes["imageHeight"])

	// `createdAt` is RFC 3339, like every other resource's. The record's
	// own column is PocketBase's internal layout, which a client's
	// `new Date(...)` reads differently (or not at all).
	createdAt, ok := attributes["createdAt"].(string)
	require.True(t, ok)
	require.Contains(t, createdAt, "T", "createdAt must be RFC 3339: %q", createdAt)

	// `path` is the stored name, and `uuid` is the id the download route
	// takes.
	require.NotEmpty(t, attributes["path"])
	require.Equal(t, resource["id"], attributes["uuid"])

	// The embed string is Markdown, which is what the forum renders.
	require.Equal(t,
		"["+attributes["baseName"].(string)+"]("+attributes["url"].(string)+")",
		attributes["bbcode"])

	// The actor relationship is upstream's.
	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, relationships, "actor")
}

// TestUploads_TheUpstreamUploadRouteTakesManyFiles pins `POST
// /api/fof/upload`: upstream's path, its repeated `files` field, and its
// collection response.
//
// The port served `/api/uploads` with a single `file` part and answered ONE
// resource. A client written against the extension posts several files to
// the other path and iterates `data`, so a single object is a `data.map is
// not a function` in the browser.
func TestUploads_TheUpstreamUploadRouteTakesManyFiles(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "multiuploader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadMany(t, handler, cookies, "/forum/api/fof/upload", nil, "one.txt", "two.txt")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	resources := uploadedCollection(t, recorder)
	require.Len(t, resources, 2)

	names := make([]string, 0, 2)
	for _, resource := range resources {
		attributes, ok := resource["attributes"].(map[string]any)
		require.True(t, ok)
		names = append(names, attributes["baseName"].(string))
	}
	require.ElementsMatch(t, []string{"one.txt", "two.txt"}, names)
}

// TestUploads_TheUploadNeedsTheUploadPermission pins upstream's gate.
//
// `fof-upload.upload` is what its `Upload` command asserts, and the port
// used to accept any signed-in caller. The permission is seeded for the
// member group (see the seed's note), so revoking it is how an operator
// turns uploads off — and the endpoint has to honour that.
func TestUploads_TheUploadNeedsTheUploadPermission(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "noupload")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	removeMemberPermission(t, app, domain.PermissionUploadFiles)

	recorder := uploadFile(t, handler, cookies, "notes.txt", "hello", "")
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestUploads_TheSharedLibraryNeedsItsPermission pins the two shared-file
// permissions.
//
// `access-shared-files` is the read side and `upload-shared-files` the
// write side, exactly as upstream's `FileResource::scope()` and
// `FilePolicy` split them: a caller who may only read the library does not
// see its hidden entries, and a caller with neither is refused outright.
func TestUploads_TheSharedLibraryNeedsItsPermission(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "libraryreader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// Neither permission: the listing is refused, and so is a shared
	// upload.
	recorder := do(t, handler, http.MethodGet, "/forum/api/fof/uploads?filter[shared]=1", "", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	recorder = uploadMany(t, handler, cookies, "/forum/api/fof/upload",
		map[string]string{"options[shared]": "true"}, "shared.txt")
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The read permission alone is enough to list it.
	grantGroupPermission(t, app, domain.GroupMemberID, domain.PermissionAccessSharedFiles)

	recorder = do(t, handler, http.MethodGet, "/forum/api/fof/upload/shared-files", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Empty(t, uploadedCollectionOfList(t, recorder))
}

// TestUploads_AHiddenFileLeavesTheListing pins the media manager's switch.
//
// Hiding is not deleting: the file stays readable by whoever can read the
// post it is on, and it disappears from the owner's library — which is
// what upstream's `hidden` column means and what its scope filters on.
func TestUploads_AHiddenFileLeavesTheListing(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "hider")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "hide-me.txt", "hello", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/files", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Len(t, uploadedCollectionOfList(t, recorder), 1)

	recorder = do(t, handler, http.MethodPatch, "/forum/api/fof/upload/hide",
		`{"uuid":"`+fileID+`","hidden":true}`, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/files", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Empty(t, uploadedCollectionOfList(t, recorder),
		"a hidden file must leave its owner's listing")

	// Still readable directly: hiding is not deleting.
	recorder = do(t, handler, http.MethodGet, "/forum/api/files/"+fileID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// TestUploads_TheDeleteByUUIDRouteAnswers204 pins upstream's delete path.
//
// The extension deletes through `DELETE /api/fof/upload/delete/{uuid}` and
// answers a BODYLESS 204 (`new EmptyResponse(204)`); the port's own route
// answered 204 with `{"data":null}`, which a client that parses the body
// has to special-case.
func TestUploads_TheDeleteByUUIDRouteAnswers204(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "uuiddeleter")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "gone.txt", "bye", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = do(t, handler, http.MethodDelete, "/forum/api/fof/upload/delete/"+fileID, "", cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	require.Empty(t, strings.TrimSpace(recorder.Body.String()), "a 204 carries no body")

	recorder = do(t, handler, http.MethodGet, "/forum/api/files/"+fileID, "", cookies)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

// TestUploads_TheDownloadRouteServesTheBytes pins upstream's download URL.
//
// `File::url` is the extension's own route
// (`/api/fof/download/{uuid}`), so a client that stores that URL has to be
// able to fetch the bytes from it — the same handler the forum's own
// `/files/{id}` route uses, and the same visibility rule.
func TestUploads_TheDownloadRouteServesTheBytes(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "downloader2")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "notes.txt", "hello", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/fof/download/"+fileID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "hello", recorder.Body.String())

	// A caller who may not read the file gets the same 404 the forum's own
	// route answers: the denial never confirms that the file exists.
	other := testUser(t, app, "intruder2")
	otherToken, err := other.NewAuthToken()
	require.NoError(t, err)

	recorder = do(t, handler, http.MethodGet, "/forum/api/fof/download/"+fileID, "",
		sessionCookies(otherToken, "csrf-token"))
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

// TestUploads_TheSizeLimitComesFromTheSetting pins `fof-upload.maxFileSize`.
//
// The setting is upstream's key in upstream's unit (KiB), and it LOWERS the
// limit; the collection's own field maximum is the ceiling, so a larger
// setting cannot make PocketBase accept more.
func TestUploads_TheSizeLimitComesFromTheSetting(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "sizelimited")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// 2 KiB is fine under the 32 MiB default.
	recorder := uploadFile(t, handler, cookies, "big.txt", strings.Repeat("x", 2048), "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	// One KiB is not.
	setForumSetting(t, app, "fof-upload.maxFileSize", "1")

	recorder = uploadFile(t, handler, cookies, "bigger.txt", strings.Repeat("x", 2048), "")
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
}

// TestUploads_AModeratorMayDeleteAnybodysFile pins the permission tier.
//
// Ownership is not enough to delete somebody else's file: upstream's
// `FilePolicy::delete()` asks for `deleteUserUploads`, which the seed puts
// in the MODERATOR group — so a member can delete their own and nothing
// more.
func TestUploads_AModeratorMayDeleteAnybodysFile(t *testing.T) {
	app, handler := newTestForum(t)

	owner := testUser(t, app, "owner2")
	ownerToken, err := owner.NewAuthToken()
	require.NoError(t, err)
	ownerCookies := sessionCookies(ownerToken, "csrf-token")

	recorder := uploadFile(t, handler, ownerCookies, "ours.txt", "ours", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	moderator := testUser(t, app, "moderator2")
	require.NoError(t, testRepo(t, app).AddUserToGroup(moderator.Id, domain.GroupModeratorID))
	moderatorToken, err := moderator.NewAuthToken()
	require.NoError(t, err)
	moderatorCookies := sessionCookies(moderatorToken, "csrf-token")

	recorder = do(t, handler, http.MethodDelete, "/forum/api/files/"+fileID, "", moderatorCookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}

// TestUploads_TheListingIsPaged pins upstream's pagination.
//
// `FileResource` declares `->paginate(20, 50)`, and the frontend reads
// `meta.page.total`, `meta.page.offset`, `meta.perPage` and `links.next` —
// the last of which is its "load more" button. A listing without them
// shows the first page and stops.
func TestUploads_TheListingIsPaged(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "paged")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		recorder := uploadFile(t, handler, cookies, name, "x", "")
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	}

	recorder := do(t, handler, http.MethodGet, "/forum/api/files?page[limit]=2", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok)
	require.Len(t, resources, 2, "page[limit] must bound the page")
	require.NotNil(t, document.Meta, "the listing must carry pagination metadata")
	require.NotNil(t, document.Meta.Page)
	require.Equal(t, 3, document.Meta.Page.Total, "the total is the WHOLE matching set")
	require.Equal(t, 2, document.Meta.PerPage)
	require.NotNil(t, document.Links, "a first page of three has a next page")
	require.NotEmpty(t, document.Links.Next)

	// The second page holds the remainder and has no next link.
	recorder = do(t, handler, http.MethodGet, "/forum/api/files?page[limit]=2&page[offset]=2", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document = decodeDocument(t, recorder)
	resources, ok = document.Data.([]any)
	require.True(t, ok)
	require.Len(t, resources, 1)
	if document.Links != nil {
		require.Empty(t, document.Links.Next, "the last page has no next link")
	}
}

// TestUploads_TheListingDefaultsToNewestFirst pins the sort.
//
// Upstream sorts `-id`; PocketBase ids are random, so `-id` would be an
// arbitrary order here and the default is `-created_at`, which is the order
// upstream's `-id` produces on a sequential key.
func TestUploads_TheListingDefaultsToNewestFirst(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "sorted")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	for _, name := range []string{"first.txt", "second.txt"} {
		recorder := uploadFile(t, handler, cookies, name, "x", "")
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	}

	recorder := do(t, handler, http.MethodGet, "/forum/api/files", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	resources := uploadedCollectionOfList(t, recorder)
	require.Len(t, resources, 2)

	newest, ok := resources[0]["attributes"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "second.txt", newest["baseName"], "the newest upload comes first")
}

// TestUploads_TheForumDocumentCarriesTheCanUploadFlag pins the attribute the
// composer's button reads.
//
// The button is offered when `fof-upload.canUpload` is true, and the flag is
// computed with the SAME permission the upload endpoint checks. Both halves
// matter: a flag that says yes where the endpoint says no is a button the
// server refuses, and a flag that is always false is a permission an
// operator can grant that changes nothing.
func TestUploads_TheForumDocumentCarriesTheCanUploadFlag(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "flagged")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes := forumAttributesOf(t, recorder.Body.String())
	require.Equal(t, true, attributes["fof-upload.canUpload"],
		"a member holds the upload permission, so the button is offered")

	// Revoke it: the flag and the endpoint must agree.
	removeMemberPermission(t, app, domain.PermissionUploadFiles)

	recorder = do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	attributes = forumAttributesOf(t, recorder.Body.String())
	require.Equal(t, false, attributes["fof-upload.canUpload"])

	recorder = uploadFile(t, handler, cookies, "refused.txt", "x", "")
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}
