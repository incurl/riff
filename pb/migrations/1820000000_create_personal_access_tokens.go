package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createPersonalAccessTokensCollection backs the three
// UserService.PersonalAccessToken RPCs declared in
// proto/api/v1/user_service.proto. One row per long-lived
// token a user has minted. The raw token value is never
// persisted: only a bcrypt hash of it lives in `token_hash`,
// and the raw value is returned to the caller exactly once
// (on Create).
//
// Why bcrypt and not the same plain TextField pattern as
// user_webhooks.signing_secret? Bcrypt is the only verification
// path that lets the Connect handler compare a presented
// token against the stored value without ever round-tripping
// the secret. A plaintext column would let anyone with read
// access to the SQLite file impersonate the user. The
// webhook's signing secret is read-by-the-server (the drain
// goroutine signs outbound deliveries) and the server needs
// the raw bytes, so plain text is correct there; here the
// server only verifies, so a one-way hash is right.
//
// Fields:
//
//	user         (relation → users, single, required,
//	              CascadeDelete) — the user that owns the
//	              token. Deleting the user hard-deletes all
//	              of their tokens in the same transaction.
//	description  (text, max 200) — operator-supplied label.
//	token_hash   (text, max 256, indexed) — bcrypt(raw).
//	              The raw token is `memos_pat_<32 random
//	              base64url chars>` (24 bytes of entropy);
//	              the bcrypt cost is the framework default
//	              (10) so a token verify stays in the
//	              single-millisecond range.
//	created_at   (autodate, onCreate)
//	updated_at   (autodate, onUpdate)
//	last_used_at (date, optional) — written by the
//	              verifier; left null until the token is
//	              presented.
//	expires_at   (date, optional) — when set, the verifier
//	              rejects the token once `now > expires_at`.
//	              A zero value means "never expires" (the
//	              SPA offers the operator a "0 = never"
//	              picker).
//
// Access rules: server-only on every verb. The Connect
// handler is the sole authority for who can list / create /
// delete; the *Rule fields are nil so PocketBase's
// superuser privilege is the only path that can mutate the
// table from the collection REST API.
//
// The CascadeDelete on `user` mirrors the user_webhooks
// migration: deleting a user removes all of their tokens
// in the same transaction, so a stale token cannot outlive
// the user that minted it.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		if existing, err := txApp.FindCollectionByNameOrId("personal_access_tokens"); err == nil && existing != nil {
			return nil
		}

		c := core.NewBaseCollection("personal_access_tokens")

		c.Fields.Add(&core.RelationField{
			Name:          "user",
			CollectionId:  users.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true,
		})
		c.Fields.Add(&core.TextField{
			Name: "description",
			Max:  200,
		})
		c.Fields.Add(&core.TextField{
			Name:   "token_hash",
			Max:    256,
			Hidden: true,
		})
		c.Fields.Add(&core.DateField{
			Name: "last_used_at",
		})
		c.Fields.Add(&core.DateField{
			Name: "expires_at",
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created_at",
			OnCreate: true,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "updated_at",
			OnUpdate: true,
		})

		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		c.AddIndex("idx_pat_user", false, "user", "")
		c.AddIndex("idx_pat_token_hash", false, "token_hash", "")

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("personal_access_tokens")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}
