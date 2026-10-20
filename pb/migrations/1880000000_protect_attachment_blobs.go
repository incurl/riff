package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// protectAttachmentBlobs marks the `attachments.blob` file field
// as `Protected`.
//
// PocketBase's built-in file server (`GET
// /api/files/{collection}/{recordId}/{filename}`) only evaluates
// the collection's `ViewRule` when the file field is marked
// `Protected` (see `apis/file.go::download`, the
// `if fileField.Protected` branch). An unprotected file field is
// served to anybody who knows the URL, with no regard for the
// owning record's visibility.
//
// For the `attachments` collection that was a real leak: the
// `ViewRule` is the memo-visibility rule
// (`memo.author = @request.auth.id || memo.visibility =
// "PROTECTED" || memo.visibility = "PUBLIC" || ...`), but the
// unprotected `blob` field bypassed it entirely. A file attached
// to a PRIVATE memo was downloadable by an anonymous caller who
// had the id.
//
// Marking the field `Protected` closes the native route: the
// `ViewRule` is now evaluated, so a PRIVATE memo's attachment
// answers 404 for anyone but the author. A PUBLIC memo's
// attachment is still served by the native route (the rule
// passes for an anonymous caller), so nothing that worked
// before stops working.
//
// The riff-authored `/file/...` surface (see
// `pb/api/file_routes.go`) does its own authorization against the
// same `ViewRule` and additionally supports `?thumbnail=true`,
// `?motion=true`, and `?share_token=…`; it does not use
// PocketBase file tokens, so the `Protected` flag does not
// affect it.
//
// The migration is idempotent: it skips a collection whose
// `blob` field is already protected, and it skips a collection
// with no `blob` field at all (the `patches` / `samples`
// collections that predate the unified `attachments`
// collection do not have one).
func init() {
	migrations.Register(func(txApp core.App) error {
		col, err := txApp.FindCollectionByNameOrId("attachments")
		if err != nil {
			// The `attachments` collection does not exist
			// yet on a fresh install; the create
			// migration that introduces it already sets
			// `Protected: true`, so there is nothing to do.
			return nil
		}
		field := col.Fields.GetByName("blob")
		if field == nil {
			return nil
		}
		fileField, ok := field.(*core.FileField)
		if !ok || fileField.Protected {
			return nil
		}
		fileField.Protected = true
		return txApp.Save(col)
	}, func(txApp core.App) error {
		col, err := txApp.FindCollectionByNameOrId("attachments")
		if err != nil {
			return nil
		}
		field := col.Fields.GetByName("blob")
		if field == nil {
			return nil
		}
		fileField, ok := field.(*core.FileField)
		if !ok || !fileField.Protected {
			return nil
		}
		fileField.Protected = false
		_ = txApp.Save(col)
		return nil
	})
}
