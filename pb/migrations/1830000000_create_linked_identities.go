package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createLinkedIdentitiesCollection backs the four
// UserService.LinkedIdentity RPCs declared in
// proto/api/v1/user_service.proto. One row per OAuth
// identity a user has linked to their account. The row
// is the user-side counterpart of the `_externalAuths`
// link table: the link table records the OAuth provider's
// internal id, this row records the same id together with
// a human-friendly display string and the identity
// provider the user linked through.
//
// The split matters for two reasons:
//
//  1. The Memos proto has a `LinkedIdentity` resource
//     keyed by `users/{user}/linkedIdentities/{li}`.
//     The SPA's LinkedIdentitySection.tsx renders that
//     resource directly. The link table's primary key
//     shape does not match the proto's resource
//     pattern, so we would otherwise need a converter
//     table either way.
//  2. The user_id on the link table is implicit (the
//     collectionRef is the users collection, and the
//     recordRef is the user row). Surfacing the
//     relationship as its own collection makes the
//     listing path (filter on user) and the deletion
//     path (cascade from the user) one query each.
//
// Fields:
//
//	user         (relation → users, single, required,
//	              CascadeDelete) — the user that owns the
//	              link. Deleting the user hard-deletes
//	              every link row in the same transaction.
//	idp          (relation → identity_providers, single,
//	              required, CascadeDelete) — the IdP the
//	              user linked through. Deleting the IdP
//	              revokes every link through it, matching
//	              the `identity_providers` migration's
//	              own cascade behaviour.
//	provider_name (text, max 64, required) — the
//	              auth.Providers registry key
//	              (pbauth.NameGoogle etc.). The SPA
//	              doesn't read this field, but the Connect
//	              handler needs it to look up the OAuth
//	              userinfo mapping when a future
//	              "refresh link" RPC lands. Storing the
//	              resolved key here keeps the row
//	              self-describing.
//	provider_id  (text, max 256, required, indexed) —
//	              the OAuth provider's user id. Indexed
//	              because the find-or-create path on
//	              future sign-in uses it as a lookup key.
//	extern_uid   (text, max 256) — the human-friendly
//	              external identifier (typically the IdP
//	              username or email). Surfaced to the SPA
//	              as `LinkedIdentity.extern_uid` so the
//	              settings page can show "linked as
//	              alice@example.com".
//	created      (autodate, onCreate)
//
// A unique index on (user, idp) keeps the link table
// idempotent: a second Link call for the same IdP fails
// with a constraint violation that the handler maps to
// a clean AlreadyExists Connect error.
//
// Access rules: server-only on every verb. The Connect
// handler is the sole authority; the *Rule fields are
// nil so PocketBase's superuser privilege is the only
// path that can mutate the table from the collection REST
// API.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		idps, err := txApp.FindCollectionByNameOrId("identity_providers")
		if err != nil {
			return err
		}
		if existing, err := txApp.FindCollectionByNameOrId("linked_identities"); err == nil && existing != nil {
			return nil
		}

		c := core.NewBaseCollection("linked_identities")

		c.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		c.Fields.Add(&core.RelationField{
			Name:          "idp",
			CollectionId:  idps.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		c.Fields.Add(&core.TextField{
			Name:     "provider_name",
			Required: true,
			Max:      64,
		})
		c.Fields.Add(&core.TextField{
			Name:     "provider_id",
			Required: true,
			Max:      256,
		})
		c.Fields.Add(&core.TextField{
			Name: "extern_uid",
			Max:  256,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		c.AddIndex("idx_linked_identities_user", false, "user", "")
		c.AddIndex("idx_linked_identities_provider_id", false, "provider_id", "")
		c.AddIndex("idx_linked_identities_user_idp", true, "user,idp", "")

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("linked_identities")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}
