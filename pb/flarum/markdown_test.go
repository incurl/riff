package flarum

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestForum_PostContentIsRenderedToHTML pins the rendering contract.
//
// The reused frontend inserts `contentHtml` as trusted HTML
// (`m.trust(contentHtml)` in flarum/forum/components/Comment.tsx), so
// the forum MUST render the Markdown server-side. Serving the source
// would show raw Markdown to the reader.
func TestForum_PostContentIsRenderedToHTML(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	// A reply needs a discussion; create one first through the API.
	discussion := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Rendered","content":"# Heading\n\nA **bold** word."}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, discussion.Code, discussion.Body.String())

	document := decodeDocument(t, discussion)
	require.Len(t, document.Included, 1, "the created discussion should include its first post")

	attributes := document.Included[0].Attributes
	require.NotNil(t, attributes)

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post must carry contentHtml")
	require.Contains(t, contentHTML, "<h1", "the heading must be rendered as HTML")
	require.Contains(t, contentHTML, "<strong>bold</strong>", "the emphasis must be rendered as HTML")
	require.NotContains(t, contentHTML, "**bold**", "the raw Markdown must not be served as the HTML")

	// The source is kept alongside the rendered body: the frontend's
	// edit form sends it back.
	content, ok := attributes["content"].(string)
	require.True(t, ok, "the post must carry the Markdown source")
	require.Contains(t, content, "**bold**")
}

// TestForum_PostContentIsRenderedOnRead pins that the rendered body
// survives the round-trip through the database.
func TestForum_PostContentIsRenderedOnRead(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Stored","content":"A [link](https://example.com)."}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	document := decodeDocument(t, created)
	require.Len(t, document.Included, 1)
	postID := document.Included[0].ID

	read := do(t, handler, http.MethodGet, "/forum/api/posts/"+postID, "", nil)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())

	readDocument := decodeDocument(t, read)
	resource, ok := readDocument.Data.(map[string]any)
	require.True(t, ok)
	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post must carry contentHtml")
	require.Contains(t, contentHTML, `<a href="https://example.com">link</a>`,
		"the stored body must be the rendered HTML")
}

// TestForum_RawHTMLIsNotTrusted pins that a raw HTML block in a post is
// not passed through.
//
// The rendered body is inserted into the page as trusted HTML, so an
// unescaped script tag would be an injection vector.
func TestForum_RawHTMLIsNotTrusted(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Unsafe","content":"<script>alert(1)</script>"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	document := decodeDocument(t, created)
	require.Len(t, document.Included, 1)
	attributes := document.Included[0].Attributes

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post must carry contentHtml")
	require.NotContains(t, contentHTML, "<script>", "a raw script tag must not reach the trusted HTML")
}

// TestForum_HashtagAndMentionTextIsPreserved pins that the forum
// renders a post with the plain markdown service.
//
// riff's `#tag` and `@mention` extensions build AST nodes with no HTML
// node renderer, so enabling them makes the rendered body drop the
// text. In a forum post both are literal characters, which is what
// Flarum core shows too — so the forum must not enable them.
func TestForum_HashtagAndMentionTextIsPreserved(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Tags","content":"hi #work and @alice"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	document := decodeDocument(t, created)
	require.Len(t, document.Included, 1)
	attributes := document.Included[0].Attributes
	require.NotNil(t, attributes)

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post must carry contentHtml")
	require.Contains(t, contentHTML, "#work", "a hashtag must not vanish from the rendered body")
	require.Contains(t, contentHTML, "@alice", "a mention must not vanish from the rendered body")
}

// TestForum_EditRerendersTheBody pins that an edit re-renders the
// stored HTML rather than storing the new source verbatim.
func TestForum_EditRerendersTheBody(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Edited","content":"plain text"}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	document := decodeDocument(t, created)
	require.Len(t, document.Included, 1)
	postID := document.Included[0].ID

	edited := do(t, handler, http.MethodPatch, "/forum/api/posts/"+postID,
		`{"data":{"type":"posts","id":"`+postID+`","attributes":{"content":"## Retitled\n\na **bold** edit"}}}`,
		cookies)
	require.Equal(t, http.StatusOK, edited.Code, edited.Body.String())

	editedDocument := decodeDocument(t, edited)
	resource, ok := editedDocument.Data.(map[string]any)
	require.True(t, ok)
	attributes, ok := resource["attributes"].(map[string]any)
	require.True(t, ok, "the resource should carry attributes")

	contentHTML, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post must carry contentHtml")
	require.Contains(t, contentHTML, "<h2", "the edit's heading must be rendered")
	require.Contains(t, contentHTML, "<strong>bold</strong>", "the edit's emphasis must be rendered")
	require.NotContains(t, contentHTML, "**bold**", "the raw Markdown must not be stored as the HTML")
}

// TestForum_PostContentTypeIsHTML pins the content type the frontend
// reads.
func TestForum_PostContentTypeIsHTML(t *testing.T) {
	app, handler := newTestForum(t)
	user := testUser(t, app, "writer")

	token, err := user.NewAuthToken()
	require.NoError(t, err)
	cookies := sessionCookies(token, "csrf-token")

	created := do(t, handler, http.MethodPost, "/forum/api/discussions",
		`{"data":{"type":"discussions","attributes":{"title":"Typed","content":"Body."}}}`,
		cookies)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	document := decodeDocument(t, created)
	require.Len(t, document.Included, 1)
	attributes := document.Included[0].Attributes

	require.Equal(t, "text/html", attributes["contentType"])
}
