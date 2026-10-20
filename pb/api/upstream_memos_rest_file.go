package api

import (
	"errors"

	"github.com/pocketbase/pocketbase/core"
)

// fileServerHandler returns a handler that serves one of
// the three upstream /file/... routes. The builder key
// selects the kind of file: "attachment" reads an
// `attachments` row by UID, "user_avatar" reads a users
// row by username or email.
//
// The upstream /memos/api/file/... family and the public
// /file/... family are the same surface with a different
// prefix, so both dispatch to the handlers in
// file_routes.go. Sharing the handler is what keeps the two
// surfaces honest: before this file existed, the upstream
// family served any attachment to any caller with no
// authorization at all, while the public family did not
// exist. Both now go through authorizeAttachment.
func fileServerHandler(app core.App, builder string) func(*core.RequestEvent) error {
	switch builder {
	case "attachment":
		return attachmentFileHandler(app)
	case "user_avatar":
		return userAvatarFileHandler(app)
	default:
		return unknownFlavorHandler("file_server:" + builder)
	}
}

// findAttachmentRecord returns the row in the unified
// `attachments` collection whose id matches uid. The
// one-collection lookup matches the 1850xxx schema
// simplification; pre-cutover code that split the lookup
// across `patches`/`samples` is no longer needed.
func findAttachmentRecord(app core.App, uid string) (*core.Record, error) {
	rec, err := app.FindRecordById("attachments", uid)
	if err != nil {
		return nil, errors.New("attachment not found")
	}
	return rec, nil
}

// findUserByIdentifier resolves a user by username, email,
// or id. The lookup order matches the upstream Memos
// avatar route.
func findUserByIdentifier(app core.App, identifier string) (*core.Record, error) {
	rec, err := app.FindRecordById("users", identifier)
	if err == nil {
		return rec, nil
	}
	rec, err = app.FindFirstRecordByFilter("users",
		"username = {:u} || LOWER(username) = LOWER({:u})", map[string]any{"u": identifier})
	if err == nil {
		return rec, nil
	}
	rec, err = app.FindFirstRecordByFilter("users",
		"email = {:e} || LOWER(email) = LOWER({:e})", map[string]any{"e": identifier})
	if err == nil {
		return rec, nil
	}
	return nil, errors.New("user not found")
}
