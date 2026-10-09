package connect

import (
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// recordToUserWebhook converts a user_webhooks row
// to the v1.UserWebhook proto. The caller supplies
// the owner's username; the row's `user` column is
// the PocketBase id, but the proto's resource name
// uses the username (so it is stable across
// renames). Handlers always have the user record in
// hand (they just looked it up to authorise the
// call) so we don't pay for a second query here.
//
// The signing secret is read from the row but is
// never returned on the read path; the only RPC
// that surfaces it is GetUserWebhookSigningSecret,
// which reads the column directly via the dedicated
// handler.
//
// `signing_secret_set` is derived from the column's
// emptiness rather than stored explicitly: the
// proto requires an OUTPUT_ONLY boolean, and the
// column carries the only source of truth. The
// boolean is always in lockstep with the column.
func recordToUserWebhookWithUsername(rec *core.Record, username string) *v1.UserWebhook {
	if rec == nil {
		return nil
	}
	out := &v1.UserWebhook{
		Name:             userWebhookName(username, rec.Id),
		Url:              rec.GetString("url"),
		DisplayName:      rec.GetString("display_name"),
		SigningSecretSet: rec.GetString("signing_secret") != "",
	}
	if created := rec.GetDateTime("created"); !created.IsZero() {
		out.CreateTime = timestamppb.New(created.Time())
	}
	if updated := rec.GetDateTime("updated"); !updated.IsZero() {
		out.UpdateTime = timestamppb.New(updated.Time())
	}
	return out
}

// recordToUserWebhookSigningSecret reads the raw
// signing-secret column and returns the plaintext. It
// returns "" if the row has no secret configured, which
// the handler surfaces as InvalidArgument (you can't
// "get" something that was never set). Callers MUST go
// through the authorisation path (canManageUserWebhook)
// before calling this.
func recordToUserWebhookSigningSecret(rec *core.Record) string {
	if rec == nil {
		return ""
	}
	return rec.GetString("signing_secret")
}

// userWebhookName returns the canonical resource name
// for a webhook row. The path mirrors the proto's
// `users/{user}/webhooks/{webhook}` shape, with the
// webhook segment being the row id (not the
// display_name) so the resource name is stable across
// renames.
func userWebhookName(username, id string) string {
	return "users/" + username + "/webhooks/" + id
}

// splitUserWebhookName parses
// "users/{username}/webhooks/{webhook}" into its parts.
// It returns ok=false for any malformed input —
// the caller surfaces that as InvalidArgument.
//
// The webhook segment is the row id; the function does
// not validate that it is a valid PocketBase id (15
// characters, lowercase alnum) because future
// migrations may change the id format and we don't
// want to fail closed on a rename.
func splitUserWebhookName(name string) (username, id string, ok bool) {
	// "users/{u}/webhooks/{w}" splits into 4 parts.
	parts := splitPath(name, '/')
	if len(parts) != 4 || parts[0] != "users" || parts[2] != "webhooks" {
		return "", "", false
	}
	if parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

// splitPath is a small wrapper around strings.Split
// that returns a fixed-size slice. The webhook-name
// parser uses it to keep the off-by-one checks
// readable.
func splitPath(s string, sep byte) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// canManageUserWebhook reports whether the caller
// can read, update, or delete a webhook row. A user
// can manage their own webhooks; an instance admin
// (PocketBase superuser) can manage any user's
// webhooks. The check is the single authority the
// handlers rely on; the migration's *Rule fields are
// nil so the rule engine never overrides it.
func canManageUserWebhook(viewer *core.Record, webhook *core.Record) bool {
	if viewer == nil || webhook == nil {
		return false
	}
	if viewer.IsSuperuser() {
		return true
	}
	return viewer.Id == webhook.GetString("user")
}
