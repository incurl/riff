package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

	// The author may.
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

	recorder = do(t, handler, http.MethodPatch, "/forum/api/discussions/"+discussionID,
		`{"data":{"type":"discussions","attributes":{"title":"Renamed by the author"}}}`,
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
