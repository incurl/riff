// file_serve.go is the shared core of riff's public file surface.
//
// Two route families serve the same bytes:
//
//	/file/attachments/{uid}[/{filename}]              <- the SPA's URLs
//	/memos/api/file/attachments/{uid}[/{filename}]    <- the upstream-Memos REST surface
//
// The first family is what the SPA, the markdown renderer, and the
// share links emit (see web/src/utils/attachment.ts and
// web/src/utils/managed-attachment.ts). The second family is what
// the upstream-Memos REST table declares (see
// upstream_memos_rest.go). Before this file existed, only the
// second family was registered, so every SPA-emitted `/file/...`
// URL fell through to the SPA's catch-all route
// (server/frontend/embed.go) and answered 200 with index.html —
// a broken image in the browser rather than a 404.
//
// The handler also owns the three behaviours the SPA asks for
// through query parameters:
//
//	?thumbnail=true  — a downscaled JPEG (upstream's 600px box)
//	?motion=true     — the MP4 clip embedded in a Motion Photo
//	?share_token=…   — the bearer credential for a shared memo
//
// Authorization is enforced here rather than left to the record
// rules because a file download is not a collection read: the
// `attachments` collection's ViewRule is the *only* thing standing
// between a private memo's file and the public internet, and the
// pre-existing /memos/api/file/... handler did not consult it.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/motionphoto"
)

// fileContentTypeByExtension maps a filename extension to the
// Content-Type the file server sends. The table is deliberately
// narrower than the browser's registry: an unlisted extension
// falls back to application/octet-stream, which the browser
// downloads rather than renders. That is the safe default for
// user-supplied filenames (an attacker must not be able to upload
// an .html file and have it execute on the instance's origin).
var fileContentTypeByExtension = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".svg":  "image/svg+xml",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".flac": "audio/flac",
	".mid":  "audio/midi",
	".midi": "audio/midi",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".pdf":  "application/pdf",
	".json": "application/json",
	".txt":  "text/plain; charset=utf-8",
	".md":   "text/plain; charset=utf-8",
}

// inlineFileContentTypes are the content types the file server is
// willing to render in the browser. Everything else is served with
// `Content-Disposition: attachment` so the browser downloads it
// instead of executing it against the instance's origin.
var inlineFileContentTypes = map[string]struct{}{
	"image/png":       {},
	"image/jpeg":      {},
	"image/gif":       {},
	"image/webp":      {},
	"image/bmp":       {},
	"image/tiff":      {},
	"image/x-icon":    {},
	"video/webm":      {},
	"video/mp4":       {},
	"video/quicktime": {},
	"audio/mpeg":      {},
	"audio/mp3":       {},
	"audio/wav":       {},
	"audio/x-wav":     {},
	"audio/wave":      {},
	"audio/aac":       {},
	"audio/ogg":       {},
	"audio/midi":      {},
	"application/pdf": {},
}

// fileError writes a Memos-shape JSON error and
// returns nil so the PocketBase handler chain sees a
// completed response.
//
// The JSON shape (`{"code":5,"message":"…"}`) matches the
// upstream-Memos file server so a Memos client can parse the
// error body. The HTTP status is the one the upstream server
// would use for the same condition.
func fileError(e *core.RequestEvent, status int, message string) error {
	e.Response.Header().Set("Content-Type", "application/json")
	e.Response.WriteHeader(status)
	_, _ = io.WriteString(e.Response,
		fmt.Sprintf(`{"code":5,"message":%q}`, message))
	return nil
}

// fileNotFound writes the 404 the file server returns for an
// unknown attachment. It is a separate helper so every "this
// file does not exist (or you may not see it)" path produces the
// same response — the file server must not leak the existence of
// a file the caller is not allowed to read.
func fileNotFound(e *core.RequestEvent) error {
	return fileError(e, http.StatusNotFound, "attachment not found")
}

// sniffContentType returns a Content-Type for the file.
// It prefers the file extension's registered MIME
// type, falling back to application/octet-stream.
func sniffContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ct, ok := fileContentTypeByExtension[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}

// isInlineContentType reports whether the content type may be
// rendered by the browser. See inlineFileContentTypes.
func isInlineContentType(contentType string) bool {
	_, ok := inlineFileContentTypes[contentType]
	return ok
}

// attachmentFileKey composes the PocketBase filesystem key for an
// attachment's blob. PocketBase stores a FileField's bytes at
// <collectionId>/<recordId>/<filename>; the unified `attachments`
// collection has MaxSelect:1 on its `blob` field, so the simple
// form applies.
func attachmentFileKey(rec *core.Record, filename string) string {
	return filepath.ToSlash(filepath.Join(rec.Collection().Id, rec.Id, filename))
}

