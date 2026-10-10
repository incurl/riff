package connect

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// recordToUser converts a PocketBase `users` record to the Memos
// `User` proto. The mapping is straightforward but has a few
// PO-33-specific notes:
//
//   - The proto's `name` field is "users/{username}". We use
//     username (not the record id) so the resource name is stable
//     across renames.
//   - `display_name` falls back to `name` (PocketBase's built-in
//     field) if `display_name` is empty.
//   - `avatar_url` is a relative path that the React client
//     resolves against the PocketBase base URL via the file token
//     system. We just expose the filename; the SDK knows how to
//     fetch it.
//   - `role` is USER for every `users` record. ADMIN maps to
//     PocketBase's separate superuser collection, so a `users`
//     record can never report ADMIN.
//   - `state` is NORMAL: the schema has no archive flag for users.
//   - `description` is backed by the `bio` field.
//
// `IgnoreEmailVisibility(true)` is required to expose the email
// field on auth records; without it, the field is hidden unless
// the viewer is the record's owner.
func recordToUser(rec *core.Record) *v1.User {
	if rec == nil {
		return nil
	}
	rec.IgnoreEmailVisibility(true)

	displayName := rec.GetString("display_name")
	if displayName == "" {
		displayName = rec.GetString("name")
	}

	role := v1.User_USER
	if rec.IsSuperuser() {
		role = v1.User_ADMIN
	}

	username := rec.GetString("username")
	if username == "" {
		// Superuser records have no `username` column; the
		// admin's identity lives in `email`. The SPA's resource
		// name still wants `users/{name}` so the same record
		// name works for both collections.
		username = rec.GetString("email")
	}
	avatar := rec.GetString("avatar")
	// PocketBase stores file fields as just the filename;
	// the client SDK knows how to fetch it. We expose a
	// stable URL shape that the React layer can resolve.
	var avatarURL string
	if avatar != "" {
		avatarURL = "/api/files/users/" + rec.Id + "/" + avatar
	}

	return &v1.User{
		Name:        "users/" + username,
		Role:        role,
		Username:    username,
		Email:       rec.GetString("email"),
		DisplayName: displayName,
		AvatarUrl:   avatarURL,
		Description: rec.GetString("bio"),
		State:       v1.State_NORMAL,
		CreateTime:  timestampToProto(rec.GetDateTime("created")),
		UpdateTime:  timestampToProto(rec.GetDateTime("updated")),
	}
}

// userToRecord applies the profile fields of a v1.User to a *core.Record. Only
// the fields that exist in our schema are set; unknown fields are ignored.
//
// `password` is deliberately not handled here: PocketBase's own password field
// and the parallel `password_hash` column the Connect sign-in path reads have
// to move together, so passwords go through setPassword.
func userToRecord(rec *core.Record, u *v1.User) {
	if u.Username != "" {
		rec.Set("username", u.Username)
	}
	if u.Email != "" {
		rec.Set("email", u.Email)
	}
	if u.DisplayName != "" {
		rec.Set("name", u.DisplayName)
	}
	if u.Description != "" {
		rec.Set("bio", u.Description)
	}
}

// applyUserFields copies the writable profile fields of a v1.User onto a fresh
// `users` record, rejecting the two fields riff cannot represent so the caller
// learns immediately instead of silently losing data:
//
//   - `avatar_url` is a URL, but the schema stores an uploaded file.
//   - the ADMIN role lives in the superuser collection, not in `users`.
func applyUserFields(rec *core.Record, u *v1.User) error {
	if u.GetRole() != v1.User_USER && u.GetRole() != v1.User_ROLE_UNSPECIFIED {
		return connect.NewError(connect.CodeFailedPrecondition,
			"user rows cannot be created as ADMIN; create a superuser account instead")
	}
	if u.GetAvatarUrl() != "" {
		return connect.NewError(connect.CodeInvalidArgument,
			"avatar_url cannot be set here; upload the avatar to the users collection instead")
	}

	userToRecord(rec, u)
	if u.GetPassword() != "" {
		return setPassword(rec, u.GetPassword())
	}
	return nil
}

