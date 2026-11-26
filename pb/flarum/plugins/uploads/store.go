package uploads

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// fileRecord projects a file record onto the plugin's own view of it.
//
// The plugin does not introduce a domain model: the file is a
// projection of a PocketBase record, and the API layer renders it
// straight from the record. That is the same shape the Tags plugin
// uses for its tags.
type fileRecord struct {
	// ID is the record's id.
	ID string
	// ActorID is the uploader's id.
	ActorID string
	// PostID is the post the file is attached to, empty when it is
	// not attached to one yet.
	PostID string
	// Filename is the name the file was uploaded under.
	Filename string
	// MimeType is the file's media type.
	MimeType string
	// Size is the file's size in bytes.
	Size int64
	// ImageWidth and ImageHeight are the image's dimensions, zero for
	// a file that is not an image.
	ImageWidth  int
	ImageHeight int
	// Hidden reports whether the file is hidden from its owner.
	Hidden bool
	// Shared reports whether the file belongs to the forum's shared
	// library rather than to its uploader.
	//
	// It is upstream's `shared` column: a shared file is written and
	// deleted with `upload-shared-files` instead of ownership, and the
	// listing that shows it is a separate endpoint.
	Shared bool
	// Blob is the stored file's name, empty when nothing was stored.
	Blob string
}

// findFile returns a file record by id.
func findFile(app core.App, physical, id string) (*core.Record, error) {
	record, err := app.FindRecordById(physical, id)
	if err != nil {
		return nil, errors.Wrapf(err, "uploads: the file %q does not exist", id)
	}
	return record, nil
}

// fileFromRecord projects a file record.
func fileFromRecord(record *core.Record) fileRecord {
	return fileRecord{
		ID:          record.Id,
		ActorID:     record.GetString("actor"),
		PostID:      record.GetString("post"),
		Filename:    record.GetString("filename"),
		MimeType:    record.GetString("mime_type"),
		Size:        int64(record.GetInt("size")),
		ImageWidth:  record.GetInt("image_width"),
		ImageHeight: record.GetInt("image_height"),
		Hidden:      record.GetBool("hidden"),
		Shared:      record.GetBool("shared"),
		Blob:        record.GetString("blob"),
	}
}

// filesForPost returns the files attached to a post.
//
// The order is by creation time, so the files keep the order they were
// attached in.
func filesForPost(app core.App, physical, postID string) ([]*core.Record, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, err
	}
	records, err := app.FindRecordsByFilter(collection, "post = {:post}", "", 0, 0,
		map[string]any{"post": postID})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// mimeFromFilename guesses a file's media type from its name.
//
// It is the same shape as the memo side's `connect.GuessMimeType`: the
// browser's `Content-Type` is not trusted (it is whatever the client
// felt like sending), so the type is derived from the extension.
//
// The list is deliberately small: the forum serves what a forum post
// carries — images, documents, archives — and anything unknown is
// served as a generic binary.
func mimeFromFilename(filename string) string {
	lower := strings.ToLower(filename)
	if dot := strings.LastIndex(lower, "."); dot >= 0 {
		lower = lower[dot+1:]
	}

	switch lower {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "bmp":
		return "image/bmp"
	case "tif", "tiff":
		return "image/tiff"
	case "avif":
		return "image/avif"
	case "heic":
		return "image/heic"
	case "heif":
		return "image/heif"
	case "pdf":
		return "application/pdf"
	case "txt", "md":
		return "text/plain"
	case "html", "htm":
		return "text/html"
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	case "xml":
		return "application/xml"
	case "zip":
		return "application/zip"
	case "gz", "tgz":
		return "application/gzip"
	case "tar":
		return "application/x-tar"
	case "7z":
		return "application/x-7z-compressed"
	case "rar":
		return "application/vnd.rar-compressed"
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "ogg", "oga":
		return "audio/ogg"
	case "m4a":
		return "audio/mp4"
	case "flac":
		return "audio/flac"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	default:
		return "application/octet-stream"
	}
}
