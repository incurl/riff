package uploads

import (
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The collection names the plugin owns.
//
// The plugin's tables are named after upstream's `fof/upload`, which is
// the extension upstream Flarum moved attachments to: upstream Flarum
// v2.0.0's core has NO attachment support at all — the `post_attachments`
// table and the `Post->upload()` relationship were removed from the
// core and live in `fof/upload` instead (`flarum/core: ^2.0.0`).
//
// The naming follows the plugin convention rather than upstream's
// `fof_upload_files`: a plugin-owned collection carries the
// `forum_ext_` prefix (see `repository.Registry::ExtensionPhysical`), so
// it cannot collide with the host's own tables.
const (
	// filesCollection holds the uploaded files.
	//
	// It is the plugin's equivalent of upstream's `fof_upload_files`
	// table, and it carries the same information: who uploaded the
	// file, which post it is attached to, its size and its type.
	filesCollection = "files"

	// The maximum size of an uploaded file, in bytes.
	//
	// It matches the memo attachment's limit (32 MiB — see
	// `pb/migrations/1850000000_create_attachments.go`), so a file that
	// the Memos side accepts is accepted here too.
	MaxFileSize = 32 * 1024 * 1024
)

// createFilesCollection creates the plugin's file collection.
//
// The collection is idempotent: it is created only when it is missing,
// so re-running the migration is safe.
//
// The access rules are deliberately permissive on read and closed on
// write: the frontend renders the files on a post, and every mutation
// goes through the plugin's handlers, which enforce the permission
// service and the post's own visibility.
//
// The `blob` field is `Protected`, which makes PocketBase's own file
// server evaluate the collection's ViewRule before it serves the bytes.
// The forum does not USE that server — the bytes are streamed through
// the plugin's own route, because PocketBase's `/api/files/...` URL
// lives at the root of the listener, which the forum does not own (the
// same reason the avatar route exists) — but the flag is the right
// default for a table whose rows may be private.
func createFilesCollection(app core.App, physical string) error {
	if existing, err := app.FindCollectionByNameOrId(physical); err == nil && existing != nil {
		return nil
	}

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return err
	}
	posts, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return err
	}

	collection := core.NewBaseCollection(physical)
	collection.Fields.Add(&core.RelationField{
		Name:          "actor",
		CollectionId:  users.Id,
		Required:      true,
		MaxSelect:     1,
		MinSelect:     1,
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.RelationField{
		Name:         "post",
		CollectionId: posts.Id,
		Required:     false,
		MaxSelect:    1,
		MinSelect:    0,
		// Deleting the post removes its files: upstream's
		// `fof_upload_file_posts` pivot cascades the same way.
		CascadeDelete: true,
	})
	collection.Fields.Add(&core.FileField{
		Name:      "blob",
		Required:  false,
		MaxSelect: 1,
		MaxSize:   MaxFileSize,
		Protected: true,
	})
	collection.Fields.Add(&core.TextField{
		Name: "filename",
		Max:  512,
	})
	collection.Fields.Add(&core.TextField{
		Name: "mime_type",
		Max:  128,
	})
	collection.Fields.Add(&core.NumberField{
		Name: "size",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "image_width",
	})
	collection.Fields.Add(&core.NumberField{
		Name: "image_height",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "hidden",
	})
	collection.Fields.Add(&core.BoolField{
		Name: "shared",
	})
	collection.Fields.Add(&core.AutodateField{
		Name:     "created_at",
		OnCreate: true,
	})

	collection.ListRule = types.Pointer("")
	collection.ViewRule = types.Pointer("")
	collection.CreateRule = nil
	collection.UpdateRule = nil
	collection.DeleteRule = nil

	collection.AddIndex("idx_"+physical+"_post", false, "post", "")
	collection.AddIndex("idx_"+physical+"_actor", false, "actor", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "uploads: could not create the %q collection", physical)
	}
	return nil
}

// ensureSharedField adds the `shared` column to an existing collection.
//
// It is a SEPARATE, idempotent migration step rather than a change to
// `createFilesCollection`, because a plugin migration that has already
// been recorded does NOT run again: an instance created before the shared
// library existed would never gain the column, and every read of it would
// silently report `false` — the shared listing would be empty forever.
//
// Upstream stores the same flag in the same shape (`fof_upload_files
// .shared`, added by `2023_12_04_000000_modify_fof_upload_files_add_shared_column.php`).
func ensureSharedField(app core.App, physical string) error {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		// No collection means the create step has not run; nothing to
		// repair, and creating it here would race the create step.
		return nil
	}

	if collection.Fields.GetByName("shared") != nil {
		return nil
	}

	collection.Fields.Add(&core.BoolField{Name: "shared"})
	collection.AddIndex("idx_"+physical+"_shared", false, "shared", "")

	if err := app.Save(collection); err != nil {
		return errors.Wrapf(err, "uploads: could not add the shared column to %q", physical)
	}
	return nil
}
