// Package pattoken verifies riff's `memos_pat_*` personal access tokens.
//
// The token is the instance's revocable credential: a PocketBase auth token
// is a stateless JWT with a fixed lifetime, while a PAT can be deleted from
// the `personal_access_tokens` table and stops working immediately. That is
// what makes it the credential an agent should carry — and it is why every
// surface that accepts a caller has to understand it, not just the
// Connect/gRPC API it was introduced for.
//
// The lifecycle (Create/List/Delete) lives in `pb/connect`, which owns the
// RPCs; this package owns the READ side, because three packages need the
// same verdict: `pb/connect` (its auth interceptor), `pb/api` (the file
// server), and the SPA-facing module resolvers (`pb/flarum`'s session and
// `pb/leafwiki`'s current-user lookup). Copying a bcrypt scan into each of
// them is precisely the kind of duplicate that drifts.
//
// It is in `internal/` because the three callers are siblings in the import
// graph: none of them may import another, and all of them may import this.
package pattoken

import (
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"
)

// Prefix is the literal prefix every riff personal access token carries.
//
// It is checked before the (relatively expensive) bcrypt scan, so a
// PocketBase auth token costs nothing here.
const Prefix = "memos_pat_"

// Collection is the schema name of the PAT table, created by
// `pb/migrations/1820000000_create_personal_access_tokens.go`.
const Collection = "personal_access_tokens"

// Verify reports which account a personal access token belongs to.
//
// It returns:
//
//   - (user, nil) for a verified, unexpired token whose owner can be loaded;
//   - (nil, nil) when the value does not look like a PAT, carries no known
//     token, or the table does not exist (an unrouted/legacy install) — the
//     caller treats the request as unauthenticated;
//   - (nil, err) when the token IS a PAT but is expired or its owner is
//     gone, because those are refusals worth reporting rather than a silent
//     fall-through.
//
// A full table scan is deliberate: `token_hash` is a bcrypt digest, so it
// cannot be matched in SQL. The table holds the tokens one account has
// minted — a handful — and a bcrypt compare at the framework's default cost
// is about a millisecond.
func Verify(app core.App, raw string) (*core.Record, error) {
	if !strings.HasPrefix(raw, Prefix) {
		return nil, nil
	}

	collection, err := app.FindCollectionByNameOrId(Collection)
	if err != nil {
		// The table is missing: the migration never ran (an older install,
		// or a host that dropped this surface in). The request is simply
		// unauthenticated.
		return nil, nil //nolint:nilerr // a missing table means "no such credential"
	}

	rows, err := app.FindAllRecords(collection)
	if err != nil {
		return nil, errors.Wrap(err, "personal access token lookup")
	}

	now := time.Now().UTC()
	rawToken := []byte(raw)
	for _, row := range rows {
		hash := row.GetString("token_hash")
		if hash == "" {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), rawToken); err != nil {
			// ErrMismatchedHashAndPassword is the expected "not this row"
			// signal; skip without logging.
			continue
		}

		if expires := row.GetDateTime("expires_at"); !expires.IsZero() && now.After(expires.Time()) {
			return nil, errors.New("personal access token has expired")
		}

		userID := row.GetString("user")
		if userID == "" {
			return nil, errors.New("personal access token has no owning user")
		}

		user, err := app.FindRecordById("users", userID)
		if err != nil {
			return nil, errors.Wrap(err, "personal access token user lookup")
		}

		// Stamp last_used_at on a best-effort basis: the token is already
		// proven valid, and failing the request over a missing audit stamp
		// would be the wrong trade. The recover covers a write that panics
		// on a torn-down app during shutdown.
		func() {
			defer func() {
				_ = recover()
			}()
			row.Set("last_used_at", now)
			_ = app.Save(row)
		}()

		return user, nil
	}

	return nil, nil
}
