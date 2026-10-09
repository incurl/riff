package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// addPasswordHash extends the `users` collection with a
// `password_hash` text field. PocketBase v0.40's internal
// `password` field is hashed with PBKDF2 and not directly
// comparable from Go code; we add a parallel bcrypt hash on
// the user record so the Connect SignIn handler can verify a
// password without a self-call to PocketBase's auth endpoint.
//
// SignIn flow:
//  1. Look up user by username/email.
//  2. Compare the supplied password against the bcrypt
//     `password_hash` field.
//  3. On match, issue a JWT via NewAuthToken (PocketBase's
//     standard path; the JWT is the same shape the Memos
//     frontend already accepts).
//
// On user create, the Connect handler must also populate this
// field. See pb/connect/user.go for the create-user flow.
func init() {
	migrations.Register(func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		if users.Fields.GetByName("password_hash") != nil {
			return nil // already added
		}
		users.Fields.Add(&core.TextField{
			Name: "password_hash",
			Max:  200,
		})
		return txApp.Save(users)
	}, func(txApp core.App) error {
		users, err := txApp.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		users.Fields.RemoveByName("password_hash")
		return txApp.Save(users)
	})
}
