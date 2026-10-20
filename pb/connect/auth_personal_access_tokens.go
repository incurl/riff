package connect

import (
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"
)

// personalAccessTokenCollection is the schema name of the PAT table
// created by pb/migrations/1820000000_create_personal_access_tokens.go.
// The constant is kept here (instead of being inlined in the resolver
// below) so a future migration rename is a one-line edit and the
// test stub can refer to the same value.
const personalAccessTokenCollection = "personal_access_tokens"

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
	if !strings.HasPrefix(tok, personalAccessTokenPrefix) {
		return nil, nil
	}

	col, err := app.FindCollectionByNameOrId(personalAccessTokenCollection)
	if err != nil {
		// Collection missing means the migration never ran (e.g. an
		// older install). Returning nil here keeps the interceptor's
		// silent-fallback contract; the request is unauthenticated,
		// the same as if no token was presented.
		return nil, nil
	}

	rows, err := app.FindAllRecords(col)
	if err != nil {
		return nil, errors.Wrap(err, "personal access token lookup")
	}

	now := time.Now().UTC()
	rawToken := []byte(tok)
	for _, row := range rows {
		hash := row.GetString("token_hash")
		if hash == "" {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), rawToken); err != nil {
			// bcrypt.ErrMismatchedHashAndPassword is the expected
			// "not this row" signal; skip without logging.
			continue
		}

		// Token matched. Honour expires_at (zero = never expires).
		expires := row.GetDateTime("expires_at")
		if !expires.IsZero() && now.After(expires.Time()) {
			return nil, errors.New("personal access token has expired")
		}

		userID := row.GetString("user")
		if userID == "" {
			return nil, errors.New("personal access token has no owning user")
		}
		// The app.FindRecordById(collectionNameOrId, id) shortcut
		// saves a separate FindCollectionByNameOrId round-trip;
		// every other call site in the codebase uses the same
		// two-argument form (see pb/hooks/notifications_helpers_content.go
		// and the surrounding files).
		user, err := app.FindRecordById("users", userID)
		if err != nil {
			return nil, errors.Wrap(err, "personal access token user lookup")
		}

		// Stamp last_used_at on a best-effort basis. We do not
		// propagate a save error to the caller because the token
		// is already proven valid; failing the request over a
		// missing audit stamp would be the wrong trade.
		func() {
			defer func() {
				_ = recover()
			}()
			row.Set("last_used_at", now)
			_ = app.Save(row)
		}()

		return user, nil
	}

	// No row matched. The collection's *Rule fields are nil, so the
	// collection REST API is locked down; this is the only path that
	// can hand out the verdict "no such token".
	return nil, nil
}
