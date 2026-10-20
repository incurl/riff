package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// createAttachmentsCollection is the unified attachment table that
// replaces the previous `patches` + `samples` split. The split existed
// while riff was PO-33-only; upstream Memos stores every attachment in
// a single table. Aligning riff with that shape makes the storage layer
// byte-for-byte compatible with a stock Memos server, so generic Memos
// clients (MoeMemos, Web Clipper) see no difference between riff and
// upstream.
//
// The collection is named `attachments` to match the proto surface
// (the gRPC path is `attachments/{id}` and the Memos admin UI labels
// the table "Attachment"). The PO-33 features are kept as additive
// columns on the same row: `device_fingerprint_snapshot`,
// `compatible_po_version`, `motion_media`, and `media_metadata`.
// Memos clients ignore them; the riff admin UI and PO-33 device
// flow read them via plain column reads (no wire-format change).
// The `motion_media` JSON column is populated by the after-create
// hook in `pb/hooks/motion_media.go`, and the remote mirror URL is
// set by the storage bridge in `pb/hooks/storage_bridge.go`.
//
// Fields:
//
//	memo                        (relation → memos, single, required, cascade)
//	blob                        (file, single, 32 MB, permissive MIME list)
//	filename                    (text) — derived from the upload
//	mime_type                   (text) — derived from the upload
//	size                        (number) — derived from the upload
//	storage                     (text, default "local")
//	external_link               (text, optional)
//	description                 (text, optional)
//	device_fingerprint_snapshot (text, max 64, optional)
//	compatible_po_version       (text, max 16, optional)
//	motion_media                (text, max 8192, optional JSON)
//	media_metadata              (text, max 8192, optional JSON)
//	remote_url                  (text, max 2048, optional)
//
// The MIME list mirrors upstream Memos's permissive allow-list:
// images, audio, video, PDFs, plain text, common archives, octet-stream
// as a fallback. There is no spec-time MIME rejection in
// `pb/connect/attachment_upload.go` — PocketBase validates the field
// at Save time, and a 32 MB cap matches upstream's default. The
// previous split enforced a per-collection 8 MB / 32 MB cap; the
// new field uses a single 32 MB cap, which is the larger of the
// two and matches what upstream Memos uses.
//
// Access rules inherit from the parent memo (lifted from the
// previous `patches` access rules; `samples` used the same shape).
//
// Idempotency: the migration is a no-op when the collection already
// exists. The data migration in 1850000001 picks up where this one
// leaves off and runs only on the first apply (because it copies
// from `patches` and `samples`, which become empty after the
// follow-up drop in 1850000002).
func init() {
	migrations.Register(func(txApp core.App) error {
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		// Idempotency guard: if the collection already exists,
		// do nothing. A re-run of migrations (RunAllMigrations)
		// must be safe.
		if existing, _ := txApp.FindCollectionByNameOrId("attachments"); existing != nil {
			return nil
		}

		r := core.NewBaseCollection("attachments")

		r.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      false,
			MaxSelect:     1,
			MinSelect:     0,
			CascadeDelete: true, // deleting the parent memo removes its attachments
		})
		r.Fields.Add(&core.FileField{
			Name:      "blob",
			Required:  false,
			MaxSelect: 1,
			MaxSize:   32 * 1024 * 1024, // 32 MB
			// `Protected` makes PocketBase's native file
			// server (`GET /api/files/{collection}/{id}/{file}`)
			// evaluate the collection's `ViewRule` before it
			// serves the bytes. Without it the file is
			// downloadable by anyone who knows the URL, which
			// bypasses the owning memo's visibility. See
			// pb/migrations/1880000000_protect_attachment_blobs.go.
			Protected: true,
			MimeTypes: []string{
				// Images
				"image/png", "image/jpeg", "image/jpg", "image/gif",
				"image/webp", "image/svg+xml", "image/bmp", "image/tiff",
				"image/heic", "image/heif", "image/avif",
				// Audio
				"audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp3",
				"audio/aac", "audio/flac", "audio/ogg", "audio/m4a",
				"audio/webm", "audio/midi", "audio/x-midi",
				// Video
				"video/mp4", "video/webm", "video/ogg", "video/quicktime",
				// Documents
				"application/pdf", "text/plain", "text/html", "text/css",
				"text/javascript", "text/markdown", "text/csv",
				"application/json", "application/xml",
				// Archives
				"application/zip", "application/x-tar", "application/gzip",
				"application/x-7z-compressed", "application/x-rar-compressed",
				// Office
				"application/msword",
				"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"application/vnd.ms-excel",
				"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
				// Generic binary — the PO-33 device uploads patches
				// with this MIME.
				"application/octet-stream",
			},
		})
		r.Fields.Add(&core.TextField{
			Name:     "filename",
			Required: false,
			Max:      512,
		})
		r.Fields.Add(&core.TextField{
			Name:     "mime_type",
			Required: false,
			Max:      128,
		})
		r.Fields.Add(&core.NumberField{
			Name:     "size",
			Required: false,
		})
		r.Fields.Add(&core.TextField{
			Name:     "storage",
			Required: false,
			Max:      32,
		})
		r.Fields.Add(&core.TextField{
			Name:     "external_link",
			Required: false,
			Max:      2048,
		})
		r.Fields.Add(&core.TextField{
			Name:     "description",
			Required: false,
			Max:      500,
		})
		// PO-33 additive columns — populated by the device upload
		// path, the storage bridge, and the motion-photo detector.
		// Memos clients ignore them; the riff admin UI reads them.
		r.Fields.Add(&core.TextField{
			Name:     "device_fingerprint_snapshot",
			Required: false,
			Max:      64,
		})
		r.Fields.Add(&core.TextField{
			Name:     "compatible_po_version",
			Required: false,
			Max:      16,
		})
		r.Fields.Add(&core.TextField{
			Name:     "motion_media",
			Required: false,
			Max:      8192,
		})
		r.Fields.Add(&core.TextField{
			Name:     "media_metadata",
			Required: false,
			Max:      8192,
		})
		r.Fields.Add(&core.TextField{
			Name:     "remote_url",
			Required: false,
			Max:      2048,
		})
		// Auto-managed timestamps. The connect handlers
		// sort by `-created` (newest first) and read the
		// values back through GetDateTime("created"), so
		// the column is part of the wire contract, not a
		// presentational nicety. Defined here so the
		// `attachments` collection has the same shape as
		// every other rif collection from the moment of
		// its creation, regardless of whether the
		// 1720xxx timestamp migration has run against it.
		r.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		r.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnCreate: true,
			OnUpdate: true,
		})

		// Visibility inherits from parent memo. Same access rule
		// shape the previous `patches` collection used.
		visibilityRule := `memo.author = @request.auth.id` +
			` || memo.visibility = "PROTECTED"` +
			` || memo.visibility = "PUBLIC"` +
			` || (memo.visibility = "SPACE"` +
			`     && memo.space != null` +
			`     && memo.space.space_members_via_space.user.id ?= @request.auth.id` +
			`     && memo.space.space_members_via_space.status = "ACTIVE")`
		r.ListRule = types.Pointer(visibilityRule)
		r.ViewRule = types.Pointer(visibilityRule)
		r.CreateRule = types.Pointer("memo.author = @request.auth.id")
		r.UpdateRule = types.Pointer("memo.author = @request.auth.id")
		r.DeleteRule = types.Pointer("memo.author = @request.auth.id")

		// Indexes
		r.AddIndex("idx_attachments_memo", false, "memo", "")
		r.AddIndex("idx_attachments_created", false, "created", "")

		return txApp.Save(r)
	}, func(txApp core.App) error {
		r, err := txApp.FindCollectionByNameOrId("attachments")
		if err != nil {
			return nil // already gone
		}
		return txApp.Delete(r)
	})
}
