package migrations

import (
	"path/filepath"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

// migratePatchesAndSamplesToAttachments copies every existing
// `patches` and `samples` row into the new `attachments` collection
// that the previous migration just created. The migration is
// idempotent: re-running it on a partially-migrated instance is
// safe (each copy is gated on whether an `attachments` row with
// the same `id` already exists).
//
// The copy preserves the per-row metadata that the new
// collection cares about:
//
//   - memo                      (relation → memos, identical id)
//   - blob                      (the file, re-mounted via the
//     filesystem's GetReuploadableFile so we never load the
//     bytes into memory)
//   - filename, size            (derived from the file metadata)
//   - storage                   (set to "local")
//   - description, device_fingerprint_snapshot, compatible_po_version,
//     motion_media, media_metadata, remote_url
//     (lifted verbatim from the old row; PO-33-only fields)
//
// Multi-file sample packs: each filename in a `samples.files` slice
// becomes its own `attachments` row, with the per-row metadata
// (description, device_fingerprint_snapshot) shared. The proto
// surface already models one file per attachment, so this matches
// the wire format a generic Memos client expects.
//
// On rollback (the second function passed to Register) the
// migration does not undo the data copy. The destructive step
// (dropping the old collections) is in 1850000002 and has its
// own rollback that recreates the empty collections.
func init() {
	migrations.Register(func(txApp core.App) error {
		// The new collection must exist before we can copy.
		// If it doesn't, the previous migration's idempotency
		// guard is masking a real failure — surface it.
		attachments, err := txApp.FindCollectionByNameOrId("attachments")
		if err != nil {
			return err
		}

		fsys, err := txApp.NewFilesystem()
		if err != nil {
			return err
		}
		defer func() { _ = fsys.Close() }()

		// --- patches: single `file` field, one row per patch ---
		if _, err := txApp.FindCollectionByNameOrId("patches"); err == nil {
			patches, err := txApp.FindRecordsByFilter("patches", "", "", 0, 0)
			if err != nil {
				return err
			}
			for _, p := range patches {
				if err := copyAttachmentRow(txApp, fsys, attachments, p, p.GetStringSlice("file")); err != nil {
					return err
				}
			}
		}

		// --- samples: multi `files` field, one row per file ---
		if _, err := txApp.FindCollectionByNameOrId("samples"); err == nil {
			samples, err := txApp.FindRecordsByFilter("samples", "", "", 0, 0)
			if err != nil {
				return err
			}
			for _, s := range samples {
				files := s.GetStringSlice("files")
				if err := copyAttachmentRow(txApp, fsys, attachments, s, files); err != nil {
					return err
				}
			}
		}

		return nil
	}, func(txApp core.App) error {
		// Rollback: delete every `attachments` row that has
		// a `device_fingerprint_snapshot` or
		// `compatible_po_version` set, i.e. the migrated
		// PO-33 rows. Non-PO-33 rows (if any) stay. The
		// `patches` / `samples` collections are not
		// restored — the previous migration's rollback
		// drops the `attachments` collection entirely, so
		// any data is gone. The destructive step is in
		// 1850000002 and is a separate transaction.
		if _, err := txApp.FindCollectionByNameOrId("attachments"); err != nil {
			return nil
		}
		rows, err := txApp.FindRecordsByFilter("attachments",
			`device_fingerprint_snapshot != "" || compatible_po_version != ""`,
			"", 0, 0)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := txApp.Delete(r); err != nil {
				return err
			}
		}
		return nil
	})
}

// copyAttachmentRow copies a single row from `patches` or
// `samples` into the `attachments` collection. `filenames` is
// the slice of stored filenames on the source row's FileField.
// If the slice is non-empty, each filename becomes its own
// `attachments` row; if the slice is empty, no row is written
// (the source row is metadata-only and the new collection has
// nowhere to carry it).
//
// The copy is idempotent: if an `attachments` row with the same
// id as the source row already exists, the whole batch is
// treated as already-copied. The first file in a multi-file
// sample pack reuses the source id (so any external URL
// pointing at `attachments/{src.id}` keeps working); the rest
// get fresh PocketBase-generated ids.
func copyAttachmentRow(txApp core.App, fsys *filesystem.System, dest *core.Collection, src *core.Record, filenames []string) error {
	if len(filenames) == 0 {
		return nil
	}
	// Idempotency: skip if an `attachments` row with the
	// same id already exists. The first file reuses the
	// source id, so this is the per-batch check.
	if existing, err := txApp.FindRecordById("attachments", src.Id); err == nil && existing != nil {
		return nil
	}

	for i, filename := range filenames {
		if filename == "" {
			continue
		}
		// Re-mount the source file onto the new record.
		// GetReuploadableFile reads the file attributes
		// (size, mime, etag) and constructs a *File that
		// the destination FileField's interceptor can
		// persist; we never touch the bytes in Go. The
		// preserveName=true keeps the on-disk filename so
		// the on-disk path is identical to the old layout
		// (the new collection id differs from the old
		// one, but the filename inside the record stays
		// stable).
		key := filepath.ToSlash(filepath.Join(src.Collection().Id, src.Id, filename))
		f, err := fsys.GetReuploadableFile(key, true)
		if err != nil {
			return err
		}

		dst := core.NewRecord(dest)
		if i == 0 {
			// First file reuses the source id so
			// any URL pointing at
			// `attachments/{src.id}` keeps
			// working.
			dst.Id = src.Id
		}
		if memo := src.GetString("memo"); memo != "" {
			dst.Set("memo", memo)
		}
		dst.Set("filename", filename)
		if v := src.GetString("description"); v != "" {
			dst.Set("description", v)
		}
		// size is informational; readers may rely on it.
		// GetReuploadableFile populates File.Size for us.
		dst.Set("size", f.Size)
		dst.Set("storage", "local")
		if v := src.GetString("device_fingerprint_snapshot"); v != "" {
			dst.Set("device_fingerprint_snapshot", v)
		}
		if v := src.GetString("compatible_po_version"); v != "" {
			dst.Set("compatible_po_version", v)
		}
		if v := src.GetString("motion_media"); v != "" {
			dst.Set("motion_media", v)
		}
		if v := src.GetString("media_metadata"); v != "" {
			dst.Set("media_metadata", v)
		}
		if v := src.GetString("remote_url"); v != "" {
			dst.Set("remote_url", v)
		}

		dst.Set("blob", f)

		if err := txApp.Save(dst); err != nil {
			return err
		}
	}
	return nil
}
