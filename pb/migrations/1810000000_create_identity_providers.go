package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createIdentityProvidersCollection backs the five
// IdentityProviderService RPCs declared in
// proto/api/v1/idp_service.proto (List / Get / Create /
// Update / Delete). One row per OAuth2 identity provider an
// instance admin has configured. The Connect handler is the
// sole authority for who can read and mutate the table; the
// `*Rule` fields are nil so the PocketBase rule engine cannot
// be tricked into letting a non-admin through the collection
// REST surface.
//
// Why a separate collection instead of leaning on the
// `users.OAuth2.Providers` shape that PocketBase already
// exposes?
//
//   - The Memos Connect contract is a CRUD API over a
//     first-class resource with its own name, type, and
//     config blob. Folding it into the auth collection's
//     meta payload would force the SPA to model the
//     "providers" list as a side-channel of the auth
//     collection's options, and we would lose the resource
//     name (identity-providers/{idp}) the SPA's OpenAPI
//     client types are bound to.
//   - We want provider-row lifecycle events (audit logs,
//     future webhook triggers) to ride on the record hook
//     chain the same way every other riff resource does.
//   - The actual code-exchange at sign-in time still goes
//     through PocketBase's `auth.Providers` registry and
//     the `_externalAuths` link table — we are not
//     re-implementing OAuth2 here, only managing the
//     configuration rows. See `pb/connect/auth.go` for the
//     SignIn sso_credentials branch.
//
// Fields:
//
//	name             (text, PK id) — the slug the resource
//	                  name `identity-providers/{idp}` resolves
//	                  to. Pattern: ^[a-zA-Z0-9]([a-zA-Z0-9_-]{0,34}[a-zA-Z0-9])?$.
//	type             (select) — the OAuth2 protocol family.
//	                  Today only OAUTH2 is supported; the
//	                  proto enum has a TYPE_UNSPECIFIED zero
//	                  value we use as a sentinel on read.
//	title            (text, required, max 100) — the human
//	                  label shown on the SPA's sign-in button.
//	identifier_filter (text, max 200) — optional glob/regex
//	                  applied to the IdP-provided identifier
//	                  (typically the email). When non-empty the
//	                  SignIn handler refuses to bind a user
//	                  whose identifier does not match, so a
//	                  single Google Workspace tenant cannot
//	                  accidentally authorise personal Gmail
//	                  accounts. See `pb/connect/auth.go` for
//	                  the parser.
//	config           (json) — the OAuth2Config blob
//	                  (client_id, client_secret, auth_url,
//	                  token_url, user_info_url, scopes,
//	                  field_mapping). Stored as JSON so the
//	                  proto IdentityProviderConfig oneof
//	                  round-trips 1:1; the converter in
//	                  `pb/connect/identity_providers_converters.go`
//	                  is the only caller. `client_secret` is
//	                  never returned on read (the converter
//	                  masks it before serialising), matching
//	                  the proto's INPUT_ONLY convention.
//	created          (autodate, onCreate)
//	updated          (autodate, onUpdate)
//
// Access rules: nil on every verb. The Connect handler is the
// sole authority.
func init() {
	migrations.Register(func(txApp core.App) error {
		if existing, err := txApp.FindCollectionByNameOrId("identity_providers"); err == nil && existing != nil {
			return nil
		}

		c := core.NewBaseCollection("identity_providers")

		c.Fields.Add(&core.TextField{
			Name:     "name",
			System:   true,
			Required: true,
			Min:      1,
			Max:      36,
			Pattern:  `^[a-zA-Z0-9]([a-zA-Z0-9_-]{0,34}[a-zA-Z0-9])?$`,
		})
		c.Fields.Add(&core.SelectField{
			Name:      "type",
			Required:  true,
			Values:    []string{"OAUTH2"},
			MaxSelect: 1,
		})
		c.Fields.Add(&core.TextField{
			Name:     "title",
			System:   false,
			Required: true,
			Max:      100,
		})
		c.Fields.Add(&core.TextField{
			Name: "identifier_filter",
			Max:  200,
		})
		c.Fields.Add(&core.JSONField{
			Name:    "config",
			MaxSize: 8192,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnUpdate: true,
		})

		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("identity_providers")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}
