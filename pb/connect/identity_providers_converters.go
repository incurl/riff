package connect

import (
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// recordToIdentityProvider converts a row in the
// `identity_providers` collection into its proto shape.
//
// The `config` JSON column is the proto
// `IdentityProviderConfig` oneof serialised via
// protojson — the read path round-trips that byte-for-byte
// and then `maskIdentityProviderSecret` strips the
// `client_secret` so it never leaves the server. The proto
// marks the field INPUT_ONLY for the CreateIdentityProvider
// and UpdateIdentityProvider RPCs; admin reveal goes
// through `GetIdentityProviderClientSecret`, which calls
// `extractIdentityProviderSecret` to recover the unmasked
// value.
func recordToIdentityProvider(rec *core.Record) *v1.IdentityProvider {
	if rec == nil {
		return nil
	}
	cfg := decodeIdentityProviderConfig(rec.GetString("config"))
	maskIdentityProviderSecret(cfg)
	out := &v1.IdentityProvider{
		Name:             "identity-providers/" + rec.GetString("name"),
		Title:            rec.GetString("title"),
		IdentifierFilter: rec.GetString("identifier_filter"),
		Config:           cfg,
	}
	switch rec.GetString("type") {
	case "OAUTH2":
		out.Type = v1.IdentityProvider_OAUTH2
	default:
		// TYPE_UNSPECIFIED — the proto zero value signals
		// "we don't know what this is" to the SPA.
		out.Type = v1.IdentityProvider_TYPE_UNSPECIFIED
	}
	return out
}

// maskIdentityProviderSecret zeroes the OAuth2
// `client_secret` on the supplied config in place. Other
// fields (auth_url, scopes, field_mapping, etc.) are
// preserved. The helper tolerates a nil config and a
// config whose oneof is unset.
func maskIdentityProviderSecret(cfg *v1.IdentityProviderConfig) {
	if cfg == nil {
		return
	}
	oc := cfg.GetOauth2Config()
	if oc == nil {
		return
	}
	oc.ClientSecret = ""
}

// extractIdentityProviderSecret returns the OAuth2
// `client_secret` from the supplied config, or the empty
// string when the config has no OAuth2 oneof populated.
// The function does not allocate or copy — the caller is
// the trusted admin-reveal path.
func extractIdentityProviderSecret(cfg *v1.IdentityProviderConfig) string {
	if cfg == nil {
		return ""
	}
	oc := cfg.GetOauth2Config()
	if oc == nil {
		return ""
	}
	return oc.GetClientSecret()
}

// decodeIdentityProviderConfig parses the JSON blob
// persisted in the `config` column into the proto oneof
// shape. An empty or malformed blob becomes a
// `TYPE_UNSPECIFIED`-shaped config; the SPA treats that as
// "no usable config" and refuses to render a sign-in
// button for the row.
func decodeIdentityProviderConfig(raw string) *v1.IdentityProviderConfig {
	if raw == "" {
		return &v1.IdentityProviderConfig{}
	}
	out := &v1.IdentityProviderConfig{}
	if err := protojson.Unmarshal([]byte(raw), out); err != nil {
		// A malformed config (e.g. truncated JSON) is a
		// configuration bug; we surface it as an empty
		// config rather than a 500 so the rest of the
		// admin UI stays usable.
		return &v1.IdentityProviderConfig{}
	}
	return out
}

// encodeIdentityProviderConfig serialises the proto oneof
// into a JSON string suitable for the `config` column. An
// entirely-empty config is persisted as "{}" so the read
// path never has to deal with NULL-vs-empty distinctions.
func encodeIdentityProviderConfig(cfg *v1.IdentityProviderConfig) (string, error) {
	if cfg == nil {
		cfg = &v1.IdentityProviderConfig{}
	}
	b, err := protojson.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