// setPassword writes both password fields. PocketBase hashes `password` itself
// and uses it for the collection auth endpoints and record validation;
// `password_hash` is the bcrypt digest the Connect SignIn path verifies (see
// AuthServer.verifyPassword). Keeping them in lockstep is what lets an account
// created or changed over Connect still work against PocketBase's own auth API.
func setPassword(rec *core.Record, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return connect.NewError(connect.CodeInternal, "password hashing failed")
	}
	rec.Set("password", password)
	rec.Set("password_hash", string(hash))
	return nil
}

// createSuperuserRecord provisions a new record in the `_superusers`
// collection. It is the bootstrap-only path: the caller is responsible
// for asserting that the instance still needs setup (no real admin
// exists) and that the request was unauthenticated.
//
// The submitted `username` becomes the superuser's `email`, matching
// how the Memos SPA's sign-up form treats the "username" field as the
// admin's identity on the bootstrap path (PocketBase's superuser
// collection has no `username` column, only `email`). A
// `password_hash` column is written alongside the built-in
// `password` field for the same reason it is written on `users` rows
// (see migration 1790000000): the Connect SignIn path bcrypt-verifies
// directly, and PB's PBKDF2 hash is not directly comparable from Go.
//
// `displayName` is optional; an empty string leaves the field unset
// and PocketBase will store the empty string.
//
// A fresh `superusers` record's `IsSuperuser()` is true, so
// `recordToUser` will surface role=ADMIN to the SPA without further
// translation.
func createSuperuserRecord(ctx context.Context, app core.App, username, password, displayName string) (*core.Record, error) {
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "username is required")
	}
	if password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "password is required")
	}
	superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(superusers)
	rec.Set("email", username)
	if displayName != "" {
		rec.Set("name", displayName)
	}
	if err := setPassword(rec, password); err != nil {
		return nil, err
	}
	if err := createRecordRequest(ctx, app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return rec, nil
}

// userHasContent reports whether anything in the schema still points at the
// user. Those relations all lack a cascade, so the row cannot be deleted while
// they exist.
func userHasContent(app core.App, userID string) (bool, error) {
	checks := []struct{ collection, filter string }{
		{"memos", "author={:id}"},
		{"spaces", "owner={:id}"},
		{"space_members", "user={:id}"},
		{"reactions", "user={:id}"},
	}
	for _, c := range checks {
		rows, err := app.FindRecordsByFilter(c.collection, c.filter, "", 1, 0, map[string]any{"id": userID})
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// purgeUserContent unwinds everything a user owns so the account can be removed
// under `force`.
//
// Every delete goes through the record-request bridge, so the memo and space
// cascades run and the last-ADMIN invariant still applies: a user who is the
// last active ADMIN of a space cannot be force-deleted until they hand the role
// on, which surfaces as an error rather than a silent half-deletion.
func purgeUserContent(ctx context.Context, app core.App, userID string) error {
	deletions := []struct{ collection, filter string }{
		// Memos first: their cascade clears patches, samples, reactions
		// and relations.
		{"memos", "author={:id}"},
		// Owned spaces next; the space cascade removes that space's memos
		// and memberships.
		{"spaces", "owner={:id}"},
		// Reactions left on other people's memos.
		{"reactions", "user={:id}"},
		// Memberships in other people's spaces.
		{"space_members", "user={:id}"},
	}
	for _, d := range deletions {
		rows, err := app.FindRecordsByFilter(d.collection, d.filter, "", 0, 0, map[string]any{"id": userID})
		if err != nil {
			return err
		}
		for _, rec := range rows {
			if err := deleteRecordRequest(ctx, app, rec); err != nil {
				return err
			}
		}
	}
	return nil
}