// streamRecordFile opens the named file on the
// record's primary filesystem and streams its bytes
// to the response. PocketBase stores file-field blobs
// at <collectionId>/<recordId>/<filename>; the unified
// `attachments` collection has MaxSelect:1 on its `blob`
// field, so the simple form applies. The Cache-Control
// is `private, no-store` to match the upstream Memos
// default; the Content-Type is sniffed from the file
// extension.
//
// A file that is not an inline-safe content type is served with
// `Content-Disposition: attachment` so the browser downloads it
// instead of executing it against the instance's origin. The
// filename is attacker-controlled, so an .html or .svg upload
// must never be rendered inline.
//
// The ETag is derived from the record id, the filename, and the
// blob size, so a conditional GET short-circuits without
// re-reading the file.
func streamRecordFile(e *core.RequestEvent, app core.App, rec *core.Record, filename string, public bool) error {
	fs, err := app.NewFilesystem()
	if err != nil {
		return fileError(e, http.StatusInternalServerError, "filesystem unavailable")
	}
	defer fs.Close()

	fileKey := attachmentFileKey(rec, filename)
	blob, err := fs.GetReader(fileKey)
	if err != nil {
		return fileNotFound(e)
	}
	defer blob.Close()

	contentType := sniffContentType(filename)
	// ETag from record id + filename + size so a
	// conditional GET short-circuits.
	hash := sha256.Sum256([]byte(rec.Id + filename + fmt.Sprint(blob.Size())))
	etag := `"` + hex.EncodeToString(hash[:16]) + `"`
	if match := e.Request.Header.Get("If-None-Match"); match == etag {
		e.Response.Header().Set("ETag", etag)
		e.Response.WriteHeader(http.StatusNotModified)
		return nil
	}

	e.Response.Header().Set("Content-Type", contentType)
	e.Response.Header().Set("Content-Length", fmt.Sprint(blob.Size()))
	e.Response.Header().Set("Cache-Control", fileCacheControl(public))
	e.Response.Header().Set("ETag", etag)
	if !isInlineContentType(contentType) {
		e.Response.Header().Set("Content-Disposition", "attachment; filename="+filename)
	}
	e.Response.WriteHeader(http.StatusOK)
	_, err = io.Copy(e.Response, blob)
	return err
}

// fileCacheControl returns the Cache-Control for a file the
// caller was allowed to read.
//
// A public memo's attachment is cacheable by the browser and by
// any intermediary; everything else is `private, no-store` so a
// shared machine does not keep a private memo's image on disk.
//
// The distinction matters in practice: the SPA requests the same
// thumbnail URL many times while scrolling a feed, and a
// `no-store` response would re-download it every time.
func fileCacheControl(public bool) string {
	if public {
		return "public, max-age=3600"
	}
	return "private, no-store"
}

// serveFileBytes writes an in-memory payload (a generated
// thumbnail or an extracted motion clip) to the response. The
// payload is produced by riff rather than read from the
// filesystem, so the Cache-Control is the same short-lived
// value the upstream server uses for its generated artefacts.
func serveFileBytes(e *core.RequestEvent, contentType, filename string, payload []byte) error {
	e.Response.Header().Set("Content-Type", contentType)
	e.Response.Header().Set("Content-Length", fmt.Sprint(len(payload)))
	e.Response.Header().Set("Cache-Control", "private, max-age=3600")
	e.Response.Header().Set("Content-Disposition", "inline; filename="+filename)
	e.Response.WriteHeader(http.StatusOK)
	_, err := e.Response.Write(payload)
	return err
}

// motionClipName returns the filename the extracted motion clip is
// served under. The extension is .mp4 because the embedded payload
// is always an MP4 (see internal/motionphoto).
func motionClipName(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if base == "" {
		base = "motion"
	}
	return base + ".mp4"
}

// extractMotionClip pulls the embedded MP4 out of a Motion Photo
// JPEG. The file is read into memory (bounded by the caller) and
// handed to internal/motionphoto, which knows the container
// layouts.
//
// A nil result means "this is not a Motion Photo" — the caller
// answers 404 rather than serving the still frame, because the
// SPA asked for a motion clip and a still frame would silently
// look like a broken video.
func extractMotionClip(blob []byte) []byte {
	clip, _ := motionphoto.ExtractVideo(blob)
	return clip
}

// fileEtagFor computes the ETag for a generated (thumbnail or
// motion clip) payload. The bytes are the input, so two
// identical payloads always produce the same tag.
func fileEtagFor(payload []byte) string {
	hash := sha256.Sum256(payload)
	return `"` + hex.EncodeToString(hash[:16]) + `"`
}

// writeIfNotModified answers a conditional GET for a generated
// payload. It reports whether the response was completed (the
// caller must then return nil without writing a body).
func writeIfNotModified(e *core.RequestEvent, etag string) bool {
	if e.Request.Header.Get("If-None-Match") != etag {
		return false
	}
	e.Response.Header().Set("ETag", etag)
	e.Response.WriteHeader(http.StatusNotModified)
	return true
}
