package flarum

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"
)

// The Uploads plugin: post attachments.
//
// Upstream Flarum v2.0.0's CORE has no attachments at all — the
// `post_attachments` table and the `Post->upload()` relationship were
// removed from the core and live in the `fof/upload` extension. This
// plugin is the forum's implementation of them, and these tests pin
// the contract that extension established.

// uploadFile performs a multipart upload against the forum.
func uploadFile(t *testing.T, handler http.Handler, cookies []*http.Cookie, filename, content, postID string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write([]byte(content))
	require.NoError(t, err)

	if postID != "" {
		require.NoError(t, writer.WriteField("post", postID))
	}
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/forum/api/uploads", &body)
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

// uploadedResource decodes the FIRST resource of an upload response.
//
// The response is a JSON:API COLLECTION — upstream's `UploadHandler` answers
// an array, because one request may carry several files — so a single-file
// upload is an array holding one resource.
func uploadedResource(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "an upload answers a collection, not a single resource: %s", recorder.Body.String())
	require.Len(t, resources, 1, recorder.Body.String())

	resource, ok := resources[0].(map[string]any)
	require.True(t, ok)
	return resource
}

// singleResource decodes a document that carries ONE resource.
//
// The show, attach and post endpoints answer `data` as an object; only an
// upload answers a collection (see uploadedResource).
func singleResource(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	document := decodeDocument(t, recorder)
	resource, ok := document.Data.(map[string]any)
	require.True(t, ok, "the data should be a resource object: %s", recorder.Body.String())
	return resource
}

// uploadedID returns the id of the first file an upload answered with.
//
// `resourceIDOf` reads a SINGLE-resource document, which is what every
// other endpoint answers; an upload answers a collection.
func uploadedID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	id, ok := uploadedResource(t, recorder)["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, id)
	return id
}

// TestUploads_MigrationCreatesTheCollection pins layer 1 of the
// three-layer flow: the plugin's own migration created its collection.
func TestUploads_MigrationCreatesTheCollection(t *testing.T) {
	app, _ := newTestForum(t)

	collection, err := app.FindCollectionByNameOrId("forum_files")
	require.NoError(t, err, "the uploads plugin must create its collection")

	require.Equal(t, core.CollectionTypeBase, collection.Type)

	// The blob is a file field, and the forum's posts are what the
	// files hang off.
	require.NotNil(t, collection.Fields.GetByName("blob"))
	require.NotNil(t, collection.Fields.GetByName("post"))
	require.NotNil(t, collection.Fields.GetByName("actor"))
}

// TestUploads_AFileIsUploaded pins the upload itself.
//
// The response is the `files` resource — upstream's `fof/upload` type
// name — and it carries the attributes the frontend reads.
func TestUploads_AFileIsUploaded(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "uploader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)

	recorder := uploadFile(t, handler, sessionCookies(token, "csrf-token"), "notes.txt", "hello", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	resource := uploadedResource(t, recorder)

	require.Equal(t, "files", resource["type"], "the resource type is upstream's")
	require.NotEmpty(t, resource["id"])

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok)

	require.Equal(t, "notes.txt", attributes["baseName"])
	require.Equal(t, "text/plain", attributes["type"])
	require.Equal(t, "5 B", attributes["humanSize"])
	require.Equal(t, false, attributes["hidden"])

	// The URL is the forum's own file route, under the mount point.
	url, ok := attributes["url"].(string)
	require.True(t, ok)
	require.Contains(t, url, "/forum/files/")
}

// TestUploads_AMemberMayNotUpload pins the auth gate.
func TestUploads_AMemberMayNotUpload(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := uploadFile(t, handler, nil, "notes.txt", "hello", "")
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
}

// TestUploads_TheFileIsAttachedToAPost pins the post's `files`
// relationship.
//
// The relationship is what the frontend renders: the post carries the
// identifiers of its files, and the `files` resource is the type
// upstream's extension uses.
func TestUploads_TheFileIsAttachedToAPost(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "poster")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// A discussion with a post.
	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"With a file","content":"body"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	postID := firstPostIDOf(t, recorder)
	require.NotEmpty(t, postID)

	// Upload a file and attach it to the post.
	recorder = uploadFile(t, handler, cookies, "notes.txt", "hello", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/files",
		`{"fileId":"`+fileID+`"}`, cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The post now carries the file.
	recorder = do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	resource := singleResource(t, recorder)

	relationships, ok := resource["relationships"].(map[string]any)
	require.True(t, ok)

	files, ok := relationships["files"].(map[string]any)
	require.True(t, ok, "the post should carry a files relationship: %#v", relationships)

	data, ok := files["data"].([]any)
	require.True(t, ok, "the files relationship should be a list")
	require.Len(t, data, 1)

	identifier, ok := data[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "files", identifier["type"])
	require.Equal(t, fileID, identifier["id"])
}

