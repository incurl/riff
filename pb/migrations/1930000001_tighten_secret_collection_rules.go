package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// tightenSecretCollectionRules scopes three collections to their
// owner.
//
// The collections are:
//
//   - personal_access_tokens — a PAT is a bearer credential; a
//     stranger who could list one would learn its description and
//     (before the Hidden fix) its bcrypt digest.
//   - user_webhooks — the signing_secret is the `whsec_…` token the
//     drain signs outbound deliveries with.
//   - linked_identities — the provider_subject and
//     provider_username bind an external account to a user.
//
// All three carried `nil` rules, which in PocketBase means
// SUPERUSER-ONLY, not public. The Memos-shaped REST surface reached
// them through the `pb_records_crud` flavour, which calls
// `app.FindRecordsByFilter` directly and never consults the rule
// engine — so a nil rule protected nothing on that path and an
// anonymous caller could list every row.
//
// The routes now dispatch through the Connect handlers (which
// authorize the caller against the target user), and these rules
// make the collection itself enforce the same boundary — including
// on PocketBase's own `/api/collections/{name}/records` surface,
// which is mounted and reachable.
//
// Idempotency: the migration is a no-op when the collection does not
// exist yet (a fresh install creates it later) or when its rules are
// already owner-scoped.
func init() {
	migrations.Register(func(txApp core.App) error {
		// The owner-scoped rule: the row belongs to the caller.
		ownerRule := "user = @request.auth.id"

		for _, name := range []string{
			"personal_access_tokens",
			"user_webhooks",
			"linked_identities",
		} {
			col, err := txApp.FindCollectionByNameOrId(name)
			if err != nil {
				// The collection has not been created yet on a
				// fresh install; its own create migration owns
				// the schema.
				continue
			}
			if col.ListRule != nil && *col.ListRule == ownerRule {
				continue
			}
			col.ListRule = types.Pointer(ownerRule)
			col.ViewRule = types.Pointer(ownerRule)
			// A create may only stamp the caller's own id, which
			// blocks a forged `user` field.
			col.CreateRule = types.Pointer(ownerRule)
			col.UpdateRule = types.Pointer(ownerRule)
			col.DeleteRule = types.Pointer(ownerRule)
			if err := txApp.Save(col); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
