package leafwiki

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// TestWiki_APersonalAccessTokenWrites pins the credential rule the MCP
// service depends on: a riff personal access token ("memos_pat_*") is the
// instance's revocable credential, and an MCP tool call forwards nothing but
// the Authorization header.
//
// The wiki's CSRF check already skips a header-authenticated caller, so the
// only thing standing between a PAT and a page write was the token lookup.
func TestWiki_APersonalAccessTokenWrites(t *testing.T) {
	server, app, handler := newTestAPI(t)

	author := testUser(t, app, "wiki_pat_writer")
	page, err := server.Tree().CreatePage(Actor{ID: author.Id}, "root", "Credentials", "", NodeKindPage)
	require.NoError(t, err)

	raw := seedWikiPersonalAccessToken(t, app, author.Id)

	recorder := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID,
		map[string]any{"content": "Written by an agent."},
		map[string]string{"Authorization": "Bearer " + raw})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "Written by an agent.")

	// The same write without a credential is refused, so the pass above is the
	// token's doing rather than an open door.
	recorder = doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID,
		map[string]any{"content": "Anonymous."}, nil)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
}

// seedWikiPersonalAccessToken writes a PAT row and returns the raw token.
func seedWikiPersonalAccessToken(t *testing.T, app core.App, userID string) string {
	t.Helper()

	raw := "memos_pat_" + strings.Repeat("W", 32)
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