// TestUploads_TheBytesAreServed pins the file route.
//
// The bytes are served through the forum's OWN route, because
// PocketBase's native file URL lives at the root of the listener,
// which the forum does not own.
func TestUploads_TheBytesAreServed(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "downloader")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "notes.txt", "hello world", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/files/"+fileID, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "hello world", recorder.Body.String())
	require.Equal(t, "text/plain", recorder.Header().Get("Content-Type"))

	// The bytes are an ATTACHMENT, never inline: an HTML or SVG file
	// served inline could run against the forum's origin.
	require.Contains(t, recorder.Header().Get("Content-Disposition"), "attachment")
}

// TestUploads_AnUnattachedFileIsPrivate pins the orphan rule.
//
// A file that is not attached to a post has no post to inherit a
// visibility from, so only its uploader may read it.
func TestUploads_AnUnattachedFileIsPrivate(t *testing.T) {
	app, handler := newTestForum(t)

	owner := testUser(t, app, "file_owner")
	ownerToken, err := owner.NewAuthToken()
	require.NoError(t, err)

	intruder := testUser(t, app, "file_intruder")
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	recorder := uploadFile(t, handler, sessionCookies(ownerToken, "csrf-token"), "secret.txt", "mine", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	// The owner may read it.
	recorder = do(t, handler, http.MethodGet, "/forum/files/"+fileID, "", sessionCookies(ownerToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// Another member may not, and the denial is a 404 so the response
	// never confirms that the file exists.
	recorder = do(t, handler, http.MethodGet, "/forum/files/"+fileID, "", sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

// TestUploads_AMemberMayNotDeleteSomebodyElsesFile pins the delete
// gate.
func TestUploads_AMemberMayNotDeleteSomebodyElsesFile(t *testing.T) {
	app, handler := newTestForum(t)

	owner := testUser(t, app, "deleter_owner")
	ownerToken, err := owner.NewAuthToken()
	require.NoError(t, err)

	intruder := testUser(t, app, "deleter_intruder")
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	recorder := uploadFile(t, handler, sessionCookies(ownerToken, "csrf-token"), "mine.txt", "mine", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = do(t, handler, http.MethodDelete, "/forum/api/files/"+fileID, "",
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	// The owner may.
	recorder = do(t, handler, http.MethodDelete, "/forum/api/files/"+fileID, "",
		sessionCookies(ownerToken, "csrf-token"))
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}

// TestUploads_TheDeleteHonoursTheMethodOverride pins the override.
//
// The frontend turns every method other than GET and POST into a POST
// carrying `X-HTTP-Method-Override`, and PocketBase's router matches
// on the REAL method — so the POST route has to resolve the effective
// method itself.
func TestUploads_TheDeleteHonoursTheMethodOverride(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "override_user")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "gone.txt", "bye", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/files/"+fileID, "",
		cookies, map[string]string{"X-HTTP-Method-Override": "DELETE"})
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
}

// TestUploads_AMemberMayNotAttachSomebodyElsesFile pins the attach
// gate.
func TestUploads_AMemberMayNotAttachSomebodyElsesFile(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "attach_author")
	authorToken, err := author.NewAuthToken()
	require.NoError(t, err)

	intruder := testUser(t, app, "attach_intruder")
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	authorCookies := sessionCookies(authorToken, "csrf-token")
	intruderCookies := sessionCookies(intruderToken, "csrf-token")

	// The author writes a post.
	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Owned","content":"body"}}}`,
		authorCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", authorCookies)
	postID := firstPostIDOf(t, recorder)

	// The intruder uploads a file.
	recorder = uploadFile(t, handler, intruderCookies, "intruder.txt", "mine", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	// Attaching it to somebody else's post is refused.
	recorder = do(t, handler, http.MethodPost, "/forum/api/posts/"+postID+"/files",
		`{"fileId":"`+fileID+`"}`, intruderCookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestUploads_TheMediaTypeComesFromTheName pins the type derivation.
//
// The browser's Content-Type is whatever the client felt like sending,
// so the type is derived from the file's NAME. A client that claims
// `image/png` about a script gets a script — served as an attachment,
// so it cannot run against the forum's origin.
func TestUploads_TheMediaTypeComesFromTheName(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "mime_user")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "payload.html", "<script>alert(1)</script>", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	attributes := uploadedResource(t, recorder)["attributes"].(map[string]any)

	require.Equal(t, "text/html", attributes["type"])
}

// TestUploads_TheFileNameIsSanitised pins the name handling.
//
// A name is used as a download's `Content-Disposition` filename and as
// the storage key's leaf, so a name carrying a path separator or a
// quote is reduced.
func TestUploads_TheFileNameIsSanitised(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "name_user")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, `../../etc/"passwd".txt`, "x", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	attributes := uploadedResource(t, recorder)["attributes"].(map[string]any)

	baseName, ok := attributes["baseName"].(string)
	require.True(t, ok)
	require.NotContains(t, baseName, "/")
	require.NotContains(t, baseName, "..")
	require.NotContains(t, baseName, `"`)
}

// TestUploads_TheListHoldsTheCallersFiles pins the listing.
func TestUploads_TheListHoldsTheCallersFiles(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "lister")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := uploadFile(t, handler, cookies, "a.txt", "a", "")
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/files", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	document := decodeDocument(t, recorder)
	resources, ok := document.Data.([]any)
	require.True(t, ok, "the listing should be a collection")
	require.Len(t, resources, 1)
}

// TestUploads_TheFileIsDeletedWithItsPost pins the cascade.
//
// Deleting a post removes its files: upstream's `fof_upload_file_posts`
// pivot cascades the same way, and a file left behind would be
// readable by anyone who knew its id.
func TestUploads_TheFileIsDeletedWithItsPost(t *testing.T) {
	app, handler := newTestForum(t)

	user := testUser(t, app, "cascade_user")
	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Doomed","content":"body"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", cookies)
	postID := firstPostIDOf(t, recorder)

	recorder = uploadFile(t, handler, cookies, "doomed.txt", "bye", postID)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	// Deleting the post deletes the file.
	recorder = do(t, handler, http.MethodDelete, "/forum/api/discussions/"+discussionID, "", cookies)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/files/"+fileID, "", cookies)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

// TestUploads_TheDeleteFlagAgreesWithTheEndpoint pins the `canDelete`
// flag.
//
// The flag has to agree with what the endpoint enforces, or the
// frontend offers a delete control that the API refuses.
//
// The file has to be ATTACHED to a post for another member to read it
// at all: an unattached file belongs to its uploader alone, and the
// intruder gets a 404 before the flag is even reached.
func TestUploads_TheDeleteFlagAgreesWithTheEndpoint(t *testing.T) {
	app, handler := newTestForum(t)

	owner := testUser(t, app, "flag_owner")
	ownerToken, err := owner.NewAuthToken()
	require.NoError(t, err)

	intruder := testUser(t, app, "flag_intruder")
	intruderToken, err := intruder.NewAuthToken()
	require.NoError(t, err)

	ownerCookies := sessionCookies(ownerToken, "csrf-token")

	// A post to attach the file to.
	recorder := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Owned","content":"body"}}}`,
		ownerCookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	discussionID := resourceIDOf(t, recorder)

	recorder = do(t, handler, http.MethodGet, "/forum/api/discussions/"+discussionID, "", ownerCookies)
	postID := firstPostIDOf(t, recorder)

	// A file attached to that post.
	recorder = uploadFile(t, handler, ownerCookies, "mine.txt", "mine", postID)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	fileID := uploadedID(t, recorder)

	// The uploader may delete it.
	recorder = do(t, handler, http.MethodGet, "/forum/api/files/"+fileID, "", ownerCookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	attributes := fileAttributesOf(t, recorder)
	require.Equal(t, true, attributes["canDelete"])

	// Another member may read it (it is on a post in a public
	// discussion) but may not delete it.
	recorder = do(t, handler, http.MethodGet, "/forum/api/files/"+fileID, "",
		sessionCookies(intruderToken, "csrf-token"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	attributes = fileAttributesOf(t, recorder)
	require.Equal(t, false, attributes["canDelete"])
}

// fileAttributesOf reads a file resource's attributes.
func fileAttributesOf(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	resource := singleResource(t, recorder)

	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")
	return attributes
}

// firstPostIDOf reads a discussion's first post's id.
//
// The post's id comes from the discussion's `posts` relationship, which
// is what the frontend reads it from.
func firstPostIDOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var document struct {
		Data struct {
			Relationships struct {
				Posts struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"posts"`
			} `json:"relationships"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	if len(document.Data.Relationships.Posts.Data) == 0 {
		return ""
	}
	return document.Data.Relationships.Posts.Data[0].ID
}
