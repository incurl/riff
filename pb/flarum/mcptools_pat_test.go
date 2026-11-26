package flarum

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// TestForum_APersonalAccessTokenWrites pins the credential rule the MCP
// service depends on.
//
// A riff personal access token ("memos_pat_*") is the instance's REVOCABLE
// credential, so it is the one an agent carries — an MCP tool call forwards
// the Authorization header and nothing else. The forum's session resolver
// used to understand PocketBase auth tokens only, which meant a PAT could
// post a memo through /mcp and not a forum reply.
func TestForum_APersonalAccessTokenWrites(t *testing.T) {
	app, handler := newTestForum(t)

	author := testUser(t, app, "pat_writer")
	raw := seedPersonalAccessToken(t, app, author.Id)

	body := `{"data":{"type":"discussions","attributes":{"title":"From a PAT","content":"Hello"}}}`
	recorder := doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, nil,
		map[string]string{"Authorization": "Bearer " + raw})
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), author.Id,
		"the discussion belongs to the token's owner")

	// A token that only LOOKS like a PAT is not a credential.
	recorder = doWithHeaders(t, handler, http.MethodPost, "/forum/api/discussions", body, nil,
		map[string]string{"Authorization": "Bearer memos_pat_not-a-real-token"})
	require.NotEqual(t, http.StatusCreated, recorder.Code,
		"an unknown PAT must not authenticate; got %s", recorder.Body.String())
}

// seedPersonalAccessToken writes a PAT row and returns the raw token.
func seedPersonalAccessToken(t *testing.T, app core.App, userID string) string {
	t.Helper()

	raw := "memos_pat_" + strings.Repeat("Q", 32)
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	require.NoError(t, err)

	collection, err := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, err, "the PAT migration must have run")

	row := core.NewRecord(collection)
	row.Set("user", userID)
	row.Set("description", "the MCP agent")
	row.Set("token_hash", string(hash))
	require.NoError(t, app.Save(row))

	return raw
}
