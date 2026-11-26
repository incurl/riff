package connect

import (
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/pattoken"
)

// personalAccessTokenCollection is the schema name of the PAT table
// created by pb/migrations/1820000000_create_personal_access_tokens.go.
// The constant is kept here (instead of being inlined in the resolver
// below) so a future migration rename is a one-line edit and the
// test stub can refer to the same value.
const personalAccessTokenCollection = pattoken.Collection

// resolvePersonalAccessToken attempts to authenticate the bearer of a
// riff-issued "memos_pat_*" personal access token. It returns:
//
//   - (rec, nil) on a verified, non-expired token whose owning user
//     can be loaded;
//   - (nil, nil) when the token does not look like a PAT (caller
//     should fall back to whatever the next resolver is); and
//   - (nil, err) when the token is structurally a PAT but is unknown,
//     revoked, expired, or whose owning user record could not be
//     loaded.
//
// The PAT lifecycle lives in user_personal_access_tokens.go (Create /
// List / Delete). This file owns the read path: the work the auth
// interceptor at connect.go::withAuth needs to do to honour a Bearer
// header that starts with personalAccessTokenPrefix.
//
// Why a full table scan? token_hash is a bcrypt digest, so we cannot
// SELECT WHERE token_hash = ?. The collection is per-user; the
// expected row count is "tokens a single user has minted", which is
// small (a handful at most). The bcrypt cost is the framework default
// (10) so a single compare is in the ~1 ms range. We bail at the
// first match because the unique index on token_hash makes a
// second-row match impossible in practice.
//
// ResolveFilePersonalAccessToken resolves a bearer token that may be a
// riff personal access token, for the file server in pb/api.
//
// The file server is not a Connect procedure, so it cannot use the
// withAuth interceptor's context plumbing; it needs the token
// resolved from the raw Authorization header. This wrapper exposes
// exactly that and nothing more: a non-PAT token yields (nil, nil),
// so the caller can treat the request as unauthenticated.
//
// The `memos_pat_` prefix is checked before the (relatively
// expensive) bcrypt scan, so a PocketBase auth token costs nothing
// here — the file server's normal session path goes through
// PocketBase's own loadAuthToken middleware instead.
func ResolveFilePersonalAccessToken(app core.App, tok string) (*core.Record, error) {
	return resolvePersonalAccessToken(app, tok)
}

// Best-effort last_used_at: a successful verify stamps the row's
// last_used_at column so the SPA can show "last used N hours ago".
// The write is wrapped in a recover() because the write is on the
// request path: a SQLite lock contention or transient I/O error must
// not turn a valid auth into a 5xx.
func resolvePersonalAccessToken(app core.App, tok string) (*core.Record, error) {
	// The scan, the bcrypt compare, the expiry check and the last_used_at
	// stamp live in `internal/pattoken`, because two other surfaces need the
	// same verdict: the file server in pb/api, and the forum's and the
	// wiki's session resolvers (an agent carries a PAT, not a session).
	return pattoken.Verify(app, tok)
}
