// file_thumbnail.go generates the downscaled JPEG that the SPA asks
// for with `?thumbnail=true`.
//
// The upstream Memos server generates a 600px-box thumbnail and
// caches it on disk. riff had no thumbnail support at all: the
// query parameter was accepted by the SPA and ignored by the
// server, so every image in a feed was downloaded at full size.
//
// The implementation is deliberately small:
//
//   - the box is 600px on the long edge (upstream's number), so a
//     feed of photos does not pull megabytes per tile;
//   - the output is always JPEG, which every browser renders and
//     which drops the EXIF payload that `imaging` does not carry
//     over — a bonus privacy win over serving the original;
//   - the result is cached in the PocketBase filesystem under a
//     dedicated key prefix, so the second request for the same
//     attachment is a plain file read.
//
// The cached file is keyed by the *content* of the original (its
// size and the record's updated timestamp), not by the attachment
// id alone, so replacing an attachment's bytes invalidates the
// cache automatically.
package api

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/imagelimit"
)

// thumbnailMaxSize is the maximum width and height of a generated
// thumbnail, in pixels. It matches the upstream Memos server's
// box so the two produce visually equivalent feeds.
const thumbnailMaxSize = 600

// thumbnailCachePrefix is the PocketBase filesystem key prefix
// under which generated thumbnails are stored. The prefix is
// outside every collection id, so it cannot collide with an
// uploaded file.
const thumbnailCachePrefix = ".riff_thumbnails"

// thumbnailableContentTypes are the image content types the
// generator can decode. The list matches the formats `imaging`
// supports; anything else falls back to the original bytes.
//
// A content type is only a hint — the bytes are what actually get
// decoded — so an SVG or a mislabelled file simply fails the
// decode and the caller falls back to the original.
var thumbnailableContentTypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/gif":  {},
	"image/bmp":  {},
	"image/tiff": {},
}

// isThumbnailable reports whether the content type is one the
// thumbnail generator handles.
func isThumbnailable(contentType string) bool {
	_, ok := thumbnailableContentTypes[contentType]
	return ok
}

// thumbnailKey composes the filesystem key for an attachment's
// cached thumbnail.
//
// The key embeds a short digest of the source file's identity —
// the record id, the filename, the recorded size, and the record's
// update timestamp — so replacing an attachment's bytes
// invalidates the cache automatically. The digest is truncated to
// 16 hex characters so the key stays readable in a directory
// listing.
func thumbnailKey(rec *core.Record, filename string, size int64) string {
	updated := rec.GetDateTime("updated")
	stamp := ""
	if !updated.IsZero() {
		stamp = updated.Time().UTC().Format("20060102150405")
	}
	digest := fileEtagFor([]byte(rec.Id + "|" + filename + "|" + fmt.Sprint(size) + "|" + stamp))
	// fileEtagFor returns a quoted hex string; strip the quotes and
	// keep the first 16 hex characters.
	trimmed := strings.Trim(digest, `"`)
	if len(trimmed) > 16 {
		trimmed = trimmed[:16]
	}
	return filepath.ToSlash(filepath.Join(thumbnailCachePrefix, rec.Id+"-"+trimmed+".jpg"))
}

// generateThumbnail produces the downscaled JPEG for an
// attachment.
//
// The original bytes are read into memory (the `blob` field caps
// uploads at 32 MB), decoded with an orientation correction, and
// resized to fit the 600px box. The decoded size is checked
// against internal/imagelimit before the full decode, so a
// decompression bomb is refused rather than allocated.
//
// A nil result means "no thumbnail could be produced"; the caller
// serves the original bytes instead.
func generateThumbnail(app core.App, rec *core.Record, filename string) []byte {
	fs, err := app.NewFilesystem()
	if err != nil {
		return nil
	}
	defer fs.Close()

	reader, err := fs.GetReader(attachmentFileKey(rec, filename))
	if err != nil {
		return nil
	}
	defer reader.Close()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil
	}

	// Refuse absurd dimensions before paying for the decode. The
	// check only reads the header, so a 32 MB file with a
	// 100000x100000 header costs a few kilobytes to reject.
	if err := imagelimit.CheckReader(bytes.NewReader(raw)); err != nil {
		slog.Warn("thumbnail: image too large to decode", "record", rec.Id, "err", err.Error())
		return nil
	}

	src, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return nil
	}

	// Only downscale. An image already smaller than the box is
	// re-encoded as-is so the caller still gets a JPEG (and loses
	// the EXIF payload), but it is not enlarged.
	thumb := imaging.Fit(src, thumbnailMaxSize, thumbnailMaxSize, imaging.Lanczos)
	if thumb == nil {
		return nil
	}

	var out bytes.Buffer
	if err := imaging.Encode(&out, thumb, imaging.JPEG); err != nil {
		return nil
	}
	return out.Bytes()
}

// cachedThumbnail reads a previously generated thumbnail from the
// PocketBase filesystem. A nil result means "not cached yet".
func cachedThumbnail(app core.App, key string) []byte {
	fs, err := app.NewFilesystem()
	if err != nil {
		return nil
	}
	defer fs.Close()

	reader, err := fs.GetReader(key)
	if err != nil {
		return nil
	}
	defer reader.Close()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil
	}
	return raw
}

// storeThumbnail writes a generated thumbnail into the PocketBase
// filesystem cache.
//
// The write is best-effort: a failure means the next request
// regenerates the thumbnail, which is slower but not wrong. The
// error is logged rather than returned so a read-only or full
// disk degrades to "no cache" instead of "no images".
func storeThumbnail(app core.App, key string, payload []byte) {
	fs, err := app.NewFilesystem()
	if err != nil {
		slog.Warn("thumbnail: cannot open filesystem", "err", err.Error())
		return
	}
	defer fs.Close()

	if err := fs.Upload(payload, key); err != nil {
		slog.Warn("thumbnail: cannot cache thumbnail", "key", key, "err", err.Error())
	}
}

// thumbnailFor returns the cached thumbnail for an attachment,
// generating and caching it on a miss.
//
// The nil result means "this attachment has no usable thumbnail";
// the caller serves the original bytes.
func thumbnailFor(app core.App, rec *core.Record, filename string, contentType string) []byte {
	if !isThumbnailable(contentType) {
		return nil
	}

	// The `size` column is written by the upload pipeline from the
	// number of bytes received, so it identifies the source file
	// without a second filesystem read.
	key := thumbnailKey(rec, filename, int64(rec.GetInt("size")))
	if cached := cachedThumbnail(app, key); cached != nil {
		return cached
	}

	generated := generateThumbnail(app, rec, filename)
	if generated == nil {
		return nil
	}
	storeThumbnail(app, key, generated)
	return generated
}
